package sessionpressure

import (
	"context"
	"encoding/json"
	"github.com/nstranquist/session-pressure/internal/filelock"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func automatedBrowserRoot(command string) bool {
	if strings.Contains(command, "--type=") {
		return false
	}
	automated := strings.Contains(command, " --headless") || strings.Contains(command, " --remote-debugging-port") || strings.Contains(command, " --remote-debugging-pipe")
	chrome := strings.Contains(command, "/Contents/MacOS/Google Chrome ") || strings.Contains(command, "/Contents/MacOS/Google Chrome for Testing ") || strings.HasPrefix(command, "chromium ") || strings.HasPrefix(command, "google-chrome ")
	return automated && chrome
}

// browserControlMaxRuntime is the control's finite browser lifetime.
func browserControlMaxRuntime(state BrowserControl) time.Duration {
	if state.MaxRuntimeSeconds > 0 {
		return time.Duration(state.MaxRuntimeSeconds) * time.Second
	}
	return 120 * time.Second
}

// browserControlStops decides whether the resident stops one automation
// browser. The operator's pause stops every browser, and no browser outlives
// the finite lifetime. The automatic CPU pause only gates admission: a
// running browser ends within its lifetime anyway, and stopping it throws
// away the job and makes the agent retry it, which adds load. There is no
// count limit: memory admission (browser_admission.go, WORK-1114) decides how
// many run. Before 2026-11-17 an unpaused host spared only the first young
// browser and stopped every other one, a fixed cap of one that killed a CSP
// sweep's session when a screenshot ran beside it.
func browserControlStops(operatorPaused bool, age, maxRuntime time.Duration) bool {
	return operatorPaused || age >= maxRuntime
}

// browserOperatorPaused reports an explicit operator pause. Read errors keep
// the job running; the 120 s lifetime still bounds it.
func browserOperatorPaused(dir string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	state, err := BrowserControlStatus(ctx, dir, "")
	return err == nil && state.Mode == "paused"
}

// EnforceBrowserControl is authorized by the host-wide opt-in. Only automation
// roots owned by this UID are eligible. A kernel start identity is checked
// again before signaling; personal browser sessions have no automation flag.
func EnforceBrowserControl(ctx context.Context, dir string) error {
	state, err := BrowserControlStatus(ctx, dir, "")
	if err != nil || state.Mode == "" {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	roots, err := automatedBrowserProcesses(probeCtx)
	if err != nil {
		return err
	}
	maxRuntime := browserControlMaxRuntime(state)
	for _, root := range roots {
		pid, started := root.PID, root.StartedAt
		identity, uid, err := processGuardIdentity(pid)
		if err != nil || uid != os.Getuid() || (root.StartIdentity != "" && identity != root.StartIdentity) {
			continue
		}
		if !browserControlStops(state.Mode == "paused", time.Since(started), maxRuntime) {
			continue
		}
		again, againUID, err := processGuardIdentity(pid)
		if err != nil || again != identity || againUID != uid {
			continue
		}
		if err = syscall.Kill(pid, syscall.SIGTERM); err != nil {
			continue
		}
		recordControlEvent(dir, map[string]any{"event": "browser_stopped", "pid": pid, "blocked": state.Blocked, "age_seconds": int(time.Since(started).Seconds())})
		go func(pid int, identity string) {
			time.Sleep(3 * time.Second)
			current, uid, err := processGuardIdentity(pid)
			if err == nil && current == identity && uid == os.Getuid() {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}(pid, identity)
	}
	return nil
}

func recordControlEvent(dir string, event map[string]any) {
	name, _ := event["event"].(string)
	row := map[string]any{"schema_version": 1, "event": name, "ts": time.Now().UTC().Format(time.RFC3339Nano), "source": "session-pressure-work-control", "surface": "session-pressure", "command": "session pressure work control", "privacy": "local", "attrs": event}
	if code, exists := event["exit_code"]; exists {
		row["ok"] = code == 0
	}
	body, err := json.Marshal(row)
	if err != nil {
		return
	}
	_ = filelock.WithFileLockContext(context.Background(), filepath.Join(dir, "work-control-events"), time.Second, func() error {
		path := filepath.Join(dir, "work-control-events.jsonl")
		if info, err := os.Stat(path); err == nil && info.Size() > 4<<20 {
			_ = os.Rename(path, path+".previous")
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.Write(append(body, '\n'))
		return err
	})
}

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
	slotOccupied := false
	for _, root := range roots {
		pid, started := root.PID, root.StartedAt
		identity, uid, err := processGuardIdentity(pid)
		if err != nil || uid != os.Getuid() || (root.StartIdentity != "" && identity != root.StartIdentity) {
			continue
		}
		if !state.Blocked && time.Since(started) < 120*time.Second && !slotOccupied {
			slotOccupied = true
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

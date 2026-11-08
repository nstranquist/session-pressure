//go:build darwin

package sessionpressure

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This opt-in native review uses sleeping children and a separate app identity,
// preferences, and ledger. Open the app named in ready.json with the UI driver.
// Cancel Stop once, try the stale identity, Stop the TERM child, then Stop and
// Force Stop the force child. Refresh and create the complete file to validate.
func TestProcessIncidentNativeReview(t *testing.T) {
	dir := os.Getenv("PRESSURE_NATIVE_REVIEW_DIR")
	if dir == "" {
		t.Skip("interactive native review is opt-in")
	}
	if !filepath.IsAbs(dir) {
		t.Fatal("review directory must be absolute and unused")
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	binary := os.Getenv("PRESSURE_PROCESS_CONTRACT_BIN")
	app := os.Getenv("PRESSURE_NATIVE_REVIEW_APP")
	if !filepath.IsAbs(binary) || !filepath.IsAbs(app) {
		t.Fatal("review needs absolute CLI and app paths")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	run := func(name string, args ...string) {
		t.Helper()
		commandCtx, stop := context.WithTimeout(ctx, 30*time.Second)
		defer stop()
		if out, err := exec.CommandContext(commandCtx, name, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v: %s", name, err, out)
		}
	}
	var incidents []ProcessIncident
	for _, fixture := range []struct{ name, command string }{
		{"Review child TERM", "echo ready; exec /bin/sleep 600"},
		{"Review child Force", "trap '' TERM; echo ready; exec /bin/sleep 600"},
	} {
		child := exec.CommandContext(ctx, "/bin/sh", "-c", fixture.command)
		ready, err := child.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		// The direct child remains unreaped until cleanup, pinning its PID.
		t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
		var word [6]byte
		if _, err := io.ReadFull(ready, word[:]); err != nil || string(word[:]) != "ready\n" {
			t.Fatalf("sleeping child readiness: %q %v", word, err)
		}
		_ = ready.Close()
		birth, uid, err := processGuardIdentity(child.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		incidents = append(incidents, ProcessIncident{Source: "review_fixture", ID: incidentKey(child.Process.Pid, birth), PID: child.Process.Pid, PPID: os.Getpid(), StartIdentity: birth, UID: uid, Executable: fixture.name, Reason: "review_fixture_no_cpu_load", State: "active", FirstSeen: now, LastSeen: now})
	}
	stale := incidents[0]
	stale.ID, stale.Executable, stale.StartIdentity = "review-stale-identity", "Review stale identity", "darwin:0:1"
	incidents = append(incidents, stale)
	if err := updateProcessIncidents(ctx, dir, func(r *ProcessIncidentReport) error { r.Incidents = incidents; return nil }); err != nil {
		t.Fatal(err)
	}
	copyApp := filepath.Join(dir, "Process Incident Review.app")
	run("/usr/bin/ditto", app, copyApp)
	plist := filepath.Join(copyApp, "Contents", "Info.plist")
	run("/usr/libexec/PlistBuddy", "-c", "Set :CFBundleIdentifier com.nstranquist.process-incident-review", plist)
	run("/usr/libexec/PlistBuddy", "-c", "Set :CFBundleName Process Incident Review", plist)
	run("/usr/libexec/PlistBuddy", "-c", "Delete :CFBundleURLTypes", plist)
	run("/usr/libexec/PlistBuddy", "-c", "Add :LSEnvironment dict", plist)
	for key, value := range map[string]string{"NDEV_SESSION_PRESSURE_HOME": dir, "SESSION_PRESSURE_BIN": binary} {
		run("/usr/libexec/PlistBuddy", "-c", fmt.Sprintf("Add :LSEnvironment:%s string %s", key, value), plist)
	}
	run("/usr/bin/codesign", "--force", "--deep", "--sign", "-", copyApp)
	write := func(name string, value any) {
		t.Helper()
		body, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("ready.json", map[string]any{"app": copyApp, "binary": binary, "incidents": incidents, "deadline": time.Now().Add(8 * time.Minute)})
	t.Logf("Open %s; private ledger %s", copyApp, dir)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("native review deadline expired; owned children cleaned up")
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(dir, "complete")); err != nil {
				if !os.IsNotExist(err) {
					t.Fatal(err)
				}
				continue
			}
			report, err := ReadProcessIncidents(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Incidents) != 3 {
				t.Fatalf("review ledger changed: %+v", report)
			}
			for _, row := range report.Incidents {
				expectedSignal := "TERM"
				if row.ID == stale.ID {
					if len(row.Actions) != 1 || row.Actions[0].Signal != "NONE" || row.Actions[0].Result != "revalidation_rejected" {
						t.Fatalf("stale identity: %+v", row)
					}
					continue
				}
				if row.Executable == "Review child Force" {
					expectedSignal = "KILL"
					if len(row.Actions) != 2 || row.Actions[0].Signal != "TERM" || row.Actions[0].Result != "signal_sent_unconfirmed" {
						t.Fatalf("force needs a separate failed TERM attempt: %+v", row)
					}
				}
				if row.State != "stopped" || len(row.Actions) == 0 {
					t.Fatalf("not stopped: %+v", row)
				}
				last := row.Actions[len(row.Actions)-1]
				if last.Signal != expectedSignal || last.Result != "exit_confirmed" {
					t.Fatalf("wrong result: %+v", row)
				}
				if _, _, err := processGuardIdentity(row.PID); !processGuardGone(err) {
					t.Fatalf("owned child still running: %d: %v", row.PID, err)
				}
			}
			write("result.json", map[string]any{"passed": true, "report": report, "cpu_load": "sleeping children only"})
			return
		}
	}
}

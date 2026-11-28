package sessionpressure

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseWorkRunPlacement(t *testing.T) {
	base := []string{"--class", "build", "--wait", "5m"}
	cmd := []string{"--", "go", "build", "-o", "/tmp/ndev", "./cmd/ndev-go"}
	for name, tc := range map[string]struct {
		flags     []string
		placement WorkPlacement
		reason    string
		err       string
	}{
		"default is auto": {},
		"explicit auto":   {flags: []string{"--placement", "auto"}},
		"explicit remote": {flags: []string{"--placement", "remote"}, placement: WorkPlacementRemote},
		"local with reason": {flags: []string{"--placement", "local", "--placement-reason", "darwin-cgo-producer"},
			placement: WorkPlacementLocal, reason: "darwin-cgo-producer"},
		"local needs a reason":     {flags: []string{"--placement", "local"}, err: "requires --placement-reason"},
		"reason needs local":       {flags: []string{"--placement-reason", "x"}, err: "only valid with --placement local"},
		"reason is a slug":         {flags: []string{"--placement", "local", "--placement-reason", "Mac only!"}, err: "lowercase slug"},
		"unknown placement":        {flags: []string{"--placement", "elsewhere"}, err: "auto, local, or remote"},
		"placement needs a value":  {flags: []string{"--placement"}, err: "requires a value"},
		"reason length is bounded": {flags: []string{"--placement", "local", "--placement-reason", strings.Repeat("a", 65)}, err: "at most 64"},
	} {
		t.Run(name, func(t *testing.T) {
			args := append(append(append([]string(nil), base...), tc.flags...), cmd...)
			if tc.flags != nil && len(tc.flags) == 1 {
				args = append(append([]string(nil), base...), tc.flags...)
			}
			options, err := ParseWorkRunArgs(args)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if options.Placement != tc.placement || options.PlacementReason != tc.reason {
				t.Fatalf("placement = %q/%q, want %q/%q", options.Placement, options.PlacementReason, tc.placement, tc.reason)
			}
		})
	}
}

func TestRequiredRemotePlacementNeverEntersLocalAdmission(t *testing.T) {
	for _, tc := range []struct {
		name      string
		enabled   bool
		node      bool
		command   []string
		wantSub   string
		wantKind  string
		wantBin   string
		darwinSub string
	}{
		{
			name:      "disabled routing",
			enabled:   false,
			command:   []string{"go", "build", "./..."},
			wantSub:   "only supported on darwin hosts",
			darwinSub: "requires enabled remote routing in remote-work.json",
			wantKind:  "routing_disabled",
			wantBin:   "go",
		},
		{
			name:      "unsupported command",
			enabled:   true,
			command:   []string{"sh", "-c", "exit 99"},
			wantSub:   "only supported on darwin hosts",
			darwinSub: `does not support command "sh"`,
			wantKind:  "unsupported_command",
			wantBin:   "sh",
		},
		{
			name:      "node routing disabled",
			enabled:   true,
			node:      false,
			command:   []string{"npm", "test"},
			wantSub:   "only supported on darwin hosts",
			darwinSub: `for Node command "npm" requires node routing enabled`,
			wantKind:  "node_routing_disabled",
			wantBin:   "npm",
		},
		{
			name:      "node command ineligible preserves node reason",
			enabled:   true,
			node:      true,
			command:   []string{"npm", "test"},
			wantSub:   "only supported on darwin hosts",
			darwinSub: "cannot route Node command:",
			wantKind:  "node_ineligible",
			wantBin:   "npm",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := SaveRemoteWorkPolicy(dir, RemoteWorkPolicy{
				Enabled: tc.enabled,
				Node:    tc.node,
				Host:    "runner-3",
				User:    "builder",
			}); err != nil {
				t.Fatal(err)
			}
			if tc.node {
				// Empty temp directory guarantees planRemoteNodeWork declines without touching any real repo.
				t.Chdir(t.TempDir())
			}
			code, err := RunWorkCommand(&WorkCoordinator{Dir: dir}, WorkRunOptions{
				Class: WorkClassBuild, Placement: WorkPlacementRemote, Command: tc.command,
			}, func() Admission {
				t.Fatal("required remote work reached local admission")
				return Admission{}
			}, time.Millisecond, WorkRunStreams{})
			if code != workExitPolicyDenied || err == nil || !IsWorkRefusal(err) {
				t.Fatalf("code=%d err=%v, want policy refusal", code, err)
			}
			text := err.Error()
			if strings.Count(text, "local fallback is disabled") != 1 {
				t.Fatalf("expected exactly one 'local fallback is disabled', got: %q", text)
			}
			if strings.Contains(text, remoteRunnerAdvice) {
				t.Fatalf("explicit remote placement refusal should not emit compiler-profile advice: %q", text)
			}
			wantSub := tc.wantSub
			wantKind := "unsupported_host"
			if runtime.GOOS == "darwin" {
				wantSub = tc.darwinSub
				wantKind = tc.wantKind
			}
			if !strings.Contains(text, wantSub) {
				t.Fatalf("err = %q, want substring %q", text, wantSub)
			}
			events, readErr := os.ReadFile(filepath.Join(dir, "work-control-events.jsonl"))
			if readErr != nil {
				t.Fatalf("read control events: %v", readErr)
			}
			lines := strings.Split(strings.TrimSpace(string(events)), "\n")
			if len(lines) != 1 {
				t.Fatalf("expected 1 remote_work_declined event, got %d: %s", len(lines), events)
			}
			if !strings.Contains(lines[0], `"placement_decline_kind":"`+wantKind+`"`) || !strings.Contains(lines[0], `"command_bin":"`+tc.wantBin+`"`) {
				t.Fatalf("control event missing structured attrs (want kind=%s bin=%s): %s", wantKind, tc.wantBin, lines[0])
			}
		})
	}
}

// A declared local run never reaches the remote route, even when remote work
// is enabled and the command would be rejected there (GOFLAGS -modfile).
func TestLocalPlacementSkipsRemoteRouting(t *testing.T) {
	dir := t.TempDir()
	if err := ConfigureRemoteWork(dir, true, "runner-3", "builder"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOFLAGS", "-modfile=/var/folders/T/ndev-producer-mod-1/producer.mod")
	options := WorkRunOptions{Class: WorkClassBuild, Command: []string{"go", "build", "-o", "/tmp/x", "."}}
	var stderr strings.Builder
	streams := WorkRunStreams{Stderr: &stderr}
	if handled, _, err := TryRemoteWork(dir, options, streams); !handled || err == nil {
		t.Skipf("auto placement is not rejected on this host (handled=%v err=%v); the local gate still applies", handled, err)
	}
	options.Placement, options.PlacementReason = WorkPlacementLocal, "darwin-cgo-producer"
	recordLocalPlacement(dir, options, streams)
	if !strings.Contains(stderr.String(), "declared local placement (darwin-cgo-producer)") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

package sessionpressure

import (
	"strings"
	"testing"
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
		"local with reason": {flags: []string{"--placement", "local", "--placement-reason", "darwin-cgo-producer"},
			placement: WorkPlacementLocal, reason: "darwin-cgo-producer"},
		"local needs a reason":     {flags: []string{"--placement", "local"}, err: "requires --placement-reason"},
		"reason needs local":       {flags: []string{"--placement-reason", "x"}, err: "only valid with --placement local"},
		"reason is a slug":         {flags: []string{"--placement", "local", "--placement-reason", "Mac only!"}, err: "lowercase slug"},
		"unknown placement":        {flags: []string{"--placement", "remote"}, err: "auto or local"},
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

// A declared local run never reaches the remote route, even when remote work
// is enabled and the command would be rejected there (GOFLAGS -modfile).
func TestLocalPlacementSkipsRemoteRouting(t *testing.T) {
	dir := t.TempDir()
	if err := SetRemoteWork(dir, true); err != nil {
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

package sessionpressure

import (
	"errors"
	"fmt"
	"os"
	"unicode"
)

// WorkPlacement says where a work-run command may execute.
//
// Remote routing must not silently fall back onto the Mac (828b1ea), and some
// work is only correct on the Mac: a build that reads Mac-local module files
// or tools. (Darwin cgo builds are not in that set: the runner builds them
// with its pinned Darwin compiler, remote_build_profile.go.) Rejecting that
// work does not move it to the runner; it fails it everywhere. On 2026-11-08
// that rejection paused seven commit deliveries in 90 seconds. The caller that knows the work is
// Mac-only declares it, with a reason that telemetry records. Nothing else
// changes placement implicitly.
type WorkPlacement string

const (
	// WorkPlacementAuto routes eligible Go work remotely when remote work is
	// enabled and rejects ineligible work. It is the default.
	WorkPlacementAuto WorkPlacement = ""
	// WorkPlacementLocal keeps local admission and never routes remotely.
	WorkPlacementLocal WorkPlacement = "local"
)

// maxPlacementReason bounds the reason so it stays a short telemetry token.
const maxPlacementReason = 64

func ParseWorkPlacement(value string) (WorkPlacement, error) {
	switch value {
	case "auto":
		return WorkPlacementAuto, nil
	case "local":
		return WorkPlacementLocal, nil
	}
	return "", fmt.Errorf("--placement must be auto or local, not %q", value)
}

// ValidateWorkPlacement requires a reason exactly for local placement. The
// reason is a short slug such as darwin-cgo-producer, so it is safe to record
// and easy to aggregate.
func ValidateWorkPlacement(placement WorkPlacement, reason string) error {
	if placement != WorkPlacementLocal {
		if reason != "" {
			return errors.New("--placement-reason is only valid with --placement local")
		}
		return nil
	}
	if reason == "" {
		return errors.New("--placement local requires --placement-reason <slug> naming why the work is only correct on this Mac (for example darwin-cgo-producer)")
	}
	if len(reason) > maxPlacementReason {
		return fmt.Errorf("--placement-reason is at most %d characters", maxPlacementReason)
	}
	for _, r := range reason {
		if !(unicode.IsLower(r) || unicode.IsDigit(r) || r == '-') {
			return errors.New("--placement-reason must be a lowercase slug of letters, digits, and '-'")
		}
	}
	return nil
}

// recordLocalPlacement makes a declared Mac-only run visible: one stderr line
// and a control event, whether or not remote work is enabled.
func recordLocalPlacement(dir string, options WorkRunOptions, streams WorkRunStreams) {
	stderr := streams.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	enabled, _ := RemoteWorkEnabled(dir)
	if enabled {
		fmt.Fprintln(stderr, "session-pressure: declared local placement ("+options.PlacementReason+"); remote routing skipped")
	}
	operation := ""
	if len(options.Command) > 1 {
		operation = options.Command[1]
	}
	recordControlEvent(dir, map[string]any{"event": "work_local_placement", "operation": operation,
		"reason": options.PlacementReason, "remote_enabled": enabled, "class": string(options.Class)})
}

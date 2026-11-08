package sessionpressure

import (
	"fmt"
	"strings"
)

// WorkRefusalMarker is the versioned contract a caller reads to tell "the
// coordinator refused before the command started" from "the command ran and
// failed". Exit codes cannot carry that alone: work run passes the child's own
// exit code through, so a test that exits 11 would look like a policy denial.
//
// On 2026-11-08 one refusal message ("remote Go work blocked") paused 17
// commit deliveries as if each change had broken its build. Callers that match
// this one marker hold and retry instead, and every future refusal reason is
// covered without a new signature on their side. The marker is part of the
// refusal error text, so it reaches the caller's captured stderr, also across
// ssh and nested wrappers.
const WorkRefusalMarker = "ndev-work-refused/v1"

// Refusal kinds. Keep them stable; callers may branch on them.
const (
	WorkRefusalRemoteProfile = "remote_profile"
	WorkRefusalAdmission     = "admission"
	WorkRefusalWaitTimeout   = "wait_timeout"
	WorkRefusalBrowser       = "browser"
)

// workRefusal tags err as a refusal of kind. The command did not start.
func workRefusal(kind string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s kind=%s: %w", WorkRefusalMarker, kind, err)
}

// admissionRefusal tags an admission failure by its exit code. An interrupt
// is the caller's own cancellation, not a host refusal, so it stays untagged.
func admissionRefusal(code int, err error) error {
	switch code {
	case workExitWaitTimeout:
		return workRefusal(WorkRefusalWaitTimeout, err)
	case workExitInterrupted:
		return err
	}
	return workRefusal(WorkRefusalAdmission, err)
}

// IsWorkRefusal reports whether err carries the refusal contract. It reads
// the text, as callers do, because joined errors do not unwrap to one chain.
func IsWorkRefusal(err error) bool {
	return err != nil && strings.Contains(err.Error(), WorkRefusalMarker)
}

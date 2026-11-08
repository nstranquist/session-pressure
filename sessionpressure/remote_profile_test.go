package sessionpressure

import (
	"strings"
	"testing"
)

func TestRemoteProfileProtectsLocalInputsAndBuildABI(t *testing.T) {
	for _, tc := range []struct {
		name    string
		argv    []string
		env     map[string]string
		blocked bool
	}{
		{"plain test", []string{"go", "test", "./..."}, nil, false},
		{"producer modfile", []string{"go", "build", "-o", "app", "."}, map[string]string{"GOFLAGS": "-modfile=/var/folders/producer.mod"}, true},
		{"argument overlay", []string{"go", "test", "-overlay=patch.json", "./..."}, nil, true},
		{"default cgo build", []string{"go", "build", "-o", "app", "."}, nil, true},
		{"explicit portable build", []string{"go", "build", "-o", "app", "."}, map[string]string{"CGO_ENABLED": "0"}, false},
		{"test target", []string{"go", "test", "./..."}, map[string]string{"GOOS": "darwin"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := remoteProfileReason(tc.argv, func(k string) string { return tc.env[k] }) != ""
			if got != tc.blocked {
				t.Fatalf("blocked=%v want %v", got, tc.blocked)
			}
		})
	}
}

func TestRemoteClassificationDoesNotHideProfileDeclines(t *testing.T) {
	t.Setenv("GOFLAGS", "-modfile=/local/producer.mod")
	if !RemoteGoCommand([]string{"go", "build", "-o", "out", "."}) {
		t.Fatal("direct Go operations must reach the eligibility check and decline telemetry")
	}
	if remoteProfileReason([]string{"go", "build", "-o", "out", "."}, func(key string) string {
		if key == "GOFLAGS" {
			return "-modfile=/local/producer.mod"
		}
		return ""
	}) == "" {
		t.Fatal("local-only profile must be rejected")
	}
}

func TestRemoteRejectionCannotFallBackToLocalWork(t *testing.T) {
	handled, code, err := rejectRemoteWork(t.TempDir(), "build", "GOFLAGS needs an explicit runner profile")
	if !handled || code != workExitPolicyDenied || !IsWorkRefusal(err) || !strings.Contains(err.Error(), "kind=remote_profile") || !strings.Contains(err.Error(), "local fallback is disabled") {
		t.Fatalf("rejection must stop local execution: handled=%v code=%d err=%v", handled, code, err)
	}
}

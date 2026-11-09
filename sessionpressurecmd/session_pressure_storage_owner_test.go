package sessionpressurecmd

import (
	"regexp"
	"testing"
)

func TestGoCacheOwnerIsLocalExecutable(t *testing.T) {
	pattern := regexp.MustCompile(storageGoOwnerPattern)
	for _, tc := range []struct {
		command string
		active  bool
	}{
		{"go test ./...", true},
		{"/opt/homebrew/bin/go -C /repo test ./...", true},
		{"go run /repo/commit-helper.go", true},
		{"/opt/homebrew/libexec/pkg/tool/darwin_arm64/compile -o /cache/item", true},
		{"/opt/homebrew/libexec/pkg/tool/darwin_arm64/link -V=full", true},
		{"/usr/bin/ssh builder@gaterunner-1 bash -c '/usr/local/go/bin/go build ./...'", false},
		{"/bin/zsh -c ssh builder@gaterunner-1 /usr/local/go/bin/go test ./...", false},
		{"go env GOCACHE", false},
		{"/repo/telemetry.test -test.run TestGoCache", false},
	} {
		if got := pattern.MatchString(tc.command); got != tc.active {
			t.Errorf("%q active=%v want=%v", tc.command, got, tc.active)
		}
	}
}

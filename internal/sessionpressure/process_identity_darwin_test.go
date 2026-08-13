//go:build darwin

package sessionpressure

import (
	"os"
	"strings"
	"testing"
)

func TestDarwinProcessStartIdentityIsStableForLiveProcess(t *testing.T) {
	first, err := processStartIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	second, err := processStartIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !strings.HasPrefix(first, "darwin:") {
		t.Fatalf("unstable process identity first=%q second=%q", first, second)
	}
}

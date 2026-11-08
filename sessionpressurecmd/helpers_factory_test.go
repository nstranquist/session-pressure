package sessionpressurecmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestOptionalFactoryCoverageRoot(t *testing.T) {
	for _, missing := range []error{os.ErrNotExist, fmt.Errorf("discover factory: %w", os.ErrNotExist)} {
		root, err := optionalFactoryRepoRoot("", missing)
		if root != "" || err != nil {
			t.Fatalf("absent optional adapter: root=%q err=%v", root, err)
		}
	}
	permission := fmt.Errorf("discover factory: %w", os.ErrPermission)
	if root, err := optionalFactoryRepoRoot("", permission); root != "" || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("real discovery error was hidden: root=%q err=%v", root, err)
	}
	dir := filepath.Join(t.TempDir(), "nicos-dev")
	if root, err := optionalFactoryRepoRoot(dir, nil); root != filepath.Dir(dir) || err != nil {
		t.Fatalf("configured adapter: root=%q err=%v", root, err)
	}
}

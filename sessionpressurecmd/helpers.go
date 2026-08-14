package sessionpressurecmd

import (
	"fmt"
	"os"
	"path/filepath"
)

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func nicosDevDir() (string, error) {
	if override := os.Getenv("NDEV_GO_TEST_NICOS_DEV_DIR"); override != "" {
		return override, nil
	}
	for _, key := range []string{"NDEV_PATH", "NICOS_DEV_PATH"} {
		if value := os.Getenv(key); value != "" {
			return value, nil
		}
	}
	for _, key := range []string{"NICOS_TOOLS_PATH", "NICOS_TOOLS_HOME", "NICOS_TOOLS_DIR", "NICOS_TOOLS_REPO_ROOT"} {
		if value := os.Getenv(key); value != "" {
			return filepath.Join(value, "nicos-dev"), nil
		}
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for _, candidate := range []string{
			dir,
			filepath.Dir(dir),
			filepath.Join(filepath.Dir(dir), "nicos-dev"),
			filepath.Join(filepath.Dir(filepath.Dir(dir)), "nicos-dev"),
		} {
			if fileExists(filepath.Join(candidate, "go.mod")) {
				return candidate, nil
			}
		}
	}
	return "", os.ErrNotExist
}

func nicosToolsRepoRoot() (string, error) {
	dir, err := nicosDevDir()
	if err != nil {
		return "", err
	}
	return filepath.Dir(dir), nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

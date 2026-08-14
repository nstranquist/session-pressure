package sessionpressurecmd

import "os"

// executable reports whether path resolves to a non-directory with an execute
// bit. SessionPressure API discovery uses this for explicit overrides and
// sibling/installed binary candidates.
func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0
}

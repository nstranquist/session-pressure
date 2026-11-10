package sessionpressure

import (
	"os"
	"path/filepath"
	"strings"
)

// Repository-aware tests use fixtures above a nested Go module. Retain those
// working-tree inputs, including pending files and deletions, without sending
// Git history, ignored caches, credentials, symlinks or unbounded artifacts.
func remoteRepositoryRoots(modules []string) []string {
	seen := map[string]bool{}
	var roots []string
	for _, module := range modules {
		for dir := module; ; dir = filepath.Dir(dir) {
			if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
				if !seen[dir] {
					seen[dir] = true
					roots = append(roots, dir)
				}
				break
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	return roots
}

// remoteExcludedRepositoryInput filters names from Git's inventory. Git
// already applied the repository's ignore rules to them, so `.ndev/` stays:
// its tracked policy and hooks are test inputs (nicos-commit-delivery reads
// .ndev/commit-delivery.yaml; 2026-11-10 two tests failed on every remote
// run without it). The module walk for out-of-repository modules still skips
// it, because nothing filters that walk.
func remoteExcludedRepositoryInput(name string) bool {
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		switch part {
		case ".git", ".build", "target", "node_modules", "bin", ".cache", ".next", "dist", ".tmp":
			return true
		}
		if part == ".env" || strings.HasPrefix(part, ".env.") || strings.HasSuffix(part, ".pem") || strings.HasSuffix(part, ".key") {
			return true
		}
	}
	return false
}

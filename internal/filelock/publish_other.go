//go:build !linux && !darwin

package filelock

// platformExclusiveRename is nil where no exclusive rename is wired; the
// hard-link publish is used.
var platformExclusiveRename func(from, to string) error

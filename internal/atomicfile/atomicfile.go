// Package atomicfile provides small, durable temp-file + rename writes.
package atomicfile

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// WriteFile atomically writes body to path with perm.
func WriteFile(path string, body []byte, perm os.FileMode) error {
	return writeFile(osFileSystem{}, path, body, perm)
}

type fileSystem interface {
	CreateTemp(dir, pattern string) (tempFile, error)
	Remove(name string) error
	Rename(oldpath, newpath string) error
}

type tempFile interface {
	io.Writer
	Chmod(os.FileMode) error
	Close() error
	Name() string
	Sync() error
}

type osFileSystem struct{}

func (osFileSystem) CreateTemp(dir, pattern string) (tempFile, error) {
	return os.CreateTemp(dir, pattern)
}

func (osFileSystem) Remove(name string) error {
	return os.Remove(name)
}

func (osFileSystem) Rename(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}

func writeFile(fs fileSystem, path string, body []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)

	tmp, err := fs.CreateTemp(dir, ".~"+base+".")
	if err != nil {
		return fmt.Errorf("atomicfile: create temp: %w", err)
	}
	tmpPath := tmp.Name()

	cleanupOpen := func() {
		_ = tmp.Close()
		_ = fs.Remove(tmpPath)
	}
	cleanupClosed := func() {
		_ = fs.Remove(tmpPath)
	}

	n, err := tmp.Write(body)
	if err != nil {
		cleanupOpen()
		return fmt.Errorf("atomicfile: write temp: %w", err)
	}
	if n != len(body) {
		cleanupOpen()
		return fmt.Errorf("atomicfile: write temp: %w", io.ErrShortWrite)
	}
	if err := tmp.Chmod(perm); err != nil {
		cleanupOpen()
		return fmt.Errorf("atomicfile: chmod temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanupOpen()
		return fmt.Errorf("atomicfile: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanupClosed()
		return fmt.Errorf("atomicfile: close temp: %w", err)
	}
	if err := fs.Rename(tmpPath, path); err != nil {
		cleanupClosed()
		return fmt.Errorf("atomicfile: rename temp: %w", err)
	}
	return nil
}

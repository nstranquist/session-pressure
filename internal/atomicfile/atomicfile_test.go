package atomicfile

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestWriteFileWritesContentAndMode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	if err := WriteFile(path, []byte("hello"), 0o640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("content = %q, want hello", got)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("mode = %o, want 0640", got)
	}
}

func TestWriteFileRenamesLast(t *testing.T) {
	t.Parallel()

	fs := &recordingFS{}
	if err := writeFile(fs, "/tmp/target.json", []byte(`{"ok":true}`), 0o644); err != nil {
		t.Fatalf("writeFile: %v", err)
	}

	want := []string{"CreateTemp", "Write", "Chmod", "Sync", "Close", "Rename"}
	if !slices.Equal(fs.calls, want) {
		t.Fatalf("calls = %v, want %v", fs.calls, want)
	}
	if got := fs.calls[len(fs.calls)-1]; got != "Rename" {
		t.Fatalf("last call = %s, want Rename", got)
	}
}

type recordingFS struct {
	calls []string
}

func (f *recordingFS) CreateTemp(dir, pattern string) (tempFile, error) {
	f.calls = append(f.calls, "CreateTemp")
	return &recordingFile{fs: f, name: filepath.Join(dir, pattern+"tmp")}, nil
}

func (f *recordingFS) Remove(name string) error {
	f.calls = append(f.calls, "Remove")
	return nil
}

func (f *recordingFS) Rename(oldpath, newpath string) error {
	f.calls = append(f.calls, "Rename")
	return nil
}

type recordingFile struct {
	fs   *recordingFS
	name string
}

func (f *recordingFile) Write(p []byte) (int, error) {
	f.fs.calls = append(f.fs.calls, "Write")
	return len(p), nil
}

func (f *recordingFile) Chmod(os.FileMode) error {
	f.fs.calls = append(f.fs.calls, "Chmod")
	return nil
}

func (f *recordingFile) Close() error {
	f.fs.calls = append(f.fs.calls, "Close")
	return nil
}

func (f *recordingFile) Name() string {
	return f.name
}

func (f *recordingFile) Sync() error {
	f.fs.calls = append(f.fs.calls, "Sync")
	return nil
}

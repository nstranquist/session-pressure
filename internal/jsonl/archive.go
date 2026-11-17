package jsonl

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// History rule (telemetry-retention-v1, R1): telemetry, logs and ledgers are
// never deleted. An aged day file or a rotated generation moves to a gzip
// archive beside the live file. The source is removed only after the archive
// is fsynced and its decompressed bytes match the source byte for byte. A
// failed copy or check leaves the source in place and deletes nothing.

// ArchiveFile moves src into a verified gzip archive beside it, named
// <src>.gz. If that name is taken, a numeric suffix is added: an existing
// archive is never overwritten. It returns the archive path.
func ArchiveFile(src string) (string, error) {
	return archive(src, src+".gz", true)
}

// ArchiveCopy writes a verified gzip copy of src beside it, named
// <src>.<UTC stamp>.gz, and keeps src. Use it before a rewrite that drops
// rows from a live file.
func ArchiveCopy(src string, now time.Time) (string, error) {
	return archive(src, src+"."+stamp(now)+".gz", false)
}

// ErrRotatedNotArchived reports that the live file was rotated, so writers
// start a fresh file, but the retired generation could not be compressed
// yet. The generation stays on disk uncompressed and the next RotateArchive
// on the same path retries it. Callers may treat it as success.
var ErrRotatedNotArchived = errors.New("rotated; generation kept uncompressed")

// RotateArchive retires the live file at path: it renames it to
// <path>.<UTC stamp> (so writers start a fresh file at once) and then moves
// that generation into a verified gzip archive. It also retries any older
// generation that an earlier rotation left uncompressed, including a legacy
// <path>.1. If compression fails, the renamed generation stays on disk and
// the error wraps ErrRotatedNotArchived. If a peer rotated the file first,
// the error wraps os.ErrNotExist. Nothing is deleted.
func RotateArchive(path string, now time.Time) (string, error) {
	generation, err := reserve(path + "." + stamp(now))
	if err != nil {
		return "", err
	}
	if err := os.Rename(path, generation); err != nil {
		_ = os.Remove(generation) // the empty reservation only
		return "", fmt.Errorf("rotate %s: %w", filepath.Base(path), err)
	}
	sweepGenerations(path, generation, now)
	dst, err := ArchiveFile(generation)
	if err != nil {
		return generation, fmt.Errorf("%w: %v", ErrRotatedNotArchived, err)
	}
	return dst, nil
}

// sweepGenerations archives earlier generations of path that are still
// uncompressed. It skips the generation being rotated now, empty files (a
// peer's reservation) and files written in the last minute (a late writer
// may still hold them). It is best effort: a failure keeps the file.
func sweepGenerations(path, current string, now time.Time) {
	matches, err := filepath.Glob(path + ".*")
	if err != nil {
		return
	}
	prefix := filepath.Base(path) + "."
	for _, candidate := range matches {
		rest := strings.TrimPrefix(filepath.Base(candidate), prefix)
		if candidate == current || rest == "" || rest[0] < '0' || rest[0] > '9' || strings.HasSuffix(rest, ".gz") {
			continue
		}
		info, statErr := os.Lstat(candidate)
		if statErr != nil || !info.Mode().IsRegular() || info.Size() == 0 || now.Sub(info.ModTime()) < time.Minute {
			continue
		}
		_, _ = ArchiveFile(candidate)
	}
}

func stamp(now time.Time) string {
	return now.UTC().Format("20060102T150405Z")
}

// reserve creates an empty placeholder at the first free name based on want,
// so a later rename or link cannot replace an existing file.
func reserve(want string) (string, error) {
	name := want
	for i := 1; ; i++ {
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return name, f.Close()
		}
		if !errors.Is(err, os.ErrExist) || i > 1000 {
			return "", err
		}
		name = want + "-" + strconv.Itoa(i)
	}
}

func archive(src, want string, removeSource bool) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("archive %s: not a regular file", filepath.Base(src))
	}
	dir := filepath.Dir(src)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(want)+".tmp-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	keepTmp := false
	defer func() {
		if !keepTmp {
			_ = os.Remove(tmpName) // our own unfinished temp file, never history
		}
	}()
	sourceSum := sha256.New()
	zw := gzip.NewWriter(tmp)
	zw.Name = filepath.Base(src)
	zw.ModTime = info.ModTime()
	n, err := io.Copy(zw, io.TeeReader(in, sourceSum))
	if err == nil {
		err = zw.Close()
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", fmt.Errorf("archive %s: %w", filepath.Base(src), err)
	}
	if err := verifyGzip(tmpName, n, sourceSum.Sum(nil)); err != nil {
		return "", fmt.Errorf("archive %s: %w", filepath.Base(src), err)
	}
	if err := os.Chmod(tmpName, info.Mode().Perm()); err != nil {
		return "", err
	}
	dst, err := linkUnique(tmpName, want)
	if err != nil {
		return "", fmt.Errorf("archive %s: %w", filepath.Base(src), err)
	}
	if err := syncDir(dir); err != nil {
		return dst, err
	}
	if removeSource {
		// Re-check that the source did not grow while it was copied. A
		// writer that appended after the copy keeps its file.
		if now, statErr := os.Stat(src); statErr != nil || now.Size() != n || !now.ModTime().Equal(info.ModTime()) {
			return dst, fmt.Errorf("archive %s: source changed during archive; source kept", filepath.Base(src))
		}
		if err := os.Remove(src); err != nil && !errors.Is(err, os.ErrNotExist) {
			return dst, err
		}
		if err := syncDir(dir); err != nil {
			return dst, err
		}
	}
	return dst, nil
}

func verifyGzip(path string, size int64, sum []byte) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	h := sha256.New()
	got, err := io.Copy(h, zr)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	if got != size || !bytes.Equal(h.Sum(nil), sum) {
		return errors.New("verify: archive does not match source")
	}
	return nil
}

// linkUnique hard-links tmp to the first free name based on want. Link fails
// on an existing name, so no archive is ever replaced.
func linkUnique(tmp, want string) (string, error) {
	name := want
	for i := 1; ; i++ {
		err := os.Link(tmp, name)
		if err == nil {
			return name, nil
		}
		if !errors.Is(err, os.ErrExist) || i > 1000 {
			return "", err
		}
		ext := filepath.Ext(want)
		name = want[:len(want)-len(ext)] + "-" + strconv.Itoa(i) + ext
	}
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return err
	}
	return nil
}

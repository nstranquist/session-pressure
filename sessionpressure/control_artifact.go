package sessionpressure

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const controlArtifactFileMode = 0o555

type controlArtifact struct {
	Path   string
	SHA256 string
}

func controlArtifactPath(dir, digest string) string {
	return filepath.Join(dir, "control-artifacts", "sha256-"+digest)
}

func isPromotedControlArtifact(path, dir, digest string) bool {
	if !sha256Pattern.MatchString(digest) || filepath.Clean(path) != filepath.Clean(controlArtifactPath(dir, digest)) {
		return false
	}
	return VerifyControlBinary(path, digest) == nil
}

// promoteControlArtifact gives the resident a content-addressed controller
// that is independent of the CLI publisher's short rollback window. The
// launchd plist can therefore remain valid while newer CLI revisions prune
// the source artifact used during installation.
func promoteControlArtifact(source, dir string) (controlArtifact, error) {
	digest, err := ControlBinarySHA256(source)
	if err != nil {
		return controlArtifact{}, fmt.Errorf("verify cleanup control binary: %w", err)
	}
	root := filepath.Join(dir, "control-artifacts")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return controlArtifact{}, fmt.Errorf("create cleanup control artifact directory: %w", err)
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return controlArtifact{}, errors.New("cleanup control artifact path is not a real directory")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return controlArtifact{}, fmt.Errorf("secure cleanup control artifact directory: %w", err)
	}

	path := controlArtifactPath(dir, digest)
	info, statErr := os.Lstat(path)
	if statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return controlArtifact{}, errors.New("cleanup control artifact is not a regular file")
		}
		existingDigest, hashErr := fileSHA256(path)
		if hashErr != nil || existingDigest != digest {
			return controlArtifact{}, errors.New("cleanup control artifact digest mismatch")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return controlArtifact{}, fmt.Errorf("inspect cleanup control artifact: %w", statErr)
	} else {
		if writeErr := streamControlArtifact(source, path, digest); writeErr != nil {
			return controlArtifact{}, fmt.Errorf("publish cleanup control artifact: %w", writeErr)
		}
	}
	if err := os.Chmod(path, controlArtifactFileMode); err != nil {
		return controlArtifact{}, fmt.Errorf("secure cleanup control artifact: %w", err)
	}
	if err := VerifyControlBinary(path, digest); err != nil {
		return controlArtifact{}, fmt.Errorf("verify promoted cleanup control artifact: %w", err)
	}
	return controlArtifact{Path: path, SHA256: digest}, nil
}

func streamControlArtifact(source, path, expectedDigest string) (err error) {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := input.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	output, err := os.CreateTemp(filepath.Dir(path), ".control-artifact-")
	if err != nil {
		return err
	}
	temporaryPath := output.Name()
	defer os.Remove(temporaryPath)
	hash := sha256.New()
	if _, err = io.Copy(io.MultiWriter(output, hash), input); err != nil {
		_ = output.Close()
		return err
	}
	if observed := hex.EncodeToString(hash.Sum(nil)); observed != expectedDigest {
		_ = output.Close()
		return fmt.Errorf("cleanup control binary changed while promoting: got %s want %s", observed, expectedDigest)
	}
	if err = output.Chmod(controlArtifactFileMode); err != nil {
		_ = output.Close()
		return err
	}
	if err = output.Sync(); err != nil {
		_ = output.Close()
		return err
	}
	if err = output.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func pruneControlArtifacts(dir, activeDigest string, rollbackCount int) error {
	if !sha256Pattern.MatchString(activeDigest) || rollbackCount < 0 {
		return errors.New("invalid cleanup control artifact retention request")
	}
	root := filepath.Join(dir, "control-artifacts")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	type revision struct {
		path    string
		digest  string
		modTime time.Time
	}
	revisions := make([]revision, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.Type()&os.ModeSymlink != 0 || !strings.HasPrefix(name, "sha256-") {
			continue
		}
		digest := strings.TrimPrefix(name, "sha256-")
		decoded, decodeErr := hex.DecodeString(digest)
		if decodeErr != nil || len(decoded) != sha256.Size {
			continue
		}
		path := filepath.Join(root, name)
		info, infoErr := os.Lstat(path)
		if infoErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != controlArtifactFileMode {
			continue
		}
		observed, hashErr := fileSHA256(path)
		if hashErr != nil || observed != digest {
			continue
		}
		revisions = append(revisions, revision{path: path, digest: digest, modTime: info.ModTime()})
	}
	sort.Slice(revisions, func(left, right int) bool {
		if revisions[left].digest == activeDigest {
			return true
		}
		if revisions[right].digest == activeDigest {
			return false
		}
		if revisions[left].modTime.Equal(revisions[right].modTime) {
			return revisions[left].path > revisions[right].path
		}
		return revisions[left].modTime.After(revisions[right].modTime)
	})
	retain := 1 + rollbackCount
	if len(revisions) <= retain {
		return nil
	}
	for _, revision := range revisions[retain:] {
		if revision.digest == activeDigest {
			return errors.New("refuse to prune active cleanup control artifact")
		}
		if err := os.Remove(revision.path); err != nil {
			return err
		}
	}
	return nil
}

package sessionpressure

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const remoteBuildDirectoryManifest = ".checksums"

// A trailing slash explicitly asks Go for a directory of independent outputs.
// Return a sealed archive through the existing artifact transport, retaining
// the command's single source snapshot, request identity and selected host.
func remoteBuildDirectory(output string) bool { return strings.HasSuffix(output, "/") }

func remoteBuildArtifactSeal(remote, output string) string {
	artifact := remote + "/artifact"
	if remoteBuildDirectory(output) {
		return " && (cd " + shellQuote(artifact) + " && sha256sum -- * > " + shellQuote(remoteBuildDirectoryManifest) +
			") && tar -C " + shellQuote(artifact) + " -czf " + shellQuote(remote+"/outputs.tar.gz") +
			" . && mv " + shellQuote(artifact) + " " + shellQuote(remote+"/outputs") +
			" && mv " + shellQuote(remote+"/outputs.tar.gz") + " " + shellQuote(artifact) +
			" && sha256sum " + shellQuote(artifact) + " > " + shellQuote(artifact+".sha256")
	}
	return " && sha256sum " + shellQuote(artifact) + " > " + shellQuote(artifact+".sha256")
}

// Publish into a previously absent destination. All extraction is private;
// reject links, paths, duplicates, extra/missing files and checksum failures
// before exposing any output. Never merge a partial download into a live dir.
func publishRemoteBuildDirectory(archive, output string, receipt []byte) error {
	output = filepath.Clean(output)
	lock, err := os.OpenFile(output+".publish.lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("claim remote build output: %w", err)
	}
	defer os.Remove(lock.Name())
	if err := lock.Close(); err != nil {
		return err
	}
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remote build output directory must be absent: %s", output)
	}
	stage, err := os.MkdirTemp(filepath.Dir(output), ".remote-go-outputs-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer z.Close()
	reader := tar.NewReader(z)
	files := map[string]string{}
	var manifest []byte
	var total int64
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if (header.Name == "." || header.Name == "./") && header.Typeflag == tar.TypeDir {
			continue
		}
		name := strings.TrimPrefix(header.Name, "./")
		if name == "" || name == "." || filepath.Base(name) != name || strings.ContainsAny(name, "\\\n\r") || (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) {
			return fmt.Errorf("invalid remote build output %q", header.Name)
		}
		if _, exists := files[name]; exists || (name == remoteBuildDirectoryManifest && manifest != nil) {
			return fmt.Errorf("duplicate remote build output %q", name)
		}
		total += header.Size
		if header.Size < 0 || total > 512<<20 || len(files) >= 128 {
			return errors.New("remote build outputs exceed limits")
		}
		if name == remoteBuildDirectoryManifest {
			if header.Size > 32<<10 {
				return errors.New("remote build manifest exceeds limit")
			}
			manifest, err = io.ReadAll(reader)
			if err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(name, ".") {
			return fmt.Errorf("invalid hidden remote build output %q", name)
		}
		out, err := os.OpenFile(filepath.Join(stage, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		digest := sha256.New()
		_, copyErr := io.Copy(io.MultiWriter(out, digest), reader)
		syncErr := out.Sync()
		closeErr := out.Close()
		if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
			return err
		}
		files[name] = hex.EncodeToString(digest.Sum(nil))
	}
	// Read through gzip EOF so a truncated or corrupt trailer cannot publish.
	if _, err := io.Copy(io.Discard, z); err != nil {
		return fmt.Errorf("verify remote build archive: %w", err)
	}
	if len(files) == 0 || len(manifest) == 0 {
		return errors.New("remote build outputs or manifest missing")
	}
	remaining := len(files)
	verified := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(manifest)), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 2 || len(parts[0]) != 64 || files[parts[1]] != parts[0] || verified[parts[1]] {
			return errors.New("remote build output checksum manifest mismatch")
		}
		verified[parts[1]] = true
		remaining--
	}
	if remaining != 0 {
		return errors.New("remote build outputs missing from checksum manifest")
	}
	if err := os.WriteFile(filepath.Join(stage, remoteBuildDirectoryManifest), manifest, 0o600); err != nil {
		return err
	}
	if len(receipt) > 0 {
		if err := os.WriteFile(filepath.Join(stage, ".remote-build-receipt.json"), receipt, 0o600); err != nil {
			return err
		}
	}
	// Recheck after extraction; another writer must not be replaced.
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remote build output directory appeared: %s", output)
	}
	return os.Rename(stage, output)
}

func remoteBuildDirectoryReceipt(policy RemoteWorkPolicy, profile, request, archiveSHA string, sourceBytes int64) []byte {
	body, _ := json.Marshal(map[string]any{
		"schema_version": 1, "runner_host": policy.Host, "runner_user": policy.User,
		"profile": profile, "request_key": request, "archive_sha256": archiveSHA,
		"source_bytes": sourceBytes,
	})
	return append(body, '\n')
}

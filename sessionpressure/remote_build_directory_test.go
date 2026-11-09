package sessionpressure

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type buildDirectoryFixture struct {
	name, body string
	kind       byte
}

func buildDirectoryArchive(t *testing.T, files []buildDirectoryFixture) string {
	t.Helper()
	var buf bytes.Buffer
	z := gzip.NewWriter(&buf)
	w := tar.NewWriter(z)
	for _, file := range files {
		kind := file.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		header := &tar.Header{Name: file.name, Mode: 0o755, Size: int64(len(file.body)), Typeflag: kind}
		if kind == tar.TypeSymlink {
			header.Linkname = "outside"
			header.Size = 0
		}
		if err := w.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if kind != tar.TypeSymlink {
			if _, err := w.Write([]byte(file.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "outputs.tar.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRemoteBuildDirectoryVerifiedPublication(t *testing.T) {
	manifest := fmt.Sprintf("%x  one\n%x  two\n", sha256.Sum256([]byte("first")), sha256.Sum256([]byte("second")))
	archive := buildDirectoryArchive(t, []buildDirectoryFixture{{"./one", "first", 0}, {"./two", "second", 0}, {"./.checksums", manifest, 0}})
	output := filepath.Join(t.TempDir(), "new")
	receipt := []byte(`{"schema_version":1,"runner_host":"gaterunner-3"}`)
	if err := publishRemoteBuildDirectory(archive, output, receipt); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"one": "first", "two": "second", ".checksums": manifest, ".remote-build-receipt.json": string(receipt)} {
		body, err := os.ReadFile(filepath.Join(output, name))
		if err != nil || string(body) != want {
			t.Fatalf("%s=%q, %v", name, body, err)
		}
	}
	if err := publishRemoteBuildDirectory(archive, output, nil); err == nil {
		t.Fatal("overwrote existing output")
	}
}

func TestRemoteBuildDirectoryRefusesUnverifiedOutputs(t *testing.T) {
	checksum := fmt.Sprintf("%x  one\n", sha256.Sum256([]byte("first")))
	for name, files := range map[string][]buildDirectoryFixture{
		"traversal":          {{"../one", "first", 0}},
		"absolute":           {{"/one", "first", 0}},
		"symlink":            {{"one", "", tar.TypeSymlink}},
		"nested":             {{"sub/one", "first", 0}},
		"duplicate":          {{"one", "first", 0}, {"one", "first", 0}},
		"no manifest":        {{"one", "first", 0}},
		"bad digest":         {{"one", "changed", 0}, {".checksums", checksum, 0}},
		"extra output":       {{"one", "first", 0}, {"two", "second", 0}, {".checksums", checksum, 0}},
		"missing output":     {{".checksums", checksum, 0}},
		"duplicate checksum": {{"one", "first", 0}, {".checksums", checksum + checksum, 0}},
	} {
		t.Run(name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "new")
			if err := publishRemoteBuildDirectory(buildDirectoryArchive(t, files), output, nil); err == nil {
				t.Fatal("accepted invalid output")
			}
			if _, err := os.Lstat(output); !os.IsNotExist(err) {
				t.Fatal("exposed partial output")
			}
			if _, err := os.Lstat(output + ".publish.lock"); !os.IsNotExist(err) {
				t.Fatal("leaked output lock")
			}
		})
	}
}

func TestRemoteBuildDirectoryArgv(t *testing.T) {
	for _, output := range []string{"/local/outputs/", "./outputs/"} {
		for _, flag := range [][]string{{"-o", output}, {"-o=" + output}} {
			argv := append([]string{"go", "build"}, flag...)
			argv = append(argv, "./cmd/one", "./cmd/two")
			normalized, destination, err := normalizeRemoteBuildArgv(argv)
			if err != nil || destination != output || !strings.Contains(strings.Join(normalized, " "), "/srv/build/candidates/OUTPUT/") {
				t.Fatalf("%v => %v, %q, %v", argv, normalized, destination, err)
			}
		}
	}
}

// Same source and production profile on each explicitly selected active host.
// The artifacts are retained for checksum-verified execution on the Mac.
func TestRemoteBuildDirectoryOnEnrolledRunner(t *testing.T) {
	host, retained := os.Getenv("NDEV_TEST_ENROLLED_RUNNER"), os.Getenv("NDEV_TEST_DARWIN_OUTPUTS")
	if host == "" {
		t.Skip("requires explicit enrolled host")
	}
	if !filepath.IsAbs(retained) {
		t.Fatal("NDEV_TEST_DARWIN_OUTPUTS must be absolute")
	}
	job := t.TempDir()
	preflight, err := remoteBuildPreflight(remoteProfileDarwinCgo, host, job)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two"} {
		if err := os.MkdirAll(filepath.Join(job, name), 0o755); err != nil {
			t.Fatal(err)
		}
		source := "package main\n/*\nint answer(void) { return 42; }\n*/\nimport \"C\"\nimport \"fmt\"\nfunc main() { fmt.Println(C.answer()) }\n"
		if err := os.WriteFile(filepath.Join(job, name, "main.go"), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(job, "go.mod"), []byte("module batch.fixture\n\ngo 1.27\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := preflight + "cd " + shellQuote(job) + " && env GOWORK=off GOFLAGS= " + remoteBuildEnvironment(remoteProfileDarwinCgo, job) + " go build -trimpath -buildvcs=false -o " + shellQuote(job+"/artifact/") + " ./one ./two" + remoteBuildArtifactSeal(job, "outputs/")
	if out, err := exec.Command("bash", "-c", command).CombinedOutput(); err != nil {
		t.Fatalf("production multi-output build: %v\n%s", err, out)
	}
	if err := publishRemoteBuildDirectory(job+"/artifact", retained, remoteBuildDirectoryReceipt(RemoteWorkPolicy{Host: host, User: "builder"}, remoteProfileDarwinCgo, "test-build-directory", "fixture", 0)); err != nil {
		t.Fatal(err)
	}
}

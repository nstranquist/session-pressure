package sessionpressure

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestRemoteSourceManifestRoundTrip(t *testing.T) {
	lines := map[string]remoteSourceLine{
		"a/b c.go": {hash: strings.Repeat("ab", 32), mode: 0o644},
		"run.sh":   {hash: strings.Repeat("cd", 32), mode: 0o755},
	}
	body := renderRemoteSourceManifest(lines)
	got, err := parseRemoteSourceManifest(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["a/b c.go"] != lines["a/b c.go"] || got["run.sh"] != lines["run.sh"] {
		t.Fatalf("round trip = %v", got)
	}
	if !strings.Contains(string(body), strings.Repeat("ab", 32)+" 0644 a/b c.go\n") {
		t.Fatalf("manifest = %q", body)
	}
	if _, err := parseRemoteSourceManifest(strings.NewReader("short\n")); err == nil {
		t.Fatal("a malformed line must fail")
	}
}

func TestSafeRemoteSourceRel(t *testing.T) {
	for _, ok := range []string{"go.mod", "a/b.go", "a/b c.go", "a/.hidden"} {
		if !safeRemoteSourceRel(ok) {
			t.Fatalf("%q must be safe", ok)
		}
	}
	for _, bad := range []string{"", "/abs", "../x", "a/../b", "a//b", "./a", "a\nb", `a\b`, " lead", remoteSourceControlDir + "/manifest"} {
		if safeRemoteSourceRel(bad) {
			t.Fatalf("%q must be unsafe", bad)
		}
	}
}

func TestPlanRemoteSourceDelta(t *testing.T) {
	h := func(c string) string { return strings.Repeat(c, 64) }
	entries := []remoteSourceEntry{{rel: "a", hash: h("1"), mode: 0o644}, {rel: "b", hash: h("2"), mode: 0o755}, {rel: "c", hash: h("3"), mode: 0o644}}
	base := map[string]remoteSourceLine{"a": {h("1"), 0o644}, "b": {h("2"), 0o644}, "gone": {h("9"), 0o644}}
	send, removed := planRemoteSourceDelta(entries, base)
	// b changed mode only, c is new, gone was removed.
	if len(send) != 2 || entries[send[0]].rel != "b" || entries[send[1]].rel != "c" || len(removed) != 1 || removed[0] != "gone" {
		t.Fatalf("send %v removed %v", send, removed)
	}
	if send, _ := planRemoteSourceDelta(entries, nil); len(send) != 3 {
		t.Fatalf("no base must send everything: %v", send)
	}
}

// deltaBox runs the real box script locally: ssh is "sh -c".
func deltaBox(t *testing.T) func(string) *exec.Cmd {
	t.Helper()
	for _, tool := range []string{"flock", "sha256sum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available (the box has GNU coreutils and util-linux)", tool)
		}
	}
	if out, err := exec.Command("tar", "--version").Output(); err != nil || !strings.Contains(string(out), "GNU tar") {
		t.Skip("GNU tar not available")
	}
	box := t.TempDir()
	oldRoot, oldLock := remoteSourceBaseRoot, remoteSourceTransferLock
	remoteSourceBaseRoot, remoteSourceTransferLock = filepath.Join(box, "pressure-base"), filepath.Join(box, "input-transfer.lock")
	t.Cleanup(func() { remoteSourceBaseRoot, remoteSourceTransferLock = oldRoot, oldLock })
	return func(command string) *exec.Cmd { return exec.Command("sh", "-c", command) }
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// treeFiles returns rel -> content for every regular file under root.
func treeFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		body, err := os.ReadFile(path)
		out[filepath.ToSlash(rel)] = string(body)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func sameTree(t *testing.T, label string, got, want map[string]string) {
	t.Helper()
	var diff []string
	for k, v := range want {
		if got[k] != v {
			diff = append(diff, "want "+k)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			diff = append(diff, "extra "+k)
		}
	}
	sort.Strings(diff)
	if len(diff) > 0 {
		t.Fatalf("%s: stage differs from source: %v", label, diff)
	}
}

func TestRemoteSourceDeltaEndToEnd(t *testing.T) {
	ssh := deltaBox(t)
	common := t.TempDir()
	module := filepath.Join(common, "mod")
	writeTree(t, module, map[string]string{"go.mod": "module example.test/mod\n", "a.go": "package mod\n", "sub/b.go": "package sub\n", "sub/gone.go": "package sub\n"})
	modules := []string{module}
	state := t.TempDir()
	jobs := t.TempDir()
	upload := func(label, stateDir string) remoteSourceUpload {
		t.Helper()
		remote := filepath.Join(jobs, label)
		up, err := uploadRemoteSourcesDelta(ssh, os.Stderr, stateDir, "box", remote, "pressure-"+label, common, modules, remoteScopeModules)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		sameTree(t, label, treeFiles(t, filepath.Join(remote, "source")), treeFiles(t, common))
		if _, err := os.Stat(filepath.Join(remote, ".incoming")); !os.IsNotExist(err) {
			t.Fatalf("%s: incoming directory left behind", label)
		}
		return up
	}

	if up := upload("first", state); up.mode != remoteUploadSeed || up.files != 4 {
		t.Fatalf("first upload = %+v, want a 4-file seed", up)
	}

	// One edit, one new file, one deletion: two files cross the wire.
	writeTree(t, module, map[string]string{"a.go": "package mod // edited\n", "sub/new.go": "package sub\n"})
	if err := os.Remove(filepath.Join(module, "sub", "gone.go")); err != nil {
		t.Fatal(err)
	}
	if up := upload("second", state); up.mode != remoteUploadDeltaKnown || up.files != 2 {
		t.Fatalf("second upload = %+v, want a 2-file known delta", up)
	}
	// An earlier stage is a hard-link snapshot; later base changes must not
	// reach it.
	secondStage := treeFiles(t, filepath.Join(jobs, "second", "source"))
	if _, err := os.Stat(filepath.Join(remoteSourceBaseRoot)); err != nil {
		t.Fatal(err)
	}

	// Another caller (its own state dir) moves the base; this caller's
	// remembered base is now stale and the box manifest is used instead.
	other := t.TempDir()
	writeTree(t, module, map[string]string{"sub/b.go": "package sub // other caller\n"})
	if up := upload("other", other); up.mode != remoteUploadDeltaListed || up.files != 1 {
		t.Fatalf("other caller upload = %+v, want a 1-file listed delta", up)
	}
	writeTree(t, module, map[string]string{"a.go": "package mod // third\n"})
	if up := upload("third", state); up.mode != remoteUploadDeltaListed || up.files != 1 {
		t.Fatalf("stale caller upload = %+v, want a 1-file listed delta", up)
	}
	sameTree(t, "second stage after later uploads", treeFiles(t, filepath.Join(jobs, "second", "source")), secondStage)

	// A corrupted base fails verification, is dropped, and the upload
	// reseeds instead of building a stage from bad bytes.
	bases, _ := filepath.Glob(filepath.Join(remoteSourceBaseRoot, "*.digest"))
	if len(bases) != 1 {
		t.Fatalf("bases = %v", bases)
	}
	base := strings.TrimSuffix(bases[0], ".digest")
	if err := os.WriteFile(filepath.Join(base, "mod", "go.mod"), []byte("corrupt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTree(t, module, map[string]string{"a.go": "package mod // fourth\n"})
	if up := upload("fourth", state); up.mode != remoteUploadSeed {
		t.Fatalf("upload over a corrupt base = %+v, want a seed", up)
	}
}

func TestRemoteSourceDeltaKeepsFileModes(t *testing.T) {
	ssh := deltaBox(t)
	common := t.TempDir()
	module := filepath.Join(common, "mod")
	writeTree(t, module, map[string]string{"go.mod": "module example.test/mod\n", "run.sh": "#!/bin/sh\n"})
	state, jobs := t.TempDir(), t.TempDir()
	if _, err := uploadRemoteSourcesDelta(ssh, os.Stderr, state, "box", filepath.Join(jobs, "a"), "a", common, []string{module}, remoteScopeModules); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(module, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	up, err := uploadRemoteSourcesDelta(ssh, os.Stderr, state, "box", filepath.Join(jobs, "b"), "b", common, []string{module}, remoteScopeModules)
	if err != nil {
		t.Fatal(err)
	}
	if up.mode != remoteUploadDeltaKnown || up.files != 1 {
		t.Fatalf("mode-only change = %+v, want a 1-file delta", up)
	}
	info, err := os.Stat(filepath.Join(jobs, "b", "source", "mod", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("executable bit lost: %v", info.Mode())
	}
}

func TestRemoteSourceStatCacheSkipsUnchangedFiles(t *testing.T) {
	ssh := deltaBox(t)
	common := t.TempDir()
	module := filepath.Join(common, "mod")
	writeTree(t, module, map[string]string{"go.mod": "module example.test/mod\n", "a.go": "package mod\n", "b.go": "package mod\n"})
	old := time.Now().Add(-time.Hour)
	age := func(name string) {
		t.Helper()
		if err := os.Chtimes(filepath.Join(module, name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"go.mod", "a.go", "b.go"} {
		age(name)
	}
	state, jobs := t.TempDir(), t.TempDir()
	run := func(label string) remoteSourceUpload {
		t.Helper()
		up, err := uploadRemoteSourcesDelta(ssh, os.Stderr, state, "box", filepath.Join(jobs, label), label, common, []string{module}, remoteScopeModules)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		return up
	}
	if up := run("cold"); up.hashed != 3 {
		t.Fatalf("cold upload hashed %d files, want 3", up.hashed)
	}
	if up := run("warm"); up.hashed != 0 || up.files != 0 {
		t.Fatalf("warm upload = %+v, want nothing hashed or sent", up)
	}
	// A recent edit is inside the racy window, so it is always rehashed.
	writeTree(t, module, map[string]string{"a.go": "package xx\n"})
	if up := run("edited"); up.hashed != 1 || up.files != 1 {
		t.Fatalf("edited upload = %+v, want 1 hashed and 1 sent", up)
	}
	sameTree(t, "edited", treeFiles(t, filepath.Join(jobs, "edited", "source")), treeFiles(t, common))
}

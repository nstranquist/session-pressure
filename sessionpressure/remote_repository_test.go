package sessionpressure

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRemoteNestedModuleRetainsRepositoryFixtures(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("fixture git init: %s: %v", out, err)
	}
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("nested/go.mod", "module example.test/nested\ngo 1.26\n")
	write("nested/main.go", "package main")
	write(".claude/hooks/toolguard.sh", "fixture")
	write(".ndev/commit-delivery.yaml", "policy")
	write("cmd/fixture.go", "fixture")
	write(".env", "secret")
	write(".tmp/generated", "cache")
	write(".gitignore", "ignored\nnested/generated-binary\nnested/.venv/\n")
	write("ignored", "ignored")
	write("nested/generated-binary", "not source")
	write("nested/.venv/cache", "not source")
	modules, common, err := remoteModuleClosure(filepath.Join(root, "nested"))
	if err != nil || common != root {
		t.Fatalf("common=%q err=%v", common, err)
	}
	var out bytes.Buffer
	if err := writeRemoteSources(&out, modules, common, remoteScopeRepository); err != nil {
		t.Fatal(err)
	}
	found := remoteUploadNames(t, &out)
	for _, name := range []string{"nested/go.mod", ".claude/hooks/toolguard.sh", ".ndev/commit-delivery.yaml", "cmd/fixture.go"} {
		if !found[name] {
			t.Errorf("missing repository input %s", name)
		}
	}
	for _, name := range []string{".env", ".tmp/generated", "ignored", ".git/HEAD", "nested/generated-binary", "nested/.venv/cache"} {
		if found[name] {
			t.Errorf("excluded input %s uploaded", name)
		}
	}
}

// A build sends only the module closure: repository files outside every
// module cannot be compiler inputs, and shipping them cost ~200 s per build.
func TestRemoteBuildScopeSendsOnlyModuleFiles(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("fixture git init: %s: %v", out, err)
	}
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("nested/go.mod", "module example.test/nested\ngo 1.26\nreplace example.test/lib => ../lib\n")
	write("nested/main.go", "package main")
	write("nested/assets/embed.txt", "embedded")
	write("lib/go.mod", "module example.test/lib\ngo 1.26\n")
	write("lib/lib.go", "package lib")
	write("docs/big.md", "not a compiler input")
	write("nestedness/decoy.go", "prefix sibling, not inside nested/")
	write("go.work", "go 1.26\nuse ./nested\n")
	modules, common, err := remoteModuleClosure(filepath.Join(root, "nested"))
	if err != nil || common != root {
		t.Fatalf("common=%q err=%v", common, err)
	}
	var out bytes.Buffer
	if err := writeRemoteSources(&out, modules, common, remoteInputScopeFor("build")); err != nil {
		t.Fatal(err)
	}
	found := remoteUploadNames(t, &out)
	for _, name := range []string{"nested/go.mod", "nested/main.go", "nested/assets/embed.txt", "lib/go.mod", "lib/lib.go"} {
		if !found[name] {
			t.Errorf("missing module input %s", name)
		}
	}
	for _, name := range []string{"docs/big.md", "nestedness/decoy.go", "go.work"} {
		if found[name] {
			t.Errorf("build upload carried non-module file %s", name)
		}
	}
	size, err := remoteSourceSize(modules, common, remoteScopeModules, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	full, err := remoteSourceSize(modules, common, remoteScopeRepository, 1<<20)
	if err != nil || full <= size {
		t.Fatalf("module scope %d must be smaller than repository scope %d (err %v)", size, full, err)
	}
	if remoteInputScopeFor("test") != remoteScopeRepository || remoteInputScopeFor("vet") != remoteScopeRepository {
		t.Fatal("tests and vet must keep repository fixtures")
	}
}

// Committed generated assets are source inputs even when an ignore pattern
// also covers them. Untracked artifacts and credentials must stay excluded.
func TestRemoteTrackedEmbedAssetsSurviveSourceSnapshot(t *testing.T) {
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	run("init", "-q")
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/embed\ngo 1.26\n")
	write("explorer/embed.go", "package explorer\nimport \"embed\"\n//go:embed all:dist\nvar Assets embed.FS\n")
	write(".gitignore", "explorer/dist/\n")
	write("explorer/dist/index.html", "committed")
	write("explorer/dist/.env", "credential fixture")
	write("explorer/dist/private.key", "credential fixture")
	write("explorer/dist/link-target", "regular")
	run("add", "-f", "go.mod", "explorer/embed.go", ".gitignore", "explorer/dist/index.html", "explorer/dist/.env", "explorer/dist/private.key")
	write("explorer/dist/ignored.js", "ignored")
	write("other/dist/untracked.js", "untracked artifact")
	if err := os.Symlink("index.html", filepath.Join(root, "explorer/dist/link")); err != nil {
		t.Fatal(err)
	}
	run("add", "-f", "explorer/dist/link")
	for _, scope := range []remoteInputScope{remoteScopeRepository, remoteScopeModules} {
		var out bytes.Buffer
		if err := writeRemoteSources(&out, []string{root}, root, scope); err != nil {
			t.Fatal(err)
		}
		found := remoteUploadNames(t, &out)
		for _, name := range []string{"go.mod", "explorer/embed.go", "explorer/dist/index.html"} {
			if !found[name] {
				t.Errorf("scope %v missing committed compiler input %s", scope, name)
			}
		}
		for _, name := range []string{"explorer/dist/.env", "explorer/dist/private.key", "explorer/dist/ignored.js", "explorer/dist/link-target", "explorer/dist/link", "other/dist/untracked.js"} {
			if found[name] {
				t.Errorf("scope %v uploaded excluded input %s", scope, name)
			}
		}
		size, err := remoteSourceSize([]string{root}, root, scope, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		var expected int64
		err = walkRemoteSources([]string{root}, root, scope, func(_, _ string, info os.FileInfo) error { expected += info.Size(); return nil })
		if err != nil || size != expected {
			t.Fatalf("size=%d expected=%d err=%v", size, expected, err)
		}
	}
}

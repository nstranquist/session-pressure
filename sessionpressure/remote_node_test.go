package sessionpressure

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteNodeVerificationShapes(t *testing.T) {
	yes := [][]string{
		{"npx", "vitest", "run"},
		{"pnpm", "exec", "vitest", "run", "src/a.test.ts"},
		{"npx", "tsc", "--noEmit"},
		{"npx", "eslint", "."},
		{"npm", "test"},
		{"pnpm", "run", "typecheck"},
		{"pnpm", "lint"},
		{"vitest", "run"},
	}
	no := [][]string{
		{"npx", "vitest", "-u"},
		{"npx", "eslint", "--fix", "."},
		{"npx", "prettier", "--write", "."},
		{"npx", "vitest", "watch"},
		{"npx", "tsc"},
		{"npm", "run", "build"},
		{"npm", "ci"},
		{"npx", "vite", "build"},
		{"node", "script.js"},
	}
	for _, argv := range yes {
		if !remoteNodeVerification(argv) {
			t.Errorf("%q should be remote-eligible verification", argv)
		}
	}
	for _, argv := range no {
		if remoteNodeVerification(argv) {
			t.Errorf("%q must stay local", argv)
		}
	}
}

func TestRemoteNodeCommandRefusesCompounds(t *testing.T) {
	cwd := t.TempDir()
	if err := os.Mkdir(filepath.Join(cwd, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	argv, dir, reason := remoteNodeCommand(cwd, []string{"bash", "-c", "cd web && npx vitest run"})
	if reason != "" || dir != filepath.Join(cwd, "web") || strings.Join(argv, " ") != "npx vitest run" {
		t.Fatalf("cd + single check: argv=%q dir=%q reason=%q", argv, dir, reason)
	}
	// The 2026-11-15 hook shape: the edit must run in this checkout.
	if _, _, reason := remoteNodeCommand(cwd, []string{"bash", "-c", "python3 edit.py && npx vitest run"}); reason != "compound shell command" {
		t.Fatalf("compound accepted: %q", reason)
	}
}

func TestMatchPin(t *testing.T) {
	pins := []string{"26.13.0", "24.22.2"}
	for declared, want := range map[string]string{
		"24": "24.22.2", "v24": "24.22.2", "24.x": "24.22.2", "24.22.2": "24.22.2", "v26.13.0": "26.13.0",
		"22.14.0": "", "24.18.0": "", "lts/*": "", "": "", ">=24": "",
	} {
		if got := matchPin(declared, pins); got != want {
			t.Errorf("matchPin(%q) = %q, want %q", declared, got, want)
		}
	}
}

func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestPlanRemoteNodeWork(t *testing.T) {
	previous := localToolVersion
	t.Cleanup(func() { localToolVersion = previous })
	localToolVersion = func(tool string) string {
		return map[string]string{"node": "24.22.2", "pnpm": "11.22.0"}[tool]
	}

	root := gitRepo(t, map[string]string{
		"package.json":          `{"packageManager":"pnpm@11.15.1+sha512.abc"}`,
		"pnpm-lock.yaml":        "lockfileVersion: '9.0'\n",
		".node-version":         "24\n",
		"apps/web/package.json": `{"name":"web"}`,
	})
	plan, reason := planRemoteNodeWork(filepath.Join(root, "apps", "web"), []string{"pnpm", "exec", "vitest", "run"})
	if reason != "" {
		t.Fatalf("declined: %s", reason)
	}
	if plan.root != root || plan.workdir != filepath.Join("apps", "web") || plan.installDir != "." || plan.manager != "pnpm" || plan.nodeVersion != "24.22.2" || plan.pnpmVersion != "11.15.1" {
		t.Fatalf("plan: %+v", plan)
	}
	command := remoteNodeRunnerCommand(plan, "0123")
	for _, want := range []string{
		"/srv/build/opt/node-v24.22.2-linux-x64/bin",
		"/srv/build/opt/pnpm-11.15.1/bin",
		"pnpm install --frozen-lockfile --prefer-offline --store-dir /srv/build/cache/pnpm-store",
		"HOME='/srv/build/candidates/pressure-0123/private/home'",
		"npm_config_cache='/srv/build/candidates/pressure-0123/private/npm-cache'",
		"cd \"$R/apps/web\" && exec env 'pnpm' 'exec' 'vitest' 'run'",
	} {
		if !strings.Contains(command, want) {
			t.Errorf("runner command lacks %q:\n%s", want, command)
		}
	}

	// An unpinned pnpm declines before upload instead of failing remotely.
	unpinned := gitRepo(t, map[string]string{
		"package.json":   `{"packageManager":"pnpm@10.24.0"}`,
		"pnpm-lock.yaml": "lockfileVersion: '9.0'\n",
	})
	if _, reason := planRemoteNodeWork(unpinned, []string{"pnpm", "test"}); !strings.Contains(reason, `pnpm "10.24.0" is not pinned`) {
		t.Fatalf("unpinned pnpm reason: %q", reason)
	}
	// No declared Node: the Mac's own version selects the pin.
	npmRepo := gitRepo(t, map[string]string{"package.json": `{}`, "package-lock.json": "{}"})
	plan, reason = planRemoteNodeWork(npmRepo, []string{"npm", "test"})
	if reason != "" || plan.manager != "npm" || plan.nodeVersion != "24.22.2" || !strings.Contains(remoteNodeRunnerCommand(plan, "1"), "npm ci --no-audit --no-fund --prefer-offline") {
		t.Fatalf("npm plan: %+v reason=%q", plan, reason)
	}
	// A lockfile for the other manager is refused.
	if _, reason := planRemoteNodeWork(npmRepo, []string{"pnpm", "test"}); reason == "" {
		t.Fatal("pnpm command against an npm lockfile accepted")
	}
	// A script that calls a Mac-only tool stays local, also one level deep.
	macRepo := gitRepo(t, map[string]string{
		"package.json":      `{"scripts":{"test":"vitest run && npm run test:ios","test:ios":"xcrun simctl list"}}`,
		"package-lock.json": "{}",
	})
	if _, reason := planRemoteNodeWork(macRepo, []string{"npm", "test"}); reason != "package script uses Mac-only xcrun" {
		t.Fatalf("mac-only script reason: %q", reason)
	}
	// No lockfile, no remote.
	bare := gitRepo(t, map[string]string{"package.json": `{}`})
	if _, reason := planRemoteNodeWork(bare, []string{"npx", "vitest", "run"}); !strings.Contains(reason, "lockfile") {
		t.Fatalf("lockfile-less reason: %q", reason)
	}
}

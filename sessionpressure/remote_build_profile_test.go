package sessionpressure

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteBuildProfileMatchesTheMacCgoSetting(t *testing.T) {
	for _, tc := range []struct{ op, cgo, want string }{
		{"build", "", remoteProfileDarwinCgo}, // the Mac default is cgo on
		{"build", "1", remoteProfileDarwinCgo},
		{"build", "0", remoteProfileDarwinNoCgo},
		{"test", "", remoteProfileLinuxNative},
		{"vet", "0", remoteProfileLinuxNative},
	} {
		if got := remoteBuildProfile(tc.op, tc.cgo); got != tc.want {
			t.Errorf("profile(%s, CGO_ENABLED=%q) = %s, want %s", tc.op, tc.cgo, got, tc.want)
		}
	}
	cgo := remoteBuildEnvironment(remoteProfileDarwinCgo, "/srv/build/candidates/pressure-j")
	for _, want := range []string{"GOOS=darwin", "GOARCH=arm64", "CGO_ENABLED=1", "CC=\"" + remoteDarwinCC + " -B${" + remoteDarwinToolsVar + ":?}/\"", "PRESSURE_MACOS_SDK='" + remoteDarwinSDK + "'", "CGO_LDFLAGS="} {
		if !strings.Contains(cgo, want) {
			t.Errorf("cgo environment %q lacks %q", cgo, want)
		}
	}
	// Go keeps its default link mode; a forced internal link broke SQLite.
	if strings.Contains(cgo, "linkmode") {
		t.Errorf("cgo environment must not force a link mode: %q", cgo)
	}
	if env := remoteBuildEnvironment(remoteProfileDarwinNoCgo, "/j"); !strings.Contains(env, "CGO_ENABLED=0") || strings.Contains(env, "CC=") {
		t.Errorf("cgo-free environment = %q", env)
	}
	if remoteBuildEnvironment(remoteProfileLinuxNative, "/j") != "" {
		t.Error("only the cgo build profile adds environment and a toolchain preflight")
	}
	for _, profile := range []string{remoteProfileLinuxNative, remoteProfileDarwinNoCgo} {
		if preflight, err := remoteBuildPreflight(profile, "unreviewed-host", nil, "/j"); err != nil || preflight != "" {
			t.Errorf("%s preflight = %q, %v; want none", profile, preflight, err)
		}
	}
}

func TestRemoteBuildPreflightBindsCompilerPinToHost(t *testing.T) {
	pins := map[string]string{
		"runner-2":  "7e85456e9acd3ebddb0289d28078291562534a8bcb3de5ac7e3105406dc81d73",
		"192.0.2.2": "7e85456e9acd3ebddb0289d28078291562534a8bcb3de5ac7e3105406dc81d73",
		"runner-3":  "d2f4a6fc1a2d831e28cff057bd02190b5e37ef24c4b9ab3344b3f3a42ff04ec8",
		"192.0.2.3": "d2f4a6fc1a2d831e28cff057bd02190b5e37ef24c4b9ab3344b3f3a42ff04ec8",
	}
	for _, tc := range []struct{ host, digest string }{
		{"runner-2", "7e85456e9acd3ebddb0289d28078291562534a8bcb3de5ac7e3105406dc81d73"},
		{"192.0.2.2", "7e85456e9acd3ebddb0289d28078291562534a8bcb3de5ac7e3105406dc81d73"},
		{"runner-3", "d2f4a6fc1a2d831e28cff057bd02190b5e37ef24c4b9ab3344b3f3a42ff04ec8"},
		{"192.0.2.3", "d2f4a6fc1a2d831e28cff057bd02190b5e37ef24c4b9ab3344b3f3a42ff04ec8"},
	} {
		preflight, err := remoteBuildPreflight(remoteProfileDarwinCgo, tc.host, pins, "/j")
		if err != nil || !strings.Contains(preflight, shellQuote(tc.digest)) {
			t.Errorf("%s preflight = %q, %v; want host's reviewed pin", tc.host, preflight, err)
		}
	}
	for _, host := range []string{"", "runner-1", "runner-4"} {
		if preflight, err := remoteBuildPreflight(remoteProfileDarwinCgo, host, pins, "/j"); err == nil || preflight != "" {
			t.Errorf("unreviewed host %q preflight = %q, %v; want refusal", host, preflight, err)
		}
	}
}

// Opt-in fleet check: use the production preflight/environment on each enrolled
// host and retain a Darwin cgo artifact for a separate Mac runtime check.
func TestRemoteBuildPreflightOnEnrolledRunner(t *testing.T) {
	host := os.Getenv("NDEV_TEST_ENROLLED_RUNNER")
	if host == "" {
		t.Skip("requires an explicit enrolled runner and retained artifact path")
	}
	artifact := os.Getenv("NDEV_TEST_DARWIN_ARTIFACT")
	if artifact == "" || !filepath.IsAbs(artifact) {
		t.Fatal("NDEV_TEST_DARWIN_ARTIFACT must be absolute")
	}
	job := t.TempDir()
	preflight, err := remoteBuildPreflight(remoteProfileDarwinCgo, host, enrolledPins(t, host), job)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(job, "main.go")
	if err := os.WriteFile(source, []byte("package main\n/*\nint answer(void) { return 42; }\n*/\nimport \"C\"\nimport \"fmt\"\nfunc main() { fmt.Println(C.answer()) }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := preflight + "env GOWORK=off GOFLAGS=" + remoteBuildEnvironment(remoteProfileDarwinCgo, job) + " go build -o " + shellQuote(artifact) + " " + shellQuote(source)
	if out, err := exec.Command("bash", "-c", command).CombinedOutput(); err != nil {
		t.Fatalf("%s production Darwin cgo profile: %v\n%s", host, err, out)
	}
}

// The preflight runs as shell on the runner. Execute it against a temporary
// compiler and SDK: a match runs the command, a mismatch refuses with the
// typed contract and the policy-denied exit code.
func TestRemoteToolchainPreflightRefusesWithTheContract(t *testing.T) {
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("sha256sum is the runner's tool; this host lacks it")
	}
	dir := t.TempDir()
	compiler := filepath.Join(dir, "cc")
	if err := os.WriteFile(compiler, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("#!/bin/sh\n"))
	digest := hex.EncodeToString(sum[:])
	sdk := filepath.Join(dir, "sdk")
	if err := os.MkdirAll(filepath.Join(sdk, "usr", "include"), 0o755); err != nil {
		t.Fatal(err)
	}
	strip := filepath.Join(dir, "llvm-strip")
	if err := os.WriteFile(strip, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(dir, "job", "tools")
	shared := filepath.Join(dir, "shared", "tools")
	// A sentinel no path can contain: "ran" matched a home directory name in
	// the refusal text, which quotes the compiler path.
	const preflightPassed = "preflight-passed-7c1e"
	run := func(preflight string) (int, string) {
		out, err := exec.Command("bash", "-c", preflight+"echo "+preflightPassed).CombinedOutput()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), string(out)
		}
		if err != nil {
			t.Fatal(err)
		}
		return 0, string(out)
	}
	if code, out := run(remoteToolchainPreflight(compiler, digest, sdk, strip, shared, tools)); code != 0 || strings.TrimSpace(out) != preflightPassed {
		t.Fatalf("pinned toolchain: exit %d, output %q", code, out)
	}
	// clang -B finds the shared strip, which must be the Mach-O strip.
	if target, err := os.Readlink(filepath.Join(shared, "strip")); err != nil || target != strip {
		t.Fatalf("shared strip = %q, %v; want a link to %s", target, err, strip)
	}
	toolsDir := func(shared string) string {
		t.Helper()
		out, err := exec.Command("bash", "-c", remoteToolchainPreflight(compiler, digest, sdk, strip, shared, tools)+"echo \"$"+remoteDarwinToolsVar+"\"").CombinedOutput()
		if err != nil {
			t.Fatalf("preflight: %v\n%s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	// A stable path keeps CC, and so every cgo action ID, the same across
	// jobs. A second job reuses the link and leaves no temp file.
	if got := toolsDir(shared); got != shared {
		t.Fatalf("tool directory = %q, want the shared %q", got, shared)
	}
	if entries, err := os.ReadDir(shared); err != nil || len(entries) != 1 {
		t.Fatalf("shared directory entries = %v, %v; want only strip", entries, err)
	}
	// A wrong link is replaced, not trusted.
	if err := os.Remove(filepath.Join(shared, "strip")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/usr/bin/strip", filepath.Join(shared, "strip")); err != nil {
		t.Fatal(err)
	}
	if got := toolsDir(shared); got != shared {
		t.Fatalf("tool directory after a wrong link = %q, want %q", got, shared)
	}
	if target, _ := os.Readlink(filepath.Join(shared, "strip")); target != strip {
		t.Fatalf("shared strip after repair = %q, want %s", target, strip)
	}
	// A shared directory the job cannot create falls back to the job's own.
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := toolsDir(filepath.Join(blocked, "tools")); got != tools {
		t.Fatalf("tool directory without a usable shared one = %q, want the job's %q", got, tools)
	}
	if target, err := os.Readlink(filepath.Join(tools, "strip")); err != nil || target != strip {
		t.Fatalf("job strip = %q, %v; want a link to %s", target, err, strip)
	}
	for name, preflight := range map[string]string{
		"compiler digest": remoteToolchainPreflight(compiler, strings.Repeat("0", 64), sdk, strip, shared, tools),
		"missing sdk":     remoteToolchainPreflight(compiler, digest, filepath.Join(dir, "absent"), strip, shared, tools),
		"missing strip":   remoteToolchainPreflight(compiler, digest, sdk, filepath.Join(dir, "absent-strip"), shared, tools),
	} {
		code, out := run(preflight)
		if code != workExitPolicyDenied || strings.Contains(out, preflightPassed) || !strings.Contains(out, WorkRefusalMarker+" kind=remote_profile: ") {
			t.Errorf("%s: exit %d, output %q; want refusal exit %d with the contract", name, code, out, workExitPolicyDenied)
		}
	}
}

// enrolledPins reads the reviewed pin for an opt-in enrolled-runner test from
// NDEV_TEST_DARWIN_CC_SHA256 (the value in that host's remote-work.json).
func enrolledPins(t *testing.T, host string) map[string]string {
	t.Helper()
	digest := os.Getenv("NDEV_TEST_DARWIN_CC_SHA256")
	if digest == "" {
		t.Fatal("NDEV_TEST_DARWIN_CC_SHA256 must name the enrolled runner's reviewed compiler pin")
	}
	return map[string]string{strings.ToLower(host): digest}
}

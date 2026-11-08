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
	for _, want := range []string{"GOOS=darwin", "GOARCH=arm64", "CGO_ENABLED=1", "CC='" + remoteDarwinCC + " -B/srv/build/candidates/pressure-j/tools/'", "PRESSURE_MACOS_SDK='" + remoteDarwinSDK + "'", "CGO_LDFLAGS="} {
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
	if remoteBuildEnvironment(remoteProfileLinuxNative, "/j") != "" || remoteBuildPreflight(remoteProfileLinuxNative, "/j") != "" || remoteBuildPreflight(remoteProfileDarwinNoCgo, "/j") != "" {
		t.Error("only the cgo build profile adds environment and a toolchain preflight")
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
	run := func(preflight string) (int, string) {
		out, err := exec.Command("bash", "-c", preflight+"echo ran").CombinedOutput()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), string(out)
		}
		if err != nil {
			t.Fatal(err)
		}
		return 0, string(out)
	}
	if code, out := run(remoteToolchainPreflight(compiler, digest, sdk, strip, tools)); code != 0 || strings.TrimSpace(out) != "ran" {
		t.Fatalf("pinned toolchain: exit %d, output %q", code, out)
	}
	// clang -B finds the job's strip, which must be the Mach-O strip.
	if target, err := os.Readlink(filepath.Join(tools, "strip")); err != nil || target != strip {
		t.Fatalf("job strip = %q, %v; want a link to %s", target, err, strip)
	}
	for name, preflight := range map[string]string{
		"compiler digest": remoteToolchainPreflight(compiler, strings.Repeat("0", 64), sdk, strip, tools),
		"missing sdk":     remoteToolchainPreflight(compiler, digest, filepath.Join(dir, "absent"), strip, tools),
		"missing strip":   remoteToolchainPreflight(compiler, digest, sdk, filepath.Join(dir, "absent-strip"), tools),
	} {
		code, out := run(preflight)
		if code != workExitPolicyDenied || strings.Contains(out, "ran") || !strings.Contains(out, WorkRefusalMarker+" kind=remote_profile: ") {
			t.Errorf("%s: exit %d, output %q; want refusal exit %d with the contract", name, code, out, workExitPolicyDenied)
		}
	}
}

package sessionpressure

import (
	"fmt"
	"strings"
)

// The runner's Darwin build profile. A remote `go build` must produce what the
// Mac would: darwin/arm64, with the Mac's cgo setting. Until 2026-11-08 the
// route forced CGO_ENABLED=0 and refused every cgo build, so delivery verify
// gates that compile cgo packages (mattn sqlite, IOKit) paused as failures.
//
// The cgo profile is the one the commit-delivery native producer proved in
// production (recipe darwin-arm64-clang-cgo-external-v1, receipt 4a4ac506):
// LLVM clang 18 targeting arm64-apple-macosx13.0 against a pinned macOS SDK,
// linked by ld64.lld. Go keeps its default link mode, as on the Mac: external
// for non-standard cgo packages. Never force -linkmode=internal: that profile
// broke SQLite parameter binding while passing a version probe.
//
// Unlike the producer (-s -w), a Mac build keeps its debug information, and
// Go's darwin external link then runs `strip -S` and dsymutil. The runner's
// GNU strip cannot read Mach-O, and clang finds /usr/bin/strip before PATH,
// so clang gets a tool directory whose strip is LLVM's, passed with -B.
// Probe 2026-11-08: the cgo binary runs on the Mac and keeps its __DWARF
// segments. The directory holds only that link; see remoteDarwinSharedTools.
const (
	remoteDarwinCC     = "/srv/build/opt/pressure-darwin-clang"
	remoteDarwinSDK    = "/srv/build/opt/macos-sdk-cgo"
	remoteDarwinCFLAGS = "-O2 -g -Wno-availability -Wno-nullability-completeness"
	remoteDarwinStrip  = "/usr/lib/llvm-18/bin/llvm-strip"
)

// remoteDarwinSharedTools is the runner-wide tool directory for clang -B.
// Its path is part of CC, and Go puts CC in the action ID of every package of
// a cgo build. A per-job path therefore made each job rebuild the standard
// library and every cgo package from scratch: on the Linux gate runner the
// catalog-providers build took 53.9 s with a new path and 1.0 s with a
// reused one, also from another source directory (WORK-1480, 2026-11-21).
// The preflight makes the link atomically; it changes nothing else.
const remoteDarwinSharedTools = "/srv/build/cache/pressure-darwin-tools"

// remoteDarwinToolsVar is the shell variable the preflight sets to the tool
// directory it verified. CC reads it, so a job that cannot use the shared
// directory still builds, only without the cross-job cache.
const remoteDarwinToolsVar = "NCD_PRESSURE_DARWIN_TOOLS"

// remoteDarwinTools is the job's private fallback tool directory for clang -B.
func remoteDarwinTools(job string) string { return job + "/tools" }

// Remote build profiles, recorded in remote_work_finished telemetry.
const (
	remoteProfileLinuxNative = "linux-native"
	remoteProfileDarwinCgo   = "darwin-arm64-cgo"
	remoteProfileDarwinNoCgo = "darwin-arm64-nocgo"
)

// remoteBuildProfile names the runner profile for a Go operation. cgo is the
// caller's CGO_ENABLED; remoteProfileReason has already refused any value
// other than "", "0", and "1". Unset is the Mac default, cgo on.
func remoteBuildProfile(operation, cgo string) string {
	switch {
	case operation != "build":
		return remoteProfileLinuxNative
	case cgo == "0":
		return remoteProfileDarwinNoCgo
	}
	return remoteProfileDarwinCgo
}

// remoteBuildEnvironment is the environment the runner adds to the command
// of the job rooted at job. The cgo profile clears every CGO_* flag it does
// not set, so a value in the runner's login environment cannot leak in.
func remoteBuildEnvironment(profile, job string) string {
	switch profile {
	case remoteProfileDarwinNoCgo:
		return " GOOS=darwin GOARCH=arm64 CGO_ENABLED=0"
	case remoteProfileDarwinCgo:
		// Double quotes: the preflight's tool directory expands at run time.
		return " GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 CC=\"" + remoteDarwinCC + " -B${" + remoteDarwinToolsVar + ":?}/\"" +
			" PRESSURE_MACOS_SDK=" + shellQuote(remoteDarwinSDK) +
			" CGO_CFLAGS=" + shellQuote(remoteDarwinCFLAGS) + " CGO_CPPFLAGS= CGO_CXXFLAGS= CGO_LDFLAGS="
	}
	return ""
}

// remoteBuildPreflight checks the pinned compiler and SDK before the command
// runs. A mismatch is a runner fault, not a build failure, so it exits with
// the policy-denied code and the refusal contract on stderr; callers such as
// commit delivery then hold instead of blaming the change.
func remoteBuildPreflight(profile, host string, pins map[string]string, job string) (string, error) {
	if profile != remoteProfileDarwinCgo {
		return "", nil
	}
	// Each enrolled runner has an independently reviewed compiler wrapper.
	// Bind the pin to the already selected endpoint (remote-work.json
	// darwin_compiler_sha256); never borrow another host's pin.
	endpoint := strings.ToLower(strings.TrimSpace(host))
	digest := pins[endpoint]
	if endpoint == "" || digest == "" {
		return "", fmt.Errorf("runner %q has no reviewed Darwin compiler pin in remote-work.json", host)
	}
	return remoteToolchainPreflight(remoteDarwinCC, digest, remoteDarwinSDK, remoteDarwinStrip, remoteDarwinSharedTools, remoteDarwinTools(job)), nil
}

// remoteToolchainPreflight verifies the pinned toolchain and sets
// remoteDarwinToolsVar. It uses the shared tool directory when its strip
// links to the pinned strip, making the link first if needed. The link is
// renamed into place, so a concurrent job never sees a missing strip. When
// the shared directory cannot be made (a pool user without write access),
// the job's private directory is used instead.
func remoteToolchainPreflight(compiler, digest, sdk, strip, shared, jobTools string) string {
	refuse := func(reason string) string {
		return fmt.Sprintf("{ echo %s >&2; exit %d; }", shellQuote(WorkRefusalMarker+" kind="+WorkRefusalRemoteProfile+": remote Go work blocked: "+reason), workExitPolicyDenied)
	}
	linked := func(dir string) string {
		return "test \"$(readlink " + shellQuote(dir+"/strip") + " 2>/dev/null)\" = " + shellQuote(strip)
	}
	tmp := shellQuote(shared+"/strip.") + "$$"
	return "{ printf '%s  %s\\n' " + shellQuote(digest) + " " + shellQuote(compiler) + " | sha256sum -c --status - || " +
		refuse("runner Darwin compiler "+compiler+" does not match its pinned SHA-256") + "; } && " +
		"{ test -d " + shellQuote(sdk+"/usr/include") + " || " + refuse("runner macOS SDK "+sdk+" is missing") + "; } && " +
		"{ test -x " + shellQuote(strip) + " || " + refuse("runner Mach-O strip "+strip+" is missing") + "; } && " +
		"{ " + linked(shared) + " || { mkdir -p " + shellQuote(shared) + " && ln -sf " + shellQuote(strip) + " " + tmp + " && mv -f " + tmp + " " + shellQuote(shared+"/strip") + "; } 2>/dev/null || rm -f " + tmp + " 2>/dev/null || :; } && " +
		"if " + linked(shared) + "; then " + remoteDarwinToolsVar + "=" + shellQuote(shared) + "; else " +
		"mkdir -p " + shellQuote(jobTools) + " && ln -sf " + shellQuote(strip) + " " + shellQuote(jobTools+"/strip") + " && " + remoteDarwinToolsVar + "=" + shellQuote(jobTools) + "; fi && "
}

package sessionpressure

import "fmt"

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
// so each job gets a private tool directory whose strip is LLVM's, passed to
// clang with -B. Probe 2026-11-08: the cgo binary runs on the Mac and keeps
// its __DWARF segments; no file on the runner changes.
const (
	remoteDarwinCC       = "/srv/build/opt/pressure-darwin-clang"
	remoteDarwinCCSHA256 = "7e85456e9acd3ebddb0289d28078291562534a8bcb3de5ac7e3105406dc81d73"
	remoteDarwinSDK      = "/srv/build/opt/macos-sdk-cgo"
	remoteDarwinCFLAGS   = "-O2 -g -Wno-availability -Wno-nullability-completeness"
	remoteDarwinStrip    = "/usr/lib/llvm-18/bin/llvm-strip"
)

// remoteDarwinTools is the job's private tool directory for clang -B.
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
		return " GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 CC=" + shellQuote(remoteDarwinCC+" -B"+remoteDarwinTools(job)+"/") +
			" PRESSURE_MACOS_SDK=" + shellQuote(remoteDarwinSDK) +
			" CGO_CFLAGS=" + shellQuote(remoteDarwinCFLAGS) + " CGO_CPPFLAGS= CGO_CXXFLAGS= CGO_LDFLAGS="
	}
	return ""
}

// remoteBuildPreflight checks the pinned compiler and SDK before the command
// runs. A mismatch is a runner fault, not a build failure, so it exits with
// the policy-denied code and the refusal contract on stderr; callers such as
// commit delivery then hold instead of blaming the change.
func remoteBuildPreflight(profile, job string) string {
	if profile != remoteProfileDarwinCgo {
		return ""
	}
	return remoteToolchainPreflight(remoteDarwinCC, remoteDarwinCCSHA256, remoteDarwinSDK, remoteDarwinStrip, remoteDarwinTools(job))
}

func remoteToolchainPreflight(compiler, digest, sdk, strip, tools string) string {
	refuse := func(reason string) string {
		return fmt.Sprintf("{ echo %s >&2; exit %d; }", shellQuote(WorkRefusalMarker+" kind="+WorkRefusalRemoteProfile+": remote Go work blocked: "+reason), workExitPolicyDenied)
	}
	return "{ printf '%s  %s\\n' " + shellQuote(digest) + " " + shellQuote(compiler) + " | sha256sum -c --status - || " +
		refuse("runner Darwin compiler "+compiler+" does not match its pinned SHA-256") + "; } && " +
		"{ test -d " + shellQuote(sdk+"/usr/include") + " || " + refuse("runner macOS SDK "+sdk+" is missing") + "; } && " +
		"{ test -x " + shellQuote(strip) + " || " + refuse("runner Mach-O strip "+strip+" is missing") + "; } && " +
		"mkdir -p " + shellQuote(tools) + " && ln -sf " + shellQuote(strip) + " " + shellQuote(tools+"/strip") + " && "
}

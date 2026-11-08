package sessionpressure

import "strings"

// remoteProfileReason protects commands whose local execution profile cannot
// be reproduced by the runner: native Linux tests and vet, and Darwin/arm64
// builds with or without cgo (remoteBuildEnvironment). A local CC or CGO_*
// value names a compiler setup the runner does not have, so it still
// declines; the runner's own pinned Darwin compiler replaces the default.
func remoteProfileReason(argv []string, getenv func(string) string) string {
	if len(argv) < 2 {
		return "not a Go operation"
	}
	for _, key := range []string{"GOFLAGS", "GOEXPERIMENT", "CC", "CXX", "CGO_CFLAGS", "CGO_CPPFLAGS", "CGO_CXXFLAGS", "CGO_LDFLAGS"} {
		if getenv(key) != "" {
			return key + " needs an explicit runner profile"
		}
	}
	for _, arg := range argv[2:] {
		for _, flag := range []string{"-modfile", "-overlay", "-toolexec", "-exec", "-vettool", "-pgo"} {
			if arg == flag || strings.HasPrefix(arg, flag+"=") {
				return flag + " needs local files or tools"
			}
		}
	}
	if len(argv) > 1 && argv[1] == "build" {
		// Unset is the Mac default (cgo on). The runner reproduces both.
		if v := getenv("CGO_ENABLED"); v != "" && v != "0" && v != "1" {
			return "CGO_ENABLED must be 0 or 1 for a remote build"
		}
		if v := getenv("GOOS"); v != "" && v != "darwin" {
			return "GOOS differs from Darwin build output"
		}
		if v := getenv("GOARCH"); v != "" && v != "arm64" {
			return "GOARCH differs from arm64 build output"
		}
	} else if getenv("GOOS") != "" || getenv("GOARCH") != "" {
		return "test and vet require the native runner profile"
	}
	return ""
}

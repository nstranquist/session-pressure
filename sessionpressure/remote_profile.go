package sessionpressure

import "strings"

// remoteProfileReason protects commands whose local execution profile cannot
// be reproduced by the finite Linux test / cgo-free Darwin build route.
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
		if getenv("CGO_ENABLED") != "0" {
			return "build requires explicit CGO_ENABLED=0 for this remote route"
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

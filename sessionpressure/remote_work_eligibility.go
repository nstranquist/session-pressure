package sessionpressure

import (
	"path/filepath"
	"strings"
)

// Remote Go work is only correct when the runner reproduces the local result.
// The runner sees the uploaded module closure under a different root and its
// own environment. Tests and vet run natively on linux; builds produce
// darwin/arm64 with the Mac's cgo setting (remoteBuildEnvironment). A command
// that depends on anything outside that contract is refused, never moved back
// onto the Mac.

// goFlagsWithValue are the go build/test/vet flags that consume the next
// argument when it is not given in -flag=value form.
var goFlagsWithValue = map[string]bool{
	"-o": true, "-p": true, "-C": true, "-tags": true, "-mod": true, "-modfile": true,
	"-overlay": true, "-pgo": true, "-toolexec": true, "-exec": true,
	"-ldflags": true, "-gcflags": true, "-asmflags": true, "-gccgoflags": true,
	"-installsuffix": true, "-pkgdir": true, "-buildmode": true, "-compiler": true,
	"-covermode": true, "-coverpkg": true, "-coverprofile": true, "-cpuprofile": true,
	"-memprofile": true, "-blockprofile": true, "-mutexprofile": true, "-trace": true,
	"-outputdir": true, "-run": true, "-skip": true, "-bench": true, "-benchtime": true,
	"-count": true, "-cpu": true, "-parallel": true, "-timeout": true, "-shuffle": true,
	"-fuzz": true, "-fuzztime": true, "-fuzzminimizetime": true, "-list": true,
	"-json": false, "-vettool": true,
}

// goFlagsNamingLocalFiles always reference a file the runner does not have
// (or a local program), whatever the spelling of their value.
var goFlagsNamingLocalFiles = map[string]bool{
	"-modfile": true, "-overlay": true, "-toolexec": true, "-exec": true, "-vettool": true,
}

type goArg struct {
	flag  string // "" for a positional argument
	value string
}

// splitGoArgs parses go tool arguments into flags and positionals. Go stops
// flag parsing at the first positional for build and vet; test passes later
// flags to the binary, which never name local inputs we must reproduce.
func splitGoArgs(args []string) []goArg {
	var out []goArg
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			out = append(out, goArg{value: arg})
			continue
		}
		name := "-" + strings.TrimLeft(arg, "-")
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			out = append(out, goArg{flag: name[:eq], value: name[eq+1:]})
			continue
		}
		if goFlagsWithValue[name] && i+1 < len(args) {
			out = append(out, goArg{flag: name, value: args[i+1]})
			i++
			continue
		}
		out = append(out, goArg{flag: name})
	}
	return out
}

func localFileReason(source string, args []goArg) string {
	for _, a := range args {
		switch {
		case a.flag == "-o":
			continue // the output is fetched back explicitly
		case goFlagsNamingLocalFiles[a.flag] && a.value != "":
			return source + " " + a.flag + " names a local file"
		case a.flag == "-pgo" && a.value != "" && a.value != "auto" && a.value != "off":
			return source + " -pgo names a local profile"
		case filepath.IsAbs(a.value):
			if a.flag == "" {
				return source + " names the absolute local path " + a.value
			}
			return source + " " + a.flag + " names the absolute local path " + a.value
		}
	}
	return ""
}

// remoteWorkIneligibleReason returns why the remote route cannot reproduce
// argv (the caller refuses it), or "" when it can. getenv is the
// command's environment.
func remoteWorkIneligibleReason(argv []string, getenv func(string) string) string {
	if len(argv) < 2 {
		return "not a go subcommand"
	}
	op := argv[1]
	args := splitGoArgs(argv[2:])
	if reason := localFileReason("argument", args); reason != "" {
		return reason
	}
	if reason := localFileReason("GOFLAGS", splitGoArgs(strings.Fields(getenv("GOFLAGS")))); reason != "" {
		return reason
	}
	if getenv("GOEXPERIMENT") != "" {
		return "GOEXPERIMENT is set; the runner does not reproduce it"
	}
	goos, goarch := getenv("GOOS"), getenv("GOARCH")
	if op != "build" {
		// Tests and vet run natively on the linux runner.
		if goos != "" || goarch != "" {
			return "GOOS/GOARCH is set; remote " + op + " runs natively on the linux/arm64 runner"
		}
		return ""
	}
	if (goos != "" && goos != "darwin") || (goarch != "" && goarch != "arm64") {
		return "GOOS/GOARCH targets " + goos + "/" + goarch + "; remote builds produce darwin/arm64"
	}
	// Both cgo settings are reproduced, and remoteProfileReason refuses a
	// local CC or CGO_* value, so the build is eligible.
	return ""
}

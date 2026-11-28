package sessionpressure

import (
	"path/filepath"
	"strconv"
	"strings"
)

// Remote Go work is only correct when the runner reproduces the local result.
// The runner sees the uploaded module closure under a different root and its
// own environment. Tests and vet run natively on linux; builds produce
// darwin/arm64 with the Mac's cgo setting (remoteBuildEnvironment). A command
// that depends on anything outside that contract is refused, never moved back
// onto the Mac.

// goFlagsWithValue are the go build/test/vet flags that consume the next
// argument when it is not given in -flag=value form. It is the one table for
// every Go argument parser in this package (go help build, go help testflag).
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
	"-vet": true, "-blockprofilerate": true, "-memprofilerate": true,
	"-mutexprofilefraction": true, "-json": false, "-vettool": true,
}

// goBoolFlags are the boolean go build and go test flags. With
// goFlagsWithValue they are every flag go test knows; go test ends its
// package list at any other flag (cmd/go testflag.go).
var goBoolFlags = map[string]bool{
	"-a": true, "-n": true, "-x": true, "-v": true, "-work": true, "-race": true,
	"-msan": true, "-asan": true, "-cover": true, "-json": true, "-linkshared": true,
	"-modcacherw": true, "-trimpath": true, "-buildvcs": true, "-c": true,
	"-failfast": true, "-fullpath": true, "-short": true, "-benchmem": true,
	"-artifacts": true,
}

// goTestBinaryFlags are the go test flags that cmd/go also accepts as
// -test.<name>, because it passes them on to the test binary (cmd/go
// passFlagToTest). No other flag has a test. spelling.
var goTestBinaryFlags = map[string]bool{
	"artifacts": true, "bench": true, "benchmem": true, "benchtime": true,
	"blockprofile": true, "blockprofilerate": true, "count": true, "coverprofile": true,
	"cpu": true, "cpuprofile": true, "failfast": true, "fullpath": true, "fuzz": true,
	"fuzzminimizetime": true, "fuzztime": true, "list": true, "memprofile": true,
	"memprofilerate": true, "mutexprofile": true, "mutexprofilefraction": true,
	"outputdir": true, "parallel": true, "run": true, "short": true, "shuffle": true,
	"skip": true, "timeout": true, "trace": true, "v": true,
}

// goTestOutputFlags write files during a go test run: the test binary (-c,
// -o), profiles and the trace, test artifacts, and new fuzz corpus entries
// under testdata. A remote go test leaves them on the runner, because the
// route returns only the output of a go build, so it refuses them instead of
// dropping the files. The test binary is also a linux binary there.
var goTestOutputFlags = map[string]bool{
	"-c": true, "-o": true, "-coverprofile": true, "-cpuprofile": true, "-memprofile": true,
	"-blockprofile": true, "-mutexprofile": true, "-trace": true, "-artifacts": true, "-fuzz": true,
}

// goTestOutputFlag reports whether a go test flag, as goFlagName reads it,
// writes a file. An empty value, a false boolean and a boolean that Go
// cannot parse (Go rejects it) write nothing.
func goTestOutputFlag(name, value string, inline bool) bool {
	if !goTestOutputFlags[name] {
		return false
	}
	if goBoolFlags[name] {
		on, err := strconv.ParseBool(value)
		return !inline || (err == nil && on)
	}
	return value != ""
}

// goTestOutputReason names the first flag in args that makes go test write a
// file, or "". After -args the arguments belong to the test binary, where
// only the -test. spellings are the testing package's own output flags.
func goTestOutputReason(source string, args []string) string {
	binaryArgs := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-args" || arg == "--args" {
			binaryArgs = true
			continue
		}
		name, value, inline := goFlagName(arg)
		if name == "" {
			continue
		}
		if !inline && goFlagsWithValue[name] && i+1 < len(args) {
			i++
			value = args[i]
		}
		if binaryArgs && !strings.HasPrefix(strings.TrimLeft(arg, "-"), "test.") {
			continue
		}
		if goTestOutputFlag(name, value, inline) {
			return source + " " + name + " makes go test write a file that the remote route does not return"
		}
	}
	return ""
}

// goBuildModeReason refuses the build modes that write a second file: a
// c-archive or c-shared build also writes a C header beside its output, and
// the remote route returns only the output.
func goBuildModeReason(source string, args []goArg) string {
	mode := ""
	for _, a := range args {
		if a.flag == "-buildmode" {
			mode = a.value
		}
	}
	if mode == "c-archive" || mode == "c-shared" {
		return source + " -buildmode=" + mode + " also writes a C header that the remote route does not return"
	}
	return ""
}

// remoteOutputReason returns why the remote route would lose a file that
// argv writes, or "". The route returns only the -o output of a go build, so
// the go test outputs and the C header of a c-archive or c-shared build would
// stay on the runner. A local run keeps them, so the caller refuses these
// with local-placement advice instead of the runner advice.
func remoteOutputReason(argv []string, getenv func(string) string) string {
	if len(argv) < 2 {
		return ""
	}
	goflags := strings.Fields(getenv("GOFLAGS"))
	switch argv[1] {
	case "test":
		if reason := goTestOutputReason("argument", argv[2:]); reason != "" {
			return reason
		}
		return goTestOutputReason("GOFLAGS", goflags)
	case "build":
		// A command-line -buildmode overrides the one in GOFLAGS.
		return goBuildModeReason("argument", append(splitGoArgs(goflags), splitGoArgs(argv[2:])...))
	}
	return ""
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

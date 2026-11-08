package sessionpressure

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Remote Go work is only correct when the runner reproduces the local result.
// The runner sees the uploaded module closure under a different root, its own
// environment, and (for builds) a forced GOOS=darwin GOARCH=arm64
// CGO_ENABLED=0 cross-compile. A command that depends on anything outside
// that contract keeps local admission. This is eligibility, not a fallback:
// once a command is routed, a remote failure still never retries locally.

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

// remoteWorkIneligibleReason returns why argv must keep local admission, or
// "" when the remote route reproduces the local result. getenv is the
// command's environment.
func remoteWorkIneligibleReason(cwd string, argv []string, getenv func(string) string) string {
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
			return "GOOS/GOARCH is set; remote " + op + " runs on linux/amd64"
		}
		return ""
	}
	if (goos != "" && goos != "darwin") || (goarch != "" && goarch != "arm64") {
		return "GOOS/GOARCH targets " + goos + "/" + goarch + "; remote builds produce darwin/arm64"
	}
	cgo := getenv("CGO_ENABLED")
	if cgo == "0" {
		return "" // the local build is also cgo-free, so the cross-compile matches
	}
	if cgo == "1" {
		return "CGO_ENABLED=1; remote builds cross-compile with cgo disabled"
	}
	for _, key := range []string{"CC", "CXX", "CGO_CFLAGS", "CGO_CPPFLAGS", "CGO_CXXFLAGS", "CGO_LDFLAGS"} {
		if getenv(key) != "" {
			return key + " is set; remote builds cross-compile with cgo disabled"
		}
	}
	return buildNeedsCgo(cwd, args)
}

// buildNeedsCgo asks the local toolchain whether the build graph links cgo.
// A darwin build defaults to CGO_ENABLED=1, so a cgo package (mattn sqlite,
// for example) would silently lose its implementation in the remote
// CGO_ENABLED=0 cross-compile. Errors keep local admission: an unknown graph
// is not a proven-equivalent one.
func buildNeedsCgo(cwd string, args []goArg) string {
	list := []string{"list", "-deps", "-f", "{{if or .CgoFiles .SwigFiles .SwigCXXFiles}}{{.ImportPath}} {{.Standard}}\n{{end}}"}
	var packages []string
	for _, a := range args {
		switch a.flag {
		case "":
			packages = append(packages, a.value)
		case "-tags", "-mod", "-C":
			list = append(list, a.flag+"="+a.value)
		}
	}
	if len(packages) == 0 {
		packages = []string{"."}
	}
	list = append(list, "--")
	list = append(list, packages...)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", list...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1", "GOOS=darwin", "GOARCH=arm64")
	out, err := cmd.Output()
	if err != nil {
		return "could not prove the build graph is cgo-free: " + strings.TrimSpace(err.Error())
	}
	// Name the package that imports C, not the runtime/cgo it pulls in.
	first := ""
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if fields[1] == "false" {
			first = fields[0]
			break
		}
		if first == "" {
			first = fields[0]
		}
	}
	if first == "" {
		return ""
	}
	return "the build links cgo package " + first + "; remote builds cross-compile with cgo disabled"
}

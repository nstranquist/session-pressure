package sessionpressure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/build"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/modfile"
)

// A go build writes the file its -o names. Without -o it writes a binary
// only when its arguments are exactly one main package (cmd/go runBuild):
// into the working directory, named after the package, for a package
// pattern too. The runner writes that binary into its own copy of the
// module, so the route names it with -o and fetches it back, or fails the
// build before upload. A build of several packages or of a library writes
// nothing and runs as written.

// remoteBuildCandidate is where the runner writes the output it returns.
const remoteBuildCandidate = "/srv/build/candidates/OUTPUT"

// remoteBuildPlan is a go command as the runner runs it and the local file
// the route fetches back from the runner.
type remoteBuildPlan struct {
	argv []string
	// output is the local file or directory (with a trailing slash) that the
	// build writes, relative to the working directory or absolute; "" when
	// the build writes nothing.
	output string
	mode   fs.FileMode // of a returned file
	// kind and probe describe the decision for telemetry. kind is explicit,
	// directory, implicit, none or discard ("" for go test and go vet);
	// probe is walk or go_list when a build without -o was examined.
	kind, probe string
}

// goBuildOutputFailure is a build that Go itself fails because of its
// output path. The route fails it the same way before upload, as a plain
// command failure, not a refusal.
type goBuildOutputFailure struct{ output, problem string }

func (e *goBuildOutputFailure) Error() string {
	return fmt.Sprintf("go: build output %q %s", e.output, e.problem)
}

// remoteBuildOutputRefusal is a build output the route cannot return.
type remoteBuildOutputRefusal struct{ reason, advice string }

func (e *remoteBuildOutputRefusal) Error() string { return e.reason }

// remoteImplicitOutputAdvice ends a refusal of a build whose implicit binary
// the route could not determine.
const remoteImplicitOutputAdvice = "name the output with -o <path>, or " + remoteLocalOutputAdvice

// planRemoteBuild is argv (a go command run from workdir) for the runner,
// with the build output redirected to remoteBuildCandidate. go is argv[0],
// the go command that reads the packages here.
func planRemoteBuild(workdir string, argv []string) (remoteBuildPlan, error) {
	plan := remoteBuildPlan{argv: append([]string(nil), argv...), mode: 0o755}
	if len(argv) < 2 || argv[1] != "build" {
		return plan, nil
	}
	args := parseGoBuildArgs(argv[2:])
	if args.buildmode == "plugin" {
		plan.mode = 0o644 // cmd/go writes c-archive, c-shared and plugin outputs 0666
	}
	switch {
	case args.writesNothing:
		plan.kind = "none"
		return plan, nil
	case args.output == "/dev/null":
		plan.kind = "discard"
		return plan, nil
	case args.output != "":
		if err := checkExplicitBuildOutput(workdir, args.output); err != nil {
			return plan, err
		}
		remote, kind := remoteBuildCandidate, "explicit"
		if remoteBuildDirectory(args.output) {
			remote, kind = remoteBuildCandidate+"/", "directory"
		}
		at := 2 + args.outputAt
		if args.outputInline {
			plan.argv[at] = plan.argv[at][:strings.IndexByte(plan.argv[at], '=')+1] + remote
		} else {
			plan.argv[at] = remote
		}
		plan.output, plan.kind = args.output, kind
		return plan, nil
	}
	implicit, err := goBuildImplicitOutput(workdir, argv[0], argv[2:])
	plan.probe = implicit.probe
	if err != nil {
		return plan, &remoteBuildOutputRefusal{reason: "cannot tell which binary go build writes: " + err.Error(), advice: remoteImplicitOutputAdvice}
	}
	if implicit.name == "" {
		plan.kind = "none"
		return plan, nil
	}
	if problem := goBuildDefaultOutputProblem(filepath.Join(workdir, implicit.name)); problem != "" {
		return plan, &goBuildOutputFailure{output: implicit.name, problem: problem}
	}
	// After every flag, so that it also replaces an empty -o (no -o to Go).
	at := 2 + args.flagCount
	plan.argv = append(append(append([]string(nil), argv[:at]...), "-o", remoteBuildCandidate), argv[at:]...)
	plan.output, plan.kind = implicit.name, "implicit"
	return plan, nil
}

// checkExplicitBuildOutput fails an -o output that Go refuses to replace and
// refuses one the route cannot place: the route publishes an output
// directory only where nothing exists yet.
func checkExplicitBuildOutput(workdir, output string) error {
	path := output
	if !filepath.IsAbs(path) {
		path = filepath.Join(workdir, path)
	}
	if remoteBuildDirectory(output) {
		if _, err := os.Lstat(filepath.Clean(path)); err == nil {
			return &remoteBuildOutputRefusal{reason: "-o " + output + " already exists, and the remote route places an output directory only where nothing exists yet", advice: remoteLocalOutputAdvice}
		}
		return nil
	}
	info, err := os.Stat(path)
	switch {
	case err != nil:
		return nil
	case info.IsDir():
		return &remoteBuildOutputRefusal{reason: "-o " + output + " is an existing directory, which go build writes into here, and the remote route places an output directory only where nothing exists yet", advice: remoteLocalOutputAdvice}
	case notGoObjectFile(path, info):
		return &goBuildOutputFailure{output: output, problem: "already exists and is not an object file"}
	}
	return nil
}

// goBuildDefaultOutputProblem is why Go fails to write its default binary
// at path, or "": Go refuses a directory there, and it never replaces a
// file that is not empty and not an object file (cmd/go checkDstOverwrite).
func goBuildDefaultOutputProblem(path string) string {
	info, err := os.Stat(path)
	switch {
	case err != nil:
		return ""
	case info.IsDir():
		return "already exists and is a directory"
	case notGoObjectFile(path, info):
		return "already exists and is not an object file"
	}
	return ""
}

// goObjectMagic are the file prefixes cmd/go accepts as an object file it
// may replace (cmd/go/internal/work objectMagic).
var goObjectMagic = [][]byte{
	[]byte("!<arch>\n"), []byte("<bigaf>\n"), // Go and AIX big archives
	{0x7F, 'E', 'L', 'F'},
	{0xFE, 0xED, 0xFA, 0xCE}, {0xFE, 0xED, 0xFA, 0xCF}, {0xCE, 0xFA, 0xED, 0xFE}, {0xCF, 0xFA, 0xED, 0xFE}, // Mach-O
	{0x4d, 0x5a, 0x90, 0x00, 0x03, 0x00}, {0x4d, 0x5a, 0x78, 0x00, 0x01, 0x00}, // PE
	{0x00, 0x00, 0x01, 0xEB}, {0x00, 0x00, 0x8a, 0x97}, {0x00, 0x00, 0x06, 0x47}, // Plan 9
	{0x00, 0x61, 0x73, 0x6D},   // WASM
	{0x01, 0xDF}, {0x01, 0xF7}, // XCOFF
}

// notGoObjectFile is true for a regular file that is not empty and does not
// start with an object file prefix: a file go build refuses to replace.
func notGoObjectFile(path string, info fs.FileInfo) bool {
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()
	head := make([]byte, 64)
	n, _ := io.ReadFull(f, head)
	for _, magic := range goObjectMagic {
		if bytes.HasPrefix(head[:n], magic) {
			return false
		}
	}
	return true
}

// goBuildArgs are the arguments of a go build after "build", read the way
// cmd/go reads them: flags up to the first package argument or "--".
type goBuildArgs struct {
	output       string // the last -o; "" for none
	outputAt     int    // the index of the argument that holds its value
	outputInline bool   // -o=value
	// writesNothing is true for -n, which only prints the commands, and for
	// a flag value Go rejects (Go then fails before it writes anything).
	writesNothing bool
	// otherFiles is true for -modfile and -overlay, which give Go other
	// files than the ones on disk.
	otherFiles bool
	buildmode  string
	flagCount  int      // the arguments before the packages
	listFlags  []string // the flags as go list reads them: without -o and -json
	packages   []string // the arguments from the first package or "--" on
}

func parseGoBuildArgs(args []string) goBuildArgs {
	var out goBuildArgs
	i := 0
	for ; i < len(args); i++ {
		arg := args[i]
		name, value, inline := goFlagName(arg)
		if name == "" {
			break // a package argument, or "--"
		}
		start := i
		if !inline && goFlagsWithValue[name] {
			if i+1 == len(args) {
				out.writesNothing = true // flag needs an argument
				break
			}
			i++
			value = args[i]
		}
		switch name {
		case "-o":
			out.output, out.outputAt, out.outputInline = value, i, inline
			continue
		case "-json":
			continue // go list has its own -json
		case "-n":
			on, err := strconv.ParseBool(value)
			out.writesNothing = out.writesNothing || !inline || err != nil || on
		case "-buildmode":
			out.buildmode = value
		case "-modfile", "-overlay":
			out.otherFiles = true
		}
		out.listFlags = append(out.listFlags, args[start:i+1]...)
	}
	out.flagCount, out.packages = i, args[i:]
	return out
}

// goBuildImplicit is what a go build without -o writes.
type goBuildImplicit struct {
	name  string // the binary in the working directory; "" for none
	fails bool   // a named package has an error, so Go fails and writes nothing
	probe string // walk or go_list
}

// goBuildImplicitOutput reports what go build with args (its arguments
// after "build", with no -o) writes when it runs in dir. A walk of the
// package patterns answers when it finds two packages and the flags leave
// the files on disk as Go reads them; otherwise go list gives cmd/go's own
// answer. goCmd is the go command to ask.
func goBuildImplicitOutput(dir, goCmd string, args []string) (goBuildImplicit, error) {
	parsed := parseGoBuildArgs(args)
	if parsed.writesNothing {
		return goBuildImplicit{}, nil
	}
	suffix := ""
	switch parsed.buildmode {
	case "", "default", "exe", "pie":
	case "plugin":
		suffix = ".so"
	case "archive", "shared":
		// These leave out main packages, so Go writes no binary.
		return goBuildImplicit{}, nil
	default:
		return goBuildImplicit{}, fmt.Errorf("-buildmode=%s is not read here", parsed.buildmode)
	}
	if !parsed.otherFiles && goPatternsMatchTwoPackages(dir, parsed.packages) {
		return goBuildImplicit{probe: "walk"}, nil
	}
	result := goBuildImplicit{probe: "go_list"}
	pkgs, err := goListBuildPackages(dir, goCmd, append(parsed.listFlags, parsed.packages...))
	if err != nil {
		return result, err
	}
	for _, pkg := range pkgs {
		result.fails = result.fails || pkg.Error != nil
	}
	if result.fails || len(pkgs) != 1 || pkgs[0].Name != "main" {
		return result, nil
	}
	if name := goExecutableName(pkgs[0]); name != "" {
		result.name = name + suffix
	}
	return result, nil
}

// goListedPackage is the part of a go list -json record that decides the
// default binary.
type goListedPackage struct {
	ImportPath, Name  string
	GoFiles, CgoFiles []string
	Error             *struct{ Err string }
}

// goListTimeout bounds the package listing before upload.
const goListTimeout = 2 * time.Minute

// goListBuildPackages lists the packages go build would load from dir. go
// list loads them as go build does; -find skips their imports. GOWORK=off
// matches the runner.
func goListBuildPackages(dir, goCmd string, args []string) ([]goListedPackage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), goListTimeout)
	defer cancel()
	listArgs := append([]string{"list", "-e", "-find", "-json=ImportPath,Name,GoFiles,CgoFiles,Error"}, args...)
	cmd := exec.CommandContext(ctx, goCmd, listArgs...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		detail, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
		return nil, fmt.Errorf("go list: %v: %s", err, boundedText(detail, 200))
	}
	var pkgs []goListedPackage
	for dec := json.NewDecoder(&stdout); ; {
		var pkg goListedPackage
		if err := dec.Decode(&pkg); errors.Is(err, io.EOF) {
			return pkgs, nil
		} else if err != nil {
			return nil, fmt.Errorf("go list output: %w", err)
		}
		pkgs = append(pkgs, pkg)
	}
}

// goExecutableName is cmd/go's name for the binary of a main package
// (load.Package.DefaultExecName): for named files, the first Go file without
// cgo, else the first cgo file; for a package, the last element of its import
// path, or the one before a major version element such as v2.
func goExecutableName(pkg goListedPackage) string {
	if pkg.ImportPath == "command-line-arguments" {
		files := pkg.GoFiles
		if len(files) == 0 {
			files = pkg.CgoFiles
		}
		if len(files) == 0 {
			return ""
		}
		return strings.TrimSuffix(filepath.Base(files[0]), ".go")
	}
	elem := pathpkg.Base(pkg.ImportPath)
	if elem != pkg.ImportPath && isGoVersionElement(elem) {
		elem = pathpkg.Base(pathpkg.Dir(pkg.ImportPath))
	}
	return elem
}

// isGoVersionElement is cmd/go's test for a major version path element.
func isGoVersionElement(s string) bool {
	if len(s) < 2 || s[0] != 'v' || s[1] == '0' || s[1] == '1' && len(s) == 2 {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || '9' < s[i] {
			return false
		}
	}
	return true
}

// goPatternsMatchTwoPackages is true when relative package patterns of the
// form dir/... are sure to match two packages from workdir, so go build
// writes no binary (or fails) and go list need not run. It walks the
// directories the way cmd/go does (search.MatchDirs) and counts one that
// holds a file every build of this host includes: no build constraint, no
// cgo, and a name that GOOS and GOARCH select. It skips every directory Go
// might skip, and it gives up (false) on any other argument, outside the
// working directory's module, or with an ignore directive in its go.mod.
// A false answer leaves the question to go list.
func goPatternsMatchTwoPackages(workdir string, patterns []string) bool {
	if len(patterns) > 0 && patterns[0] == "--" {
		patterns = patterns[1:]
	}
	module := enclosingGoModule(workdir)
	if len(patterns) == 0 || module == "" || goModIgnoresDirectories(module) {
		return false
	}
	counted := map[string]bool{}
	var walk func(path, elem string, top bool) bool
	walk = func(path, elem string, top bool) bool {
		dot := strings.HasPrefix(elem, ".") && elem != "." && elem != ".."
		if dot || strings.HasPrefix(elem, "_") || elem == "testdata" || elem == "vendor" {
			return false
		}
		if !top {
			if info, err := os.Stat(filepath.Join(path, "go.mod")); err == nil && !info.IsDir() {
				return false // another module
			}
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return false
		}
		if !counted[path] && dirHasUnconditionalGoFile(path, entries) {
			counted[path] = true
			if len(counted) == 2 {
				return true
			}
		}
		for _, entry := range entries {
			if entry.IsDir() && walk(filepath.Join(path, entry.Name()), entry.Name(), false) {
				return true
			}
		}
		return false
	}
	for _, pattern := range patterns {
		root, ok := strings.CutSuffix(pattern, "/...")
		if !ok || !isRelativeGoPath(root) || strings.Contains(root, "...") {
			return false
		}
		path := filepath.Join(workdir, root)
		if enclosingGoModule(path) != module {
			return false
		}
		if walk(path, filepath.Base(filepath.Clean(root)), true) {
			return true
		}
	}
	return false
}

// goModIgnoresDirectories is true when the go.mod in module has an ignore
// directive, or cannot be read: Go then leaves out directories the walk
// would count.
func goModIgnoresDirectories(module string) bool {
	body, err := os.ReadFile(filepath.Join(module, "go.mod"))
	if err != nil {
		return true
	}
	file, err := modfile.ParseLax("go.mod", body, nil)
	return err != nil || len(file.Ignore) > 0
}

// dirHasUnconditionalGoFile is true when dir holds a Go file that every
// build on this host includes, so Go matches dir as a package.
func dirHasUnconditionalGoFile(dir string, entries []fs.DirEntry) bool {
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") ||
			strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".") {
			continue
		}
		if unconditionalGoFile(dir, name) {
			return true
		}
	}
	return false
}

func unconditionalGoFile(dir, name string) bool {
	// MatchFile applies the GOOS and GOARCH file name rules of this host.
	if match, err := build.Default.MatchFile(dir, name); err != nil || !match {
		return false
	}
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, parser.ImportsOnly|parser.ParseComments)
	if err != nil || file.Name.Name == "documentation" {
		return false
	}
	for _, group := range file.Comments {
		if group.Pos() >= file.Package {
			break
		}
		for _, comment := range group.List {
			if constraint.IsGoBuild(comment.Text) || constraint.IsPlusBuild(comment.Text) {
				return false
			}
		}
	}
	for _, spec := range file.Imports {
		if spec.Path.Value == `"C"` {
			return false
		}
	}
	return true
}

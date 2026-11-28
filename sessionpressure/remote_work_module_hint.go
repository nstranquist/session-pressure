package sessionpressure

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// errRemoteNoModule is the module check's refusal: no go.mod is at or above
// the working directory, so the runner has no module to upload.
var errRemoteNoModule = errors.New("remote Go work requires a module")

// rejectRemoteModule refuses a Go command that runs outside any module. When
// the command names files or packages of a nested module (for example
// scripts/tool/main.go from a checkout whose only go.mod is scripts/go.mod),
// the refusal names that go.mod and gives the same command run from it,
// because the runner advice of the other refusals does not help here. The
// kind, marker, prefix and exit code stay those of every remote refusal.
// cwd is the caller's directory and workdir the one Go reads argv from (see
// remoteModuleHint).
func rejectRemoteModule(dir, cwd, workdir string, argv []string) (bool, int, error) {
	hint, modules := remoteModuleHint(cwd, workdir, argv)
	if hint == "" {
		return rejectRemoteWork(dir, argv[1], errRemoteNoModule.Error())
	}
	return refuseRemoteGo(dir, argv[1], errRemoteNoModule.Error(), hint, map[string]any{"module_hint": strings.Join(modules, ",")})
}

// remoteModuleHint says which go.mod owns the files or packages that argv
// names, and how to run argv from there so that it builds, tests and writes
// the same things. workdir is the directory Go reads argv from: cwd, or the
// directory of a leading -C, which the remote route follows and takes out of
// argv before it looks for a module. The suggested command is spelled for a
// caller in cwd. It returns "" when no nested module owns every named
// argument or the rewrite cannot be exact: an import path, a pattern over the
// module-less directory, a path that does not exist, "--", a -C that is not
// the first flag, an argument Go itself would reject, an output that cannot
// be named the same way from the module, or a symlink that makes Go read a
// moved path differently. modules are the go.mod paths as shown, in argument
// order.
//
// It does not run the command. For a build without -o it asks go list in the
// owning module which binary the build writes, as the remote route does.
// Following the file paths automatically was considered and rejected: the
// refusal stays, and its text names the way out.
func remoteModuleHint(cwd, workdir string, argv []string) (string, []string) {
	if len(argv) < 2 {
		return "", nil
	}
	head, args := argv[:2], argv[2:]
	plan, ok := planModuleRewrite(workdir, argv[0], argv[1], args)
	if !ok || len(plan.modules) == 0 {
		return "", nil
	}
	shown := make([]string, len(plan.modules))
	for i, module := range plan.modules {
		shown[i] = shownGoMod(cwd, module)
	}
	kind := "packages"
	if plan.filesOnly {
		kind = "files"
	}
	list := strings.Join(shown, ", ")
	if len(plan.modules) > 1 && plan.writes {
		// One output or binary cannot be split across several runs.
		return fmt.Sprintf("the named %s are in %d modules (%s); run a separate command in each module directory", kind, len(shown), list), shown
	}
	commands := make([]string, len(plan.modules))
	for i, module := range plan.modules {
		rewritten, ok := plan.argsFor(workdir, module)
		if !ok {
			return "", nil
		}
		commands[i] = shellCommandIn(cwd, module, append(append([]string(nil), head...), rewritten...))
	}
	if len(plan.modules) == 1 {
		return fmt.Sprintf("the named %s are in %s, so run the command from that module: %s", kind, shown[0], commands[0]), shown
	}
	return fmt.Sprintf("the named %s are in %d modules (%s); run one command in each: %s", kind, len(shown), list, strings.Join(commands, " and ")), shown
}

// leadingGoChdir splits a first -C flag from args. ok is false when -C has
// no value, or when -C appears later, which Go itself rejects.
func leadingGoChdir(args []string) (chdir string, rest []string, ok bool) {
	rest = args
	if len(args) == 0 {
		return "", rest, true
	}
	if name, value, inline := goFlagName(args[0]); name == "-C" {
		switch {
		case inline:
			chdir, rest = value, args[1:]
		case len(args) > 1:
			chdir, rest = args[1], args[2:]
		}
		if strings.TrimSpace(chdir) == "" {
			return "", nil, false
		}
	}
	for _, arg := range rest {
		if arg == "-args" || arg == "--args" {
			break
		}
		if name, _, _ := goFlagName(arg); name == "-C" {
			return "", nil, false
		}
	}
	return chdir, rest, true
}

// goFlagName is a flag argument's single-dash name and inline value, the way
// cmd/go reads it: one or two leading dashes, and the "test." prefix that go
// test accepts on the flags it passes to the test binary
// (goTestBinaryFlags); any other -test.<name> keeps its prefix. name is ""
// for a positional argument and for "--". A flag with three dashes keeps its
// spelling, so no table knows it (Go rejects it).
func goFlagName(arg string) (name, value string, inline bool) {
	if !strings.HasPrefix(arg, "-") || arg == "-" || arg == "--" {
		return "", "", false
	}
	body := strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-")
	if strings.HasPrefix(body, "-") {
		return arg, "", false
	}
	name, value, inline = strings.Cut(body, "=")
	if short, prefixed := strings.CutPrefix(name, "test."); prefixed && goTestBinaryFlags[short] {
		name = short
	}
	return "-" + name, value, inline
}

// moduleRewrite is a module-less Go command split by the module that owns
// each named file or package.
type moduleRewrite struct {
	tokens    []moduleToken
	modules   []string // owning module directories, in argument order
	filesOnly bool
	// writes is true when the command writes a named output or an implicit
	// binary, which one command per module cannot reproduce.
	writes bool
	// implicitOutput is the binary go build writes into the working
	// directory without -o, relative to it. The rewrite names it with -o.
	implicitOutput string
}

type goArgKind int

const (
	goArgPlain      goArgKind = iota
	goArgPath                 // a path relative to the working directory
	goArgPatterns             // -coverpkg: comma-separated package patterns
	goArgPerPackage           // -gcflags and the like: [pattern=]arguments
)

type moduleToken struct {
	raw string
	// value is a flag's value. separate is true when it is the next
	// argument, not -flag=value. kind says how it moves with the directory.
	value    string
	separate bool
	kind     goArgKind
	// A positional argument and its spelling in its module. -args, the test
	// binary arguments and the go test arguments after the package list have
	// no module and stay as written.
	positional goPositional
}

// goPathFlags take a path relative to the working directory. The go test
// outputs (goTestOutputFlags) never reach the planner: the remote route
// refuses them first, so -outputdir, which only places them, stays as
// written.
var goPathFlags = map[string]bool{"-o": true, "-pkgdir": true}

// goPerPackageFlags take "[pattern=]arguments", where a relative pattern is
// read from the working directory (go help build).
var goPerPackageFlags = map[string]bool{"-gcflags": true, "-ldflags": true, "-asmflags": true, "-gccgoflags": true}

func goFlagKind(name string) goArgKind {
	switch {
	case goPathFlags[name]:
		return goArgPath
	case name == "-coverpkg":
		return goArgPatterns
	case goPerPackageFlags[name]:
		return goArgPerPackage
	}
	return goArgPlain
}

// planModuleRewrite reads args the way go build, go test or go vet (op) does
// from workdir. go build and go vet read every argument after the first
// package as a package, so a later flag makes the command fail as written.
// go test reads one package list and passes the positional arguments after
// it to the test binary (cmd/go testflag.go).
func planModuleRewrite(workdir, goCmd, op string, args []string) (moduleRewrite, bool) {
	plan := moduleRewrite{filesOnly: true}
	outputs := 0
	inList, listClosed, afterUnknownFlag := false, false, false
	seen := map[string]bool{}
	var positionals []goPositional
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-args" || arg == "--args" {
			for _, rest := range args[i:] {
				plan.tokens = append(plan.tokens, moduleToken{raw: rest})
			}
			break
		}
		if arg == "--" {
			return plan, false
		}
		if name, value, inline := goFlagName(arg); name != "" {
			if (len(positionals) > 0 && op != "test") || name == "-C" {
				// A leading -C is already followed; Go rejects a later one.
				return plan, false
			}
			known := goFlagsWithValue[name] || goBoolFlags[name]
			if op == "test" && (inList || !known) {
				inList, listClosed = false, true
			}
			afterUnknownFlag = !known && !inline
			token := moduleToken{raw: arg, value: value, kind: goFlagKind(name)}
			switch {
			case inline:
			case goFlagsWithValue[name] && i+1 < len(args):
				token.value, token.separate = args[i+1], true
				i++
			case goFlagsWithValue[name]:
				return plan, false
			}
			if op == "test" && goTestOutputFlag(name, token.value, inline) {
				return plan, false // the remote route refuses it first
			}
			if name == "-o" {
				// Go keeps the last -o, and an empty one is no -o; such a
				// command has no exact rewrite with an added -o.
				outputs++
				if outputs > 1 || token.value == "" {
					return plan, false
				}
				plan.writes = plan.writes || token.value != "/dev/null"
			}
			plan.tokens = append(plan.tokens, token)
			continue
		}
		if listClosed {
			// A test binary argument: the value of the unknown flag before
			// it, after which go test goes on reading its own flags, or the
			// first of the arguments it passes on as written.
			if afterUnknownFlag {
				afterUnknownFlag = false
				plan.tokens = append(plan.tokens, moduleToken{raw: arg})
				continue
			}
			for _, rest := range args[i:] {
				plan.tokens = append(plan.tokens, moduleToken{raw: rest})
			}
			break
		}
		inList = true
		positional, ok := modulePositional(workdir, arg)
		if !ok {
			return plan, false
		}
		positionals = append(positionals, positional)
		plan.filesOnly = plan.filesOnly && positional.file
		if !seen[positional.module] {
			seen[positional.module] = true
			plan.modules = append(plan.modules, positional.module)
		}
		plan.tokens = append(plan.tokens, moduleToken{raw: arg, positional: positional})
	}
	if len(positionals) == 0 {
		return plan, false
	}
	switch {
	case op != "build" || outputs > 0:
	case len(plan.modules) > 1:
		// A module with a single main package would write a binary that
		// the one build of every package does not.
		plan.writes = true
	default:
		// Ask go list in the module what the build writes, as the remote
		// route does once the command runs there.
		module := plan.modules[0]
		args, ok := plan.argsFor(workdir, module)
		if !ok {
			return plan, false
		}
		implicit, err := goBuildImplicitOutput(module, goCmd, args)
		if err != nil || implicit.fails {
			return plan, false // Go fails on the arguments
		}
		if implicit.name != "" && isDir(filepath.Join(workdir, implicit.name)) {
			// Go refuses a default output that is an existing directory, but
			// writes into one that -o names.
			return plan, false
		}
		plan.implicitOutput, plan.writes = implicit.name, implicit.name != ""
	}
	return plan, true
}

// argsFor rebuilds the arguments for a run from module: its own positionals
// rebased, path flags and relative patterns rebased, other flags and the
// test binary arguments as written.
func (plan moduleRewrite) argsFor(cwd, module string) ([]string, bool) {
	// go build resolves -o from the physical working directory, go test
	// resolves its paths from $PWD. With a symlink between cwd and the module
	// the two disagree, so no single rewrite is exact.
	moves := plan.implicitOutput != ""
	var out []string
	if moves {
		output, ok := rebaseGoPath(cwd, module, plan.implicitOutput)
		if !ok {
			return nil, false
		}
		out = append(out, "-o", output)
	}
	for _, token := range plan.tokens {
		if token.positional.module != "" {
			if token.positional.module == module {
				out = append(out, token.positional.rewritten)
			}
			continue
		}
		value, ok := token.value, true
		switch token.kind {
		case goArgPath:
			if value != "" && value != "-" && !filepath.IsAbs(value) {
				value, ok = rebaseGoPath(cwd, module, value)
				moves = true
			}
		case goArgPatterns:
			value, ok = rebaseGoPatternList(cwd, module, value)
		case goArgPerPackage:
			value, ok = rebaseGoPerPackageFlag(cwd, module, value)
		}
		if !ok {
			return nil, false
		}
		switch {
		case token.separate:
			out = append(out, token.raw, value)
		case value != token.value:
			out = append(out, token.raw[:strings.IndexByte(token.raw, '=')+1]+value)
		default:
			out = append(out, token.raw)
		}
	}
	if moves && !sameRelativePath(cwd, module) {
		return nil, false
	}
	return out, true
}

// rebaseGoPath is value, relative to cwd, spelled relative to module. A
// trailing slash (an output directory) stays.
func rebaseGoPath(cwd, module, value string) (string, bool) {
	moved, err := filepath.Rel(module, filepath.Join(cwd, value))
	if err != nil {
		return "", false
	}
	moved = filepath.ToSlash(moved)
	if strings.HasSuffix(value, "/") {
		moved += "/"
	}
	return moved, true
}

// rebaseGoPatternList rebases each relative pattern of a comma-separated list.
func rebaseGoPatternList(cwd, module, value string) (string, bool) {
	patterns := strings.Split(value, ",")
	for i, pattern := range patterns {
		moved, ok := rebaseGoPattern(cwd, module, pattern)
		if !ok {
			return "", false
		}
		patterns[i] = moved
	}
	return strings.Join(patterns, ","), true
}

// rebaseGoPerPackageFlag rebases the pattern of "[pattern=]arguments". As in
// cmd/go, a value that starts with "-" has no pattern.
func rebaseGoPerPackageFlag(cwd, module, value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed[0] == '-' {
		return value, true
	}
	pattern, arguments, found := strings.Cut(trimmed, "=")
	if !found {
		return value, true // Go rejects it as written
	}
	moved, ok := rebaseGoPattern(cwd, module, pattern)
	if !ok {
		return "", false
	}
	if moved == pattern {
		return value, true
	}
	return moved + "=" + arguments, true
}

// rebaseGoPattern spells a relative package pattern from module. Import
// paths and keywords (all, std) stay. A relative pattern outside module, or
// with "..." anywhere but at its end, has no exact rewrite.
func rebaseGoPattern(cwd, module, pattern string) (string, bool) {
	dir, suffix := pattern, ""
	if strings.HasSuffix(dir, "/...") {
		dir, suffix = strings.TrimSuffix(dir, "/..."), "/..."
	}
	if !isRelativeGoPath(dir) {
		return pattern, true
	}
	if strings.Contains(dir, "...") {
		return "", false
	}
	rel, err := filepath.Rel(module, filepath.Join(cwd, dir))
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	moved := "."
	if rel != "." {
		moved = "./" + filepath.ToSlash(rel)
	}
	return moved + suffix, true // "." becomes "./..."
}

func isRelativeGoPath(path string) bool {
	return path == "." || path == ".." || strings.HasPrefix(path, "./") || strings.HasPrefix(path, "../")
}

// sameRelativePath is true when module is reached from cwd by the same
// relative path with and without symlinks resolved.
func sameRelativePath(cwd, module string) bool {
	lexical, err := filepath.Rel(cwd, module)
	if err != nil {
		return false
	}
	physicalCwd, errCwd := filepath.EvalSymlinks(cwd)
	physicalModule, errModule := filepath.EvalSymlinks(module)
	if errCwd != nil || errModule != nil {
		return false
	}
	physical, err := filepath.Rel(physicalCwd, physicalModule)
	return err == nil && physical == lexical
}

// goPositional is a .go file or a relative package path resolved to the
// module that owns it.
type goPositional struct {
	module    string // the module directory
	rewritten string // the argument spelled from module
	path      string // the file or package directory
	file      bool
	pattern   bool // a /... pattern
}

// modulePositional resolves a .go file or a relative package path (with an
// optional /... pattern) to its owning module and its spelling from there.
// Import paths and missing paths have no owner.
func modulePositional(cwd, arg string) (goPositional, bool) {
	if strings.HasSuffix(arg, ".go") {
		path := arg
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			return goPositional{}, false
		}
		module := enclosingGoModule(filepath.Dir(path))
		if module == "" {
			return goPositional{}, false
		}
		rewritten := arg
		if !filepath.IsAbs(arg) {
			rel, err := filepath.Rel(module, path)
			if err != nil {
				return goPositional{}, false
			}
			rewritten = filepath.ToSlash(rel)
		}
		return goPositional{module: module, rewritten: rewritten, path: path, file: true}, true
	}
	dir, pattern := arg, false
	if strings.Contains(dir, "...") {
		if !strings.HasSuffix(dir, "/...") {
			return goPositional{}, false
		}
		dir, pattern = strings.TrimSuffix(dir, "/..."), true
		if strings.Contains(dir, "...") {
			return goPositional{}, false
		}
	}
	if !isRelativeGoPath(dir) && !filepath.IsAbs(dir) {
		return goPositional{}, false // an import path names no directory here
	}
	path := dir
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	if !isDir(path) {
		return goPositional{}, false
	}
	module := enclosingGoModule(path)
	if module == "" {
		return goPositional{}, false
	}
	positional := goPositional{module: module, rewritten: arg, path: path, pattern: pattern}
	if filepath.IsAbs(dir) {
		return positional, true
	}
	rel, err := filepath.Rel(module, path)
	if err != nil {
		return goPositional{}, false
	}
	positional.rewritten = "."
	if rel != "." {
		positional.rewritten = "./" + filepath.ToSlash(rel)
	}
	if pattern {
		positional.rewritten += "/..." // "." becomes "./..."
	}
	return positional, true
}

// enclosingGoModule is the nearest directory at or above dir with a go.mod.
func enclosingGoModule(dir string) string {
	for dir = filepath.Clean(dir); ; {
		if info, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && info.Mode().IsRegular() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// shownDir is dir as the caller in cwd would type it: relative when it is
// inside cwd or one level beside it, absolute otherwise. dir can be a
// physical path (the directory of a followed -C) while cwd is the caller's
// $PWD through a symlink; a path below the physical cwd is then shown from
// it, which a shell's cd reaches the same way.
func shownDir(cwd, dir string) string {
	if rel, err := filepath.Rel(cwd, dir); err == nil && !strings.HasPrefix(rel, "../..") {
		return filepath.ToSlash(rel)
	}
	if physical, err := filepath.EvalSymlinks(cwd); err == nil && physical != cwd {
		if rel, err := filepath.Rel(physical, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			return filepath.ToSlash(rel)
		}
	}
	return dir
}

func shownGoMod(cwd, module string) string {
	shown := shownDir(cwd, module)
	if shown == "." {
		return "go.mod"
	}
	return strings.TrimSuffix(shown, "/") + "/go.mod"
}

// shellCommandIn is argv run from dir in a subshell, so the caller's working
// directory stays as it is.
func shellCommandIn(cwd, dir string, argv []string) string {
	words := make([]string, len(argv))
	for i, arg := range argv {
		words[i] = shellWord(arg)
	}
	shown := shownDir(cwd, dir)
	if strings.HasPrefix(shown, "-") {
		shown = "./" + shown // cd would read it as an option
	}
	return "(cd " + shellWord(shown) + " && " + strings.Join(words, " ") + ")"
}

var plainShellWord = regexp.MustCompile(`^[A-Za-z0-9@%_+=:,./-]+$`)

// shellWord quotes s unless every shell reads it as written. A leading "="
// is quoted too: zsh expands =name to the path of the command name.
func shellWord(s string) string {
	if plainShellWord.MatchString(s) && !strings.HasPrefix(s, "=") {
		return s
	}
	return shellQuote(s)
}

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
func rejectRemoteModule(dir, cwd string, argv []string) (bool, int, error) {
	hint, modules := remoteModuleHint(cwd, argv)
	if hint == "" {
		return rejectRemoteWork(dir, argv[1], errRemoteNoModule.Error())
	}
	return refuseRemoteGo(dir, argv[1], errRemoteNoModule.Error(), hint, map[string]any{"module_hint": strings.Join(modules, ",")})
}

// remoteModuleHint says which go.mod owns the files or packages that argv
// names, and how to run argv from there. It returns "" when no nested module
// owns every named argument: an import path, a pattern over the module-less
// directory, a path that does not exist, "--", or a -C directory outside any
// module. modules are the go.mod paths as shown, in argument order.
//
// It does not run the command. Following -C or the file paths automatically
// was considered and rejected: the refusal stays, and its text names the
// way out.
func remoteModuleHint(cwd string, argv []string) (string, []string) {
	if len(argv) < 2 {
		return "", nil
	}
	head, args := argv[:2], argv[2:]
	// Go applies -C before anything else and accepts it only as the first
	// flag, so every other argument is already relative to that directory.
	if len(args) > 0 {
		chdir, rest, ok := leadingGoChdir(args)
		if !ok {
			return "", nil
		}
		if chdir != "" {
			target := chdir
			if !filepath.IsAbs(target) {
				target = filepath.Join(cwd, target)
			}
			if !isDir(target) {
				return "", nil
			}
			module := enclosingGoModule(target)
			if module == "" {
				return "", nil
			}
			shown := shownGoMod(cwd, module)
			command := shellCommandIn(cwd, target, append(append([]string(nil), head...), rest...))
			return fmt.Sprintf("the remote route does not follow -C; %s owns %s, so run the command from that directory: %s", shown, chdir, command), []string{shown}
		}
	}
	plan, ok := planModuleRewrite(cwd, args)
	if !ok || len(plan.modules) == 0 {
		return "", nil
	}
	shown := make([]string, len(plan.modules))
	commands := make([]string, len(plan.modules))
	for i, module := range plan.modules {
		shown[i] = shownGoMod(cwd, module)
		rewritten, ok := plan.argsFor(cwd, module)
		if !ok {
			return "", nil
		}
		commands[i] = shellCommandIn(cwd, module, append(append([]string(nil), head...), rewritten...))
	}
	kind := "packages"
	if plan.filesOnly {
		kind = "files"
	}
	if len(plan.modules) == 1 {
		return fmt.Sprintf("the named %s are in %s, so run the command from that module: %s", kind, shown[0], commands[0]), shown
	}
	list := strings.Join(shown, ", ")
	if plan.output {
		// One output path cannot be split across several builds.
		return fmt.Sprintf("the named %s are in %d modules (%s); run a separate command in each module directory", kind, len(shown), list), shown
	}
	return fmt.Sprintf("the named %s are in %d modules (%s); run one command in each: %s", kind, len(shown), list, strings.Join(commands, " and ")), shown
}

// leadingGoChdir splits a first -C flag from args. ok is false when -C has
// no value, or when -C appears later, which Go itself rejects.
func leadingGoChdir(args []string) (chdir string, rest []string, ok bool) {
	rest = args
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

// goFlagName is a flag argument's single-dash name and inline value. name is
// "" for a positional argument.
func goFlagName(arg string) (name, value string, inline bool) {
	if !strings.HasPrefix(arg, "-") || arg == "-" {
		return "", "", false
	}
	return strings.Cut("-"+strings.TrimLeft(arg, "-"), "=")
}

// moduleRewrite is a module-less Go command split by the module that owns
// each named file or package.
type moduleRewrite struct {
	tokens    []moduleToken
	modules   []string // owning module directories, in argument order
	filesOnly bool
	output    bool // the command names an -o output
}

type moduleToken struct {
	raw string
	// value is a flag's value. separate is true when it is the next
	// argument, not -flag=value. rebase is true for a path relative to the
	// working directory, which must move with the directory.
	value            string
	separate, rebase bool
	// A positional argument, the module that owns it and its spelling there.
	// -args and the test binary arguments after it have neither and stay
	// as written.
	module, positional string
}

// goPathFlags take a path relative to the working directory. Profile outputs
// are relative to -outputdir instead when it is set (go help testflag).
var goPathFlags = map[string]bool{"-o": true, "-pkgdir": true, "-outputdir": true}

var goProfileFlags = map[string]bool{
	"-coverprofile": true, "-cpuprofile": true, "-memprofile": true,
	"-blockprofile": true, "-mutexprofile": true, "-trace": true,
}

func planModuleRewrite(cwd string, args []string) (moduleRewrite, bool) {
	plan := moduleRewrite{filesOnly: true}
	outputDir := false
	seen := map[string]bool{}
	positionals := 0
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
			token := moduleToken{raw: arg, value: value}
			switch {
			case inline:
			case goFlagsWithValue[name] && i+1 < len(args):
				token.value, token.separate = args[i+1], true
				i++
			case goFlagsWithValue[name]:
				return plan, false
			}
			token.rebase = goPathFlags[name] || goProfileFlags[name]
			plan.output = plan.output || name == "-o"
			outputDir = outputDir || name == "-outputdir"
			plan.tokens = append(plan.tokens, token)
			continue
		}
		module, rewritten, file, ok := modulePositional(cwd, arg)
		if !ok {
			return plan, false
		}
		positionals++
		plan.filesOnly = plan.filesOnly && file
		if !seen[module] {
			seen[module] = true
			plan.modules = append(plan.modules, module)
		}
		plan.tokens = append(plan.tokens, moduleToken{raw: arg, module: module, positional: rewritten})
	}
	if positionals == 0 {
		return plan, false
	}
	if outputDir {
		// Profile outputs are then relative to -outputdir, which moves.
		for i := range plan.tokens {
			if name, _, _ := goFlagName(plan.tokens[i].raw); goProfileFlags[name] {
				plan.tokens[i].rebase = false
			}
		}
	}
	return plan, true
}

// argsFor rebuilds the arguments for a run from module: its own positionals
// rebased, path flags rebased, other flags and the test tail as written.
func (plan moduleRewrite) argsFor(cwd, module string) ([]string, bool) {
	var out []string
	for _, token := range plan.tokens {
		if token.module != "" {
			if token.module == module {
				out = append(out, token.positional)
			}
			continue
		}
		value := token.value
		if token.rebase && value != "" && value != "-" && !filepath.IsAbs(value) {
			moved, err := filepath.Rel(module, filepath.Join(cwd, value))
			if err != nil {
				return nil, false
			}
			value = filepath.ToSlash(moved)
			if strings.HasSuffix(token.value, "/") {
				value += "/"
			}
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
	return out, true
}

// modulePositional resolves a .go file or a relative package path (with an
// optional /... pattern) to its owning module and its spelling from there.
// Import paths and missing paths have no owner.
func modulePositional(cwd, arg string) (module, rewritten string, file, ok bool) {
	if strings.HasSuffix(arg, ".go") {
		path := arg
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			return "", "", false, false
		}
		module = enclosingGoModule(filepath.Dir(path))
		if module == "" {
			return "", "", false, false
		}
		if filepath.IsAbs(arg) {
			return module, arg, true, true
		}
		rel, err := filepath.Rel(module, path)
		if err != nil {
			return "", "", false, false
		}
		return module, filepath.ToSlash(rel), true, true
	}
	dir, pattern := arg, false
	if strings.Contains(dir, "...") {
		if !strings.HasSuffix(dir, "/...") {
			return "", "", false, false
		}
		dir, pattern = strings.TrimSuffix(dir, "/..."), true
		if strings.Contains(dir, "...") {
			return "", "", false, false
		}
	}
	relative := dir == "." || dir == ".." || strings.HasPrefix(dir, "./") || strings.HasPrefix(dir, "../")
	if !relative && !filepath.IsAbs(dir) {
		return "", "", false, false // an import path names no directory here
	}
	path := dir
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	if !isDir(path) {
		return "", "", false, false
	}
	module = enclosingGoModule(path)
	if module == "" {
		return "", "", false, false
	}
	if filepath.IsAbs(dir) {
		return module, arg, false, true
	}
	rel, err := filepath.Rel(module, path)
	if err != nil {
		return "", "", false, false
	}
	rewritten = "."
	if rel != "." {
		rewritten = "./" + filepath.ToSlash(rel)
	}
	if pattern {
		rewritten += "/..." // "." becomes "./..."
	}
	return module, rewritten, false, true
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
// inside cwd or one level beside it, absolute otherwise.
func shownDir(cwd, dir string) string {
	rel, err := filepath.Rel(cwd, dir)
	if err != nil || strings.HasPrefix(rel, "../..") {
		return dir
	}
	return filepath.ToSlash(rel)
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
	return "(cd " + shellWord(shownDir(cwd, dir)) + " && " + strings.Join(words, " ") + ")"
}

var plainShellWord = regexp.MustCompile(`^[A-Za-z0-9@%_+=:,./-]+$`)

func shellWord(s string) string {
	if plainShellWord.MatchString(s) {
		return s
	}
	return shellQuote(s)
}

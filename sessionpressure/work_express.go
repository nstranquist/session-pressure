package sessionpressure

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nstranquist/session-pressure/internal/filelock"
	"golang.org/x/mod/modfile"
)

const (
	// Express defaults pack under residual capacity left by a non-exclusive
	// benchmark while remaining strictly lighter than full test/build weights.
	defaultExpressTestWeight  = 1
	defaultExpressBuildWeight = 2
	expressReuseSchema        = 2
	expressReuseMaxAgeSeconds = 120
	expressSingleflightRetry  = 3
	expressReuseDirName       = "work-express-reuse"
	expressReuseLockDirName   = "work-express-reuse-locks"
)

// IsExpressClass reports whether class is a light package-scoped tier.
func IsExpressClass(class WorkClass) bool {
	return class == WorkClassExpressTest || class == WorkClassExpressBuild
}

// workReuseEligibleClass limits successful-result reuse to source-verifiable
// work. Full tests are eligible because their dependency closure can be
// digested exactly and successful test/vet commands declare no output artifact.
// Full builds are fingerprintable, but only explicitly recognized commands
// with a fail-closed artifact or concurrent-singleflight boundary can reuse.
func workReuseEligibleClass(class WorkClass) bool {
	return IsExpressClass(class) || class == WorkClassTest || class == WorkClassBuild
}

// ResidualCapacityAdmits reports whether available weighted units can admit
// weight under the shared capacity ceiling without overcommit.
func ResidualCapacityAdmits(available, weight, capacity int) bool {
	if weight < 1 || capacity < 1 {
		return false
	}
	return available >= weight && weight <= capacity
}

// ExpressFitsBeside reports whether expressWeight can share capacity with an
// already-running non-exclusive lease of used weight.
func ExpressFitsBeside(used, expressWeight, capacity int) bool {
	return ResidualCapacityAdmits(capacity-used, expressWeight, capacity)
}

// ClassifyWorkArgv maps structural toolchain argv shapes to a coordinated work
// class. It never persists argv and never guesses runtime from free-form text:
// multi-package / workspace-wide shapes stay full weight; single-package targets
// become express. Returns ok=false when argv is not a recognized heavy toolchain.
func ClassifyWorkArgv(argv []string) (WorkClass, bool) {
	if class, ok := classifyShellWrappedWorkArgv(argv); ok {
		return class, true
	}
	return classifyDirectWorkArgvAt(argv, "")
}

func classifyDirectWorkArgvAt(argv []string, cwdOverride string) (WorkClass, bool) {
	if class, ok := classifyNDevAskEvalArgv(argv); ok {
		return class, true
	}
	if class, ok := ClassifyGoWorkArgv(argv); ok {
		return class, true
	}
	if class, ok := classifyCanonicalMakeWorkArgvAt(argv, cwdOverride); ok {
		return class, true
	}
	if class, ok := classifyCargoWorkArgv(argv); ok {
		return class, true
	}
	if class, ok := classifyNodeWorkArgv(argv); ok {
		return class, true
	}
	if class, ok := classifySwiftWorkArgv(argv); ok {
		return class, true
	}
	return "", false
}

func classifyCanonicalMakeWorkArgv(argv []string) (WorkClass, bool) {
	return classifyCanonicalMakeWorkArgvAt(argv, "")
}

func classifyCanonicalMakeWorkArgvAt(argv []string, cwdOverride string) (WorkClass, bool) {
	target, workdir, ok := canonicalMakeBuildTarget(argv, cwdOverride)
	if !ok || !canonicalNDevMakeRoot(workdir) {
		return "", false
	}
	if target == "build-ndev-all" {
		return WorkClassBuild, true
	}
	// Each other canonical target builds one Go binary plus the small atomic
	// publisher. This is the same bounded package scope as direct `go build
	// ./cmd/...`, and identical invocations already collapse through the
	// content-verified canonical-build singleflight.
	return WorkClassExpressBuild, true
}

func canonicalNDevMakeRoot(workdir string) bool {
	const maximumModuleFileBytes = 64 << 10

	modulePath := filepath.Join(workdir, "go.mod")
	for _, path := range []string{modulePath, filepath.Join(workdir, "Makefile")} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return false
		}
		if path == modulePath && info.Size() > maximumModuleFileBytes {
			return false
		}
	}
	moduleFile, err := os.Open(modulePath)
	if err != nil {
		return false
	}
	body, readErr := io.ReadAll(io.LimitReader(moduleFile, maximumModuleFileBytes+1))
	closeErr := moduleFile.Close()
	if readErr != nil || closeErr != nil || len(body) > maximumModuleFileBytes {
		return false
	}
	return modfile.ModulePath(body) == "nicos.tools/nicos-dev"
}

// classifyShellWrappedWorkArgv unwraps only a closed, literal foreground shell
// sequence. It exists for agent/toolguard invocations that reach work-run as
// `bash -lc "cd ... && make ..."`. Dynamic expansion, pipelines, backgrounding,
// redirection, unknown commands, and ambiguous shell syntax refuse
// classification and retain the caller's requested full weight.
func classifyShellWrappedWorkArgv(argv []string) (WorkClass, bool) {
	statement, ok := shellCommandStatement(argv)
	if !ok {
		return "", false
	}
	commands, ok := tokenizeSafeShellSequence(statement)
	if !ok {
		return "", false
	}
	workdir, err := os.Getwd()
	if err != nil {
		return "", false
	}
	workdir, err = filepath.Abs(workdir)
	if err != nil {
		return "", false
	}
	var strongest WorkClass
	for _, command := range commands {
		switch {
		case safeShellSetCommand(command):
			continue
		case len(command) == 2 && command[0] == "cd":
			workdir, ok = safeShellWorkDir(workdir, command[1])
			if !ok {
				return "", false
			}
			continue
		}
		class, classified := classifyDirectWorkArgvAt(command, workdir)
		if !classified {
			return "", false
		}
		strongest = strongerWorkClass(strongest, class)
	}
	if strongest == "" {
		return "", false
	}
	return strongest, true
}

func shellCommandStatement(argv []string) (string, bool) {
	if len(argv) != 3 {
		return "", false
	}
	tokens := []string{
		strings.TrimSpace(argv[0]),
		strings.TrimSpace(argv[1]),
		strings.TrimSpace(argv[2]),
	}
	switch strings.ToLower(filepath.Base(tokens[0])) {
	case "sh", "bash", "zsh":
	default:
		return "", false
	}
	switch tokens[1] {
	case "-c", "-lc", "-cl":
	default:
		return "", false
	}
	if tokens[2] == "" {
		return "", false
	}
	return tokens[2], true
}

func safeShellSetCommand(command []string) bool {
	if len(command) < 2 || command[0] != "set" {
		return false
	}
	switch strings.Join(command[1:], " ") {
	case "-e", "-eu", "-eux", "-euo pipefail", "-euxo pipefail", "-o pipefail":
		return true
	default:
		return false
	}
}

func safeShellWorkDir(current, requested string) (string, bool) {
	requested = strings.TrimSpace(requested)
	if requested == "" || requested == "-" || strings.HasPrefix(requested, "~") ||
		strings.ContainsAny(requested, "$`*?[]{}") {
		return "", false
	}
	if !filepath.IsAbs(requested) {
		requested = filepath.Join(current, requested)
	}
	absolute, err := filepath.Abs(requested)
	if err != nil {
		return "", false
	}
	return filepath.Clean(absolute), true
}

func strongerWorkClass(left, right WorkClass) WorkClass {
	rank := func(class WorkClass) int {
		switch class {
		case WorkClassExpressTest:
			return 1
		case WorkClassExpressBuild:
			return 2
		case WorkClassTest:
			return 3
		case WorkClassBuild:
			return 4
		case WorkClassBenchmark:
			return 5
		case WorkClassBenchmarkExclusive:
			return 6
		default:
			return 0
		}
	}
	if rank(right) > rank(left) {
		return right
	}
	return left
}

// classifyNDevAskEvalArgv identifies the finite Ask fixture gate without
// inspecting fixture contents or query text. Full evaluations are benchmark
// work; selector-scoped probes remain express-test work.
func classifyNDevAskEvalArgv(argv []string) (WorkClass, bool) {
	tokens := normalizeGoArgv(argv)
	if len(tokens) < 3 {
		return "", false
	}
	base := strings.ToLower(filepath.Base(tokens[0]))
	if base != "ndev" && base != "nicos-dev" && base != "ndev-go" {
		return "", false
	}
	index := 1
	for index < len(tokens) && tokens[index] == "--json" {
		index++
	}
	if index+1 >= len(tokens) || tokens[index] != "ask" || tokens[index+1] != "eval" {
		return "", false
	}
	focused := false
	for _, arg := range tokens[index+2:] {
		if arg == "--help" || arg == "-h" {
			return "", false
		}
		flag := strings.SplitN(arg, "=", 2)[0]
		switch flag {
		case "--case", "--surface", "--intent":
			focused = true
		}
	}
	if focused {
		return WorkClassExpressTest, true
	}
	return WorkClassBenchmark, true
}

// tokenizeSafeShellSequence recognizes only literal simple commands separated
// by `&&`, `;`, or newlines. The resulting argv has shell quotes removed. It is
// deliberately not a general shell parser: any construct whose expansion could
// change command count or resource scope fails closed.
func tokenizeSafeShellSequence(input string) ([][]string, bool) {
	var (
		commands           [][]string
		words              []string
		current            []byte
		inSingle, inDouble bool
		escaped            bool
		requiresNext       bool
	)
	flushWord := func() {
		if len(current) == 0 {
			return
		}
		words = append(words, string(current))
		current = current[:0]
	}
	flushCommand := func() bool {
		flushWord()
		if len(words) == 0 {
			return false
		}
		commands = append(commands, append([]string(nil), words...))
		words = words[:0]
		requiresNext = false
		return true
	}
	for index := 0; index < len(input); index++ {
		character := input[index]
		if escaped {
			current = append(current, character)
			escaped = false
			continue
		}
		if inSingle {
			if character == '\'' {
				inSingle = false
			} else {
				current = append(current, character)
			}
			continue
		}
		if inDouble {
			switch character {
			case '"':
				inDouble = false
			case '\\':
				escaped = true
			case '$', '`':
				return nil, false
			default:
				current = append(current, character)
			}
			continue
		}
		switch character {
		case '\\':
			escaped = true
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		case ' ', '\t', '\r':
			flushWord()
		case '\n':
			if len(current) > 0 || len(words) > 0 {
				if !flushCommand() {
					return nil, false
				}
			}
		case '&':
			if index+1 >= len(input) || input[index+1] != '&' || !flushCommand() {
				return nil, false
			}
			index++
			requiresNext = true
		case ';':
			if !flushCommand() {
				return nil, false
			}
		case '$', '`', '|', '<', '>', '(', ')', '{', '}', '#', '*', '?', '[':
			return nil, false
		default:
			current = append(current, character)
		}
	}
	if escaped || inSingle || inDouble {
		return nil, false
	}
	if len(current) > 0 || len(words) > 0 {
		if !flushCommand() {
			return nil, false
		}
	}
	if requiresNext || len(commands) == 0 {
		return nil, false
	}
	return commands, true
}

// ClassifyGoWorkArgv maps structural Go argv shapes to a coordinated work
// class. Multi-package / recursive packages stay full weight; single-package
// targets become express.
func ClassifyGoWorkArgv(argv []string) (WorkClass, bool) {
	tokens := normalizeGoArgv(argv)
	if len(tokens) < 2 {
		return "", false
	}
	if !isGoToolchainBinary(tokens[0]) {
		return "", false
	}
	verb := tokens[1]
	rest := tokens[2:]
	switch verb {
	case "test":
		if goTestRequiresFullWeight(rest) {
			return WorkClassTest, true
		}
		if goArgvIsExpressScope(rest) {
			return WorkClassExpressTest, true
		}
		return WorkClassTest, true
	case "vet":
		if goArgvIsExpressScope(rest) {
			return WorkClassExpressTest, true
		}
		return WorkClassTest, true
	case "build", "install":
		if goArgvIsExpressScope(rest) {
			return WorkClassExpressBuild, true
		}
		return WorkClassBuild, true
	case "run":
		// go run always launches a single main package/fileset; treat as express-build.
		return WorkClassExpressBuild, true
	case "generate":
		// Package-scoped generate runs a bounded set of generators; recursive
		// generate can fan out arbitrary tooling and stays full build weight.
		if goArgvIsExpressScope(rest) {
			return WorkClassExpressBuild, true
		}
		return WorkClassBuild, true
	default:
		return "", false
	}
}

func goTestRequiresFullWeight(args []string) bool {
	// These modes instrument, repeat, fuzz, benchmark, or profile the test
	// binary and are materially more expensive than the package-scoped unit
	// test the express lane models. Keep them at the caller's full test weight
	// even when only one package is named.
	fullWeightFlags := map[string]struct{}{
		"-race": {}, "-msan": {}, "-asan": {},
		"-cover": {}, "-covermode": {}, "-coverpkg": {}, "-coverprofile": {},
		"-bench": {}, "-benchtime": {}, "-fuzz": {}, "-fuzztime": {}, "-cpu": {},
		"-blockprofile": {}, "-cpuprofile": {}, "-memprofile": {},
		"-mutexprofile": {}, "-trace": {},
	}
	for _, arg := range args {
		flag := strings.SplitN(arg, "=", 2)[0]
		if _, expensive := fullWeightFlags[flag]; expensive {
			return true
		}
	}
	return false
}

func classifyCargoWorkArgv(argv []string) (WorkClass, bool) {
	tokens := normalizeGoArgv(argv)
	if len(tokens) < 2 || !isCargoBinary(tokens[0]) {
		return "", false
	}
	verb := tokens[1]
	rest := tokens[2:]
	switch verb {
	case "test", "check", "clippy":
		if cargoArgvIsExpressScope(rest) {
			return WorkClassExpressTest, true
		}
		return WorkClassTest, true
	case "build":
		if cargoArgvIsExpressScope(rest) {
			return WorkClassExpressBuild, true
		}
		return WorkClassBuild, true
	case "bench":
		return WorkClassTest, true
	default:
		return "", false
	}
}

func classifyNodeWorkArgv(argv []string) (WorkClass, bool) {
	tokens := normalizeGoArgv(argv)
	if len(tokens) < 2 {
		return "", false
	}
	base := strings.ToLower(filepath.Base(tokens[0]))
	rest := tokens[1:]
	switch base {
	case "npm", "pnpm", "yarn", "bun":
		// pnpm exec / yarn dlx runners
		if len(rest) >= 2 && (rest[0] == "exec" || rest[0] == "dlx") {
			tool := strings.ToLower(filepath.Base(rest[1]))
			if tool == "vitest" || tool == "jest" {
				if nodeRunnerIsExpressScope(rest[2:]) {
					return WorkClassExpressTest, true
				}
				return WorkClassTest, true
			}
			if tool == "tsc" {
				if tscArgvIsExpressScope(rest[2:]) {
					return WorkClassExpressTest, true
				}
				return WorkClassTest, true
			}
		}
		packageScoped := nodeArgvIsPackageScope(base, rest)
		script, ok := nodePackageScript(base, rest)
		if !ok {
			return "", false
		}
		if isNodeTestScript(script) {
			if packageScoped {
				return WorkClassExpressTest, true
			}
			return WorkClassTest, true
		}
		if isNodeBuildScript(script) {
			if packageScoped {
				return WorkClassExpressBuild, true
			}
			return WorkClassBuild, true
		}
	case "npx", "bunx":
		tool := strings.ToLower(filepath.Base(rest[0]))
		if tool == "vitest" || tool == "jest" {
			if nodeRunnerIsExpressScope(rest[1:]) {
				return WorkClassExpressTest, true
			}
			return WorkClassTest, true
		}
		if tool == "tsc" {
			if tscArgvIsExpressScope(rest[1:]) {
				return WorkClassExpressTest, true
			}
			return WorkClassTest, true
		}
	}
	return "", false
}

func classifySwiftWorkArgv(argv []string) (WorkClass, bool) {
	tokens := normalizeGoArgv(argv)
	if len(tokens) < 2 || !isSwiftBinary(tokens[0]) {
		return "", false
	}
	verb := tokens[1]
	rest := tokens[2:]
	switch verb {
	case "test":
		// swift test --filter Module.Tests is package-scoped; bare swift test is full.
		if swiftArgvIsExpressScope(rest) {
			return WorkClassExpressTest, true
		}
		return WorkClassTest, true
	case "build":
		// swift build of a single product is still often a full graph; keep full
		// weight unless --target narrows to one product.
		if swiftArgvIsExpressScope(rest) {
			return WorkClassExpressBuild, true
		}
		return WorkClassBuild, true
	default:
		return "", false
	}
}

func isCargoBinary(name string) bool {
	return strings.ToLower(filepath.Base(strings.TrimSpace(name))) == "cargo"
}

func isSwiftBinary(name string) bool {
	return strings.ToLower(filepath.Base(strings.TrimSpace(name))) == "swift"
}

func cargoArgvIsExpressScope(args []string) bool {
	hasPackage := false
	workspaceWide := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--workspace" || arg == "--all" || arg == "--all-targets" || arg == "--all-features":
			workspaceWide = true
		case arg == "-p" || arg == "--package":
			hasPackage = true
			if i+1 < len(args) {
				i++
			}
		case strings.HasPrefix(arg, "--package="):
			hasPackage = true
		}
	}
	return hasPackage && !workspaceWide
}

func isNodeTestScript(script string) bool {
	script = strings.ToLower(script)
	return script == "test" || strings.HasPrefix(script, "test:") ||
		script == "typecheck" || script == "check" || script == "lint" ||
		strings.HasPrefix(script, "typecheck:") || strings.HasPrefix(script, "lint:")
}

func isNodeBuildScript(script string) bool {
	script = strings.ToLower(script)
	// Verification scripts commonly compose typechecking, tests, and a real
	// production build. Charge that mixed shape as build rather than allowing
	// the lighter express-test weight to understate its resource envelope.
	return script == "build" || strings.HasPrefix(script, "build:") || script == "verify"
}

func nodeArgvIsPackageScope(manager string, args []string) bool {
	packageScopes := 0
	recordScope := func(value string, directory bool) bool {
		if directory {
			if !nodePackageDirIsConcrete(value) {
				return false
			}
		} else if !nodePackageSelectorIsSingle(value) {
			return false
		}
		packageScopes++
		return packageScopes == 1
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-r" || arg == "--recursive" || strings.HasPrefix(arg, "--recursive="):
			// A package directory/filter combined with recursive execution can
			// still fan out across a workspace and must remain full weight.
			return false
		case arg == "--filter" || arg == "-F" || arg == "--workspace":
			if i+1 >= len(args) || !recordScope(args[i+1], false) {
				return false
			}
			i++
		case strings.HasPrefix(arg, "--filter=") || strings.HasPrefix(arg, "--workspace="):
			_, value, _ := strings.Cut(arg, "=")
			if !recordScope(value, false) {
				return false
			}
		case arg == "workspace" && i+1 < len(args):
			// yarn workspace <name> <script>
			if !recordScope(args[i+1], false) {
				return false
			}
			i++
		case nodePackageDirFlag(manager, arg):
			if i+1 >= len(args) || !recordScope(args[i+1], true) {
				return false
			}
			i++
		case nodePackageDirAssignment(manager, arg):
			_, value, _ := strings.Cut(arg, "=")
			if !recordScope(value, true) {
				return false
			}
		}
	}
	return packageScopes == 1
}

func nodePackageScript(manager string, args []string) (string, bool) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--filter" || arg == "-F" || arg == "--workspace" || nodePackageDirFlag(manager, arg) {
			if i+1 < len(args) {
				i++
			}
			continue
		}
		if strings.HasPrefix(arg, "--filter=") || strings.HasPrefix(arg, "--workspace=") || nodePackageDirAssignment(manager, arg) {
			continue
		}
		if arg == "workspace" && i+1 < len(args) {
			i++
			continue
		}
		if arg == "run" {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return arg, true
	}
	return "", false
}

func nodePackageDirFlag(manager, arg string) bool {
	switch manager {
	case "pnpm":
		return arg == "-C" || arg == "--dir"
	case "npm":
		return arg == "--prefix"
	case "yarn", "bun":
		return arg == "--cwd"
	default:
		return false
	}
}

func nodePackageDirAssignment(manager, arg string) bool {
	var prefix string
	switch manager {
	case "pnpm":
		prefix = "--dir="
	case "npm":
		prefix = "--prefix="
	case "yarn", "bun":
		prefix = "--cwd="
	default:
		return false
	}
	return strings.HasPrefix(arg, prefix) && strings.TrimSpace(strings.TrimPrefix(arg, prefix)) != ""
}

func nodePackageSelectorIsSingle(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "!") || strings.Contains(value, "...") {
		return false
	}
	return !strings.ContainsAny(value, "*{}[],")
}

func nodePackageDirIsConcrete(value string) bool {
	clean := filepath.Clean(strings.TrimSpace(value))
	if clean == "." || clean == ".." || clean == string(filepath.Separator) || filepath.IsAbs(clean) {
		return false
	}
	return !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func nodeRunnerIsExpressScope(args []string) bool {
	// Exactly one concrete path/file/test name without recursive globs is
	// express. Bare `npx vitest` / `npx jest` (no target) is a full suite.
	// vitest's leading `run`/`related` tokens are mode selectors, not paths;
	// `watch` is resident and never expresses.
	paths := 0
	subcommandSlot := true
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if subcommandSlot && (arg == "run" || arg == "related") {
			subcommandSlot = false
			continue
		}
		subcommandSlot = false
		if strings.Contains(arg, "**") || strings.HasSuffix(arg, "/...") {
			return false
		}
		paths++
	}
	return paths == 1
}

// tscArgvIsExpressScope is true for a single explicit project scope
// (-p/--project <path>). Bare tsc resolves an unknown tsconfig graph and
// --build/-b walks project references; both stay full test weight.
func tscArgvIsExpressScope(args []string) bool {
	project := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-b" || arg == "--build":
			return false
		case arg == "-p" || arg == "--project":
			project = true
			if i+1 < len(args) {
				i++
			}
		case strings.HasPrefix(arg, "--project="):
			project = true
		}
	}
	return project
}

func swiftArgvIsExpressScope(args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--filter" || arg == "--target" || arg == "--product" {
			return true
		}
		if strings.HasPrefix(arg, "--filter=") || strings.HasPrefix(arg, "--target=") || strings.HasPrefix(arg, "--product=") {
			return true
		}
	}
	return false
}

// ResolveWorkClass applies structural scope safety:
//   - requested full test/build demotes to express when the command is package-scoped
//   - requested express promotes to full when the command is multi-package / recursive
//   - other classes keep the requested class unless empty
//   - exclusive benchmark becomes the capacity-sized exclusive class
func ResolveWorkClass(requested WorkClass, exclusive bool, command []string) WorkClass {
	if exclusive || requested == WorkClassBenchmarkExclusive {
		return WorkClassBenchmarkExclusive
	}
	if classified, ok := ClassifyWorkArgv(command); ok {
		switch requested {
		case WorkClassTest, WorkClassExpressTest:
			return classified
		case WorkClassBuild, WorkClassExpressBuild:
			// A structurally recognized test command uses the test envelope even
			// when a caller guessed "build". Keeping `go test` at build weight
			// wastes two capacity units without adding a safety boundary: the
			// same classifier is already authoritative when promoting an
			// express request or correcting test -> build.
			return classified
		case WorkClassHeavy:
			return requested
		default:
			if requested == "" {
				return classified
			}
			return requested
		}
	}
	return requested
}

// ClassifyGoWorkStatement extracts a leading heavy-toolchain command shape from
// a shell statement for toolguard routing (Go first, then cargo/node/swift).
func ClassifyGoWorkStatement(statement string) (id string, class WorkClass, matched bool) {
	return ClassifyWorkStatement(statement)
}

// ClassifyWorkStatement extracts a leading toolchain command for toolguard.
func ClassifyWorkStatement(statement string) (id string, class WorkClass, matched bool) {
	if commands, ok := tokenizeSafeShellSequence(strings.TrimSpace(statement)); ok && len(commands) == 1 {
		if classified, classifiedOK := ClassifyWorkArgv(commands[0]); classifiedOK {
			return classifiedWorkID(commands[0], classified), classified, true
		}
	}
	argv := extractToolchainArgvFromStatement(statement)
	if len(argv) == 0 {
		return "", "", false
	}
	classified, ok := ClassifyWorkArgv(argv)
	if !ok {
		return "", "", false
	}
	return classifiedWorkID(argv, classified), classified, true
}

func classifiedWorkID(argv []string, classified WorkClass) string {
	tool := strings.ToLower(filepath.Base(argv[0]))
	switch classified {
	case WorkClassExpressTest:
		return tool + "-express-test"
	case WorkClassExpressBuild:
		return tool + "-express-build"
	case WorkClassTest:
		return tool + "-test"
	case WorkClassBuild:
		return tool + "-build"
	case WorkClassBenchmark:
		return tool + "-benchmark"
	case WorkClassBenchmarkExclusive:
		return tool + "-benchmark-exclusive"
	default:
		return tool + "-work"
	}
}

func normalizeGoArgv(argv []string) []string {
	out := make([]string, 0, len(argv))
	for _, arg := range argv {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}
		out = append(out, arg)
	}
	return out
}

func isGoToolchainBinary(name string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(name)))
	return base == "go"
}

// goArgvIsExpressScope is true for zero or one non-recursive package target and
// no multi-package lists. Flags are ignored except for package-position tokens.
func goArgvIsExpressScope(args []string) bool {
	packages := goPackageTargets(args)
	if len(packages) > 1 {
		return false
	}
	if len(packages) == 1 && goPackageIsRecursiveOrMulti(packages[0]) {
		return false
	}
	// No package args means the current package ("."), which is package-scoped.
	return true
}

func goPackageIsRecursiveOrMulti(target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	if strings.Contains(target, "...") {
		return true
	}
	// Comma-separated lists are not a Go package syntax but agents sometimes
	// pass them; treat as full weight rather than express.
	if strings.Contains(target, ",") {
		return true
	}
	return false
}

func goPackageTargets(args []string) []string {
	var packages []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			// After --, remaining args are test binary args, not packages.
			break
		}
		if strings.HasPrefix(arg, "-") {
			// Flags that take a value: skip next token when not -flag=value.
			if strings.Contains(arg, "=") {
				continue
			}
			switch arg {
			case "-run", "-bench", "-benchtime", "-count", "-cpu", "-parallel", "-timeout",
				"-coverprofile", "-coverpkg", "-covermode", "-tags", "-mod", "-modfile",
				"-exec", "-o", "-p", "-asmflags", "-gcflags", "-ldflags", "-toolexec",
				"-C", "-overlay", "-pgo", "-vet", "-fuzz", "-fuzztime", "-skip",
				"-blockprofile", "-cpuprofile", "-memprofile", "-mutexprofile",
				"-outputdir", "-trace":
				if i+1 < len(args) {
					i++
				}
			}
			continue
		}
		// Go files (*.go) are file-list mode for a single package.
		if strings.HasSuffix(arg, ".go") {
			// File lists stay express (single package compilation unit).
			continue
		}
		packages = append(packages, arg)
	}
	return packages
}

func extractGoArgvFromStatement(statement string) []string {
	return extractToolchainArgvFromStatement(statement)
}

func extractToolchainArgvFromStatement(statement string) []string {
	// Prefer the earliest recognized toolchain token, then scan argv-like
	// tokens until a shell metacharacter boundary.
	idx := indexToolchainToken(statement)
	if idx < 0 {
		return nil
	}
	rest := strings.TrimSpace(statement[idx:])
	return tokenizeShellishArgv(rest)
}

func indexToolchainToken(statement string) int {
	tools := []string{"go", "make", "gmake", "cargo", "swift", "npm", "pnpm", "yarn", "bun", "npx", "bunx"}
	best := -1
	for _, tool := range tools {
		if i := indexBareToolToken(statement, tool); i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	return best
}

func indexBareToolToken(statement, tool string) int {
	// Earliest occurrence of tool as a whole token: preceded by start of
	// statement, whitespace, '(' subshell, or a path segment ('/'), and
	// followed by whitespace. Earliest-position wins so a compound statement
	// (`go build … && go test …`) classifies from its first command, and
	// tool names embedded in longer words (cargo, django) never match.
	for i := 0; i+len(tool) < len(statement); i++ {
		if statement[i:i+len(tool)] != tool {
			continue
		}
		if next := statement[i+len(tool)]; next != ' ' && next != '\t' {
			continue
		}
		if i > 0 {
			switch statement[i-1] {
			case ' ', '\t', '(', '/', '\'', '"':
				// whitespace, subshell, path segment, or shell-quoted start
				// (`bash -c 'go test …'`) all begin a real token.
			default:
				continue
			}
		}
		return i
	}
	return -1
}

func allDigits(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, ch := range b {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func tokenizeShellishArgv(input string) []string {
	var tokens []string
	var current []byte
	inSingle, inDouble, escaped := false, false, false
	flush := func() {
		if len(current) == 0 {
			return
		}
		tokens = append(tokens, string(current))
		current = current[:0]
	}
	for i := 0; i < len(input); i++ {
		ch := input[i]
		if escaped {
			current = append(current, ch)
			escaped = false
			continue
		}
		if ch == '\\' && !inSingle {
			escaped = true
			continue
		}
		if inSingle {
			if ch == '\'' {
				inSingle = false
			} else {
				current = append(current, ch)
			}
			continue
		}
		if inDouble {
			if ch == '"' {
				inDouble = false
			} else {
				current = append(current, ch)
			}
			continue
		}
		switch ch {
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		case ' ', '\t', '\n', '\r':
			flush()
		case ';', '&', '|', '`':
			flush()
			return tokens
		case '<', '>':
			// Redirections end the argv proper: `go test ./pkg 2>&1` must not
			// count the redirection as extra package targets (which would
			// silently promote package-scoped work to full weight). A pure-digit
			// current token is the redirection's fd prefix, not an argument.
			if allDigits(current) {
				current = current[:0]
			}
			flush()
			return tokens
		case '(':
			if len(current) == 0 && len(tokens) == 0 {
				continue
			}
			current = append(current, ch)
		case ')':
			flush()
			return tokens
		default:
			current = append(current, ch)
		}
	}
	flush()
	return tokens
}

// ExpressCommandFingerprint is the legacy public name for the privacy-
// preserving work-reuse key. Complete argv, effective environment, workdir,
// class, and toolchain enter the HMAC, but none are persisted in raw form.
type ExpressCommandFingerprint struct {
	KeyDigest string
	Class     WorkClass
	Shape     string
}

func FingerprintExpressCommand(class WorkClass, command []string, key []byte) (ExpressCommandFingerprint, error) {
	environment, err := WorkEnvironment(os.Environ(), defaultWorkLimits(runtime.NumCPU()), class)
	if err != nil {
		return ExpressCommandFingerprint{}, err
	}
	return fingerprintWorkCommandAt(class, command, key, "", environment)
}

// fingerprintExpressCommandAt is the testable identity path. cwdOverride, when
// non-empty, replaces os.Getwd so unit tests can prove distinct workdirs cannot
// share a successful-exit receipt.
func fingerprintExpressCommandAt(class WorkClass, command []string, key []byte, cwdOverride string) (ExpressCommandFingerprint, error) {
	return fingerprintWorkCommandAt(class, command, key, cwdOverride, expressCommandEnvironment(command))
}

func fingerprintWorkCommandAt(class WorkClass, command []string, key []byte, cwdOverride string, environment []string) (ExpressCommandFingerprint, error) {
	if !workReuseEligibleClass(class) {
		return ExpressCommandFingerprint{}, errors.New("work reuse fingerprint requires an eligible class")
	}
	if len(key) != workReuseKeyBytes {
		return ExpressCommandFingerprint{}, errors.New("express reuse key has invalid length")
	}
	shape := expressShapeMaterialAt(class, command, cwdOverride)
	if shape == "" {
		return ExpressCommandFingerprint{}, errors.New("express fingerprint requires a structural go command shape")
	}
	toolchainDigest, err := digestExpressToolchain(command, cwdOverride, environment)
	if err != nil {
		return ExpressCommandFingerprint{}, err
	}
	mac := hmac.New(sha256.New, key)
	// v4 also binds the complete effective environment. Test behavior can depend
	// on arbitrary variables, so a closed allowlist would make successful-result
	// replay unsound. Values remain inside the keyed digest and are never stored.
	environment = append([]string(nil), environment...)
	sort.Strings(environment)
	frames := append([]string{"ndev-work-reuse-v4", string(class), shape, toolchainDigest}, command...)
	frames = append(frames, environment...)
	for _, frame := range frames {
		if err := writeExpressDigestFrame(mac, []byte(frame)); err != nil {
			return ExpressCommandFingerprint{}, fmt.Errorf("hash express reuse identity: %w", err)
		}
	}
	return ExpressCommandFingerprint{
		KeyDigest: "sha256:" + hex.EncodeToString(mac.Sum(nil)),
		Class:     class,
		Shape:     shape,
	}, nil
}

func writeExpressDigestFrame(writer interface{ Write([]byte) (int, error) }, value []byte) error {
	if _, err := writer.Write([]byte(strconv.Itoa(len(value)) + ":")); err != nil {
		return err
	}
	if _, err := writer.Write(value); err != nil {
		return err
	}
	return nil
}

func expressShapeMaterial(class WorkClass, command []string) string {
	return expressShapeMaterialAt(class, command, "")
}

func expressShapeMaterialAt(class WorkClass, command []string, cwdOverride string) string {
	if target, workdir, ok := canonicalMakeBuildTarget(command, cwdOverride); ok {
		return strings.Join([]string{string(class), "make", workdir, target}, "\x00")
	}
	tokens := normalizeGoArgv(command)
	if len(tokens) < 2 || !isGoToolchainBinary(tokens[0]) {
		return ""
	}
	verb := tokens[1]
	rest := tokens[2:]
	workdir, workdirOK := expressEffectiveWorkDir(rest, cwdOverride)
	if !workdirOK {
		return ""
	}
	packages := goPackageTargets(rest)
	b := make([]byte, 0, len(workdir)+len(verb)+len(class)+32)
	b = append(b, string(class)...)
	b = append(b, 0)
	b = append(b, verb...)
	b = append(b, 0)
	// Absolute workdir is the primary anti-collision bound. Bare `go test` and
	// `go test .` in the same dir still share identity (both mean the current
	// package under this workdir).
	b = append(b, "workdir="...)
	b = append(b, workdir...)
	b = append(b, 0)
	if len(packages) == 0 {
		b = append(b, '.')
	} else {
		for i, pkg := range packages {
			if i > 0 {
				b = append(b, 0)
			}
			// Normalize explicit current-package aliases to one token so
			// `go test` and `go test .` still singleflight in one workdir.
			if pkg == "." {
				b = append(b, '.')
			} else {
				b = append(b, pkg...)
			}
		}
	}
	// Include a small closed set of semantic flags. -C is already folded into
	// workdir; free-form paths are never persisted outside the HMAC input.
	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		name, value, hasValue := strings.Cut(arg, "=")
		switch name {
		case "-C":
			// Workdir already carries the absolute -C target.
			if !hasValue && i+1 < len(rest) && !strings.HasPrefix(rest[i+1], "-") {
				i++
			}
			continue
		case "-run", "-tags", "-count", "-race", "-short", "-cover", "-bench":
			b = append(b, 0)
			b = append(b, name...)
			if hasValue {
				b = append(b, '=')
				b = append(b, value...)
			} else if i+1 < len(rest) && !strings.HasPrefix(rest[i+1], "-") && name != "-race" && name != "-short" && name != "-cover" {
				i++
				b = append(b, '=')
				b = append(b, rest[i]...)
			} else if name == "-race" || name == "-short" || name == "-cover" {
				b = append(b, "=1"...)
			}
		}
	}
	return string(b)
}

// expressEffectiveWorkDir returns the absolute directory that scopes package
// targets for reuse identity: the first go -C value when present, otherwise the
// process working directory. Empty/unresolvable roots refuse fingerprinting so
// we never mint a cross-directory receipt.
func expressEffectiveWorkDir(args []string, cwdOverride string) (string, bool) {
	chdir := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if arg == "-C" {
			if i+1 < len(args) {
				chdir = args[i+1]
				i++
			}
			continue
		}
		if strings.HasPrefix(arg, "-C=") {
			chdir = strings.TrimPrefix(arg, "-C=")
			continue
		}
	}
	base := strings.TrimSpace(cwdOverride)
	if base == "" {
		wd, err := os.Getwd()
		if err != nil || strings.TrimSpace(wd) == "" {
			return "", false
		}
		base = wd
	}
	base, err := filepath.Abs(base)
	if err != nil {
		return "", false
	}
	base = filepath.Clean(base)
	if chdir == "" {
		return base, true
	}
	if filepath.IsAbs(chdir) {
		return filepath.Clean(chdir), true
	}
	return filepath.Clean(filepath.Join(base, chdir)), true
}

type expressReuseReceipt struct {
	SchemaVersion int                    `json:"schema_version"`
	ReceiptID     string                 `json:"receipt_id"`
	KeyDigest     string                 `json:"key_digest"`
	Class         WorkClass              `json:"class"`
	CreatedAt     time.Time              `json:"created_at"`
	ExpiresAt     time.Time              `json:"expires_at"`
	ExitCode      int                    `json:"exit_code"`
	Outcome       string                 `json:"outcome"`
	SourceDigest  string                 `json:"source_digest"`
	Artifacts     []expressReuseArtifact `json:"artifacts"`
}

// RunWorkCommandWithExpressReuse wraps RunWorkCommand with concurrent
// singleflight and short-lived, source-verified successful-result reuse for
// express work and full Go tests. Host red admission remains fail-closed inside
// RunWorkCommand for every class.
func RunWorkCommandWithExpressReuse(coordinator *WorkCoordinator, options WorkRunOptions, admissionCheck func() Admission, retryInterval time.Duration, streams WorkRunStreams) (int, error) {
	if options.RequestedClass == "" {
		options.RequestedClass = options.Class
	}
	options.Class = ResolveWorkClass(options.Class, options.Exclusive, options.Command)
	if options.Class == WorkClassBenchmarkExclusive {
		options.Exclusive = true
	}
	if !workReuseEligibleClass(options.Class) {
		return RunWorkCommand(coordinator, options, admissionCheck, retryInterval, streams)
	}
	if options.NoReuse {
		options.ReuseStatus = "off"
		options.ReuseDecision = ExpressReuseDecisionRan
		options.ReuseRefusalReason = ExpressReuseDisabled
		return RunWorkCommand(coordinator, options, admissionCheck, retryInterval, streams)
	}
	if coordinator == nil {
		return 1, errors.New("work coordinator is required")
	}
	key, err := readOrCreateWorkReuseKey(coordinator.Dir)
	if err != nil {
		return 1, err
	}
	childEnvironment, environmentErr := WorkEnvironment(os.Environ(), coordinator.Limits, options.Class)
	if environmentErr != nil {
		options.ReuseStatus = "miss"
		options.ReuseDecision = ExpressReuseDecisionRan
		options.ReuseRefusalReason = ExpressReuseReceiptUnavailable
		return RunWorkCommand(coordinator, options, admissionCheck, retryInterval, streams)
	}
	fingerprint, fingerprintErr := fingerprintWorkCommandAt(options.Class, options.Command, key, "", childEnvironment)
	if fingerprintErr != nil {
		// Non-Go express commands and unavailable integrity inputs still execute;
		// they simply cannot replay a successful exit status.
		return RunWorkCommand(coordinator, options, admissionCheck, retryInterval, streams)
	}
	options.ReuseKeyDigest = fingerprint.KeyDigest
	if err := pruneExpressReuseReceipts(coordinator.Dir, time.Now().UTC(), fingerprint.KeyDigest); err != nil {
		return 1, fmt.Errorf("prune express reuse receipts: %w", err)
	}
	artifactPaths, cacheable, artifactErr := expressDeclaredArtifactPaths(options.Command, "")
	if artifactErr != nil || !cacheable {
		options.ReuseStatus = "miss"
		options.ReuseDecision = ExpressReuseDecisionRan
		options.ReuseRefusalReason = ExpressReuseReceiptUnavailable
		if artifactErr == nil && expressWritesUndeclaredBinary(options.Command, "") {
			// Separate the "Go wrote a binary we cannot name" population from
			// genuine I/O failures so its size stays visible in work history.
			options.ReuseRefusalReason = ExpressReuseUndeclaredBinary
		}
		return RunWorkCommand(coordinator, options, admissionCheck, retryInterval, streams)
	}
	started := time.Now()
	var singleflightWait time.Duration
	for attempt := 0; attempt < expressSingleflightRetry; attempt++ {
		lockPath := expressReuseLockPath(coordinator.Dir, fingerprint.KeyDigest)
		if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
			return 1, err
		}
		lockTimeout := options.Wait
		if lockTimeout > 0 {
			remaining := lockTimeout - time.Since(started)
			if remaining <= 0 {
				return 1, errors.New("wait for express work singleflight: deadline exceeded")
			}
			lockTimeout = remaining
		} else {
			// wait=0 still allows a brief singleflight handoff so concurrent
			// identical package jobs collapse instead of racing two leases.
			lockTimeout = 50 * time.Millisecond
		}
		waitStarted := time.Now()
		unlock, lockErr := filelock.AcquireContext(context.Background(), lockPath, lockTimeout)
		singleflightWait += time.Since(waitStarted)
		if lockErr != nil {
			if options.Wait == 0 {
				// Fall through to normal capacity path when the peer is slow.
				return RunWorkCommand(coordinator, options, admissionCheck, retryInterval, streams)
			}
			return workExitWaitTimeout, fmt.Errorf("wait for identical express work: %w", lockErr)
		}
		sourceBefore, sourceErr := snapshotExpressSource(options.Command, "")
		if sourceErr != nil {
			unlock()
			options.ReuseStatus = "miss"
			options.ReuseDecision = ExpressReuseDecisionRan
			options.ReuseRefusalReason = ExpressReuseReceiptUnavailable
			return RunWorkCommand(coordinator, options, admissionCheck, retryInterval, streams)
		}
		receipt, hit, refusalReason, receiptErr := readExpressReuseReceipt(coordinator.Dir, fingerprint, sourceBefore.Digest, artifactPaths, key, time.Now().UTC())
		if receiptErr != nil {
			unlock()
			options.ReuseStatus = "miss"
			options.ReuseDecision = ExpressReuseDecisionRan
			options.ReuseRefusalReason = refusalReason
			return RunWorkCommand(coordinator, options, admissionCheck, retryInterval, streams)
		}
		if hit && workReuseSingleflightOnly(options.Command) && receipt.CreatedAt.Before(started) {
			hit = false
			refusalReason = ExpressReuseSingleflightOnly
		}
		if hit {
			// Only return a successful-exit receipt when host admission would
			// still allow coordinated work. Identity is workdir-bound above;
			// this keeps red memory/storage from being skipped by a stale hit
			// without re-running non-identical commands.
			if admissionCheck != nil {
				if admission := admissionCheck(); !admission.Allowed {
					unlock()
					options.ReuseStatus = "miss"
					options.ReuseDecision = ExpressReuseDecisionRan
					return RunWorkCommand(coordinator, options, admissionCheck, retryInterval, streams)
				}
			}
			options.ReuseStatus = "hit"
			options.ReuseDecision = ExpressReuseDecisionReused
			options.ReceiptDigest = receipt.KeyDigest
			options.SingleflightWaitMS = max(int64(0), singleflightWait.Milliseconds())
			if err := recordExpressReuseEvent(coordinator, options, fingerprint, singleflightWait); err != nil {
				unlock()
				return 1, err
			}
			unlock()
			if streams.Stderr != nil && options.Progress != WorkProgressQuiet {
				fmt.Fprintf(streams.Stderr, "ndev session pressure: reused verified work class=%s receipt=%s\n", options.Class, strings.TrimPrefix(receipt.KeyDigest, "sha256:")[:12])
			}
			return receipt.ExitCode, nil
		}
		remaining := options
		if options.Wait > 0 {
			remaining.Wait = options.Wait - time.Since(started)
			if remaining.Wait <= 0 {
				unlock()
				return 1, errors.New("wait for express work capacity: deadline exceeded")
			}
		}
		remaining.ReuseStatus = "miss"
		remaining.ReuseDecision = ExpressReuseDecisionRan
		remaining.ReuseRefusalReason = refusalReason
		remaining.SingleflightWaitMS = max(int64(0), singleflightWait.Milliseconds())
		var executionSource expressSourceSnapshot
		remaining.reusePreparer = func() ExpressReuseRefusalReason {
			refreshedSource, refreshedErr := snapshotExpressSource(options.Command, "")
			if refreshedErr != nil {
				return ExpressReuseReceiptUnavailable
			}
			executionSource = refreshedSource
			return ""
		}
		remaining.reuseFinalizer = func() ExpressReuseRefusalReason {
			sourceAfter, sourceAfterErr := snapshotExpressSource(options.Command, "")
			if sourceAfterErr != nil {
				return ExpressReuseReceiptUnavailable
			}
			if sourceAfter.Digest != executionSource.Digest {
				return ExpressReuseSourceChanged
			}
			artifacts, artifactReason, artifactSnapshotErr := snapshotExpressArtifacts(artifactPaths, key)
			if artifactSnapshotErr != nil {
				return ExpressReuseReceiptUnavailable
			}
			if artifactReason != "" {
				return artifactReason
			}
			if writeErr := writeExpressReuseReceipt(coordinator.Dir, fingerprint, sourceAfter.Digest, artifacts, 0, time.Now().UTC()); writeErr != nil {
				return ExpressReuseReceiptUnavailable
			}
			return ""
		}
		code, runErr := RunWorkCommand(coordinator, remaining, admissionCheck, retryInterval, streams)
		if runErr != nil || code != 0 {
			unlock()
			return code, runErr
		}
		unlock()
		return 0, nil
	}
	return RunWorkCommand(coordinator, options, admissionCheck, retryInterval, streams)
}

func expressReusePath(dir, keyDigest string) string {
	return filepath.Join(dir, expressReuseDirName, strings.TrimPrefix(keyDigest, "sha256:")+".json")
}

func expressReuseLockPath(dir, keyDigest string) string {
	return filepath.Join(dir, expressReuseLockDirName, strings.TrimPrefix(keyDigest, "sha256:"))
}

func readExpressReuseReceipt(dir string, fingerprint ExpressCommandFingerprint, sourceDigest string, artifactPaths []string, key []byte, now time.Time) (expressReuseReceipt, bool, ExpressReuseRefusalReason, error) {
	path := expressReusePath(dir, fingerprint.KeyDigest)
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return expressReuseReceipt{}, false, "", nil
	}
	if err != nil {
		return expressReuseReceipt{}, false, ExpressReuseSourceChanged, fmt.Errorf("read express reuse receipt: %w", err)
	}
	var receipt expressReuseReceipt
	if err := json.Unmarshal(body, &receipt); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return expressReuseReceipt{}, false, ExpressReuseSourceChanged, fmt.Errorf("remove malformed express reuse receipt: %w", removeErr)
		}
		return expressReuseReceipt{}, false, ExpressReuseSourceChanged, nil
	}
	refusalReason := ExpressReuseSourceChanged
	invalid := receipt.SchemaVersion != expressReuseSchema || receipt.KeyDigest != fingerprint.KeyDigest || receipt.Class != fingerprint.Class || receipt.Outcome != "successful" || !validSHA256Digest(receipt.SourceDigest) || receipt.Artifacts == nil
	if !now.Before(receipt.ExpiresAt) {
		invalid = true
		refusalReason = ExpressReuseTTLExpired
	}
	if invalid {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return expressReuseReceipt{}, false, refusalReason, fmt.Errorf("remove stale express reuse receipt: %w", err)
		}
		return expressReuseReceipt{}, false, refusalReason, nil
	}
	if receipt.SourceDigest != sourceDigest {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return expressReuseReceipt{}, false, ExpressReuseSourceChanged, fmt.Errorf("remove source-stale express reuse receipt: %w", err)
		}
		return expressReuseReceipt{}, false, ExpressReuseSourceChanged, nil
	}
	artifacts, artifactReason, artifactErr := snapshotExpressArtifacts(artifactPaths, key)
	if artifactErr != nil {
		return expressReuseReceipt{}, false, artifactReason, artifactErr
	}
	if artifactReason != "" || !expressArtifactsEqual(receipt.Artifacts, artifacts) {
		if artifactReason == "" {
			artifactReason = ExpressReuseStaleArtifact
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return expressReuseReceipt{}, false, artifactReason, fmt.Errorf("remove artifact-stale express reuse receipt: %w", err)
		}
		return expressReuseReceipt{}, false, artifactReason, nil
	}
	return receipt, true, "", nil
}

func writeExpressReuseReceipt(dir string, fingerprint ExpressCommandFingerprint, sourceDigest string, artifacts []expressReuseArtifact, exitCode int, now time.Time) error {
	if exitCode != 0 {
		return nil
	}
	if !validSHA256Digest(sourceDigest) || artifacts == nil {
		return errors.New("express reuse receipt requires verified source and artifact boundaries")
	}
	receiptID, err := NewWorkOperationID()
	if err != nil {
		return err
	}
	receipt := expressReuseReceipt{
		SchemaVersion: expressReuseSchema, ReceiptID: receiptID, KeyDigest: fingerprint.KeyDigest,
		Class: fingerprint.Class, CreatedAt: now, ExpiresAt: now.Add(expressReuseMaxAgeSeconds * time.Second),
		ExitCode: 0, Outcome: "successful", SourceDigest: sourceDigest, Artifacts: artifacts,
	}
	body, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(expressReusePath(dir, fingerprint.KeyDigest)), 0o700); err != nil {
		return err
	}
	if err := atomicWrite(expressReusePath(dir, fingerprint.KeyDigest), append(body, '\n'), 0o600); err != nil {
		return fmt.Errorf("persist express reuse receipt: %w", err)
	}
	return nil
}

func pruneExpressReuseReceipts(dir string, now time.Time, preserveKeyDigest string) error {
	cacheDir := filepath.Join(dir, expressReuseDirName)
	entries, err := os.ReadDir(cacheDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for index, entry := range entries {
		if index >= 10_000 || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if entry.Name() == strings.TrimPrefix(preserveKeyDigest, "sha256:")+".json" {
			// The lookup owns validation/removal for the current key so its typed
			// refusal reason (including ttl_expired or legacy source_changed) is
			// retained in work history.
			continue
		}
		path := filepath.Join(cacheDir, entry.Name())
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		var receipt expressReuseReceipt
		if json.Unmarshal(body, &receipt) != nil || receipt.SchemaVersion != expressReuseSchema || receipt.Outcome != "successful" || !validSHA256Digest(receipt.SourceDigest) || receipt.Artifacts == nil || !now.Before(receipt.ExpiresAt) {
			if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return removeErr
			}
		}
	}
	return nil
}

func recordExpressReuseEvent(coordinator *WorkCoordinator, options WorkRunOptions, fingerprint ExpressCommandFingerprint, singleflightWait time.Duration) error {
	store := NewWorkEventStore(coordinator.Dir)
	operationID, err := NewWorkOperationID()
	if err != nil {
		return err
	}
	weight, err := coordinator.Limits.Weight(options.Class)
	if err != nil {
		weight, _ = normalizeWorkLimits(coordinator.Limits).Weight(options.Class)
	}
	exit := 0
	outcome := "successful_receipt_reused"
	if IsExpressClass(options.Class) {
		outcome = "express_reuse_hit"
	} else if workReuseSingleflightOnly(options.Command) {
		outcome = "successful_singleflight_joined"
	}
	resolvedExecutable, resolveErr := exec.LookPath(options.Command[0])
	if resolveErr != nil {
		return fmt.Errorf("resolve reused command shape: %w", resolveErr)
	}
	event := WorkEvent{
		Event: WorkEventReused, OperationID: operationID, Class: options.Class, Weight: weight,
		Outcome: outcome, ExitCode: &exit, ReuseKeyDigest: fingerprint.KeyDigest, ReceiptDigest: fingerprint.KeyDigest,
		ReuseStatus: "hit", ReuseDecision: ExpressReuseDecisionReused, SingleflightWaitMS: max(int64(0), singleflightWait.Milliseconds()),
		CommandDigest:         CommandShapeDigest(resolvedExecutable, max(0, len(options.Command)-1)),
		SessionDigest:         DetectedAgentSessionDigest(os.Environ()),
		SchedulingPolicy:      coordinator.schedulingPolicy(),
		SelectorSchemaVersion: workSelectorSchemaVersion,
	}
	if options.RequestedClass != options.Class {
		event.RequestedClass = options.RequestedClass
	}
	return store.AppendDurable(event)
}

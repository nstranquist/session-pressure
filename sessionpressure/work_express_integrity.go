package sessionpressure

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const expressSourceListMaximumBytes = 32 << 20

type expressReuseArtifact struct {
	PathDigest    string `json:"path_digest"`
	ContentDigest string `json:"content_digest"`
	SizeBytes     int64  `json:"size_bytes"`
}

type expressSourceSnapshot struct {
	Digest string
}

type expressGoListModule struct {
	Dir   string
	GoMod string
}

type expressGoListError struct {
	Err string
}

type expressGoListPackage struct {
	Dir          string
	Standard     bool
	GoFiles      []string
	CgoFiles     []string
	CFiles       []string
	CXXFiles     []string
	MFiles       []string
	HFiles       []string
	FFiles       []string
	SFiles       []string
	SwigFiles    []string
	SwigCXXFiles []string
	SysoFiles    []string
	EmbedFiles   []string
	TestGoFiles  []string
	XTestGoFiles []string
	Module       *expressGoListModule
	Error        *expressGoListError
	DepsErrors   []expressGoListError
}

func digestExpressToolchain(command []string, cwdOverride string, environment []string) (string, error) {
	if _, workdir, ok := canonicalMakeBuildTarget(command, cwdOverride); ok {
		return digestCanonicalMakeToolchains(command[0], workdir, environment)
	}
	tokens := normalizeGoArgv(command)
	if len(tokens) < 2 || !isGoToolchainBinary(tokens[0]) {
		return "", errors.New("express toolchain digest requires a Go command")
	}
	base := strings.TrimSpace(cwdOverride)
	if base == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve express toolchain cwd: %w", err)
		}
		base = wd
	}
	resolved := tokens[0]
	var err error
	if strings.ContainsRune(resolved, filepath.Separator) {
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(base, resolved)
		}
		resolved, err = filepath.Abs(resolved)
	} else {
		resolved, err = exec.LookPath(resolved)
	}
	if err != nil {
		return "", fmt.Errorf("resolve express Go toolchain: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat express Go toolchain: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("express Go toolchain is not a regular file")
	}
	digest, err := digestStableWorkBatchFile(resolved, info)
	if err != nil {
		return "", fmt.Errorf("digest express Go toolchain: %w", err)
	}
	return digest, nil
}

func digestCanonicalMakeToolchains(makeName, workdir string, environment []string) (string, error) {
	goName := strings.TrimSpace(environmentValue(environment, "GO"))
	if goName == "" {
		goName = "go"
	}
	if len(strings.Fields(goName)) != 1 {
		return "", errors.New("canonical make reuse requires GO to name one executable")
	}
	names := []string{makeName, goName, "git"}
	digest := sha256.New()
	for _, name := range names {
		resolved, err := resolveWorkExecutable(name, workdir)
		if err != nil {
			return "", fmt.Errorf("resolve canonical make toolchain %q: %w", name, err)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return "", fmt.Errorf("stat canonical make toolchain %q: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("canonical make toolchain %q is not a regular file", name)
		}
		contentDigest, err := digestStableWorkBatchFile(resolved, info)
		if err != nil {
			return "", fmt.Errorf("digest canonical make toolchain %q: %w", name, err)
		}
		writeWorkBatchFrame(digest, []byte(resolved))
		writeWorkBatchFrame(digest, []byte(contentDigest))
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

func resolveWorkExecutable(name, workdir string) (string, error) {
	if strings.ContainsRune(name, filepath.Separator) {
		if !filepath.IsAbs(name) {
			name = filepath.Join(workdir, name)
		}
		return filepath.Abs(name)
	}
	return exec.LookPath(name)
}

func environmentValue(environment []string, name string) string {
	prefix := name + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

func snapshotExpressSource(command []string, cwdOverride string) (expressSourceSnapshot, error) {
	if target, workdir, ok := canonicalMakeBuildTarget(command, cwdOverride); ok {
		return snapshotCanonicalMakeBuildSource(target, workdir, expressCommandEnvironment(command))
	}
	tokens := normalizeGoArgv(command)
	if len(tokens) < 2 || !isGoToolchainBinary(tokens[0]) {
		return expressSourceSnapshot{}, errors.New("express source snapshot requires a Go command")
	}
	verb := tokens[1]
	if verb != "build" && verb != "test" && verb != "vet" {
		return expressSourceSnapshot{}, fmt.Errorf("express successful reuse is unavailable for go %s", verb)
	}
	rest := tokens[2:]
	workdir, ok := expressEffectiveWorkDir(rest, cwdOverride)
	if !ok {
		return expressSourceSnapshot{}, errors.New("resolve express source workdir")
	}
	tool, err := resolveExpressGoTool(tokens[0], cwdOverride)
	if err != nil {
		return expressSourceSnapshot{}, err
	}
	listArgs := expressGoListArgs(verb, rest)
	ctx, cancel := contextWithExpressSourceTimeout()
	defer cancel()
	cmd := exec.CommandContext(ctx, tool, listArgs...)
	cmd.Dir = workdir
	cmd.Env = expressCommandEnvironment(command)
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return expressSourceSnapshot{}, fmt.Errorf("open express Go source closure: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return expressSourceSnapshot{}, fmt.Errorf("start express Go source closure: %w", err)
	}
	output, readErr := io.ReadAll(io.LimitReader(stdout, expressSourceListMaximumBytes+1))
	if readErr != nil {
		return expressSourceSnapshot{}, errors.Join(readErr, stopExpressSourceList(cmd))
	}
	if len(output) > expressSourceListMaximumBytes {
		return expressSourceSnapshot{}, errors.Join(errors.New("express Go source closure output exceeded 32 MiB"), stopExpressSourceList(cmd))
	}
	if err := cmd.Wait(); err != nil {
		return expressSourceSnapshot{}, fmt.Errorf("list express Go source closure: %w", err)
	}
	files := make(map[string]struct{})
	decoder := json.NewDecoder(bytes.NewReader(output))
	packages := 0
	for {
		var pkg expressGoListPackage
		if err := decoder.Decode(&pkg); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return expressSourceSnapshot{}, fmt.Errorf("decode express Go source closure: %w", err)
		}
		packages++
		if pkg.Error != nil || len(pkg.DepsErrors) > 0 {
			return expressSourceSnapshot{}, errors.New("express Go source closure is incomplete")
		}
		if !pkg.Standard {
			for _, names := range [][]string{
				pkg.GoFiles, pkg.CgoFiles, pkg.CFiles, pkg.CXXFiles, pkg.MFiles,
				pkg.HFiles, pkg.FFiles, pkg.SFiles, pkg.SwigFiles, pkg.SwigCXXFiles,
				pkg.SysoFiles, pkg.EmbedFiles, pkg.TestGoFiles, pkg.XTestGoFiles,
			} {
				for _, name := range names {
					if strings.TrimSpace(name) != "" {
						// go list -test emits synthetic cache-backed absolute files for
						// generated test mains. Their real inputs are already represented
						// by the package TestGoFiles/XTestGoFiles; binding cache paths would
						// make truthful reuse depend on ephemeral go-build entries.
						if filepath.IsAbs(name) {
							continue
						}
						files[filepath.Clean(filepath.Join(pkg.Dir, name))] = struct{}{}
					}
				}
			}
		}
		if pkg.Module != nil {
			addExpressSourceFile(files, pkg.Module.GoMod)
			if pkg.Module.Dir != "" {
				addExpressSourceFile(files, filepath.Join(pkg.Module.Dir, "go.sum"))
			}
		}
	}
	if packages == 0 {
		return expressSourceSnapshot{}, errors.New("express Go source closure was empty")
	}
	addExpressWorkspaceFiles(files, workdir)
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	if len(paths) > workBatchMaximumEntries {
		return expressSourceSnapshot{}, fmt.Errorf("express source closure exceeds %d files", workBatchMaximumEntries)
	}
	digest := sha256.New()
	var totalBytes int64
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return expressSourceSnapshot{}, fmt.Errorf("stat express source input: %w", err)
		}
		if !info.Mode().IsRegular() {
			return expressSourceSnapshot{}, errors.New("express source input is not a regular file")
		}
		totalBytes += info.Size()
		if totalBytes > workBatchMaximumInputBytes {
			return expressSourceSnapshot{}, fmt.Errorf("express source closure exceeds %d bytes", workBatchMaximumInputBytes)
		}
		contentDigest, err := digestStableWorkBatchFile(path, info)
		if err != nil {
			return expressSourceSnapshot{}, fmt.Errorf("digest express source input: %w", err)
		}
		writeWorkBatchFrame(digest, []byte(path))
		writeWorkBatchFrame(digest, []byte(contentDigest))
	}
	return expressSourceSnapshot{Digest: "sha256:" + hex.EncodeToString(digest.Sum(nil))}, nil
}

func snapshotCanonicalMakeBuildSource(target, workdir string, environment []string) (expressSourceSnapshot, error) {
	commands, err := canonicalMakeBuildGoCommands(target, environment)
	if err != nil {
		return expressSourceSnapshot{}, err
	}
	digest := sha256.New()
	for _, command := range commands {
		snapshot, err := snapshotExpressSource(command, workdir)
		if err != nil {
			return expressSourceSnapshot{}, fmt.Errorf("snapshot canonical make target %s: %w", target, err)
		}
		writeWorkBatchFrame(digest, []byte(strings.Join(command, "\x00")))
		writeWorkBatchFrame(digest, []byte(snapshot.Digest))
	}
	makefile := filepath.Join(workdir, "Makefile")
	info, err := os.Stat(makefile)
	if err != nil {
		return expressSourceSnapshot{}, fmt.Errorf("stat canonical makefile: %w", err)
	}
	makefileDigest, err := digestStableWorkBatchFile(makefile, info)
	if err != nil {
		return expressSourceSnapshot{}, fmt.Errorf("digest canonical makefile: %w", err)
	}
	writeWorkBatchFrame(digest, []byte(makefileDigest))
	for _, args := range [][]string{{"rev-parse", "HEAD"}, {"status", "--porcelain=v1", "--untracked-files=no"}} {
		ctx, cancel := contextWithExpressSourceTimeout()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = workdir
		cmd.Env = environment
		output, commandErr := cmd.Output()
		cancel()
		if commandErr != nil {
			return expressSourceSnapshot{}, fmt.Errorf("capture canonical make vcs state: %w", commandErr)
		}
		if len(output) > expressSourceListMaximumBytes {
			return expressSourceSnapshot{}, errors.New("canonical make vcs state exceeded 32 MiB")
		}
		writeWorkBatchFrame(digest, output)
	}
	return expressSourceSnapshot{Digest: "sha256:" + hex.EncodeToString(digest.Sum(nil))}, nil
}

func canonicalMakeBuildGoCommands(target string, environment []string) ([][]string, error) {
	goName := strings.TrimSpace(environmentValue(environment, "GO"))
	if goName == "" {
		goName = "go"
	}
	if len(strings.Fields(goName)) != 1 {
		return nil, errors.New("canonical make reuse requires GO to name one executable")
	}
	tags := strings.TrimSpace(environmentValue(environment, "NDEV_GO_BUILD_TAGS"))
	if tags == "" {
		tags = "sqlite_fts5"
	}
	ndev := []string{goName, "build", "-tags", tags, "./cmd/ndev-go"}
	resident := []string{goName, "build", "github.com/nstranquist/session-pressure/sessionpressure/daemon"}
	toolguard := []string{goName, "build", "./cmd/toolguard"}
	publisher := []string{goName, "build", "./cmd/ndev-binary-publish"}
	switch target {
	case "build-ndev":
		return [][]string{ndev, publisher}, nil
	case "build-ndev-session-pressure":
		return [][]string{resident, publisher}, nil
	case "build-toolguard":
		return [][]string{toolguard, publisher}, nil
	case "build-ndev-all":
		return [][]string{ndev, resident, toolguard, publisher}, nil
	default:
		return nil, fmt.Errorf("unsupported canonical make build target %q", target)
	}
}

func canonicalMakeBuildTarget(command []string, cwdOverride string) (target, workdir string, ok bool) {
	tokens := normalizeGoArgv(command)
	if len(tokens) < 2 {
		return "", "", false
	}
	tool := strings.ToLower(filepath.Base(tokens[0]))
	if tool != "make" && tool != "gmake" {
		return "", "", false
	}
	directory := ""
	directorySet := false
	for index := 1; index < len(tokens); index++ {
		arg := tokens[index]
		switch {
		case arg == "-C" || arg == "--directory":
			index++
			if index >= len(tokens) || directorySet || strings.TrimSpace(tokens[index]) == "" {
				return "", "", false
			}
			directory = tokens[index]
			directorySet = true
		case strings.HasPrefix(arg, "--directory="):
			if directorySet {
				return "", "", false
			}
			directory = strings.TrimPrefix(arg, "--directory=")
			if strings.TrimSpace(directory) == "" {
				return "", "", false
			}
			directorySet = true
		case strings.HasPrefix(arg, "-C") && len(arg) > 2:
			if directorySet {
				return "", "", false
			}
			directory = strings.TrimPrefix(arg, "-C")
			if strings.TrimSpace(directory) == "" {
				return "", "", false
			}
			directorySet = true
		case strings.HasPrefix(arg, "-") || strings.Contains(arg, "=") || target != "":
			// Alternate makefiles, parallelism flags, variable assignments, and
			// multiple targets can change the resource envelope. Keep them full
			// weight unless a future explicit contract proves otherwise.
			return "", "", false
		default:
			target = arg
		}
	}
	switch target {
	case "build-ndev", "build-ndev-session-pressure", "build-toolguard", "build-ndev-all":
	default:
		return "", "", false
	}
	base := strings.TrimSpace(cwdOverride)
	if base == "" {
		var err error
		base, err = os.Getwd()
		if err != nil {
			return "", "", false
		}
	}
	base, err := filepath.Abs(base)
	if err != nil {
		return "", "", false
	}
	if directorySet {
		if filepath.IsAbs(directory) {
			base = directory
		} else {
			base = filepath.Join(base, directory)
		}
	}
	return target, filepath.Clean(base), true
}

func workReuseSingleflightOnly(command []string) bool {
	_, _, ok := canonicalMakeBuildTarget(command, "")
	return ok
}

func stopExpressSourceList(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	killErr := cmd.Process.Kill()
	if errors.Is(killErr, os.ErrProcessDone) {
		killErr = nil
	}
	waitErr := cmd.Wait()
	if _, ok := waitErr.(*exec.ExitError); ok {
		waitErr = nil
	}
	return errors.Join(killErr, waitErr)
}

func contextWithExpressSourceTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 15*time.Second)
}

func resolveExpressGoTool(name, cwdOverride string) (string, error) {
	if strings.ContainsRune(name, filepath.Separator) {
		base := strings.TrimSpace(cwdOverride)
		if base == "" {
			var err error
			base, err = os.Getwd()
			if err != nil {
				return "", err
			}
		}
		if !filepath.IsAbs(name) {
			name = filepath.Join(base, name)
		}
		return filepath.Abs(name)
	}
	return exec.LookPath(name)
}

func expressGoListArgs(verb string, rest []string) []string {
	args := []string{"list", "-deps", "-json"}
	if verb == "test" {
		args = append(args, "-test")
	}
	valueFlags := map[string]bool{"-tags": true, "-mod": true, "-modfile": true, "-overlay": true, "-pgo": true}
	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		if arg == "--" {
			break
		}
		name, _, hasValue := strings.Cut(arg, "=")
		if !valueFlags[name] {
			continue
		}
		args = append(args, arg)
		if !hasValue && i+1 < len(rest) {
			i++
			args = append(args, rest[i])
		}
	}
	targets := expressGoListTargets(rest)
	if len(targets) == 0 {
		targets = []string{"."}
	}
	return append(args, targets...)
}

func expressGoListTargets(rest []string) []string {
	packages := goPackageTargets(rest)
	for _, arg := range rest {
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "-") && strings.HasSuffix(arg, ".go") {
			packages = append(packages, arg)
		}
	}
	return packages
}

func expressCommandEnvironment(command []string) []string {
	environment := append([]string(nil), os.Environ()...)
	if len(command) == 0 || strings.ToLower(filepath.Base(command[0])) != "env" {
		return environment
	}
	for _, token := range command[1:] {
		if isGoToolchainBinary(token) {
			break
		}
		name, value, ok := strings.Cut(token, "=")
		if !ok || name == "" {
			continue
		}
		environment = setExpressEnvironmentValue(environment, name, value)
	}
	return environment
}

func setExpressEnvironmentValue(environment []string, name, value string) []string {
	prefix := name + "="
	for index, existing := range environment {
		if strings.HasPrefix(existing, prefix) {
			environment[index] = prefix + value
			return environment
		}
	}
	return append(environment, prefix+value)
}

func addExpressSourceFile(files map[string]struct{}, path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	info, err := os.Stat(path)
	if err == nil && info.Mode().IsRegular() {
		files[filepath.Clean(path)] = struct{}{}
	}
}

func addExpressWorkspaceFiles(files map[string]struct{}, workdir string) {
	for current := filepath.Clean(workdir); ; current = filepath.Dir(current) {
		for _, name := range []string{"go.work", "go.work.sum"} {
			addExpressSourceFile(files, filepath.Join(current, name))
		}
		parent := filepath.Dir(current)
		if parent == current {
			return
		}
	}
}

func expressDeclaredArtifactPaths(command []string, cwdOverride string) ([]string, bool, error) {
	if _, _, ok := canonicalMakeBuildTarget(command, cwdOverride); ok {
		// Canonical make builds may only join a leader that was already running
		// when this request began. They never use this empty artifact set for a
		// later standalone successful-result replay.
		return []string{}, true, nil
	}
	tokens := normalizeGoArgv(command)
	if len(tokens) < 2 || !isGoToolchainBinary(tokens[0]) {
		return nil, false, nil
	}
	verb := tokens[1]
	rest := tokens[2:]
	if verb != "build" && verb != "test" && verb != "vet" {
		return nil, false, nil
	}
	workdir, ok := expressEffectiveWorkDir(rest, cwdOverride)
	if !ok {
		return nil, false, errors.New("resolve express artifact workdir")
	}
	output := ""
	compileTest := false
	for index := 0; index < len(rest); index++ {
		arg := rest[index]
		if arg == "--" {
			break
		}
		if arg == "-c" {
			compileTest = true
		}
		if strings.HasPrefix(arg, "-o=") {
			output = strings.TrimPrefix(arg, "-o=")
			continue
		}
		if arg == "-o" && index+1 < len(rest) {
			index++
			output = rest[index]
		}
	}
	if output == "" {
		if verb == "test" && compileTest {
			// go test -c writes <pkg>.test into the working directory under a
			// name this guard does not model.
			return nil, false, nil
		}
		if verb == "build" {
			// A build with no -o writes a binary only for a main package. For a
			// library package Go emits no file at all — the same artifact-free
			// shape as vet and plain test — so its success can be replayed with
			// the source digest alone. Fails closed: anything not positively
			// proven non-main keeps the conservative refusal.
			writesBinary, determined := expressBuildWritesBinary(rest, workdir)
			if !determined || writesBinary {
				return nil, false, nil
			}
		}
		return []string{}, true, nil
	}
	if !filepath.IsAbs(output) {
		output = filepath.Join(workdir, output)
	}
	return []string{filepath.Clean(output)}, true, nil
}

// expressWritesUndeclaredBinary reports whether a command is a `go build` with
// no -o whose package writes a binary Go names implicitly. Used only to label
// the refusal; it never widens what is cacheable.
func expressWritesUndeclaredBinary(command []string, cwdOverride string) bool {
	tokens := normalizeGoArgv(command)
	if len(tokens) < 2 || !isGoToolchainBinary(tokens[0]) || tokens[1] != "build" {
		return false
	}
	rest := tokens[2:]
	for index := 0; index < len(rest); index++ {
		if rest[index] == "--" {
			break
		}
		if rest[index] == "-o" || strings.HasPrefix(rest[index], "-o=") {
			return false
		}
	}
	workdir, ok := expressEffectiveWorkDir(rest, cwdOverride)
	if !ok {
		return false
	}
	writesBinary, determined := expressBuildWritesBinary(rest, workdir)
	return determined && writesBinary
}

// expressBuildWritesBinary reports whether `go build` with no -o would write a
// binary into the working directory, and whether that could be determined at
// all. Only a single local package directory is decided; wildcard patterns,
// multi-package builds, and unreadable or ambiguous directories return
// determined=false so the caller keeps refusing reuse.
func expressBuildWritesBinary(rest []string, workdir string) (writesBinary, determined bool) {
	targets := goPackageTargets(rest)
	if len(targets) != 1 {
		// No target means the current directory, which is decidable; anything
		// else is a multi-package build whose outputs we do not model.
		if len(targets) != 0 {
			return false, false
		}
		targets = []string{"."}
	}
	target := targets[0]
	if strings.Contains(target, "...") {
		return false, false
	}
	// Only local directory targets resolve without invoking the toolchain.
	if !strings.HasPrefix(target, ".") && !filepath.IsAbs(target) {
		return false, false
	}
	dir := target
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(workdir, dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, false
	}
	sources := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		clause, ok := readGoPackageClause(filepath.Join(dir, name))
		if !ok {
			return false, false
		}
		sources++
		if clause == "main" {
			return true, true
		}
	}
	if sources == 0 {
		return false, false
	}
	return false, true
}

// readGoPackageClause returns the package name declared by a Go file. Only the
// package clause is parsed, so build constraints and file size do not matter.
func readGoPackageClause(path string) (string, bool) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.PackageClauseOnly)
	if err != nil || file.Name == nil {
		return "", false
	}
	return file.Name.Name, true
}

func snapshotExpressArtifacts(paths []string, key []byte) ([]expressReuseArtifact, ExpressReuseRefusalReason, error) {
	artifacts := make([]expressReuseArtifact, 0, len(paths))
	for _, path := range paths {
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil, ExpressReuseMissingArtifact, nil
		}
		if err != nil {
			return nil, ExpressReuseStaleArtifact, fmt.Errorf("stat express output: %w", err)
		}
		if !info.Mode().IsRegular() {
			return nil, ExpressReuseStaleArtifact, nil
		}
		contentDigest, err := digestStableWorkBatchFile(path, info)
		if err != nil {
			return nil, ExpressReuseStaleArtifact, fmt.Errorf("digest express output: %w", err)
		}
		pathDigest, err := privateExpressPathDigest(key, path)
		if err != nil {
			return nil, ExpressReuseStaleArtifact, err
		}
		artifacts = append(artifacts, expressReuseArtifact{PathDigest: pathDigest, ContentDigest: contentDigest, SizeBytes: info.Size()})
	}
	return artifacts, "", nil
}

func privateExpressPathDigest(key []byte, path string) (string, error) {
	digest := hmac.New(sha256.New, key)
	if _, err := digest.Write([]byte("ndev-express-output-v1")); err != nil {
		return "", err
	}
	if _, err := digest.Write([]byte{0}); err != nil {
		return "", err
	}
	if _, err := digest.Write([]byte(filepath.Clean(path))); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

func expressArtifactsEqual(expected, actual []expressReuseArtifact) bool {
	if len(expected) != len(actual) {
		return false
	}
	for index := range expected {
		if expected[index] != actual[index] {
			return false
		}
	}
	return true
}

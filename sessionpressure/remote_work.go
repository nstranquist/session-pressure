package sessionpressure

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/nstranquist/session-pressure/runnerfleet"
	"golang.org/x/mod/modfile"
)

type RemoteWorkPolicy struct {
	Enabled bool   `json:"enabled"`
	Host    string `json:"host,omitempty"`
	User    string `json:"user,omitempty"`
	Fleet   string `json:"fleet,omitempty"`
	// Node opts Node verification commands into remote routing (work queue
	// v2 slice 3, remote_node.go). Go routing does not depend on it.
	Node bool `json:"node,omitempty"`
	// Placer lets auto placement keep a small eligible check on an idle Mac
	// when its measured local time beats the measured remote time (work queue
	// v2 slice 4, work_placer.go). Explicit remote placement is unaffected.
	Placer bool `json:"placer,omitempty"`
	// DarwinCompilerSHA256 pins the reviewed Darwin cross-compiler wrapper
	// per runner endpoint (host name, alias or address, lower case). A
	// Darwin cgo build runs only on an endpoint listed here. Hosts and pins
	// live in this operator file, never in the source tree; see
	// config/remote-work.example.json.
	DarwinCompilerSHA256 map[string]string `json:"darwin_compiler_sha256,omitempty"`
}

func SetRemoteWork(dir string, enabled bool) error {
	policy, err := RemoteWorkConfig(dir)
	if err != nil {
		return err
	}
	policy.Enabled = enabled
	return SaveRemoteWorkPolicy(dir, policy)
}

func ConfigureRemoteWork(dir string, enabled bool, host, user string) error {
	policy := RemoteWorkPolicy{Enabled: enabled, Host: host, User: user}
	if old, err := RemoteWorkConfig(dir); err == nil {
		policy.DarwinCompilerSHA256 = old.DarwinCompilerSHA256
	}
	return SaveRemoteWorkPolicy(dir, policy)
}

// SaveRemoteWorkPolicy preserves fleet routing across enable/disable operations.
// ConfigureRemoteWork remains an explicit single-host override.
func SaveRemoteWorkPolicy(dir string, policy RemoteWorkPolicy) error {
	if err := policy.validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	body, _ := json.Marshal(policy)
	return atomicWrite(filepath.Join(dir, "remote-work.json"), append(body, '\n'), 0600)
}

func RemoteWorkEnabled(dir string) (bool, error) {
	policy, err := RemoteWorkConfig(dir)
	return policy.Enabled, err
}

func (p *RemoteWorkPolicy) validate() error {
	if p.Fleet != "" && !filepath.IsAbs(p.Fleet) {
		return errors.New("remote fleet inventory path must be absolute")
	}
	if p.User == "" {
		p.User = "builder"
	}
	for endpoint, digest := range p.DarwinCompilerSHA256 {
		if endpoint == "" || endpoint != strings.ToLower(strings.TrimSpace(endpoint)) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(digest) {
			return fmt.Errorf("remote-work.json darwin_compiler_sha256: entry %q needs a lower-case endpoint and a 64-hex sha256", endpoint)
		}
	}
	if p.Host == "" {
		// The runner endpoint is operator configuration, not a source
		// default. Disabled remote work needs no endpoint.
		if p.Enabled && p.Fleet == "" {
			return errors.New("remote work is enabled but remote-work.json has neither host nor fleet; copy config/remote-work.example.json")
		}
		return nil
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*$`).MatchString(p.Host) || !regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]*$`).MatchString(p.User) {
		return errors.New("invalid remote runner host or user")
	}
	return nil
}

// Read once per operation: configuration changes cannot move an accepted job.
func RemoteWorkConfig(dir string) (RemoteWorkPolicy, error) {
	policy := RemoteWorkPolicy{}
	body, err := os.ReadFile(filepath.Join(dir, "remote-work.json"))
	if errors.Is(err, os.ErrNotExist) {
		err = policy.validate()
		return policy, err
	}
	if err != nil {
		return policy, err
	}
	if err = json.Unmarshal(body, &policy); err != nil {
		return policy, err
	}
	err = policy.validate()
	return policy, err
}

// RemoteGoCommand only delegates direct, finite Go operations. Shell scripts,
// install and commands with unknown semantics retain local admission.
func RemoteGoCommand(argv []string) bool {
	if len(argv) < 2 || filepath.Base(argv[0]) != "go" {
		return false
	}
	switch argv[1] {
	case "test", "build", "vet":
		return true
	}
	return false
}

// normalizeRemoteBuildArgv redirects explicit build outputs for fetch-back.
// Multi-package validation builds do not produce an executable. Other builds
// need an explicit local output so the remote route preserves the artifact
// contract instead of silently discarding the executable.
func normalizeRemoteBuildArgv(argv []string) ([]string, string, error) {
	out := append([]string(nil), argv...)
	if len(out) < 2 || out[1] != "build" {
		return out, "", nil
	}
	remoteOutput := func(output string) string {
		if remoteBuildDirectory(output) {
			return "/srv/build/candidates/OUTPUT/"
		}
		return "/srv/build/candidates/OUTPUT"
	}
	// goFlagName reads -o in every spelling go build accepts (-o, --o,
	// -o=, --o=). The value of another flag is never read as -o, Go keeps
	// the last -o, and an empty -o is no -o. Only a package argument makes a
	// pattern build; after "--" every argument is one.
	at, output, inline, pattern := -1, "", false, false
	for i := 2; i < len(out); i++ {
		arg := out[i]
		if arg == "--" {
			for _, rest := range out[i+1:] {
				pattern = pattern || strings.Contains(rest, "...")
			}
			break
		}
		name, value, isInline := goFlagName(arg)
		switch {
		case name == "-o" && isInline:
			at, output, inline = i, value, true
		case name == "-o" && i+1 < len(out):
			i++
			at, output, inline = i, out[i], false
		case name != "" && !isInline && goFlagsWithValue[name]:
			i++
		case name == "":
			pattern = pattern || strings.Contains(arg, "...")
		}
	}
	switch {
	case output == "/dev/null":
		// Go builds and writes nothing; the runner's /dev/null does the same.
		return out, output, nil
	case output != "" && inline:
		out[at] = out[at][:strings.IndexByte(out[at], '=')+1] + remoteOutput(output)
		return out, output, nil
	case output != "":
		out[at] = remoteOutput(output)
		return out, output, nil
	case pattern:
		return out, "", nil
	}
	return out, "", errors.New("remote go build requires -o <local-output> so its executable can be returned; use go build -o ./bin/<name> <package>, or go build ./... for validation")
}

// remoteWorkDirectory is the directory Go runs command from, and command
// without the -C that moved it there. cmd/go handles a -C that is the first
// flag after the subcommand before anything else: it changes to that
// directory and drops the flag, and os.Getwd then reports the physical path.
// The remote route does the same, so it looks for the module, runs the
// command and places outputs from that directory. A -C that Go rejects, or
// one whose directory does not exist, stays in the command, which then fails
// on the runner as it would here.
func remoteWorkDirectory(cwd string, command []string) (string, []string) {
	if len(command) < 3 {
		return cwd, command
	}
	chdir, rest, ok := leadingGoChdir(command[2:])
	if !ok || chdir == "" {
		return cwd, command
	}
	target := chdir
	if !filepath.IsAbs(target) {
		target = filepath.Join(cwd, target)
	}
	physical, err := filepath.EvalSymlinks(target)
	if err != nil || !isDir(physical) {
		return cwd, command
	}
	return physical, append(append([]string(nil), command[:2]...), rest...)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

// Match the canonical run-job.sh environment in non-login SSH shells. The
// system Git is too old for merge-file --diff-algorithm. Missing pinned tools
// must refuse the job rather than silently select a different toolchain.
func remoteRunnerPrelude() string {
	return "{ test -x /srv/build/bin/git && test -x /usr/local/go/bin/go; } || { printf '%s\n' " +
		shellQuote(WorkRefusalMarker+" kind="+WorkRefusalRemoteProfile+": pinned runner Git or Go is missing; local fallback is disabled") +
		fmt.Sprintf(" >&2; exit %d; }; ", workExitPolicyDenied) +
		"export PATH=/srv/build/bin:/usr/local/go/bin:\"$PATH\" && " +
		// A runner that runs the job as a pool user gives it its own Go
		// caches and says so with NCD_RUNNER_GO_ENV=pool (nicos-commit-delivery);
		// the pool cannot write the shared runner caches. Any
		// other environment gets the shared caches, whatever it inherited.
		"{ test \"${NCD_RUNNER_GO_ENV:-}\" = pool || export GOCACHE=/srv/build/cache/go-build GOMODCACHE=/srv/build/cache/go-mod GOPATH=/srv/build/go; } && " +
		"umask 0022 && "
}

type remoteReplaceEdge struct {
	parentDir string
	directive *modfile.Replace
}

// remoteReplaceTargetError reports a local go.mod replace directive whose
// target directory or go.mod is missing on disk. Go itself cannot build this
// module tree either, so callers treat it as a command failure rather than a
// retryable host refusal.
type remoteReplaceTargetError struct {
	goMod     string
	oldPath   string
	oldVer    string
	newPath   string
	targetDir string
	problem   string
}

func (e *remoteReplaceTargetError) directive() string {
	if e.oldVer != "" {
		return fmt.Sprintf("replace %s %s => %s", e.oldPath, e.oldVer, e.newPath)
	}
	return fmt.Sprintf("replace %s => %s", e.oldPath, e.newPath)
}

func (e *remoteReplaceTargetError) Error() string {
	return fmt.Sprintf("%s: %s: replacement directory %s %s", e.goMod, e.directive(), e.targetDir, e.problem)
}

func missingReplaceTargetError(start, parentDir string, r *modfile.Replace, targetDir string, readErr error) *remoteReplaceTargetError {
	info, statErr := os.Stat(targetDir)
	var problem string
	switch {
	case errors.Is(statErr, os.ErrNotExist):
		problem = "does not exist"
	case statErr == nil && !info.IsDir():
		problem = "is not a directory"
	case statErr == nil && errors.Is(readErr, os.ErrNotExist):
		problem = "has no go.mod"
	default:
		return nil
	}
	return &remoteReplaceTargetError{
		goMod:     shownGoMod(start, parentDir),
		oldPath:   r.Old.Path,
		oldVer:    r.Old.Version,
		newPath:   r.New.Path,
		targetDir: targetDir,
		problem:   problem,
	}
}

// remoteModuleClosure preserves relative replacement layout without copying
// unrelated projects, ignored build caches or credentials to the runner.
func remoteModuleClosure(start string) ([]string, string, error) {
	root := start
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			return nil, "", errRemoteNoModule
		}
		root = parent
	}
	seen := map[string]bool{}
	var modules []string
	var visit func(string, *remoteReplaceEdge) error
	visit = func(dir string, from *remoteReplaceEdge) error {
		dir = filepath.Clean(dir)
		if seen[dir] {
			return nil
		}
		seen[dir] = true
		if len(seen) > 100 {
			return errors.New("remote module closure exceeds 100 modules")
		}
		body, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err != nil {
			if from != nil {
				if replaceErr := missingReplaceTargetError(start, from.parentDir, from.directive, dir, err); replaceErr != nil {
					return replaceErr
				}
			}
			return err
		}
		mod, err := modfile.Parse("go.mod", body, nil)
		if err != nil {
			return err
		}
		modules = append(modules, dir)
		for _, r := range mod.Replace {
			if r.New.Version != "" {
				continue
			}
			target := r.New.Path
			if !filepath.IsAbs(target) {
				target = filepath.Join(dir, target)
			}
			if err := visit(target, &remoteReplaceEdge{parentDir: dir, directive: r}); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(root, nil); err != nil {
		return nil, "", err
	}
	common := root
	for _, dir := range append(append([]string(nil), modules...), remoteRepositoryRoots(modules)...) {
		for {
			rel, err := filepath.Rel(common, dir)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				break
			}
			parent := filepath.Dir(common)
			if parent == common {
				return nil, "", errors.New("remote modules do not share a safe source root")
			}
			common = parent
		}
	}
	if common == "/" || common == "/Users" {
		return nil, "", errors.New("remote module closure is too broad")
	}
	return modules, common, nil
}

// remoteDeliveryLineage tags a job with the commit delivery that spawned it.
// The worker exports its delivery and run IDs to derived-output generators;
// without the tag their child builds queued at background priority behind
// recurring diagnostics while the delivery itself held urgent priority
// (2026-11-09, delivery 35513d6a). The runner derives the child's priority
// from the delivery's marks; the IDs alone grant nothing.
func remoteDeliveryLineage(getenv func(string) string) string {
	out := ""
	for _, pair := range [][2]string{
		{"NDEV_COMMIT_DELIVERY_ID", "NCD_DELIVERY_ID"},
		{"NDEV_COMMIT_DELIVERY_RUN_ID", "NCD_RUN_ID"},
	} {
		if value := getenv(pair[0]); remoteLineageID.MatchString(value) {
			out += " " + pair[1] + "=" + value
		}
	}
	return out
}

var remoteLineageID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// remoteEnumerateTimeout bounds one repository input scan.
const remoteEnumerateTimeout = 60 * time.Second

// remoteSourceLimit bounds one upload; larger closures are rejected.
const remoteSourceLimit = 512 << 20

// remoteInputScope selects which repository files travel with a remote job.
type remoteInputScope int

const (
	// remoteScopeRepository sends every tracked and pending repository file.
	// Tests may read fixtures outside their module (hooks, docs, configs).
	remoteScopeRepository remoteInputScope = iota
	// remoteScopeModules sends only files inside the module closure. The Go
	// toolchain cannot read outside a module for a build (embed patterns are
	// module-bounded and the runner sets GOWORK=off), so a nicos-tools
	// provider build no longer ships ~300 MiB of docs and apps (11-09:
	// ~200 s per build at the measured 1.5 MB/s, under the host upload lock).
	remoteScopeModules
)

// remoteInputScopeFor maps the Go subcommand to its input scope.
func remoteInputScopeFor(subcommand string) remoteInputScope {
	if subcommand == "build" {
		return remoteScopeModules
	}
	return remoteScopeRepository
}

// walkRemoteSources visits each uploadable regular file of the module closure
// once. Both the pre-upload size check and the upload use this one filter.
func walkRemoteSources(modules []string, common string, scope remoteInputScope, visit func(rel, path string, info os.FileInfo) error) error {
	seen := map[string]bool{}
	repositories := remoteRepositoryRoots(modules)
	for _, module := range modules {
		// Git's candidate inventory below includes tracked inputs and pending
		// unignored files. Do not walk ignored binaries, virtual environments
		// and caches first: those are not repository source inputs.
		inRepository := false
		for _, repo := range repositories {
			rel, err := filepath.Rel(repo, module)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				inRepository = true
				break
			}
		}
		if inRepository {
			continue
		}
		err := filepath.WalkDir(module, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case ".git", ".tmp", ".build", "target", "node_modules", "bin", ".cache", ".ndev", ".next", "dist":
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if d.Name() == ".env" || strings.HasPrefix(d.Name(), ".env.") || strings.HasSuffix(d.Name(), ".pem") || strings.HasSuffix(d.Name(), ".key") {
				return nil
			}
			rel, err := filepath.Rel(common, path)
			if err != nil {
				return err
			}
			if seen[rel] {
				return nil
			}
			seen[rel] = true
			info, err := d.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || info.Size() > 20<<20 {
				return nil
			}
			return visit(rel, path, info)
		})
		if err != nil {
			return err
		}
	}
	for _, repo := range repositories {
		// The scan takes ~2 s on an idle Mac for nicos-tools; at load 40 a 5 s
		// ceiling killed it and refused a delivery gate (2026-11-09). A slow
		// enumeration is cheaper than a refused job, so bound it loosely.
		ctx, cancel := context.WithTimeout(context.Background(), remoteEnumerateTimeout)
		args := []string{"-C", repo, "ls-files", "--cached", "--others", "--exclude-standard", "-t", "-z"}
		if scope == remoteScopeModules {
			// Let git skip untracked scans outside the closure modules.
			args = append(args, "--")
			for _, module := range modules {
				if rel, err := filepath.Rel(repo, module); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					args = append(args, ":(literal)"+filepath.ToSlash(rel))
				}
			}
		}
		cmd := exec.CommandContext(ctx, "git", args...)
		body, err := cmd.Output()
		cancel()
		if err != nil {
			return fmt.Errorf("enumerate repository inputs: %w", err)
		}
		for _, entry := range strings.Split(string(body), "\x00") {
			if len(entry) < 3 {
				continue
			}
			// -t emits a two-byte status prefix; '?' is untracked. Keep
			// the distinction so committed embed assets survive filtering.
			name := entry[2:]
			if remoteExcludedRepositoryInput(name, entry[0] != '?') {
				continue
			}
			path := filepath.Join(repo, name)
			if scope == remoteScopeModules && !withinRemoteModule(modules, path) {
				continue
			}
			rel, err := filepath.Rel(common, path)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return errors.New("repository input escapes source root")
			}
			if seen[rel] {
				continue
			}
			seen[rel] = true
			info, err := os.Lstat(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			} // tracked deletion in this candidate
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || info.Size() > 20<<20 {
				continue
			}
			if err := visit(rel, path, info); err != nil {
				return err
			}
		}
	}
	return nil
}

// withinRemoteModule reports whether path lies inside one closure module.
func withinRemoteModule(modules []string, path string) bool {
	for _, module := range modules {
		rel, err := filepath.Rel(module, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// remoteSourceSize returns the upload size, or an error once it passes limit.
func remoteSourceSize(modules []string, common string, scope remoteInputScope, limit int64) (int64, error) {
	var total int64
	err := walkRemoteSources(modules, common, scope, func(_, _ string, info os.FileInfo) error {
		total += info.Size()
		if total > limit {
			return fmt.Errorf("remote source exceeds %dMiB; narrow the module", limit>>20)
		}
		return nil
	})
	return total, err
}

// writeRemoteSources streams a gzip-compressed tar. Source text compresses
// several-fold and the Mac-to-box link, not CPU, bounds the upload.
func writeRemoteSources(out io.Writer, modules []string, common string, scope remoteInputScope) error {
	zw := gzip.NewWriter(out)
	tw := tar.NewWriter(zw)
	var total int64
	err := walkRemoteSources(modules, common, scope, func(rel, path string, info os.FileInfo) error {
		total += info.Size()
		if total > remoteSourceLimit {
			return errors.New("remote source exceeds 512MiB; narrow the module")
		}
		h, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		if err = tw.WriteHeader(h); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		closeErr := f.Close()
		return errors.Join(err, closeErr)
	})
	if err != nil {
		_ = tw.Close()
		_ = zw.Close()
		return err
	}
	return errors.Join(tw.Close(), zw.Close())
}

// TryRemoteWork runs eligible commands on the tailnet runner. A failed route
// never silently falls back onto the overloaded Mac. The remote lock is shared
// by all these jobs; Go workers and compiler parallelism are bounded to two.
func TryRemoteWork(dir string, options WorkRunOptions, streams WorkRunStreams) (handled bool, exitCode int, resultErr error) {
	policy, err := RemoteWorkConfig(dir)
	if err != nil {
		return true, 1, err
	}
	if !policy.Enabled || runtime.GOOS != "darwin" {
		return false, 0, nil
	}
	if !RemoteGoCommand(options.Command) {
		return tryRemoteNodeWork(dir, policy, options, streams)
	}
	if streams.Stdin == nil {
		streams.Stdin = os.Stdin
	}
	if streams.Stdout == nil {
		streams.Stdout = os.Stdout
	}
	if streams.Stderr == nil {
		streams.Stderr = os.Stderr
	}
	cwd, err := os.Getwd()
	if err != nil {
		return true, 1, err
	}
	workdir, goArgv := remoteWorkDirectory(cwd, options.Command)
	// Reject unsupported profiles before upload. Enabled remote routing must
	// never turn an eligibility failure into heavy work on the Mac.
	reject := func(reason string) (bool, int, error) {
		return rejectRemoteWork(dir, goArgv[1], reason)
	}
	if reason := remoteProfileReason(goArgv, os.Getenv); reason != "" {
		return reject(reason)
	}
	if reason := remoteWorkIneligibleReason(goArgv, os.Getenv); reason != "" {
		return reject(reason)
	}
	if reason := remoteOutputReason(goArgv, os.Getenv); reason != "" {
		return refuseRemoteGo(dir, goArgv[1], reason, remoteLocalOutputAdvice, nil)
	}
	started := time.Now()
	timingMS := map[string]int64{}
	stageStarted, stage := started, "preparation_ms"
	finishStage := func() { timingMS[stage] += time.Since(stageStarted).Milliseconds() }
	nextStage := func(name string) { finishStage(); stage, stageStarted = name, time.Now() }
	modules, common, err := remoteModuleClosure(workdir)
	if errors.Is(err, errRemoteNoModule) {
		return rejectRemoteModule(dir, cwd, workdir, goArgv)
	}
	var replaceErr *remoteReplaceTargetError
	if errors.As(err, &replaceErr) {
		return failRemoteReplaceTarget(dir, options.Command[1], replaceErr)
	}
	if err != nil {
		return reject(err.Error())
	}
	scope := remoteInputScopeFor(options.Command[1])
	sourceBytes, err := remoteSourceSize(modules, common, scope, remoteSourceLimit)
	if err != nil {
		return reject(err.Error())
	}
	// Explicit outputs can be returned without guessing package/main names.
	argv := append([]string(nil), goArgv...)
	argv[0] = "go"
	argv, output, err := normalizeRemoteBuildArgv(argv)
	if err != nil {
		return reject(err.Error())
	}
	placementKeys := NewPlacementKeys(cwd, options.Command)
	placementKey := placementKeys.Exact
	if placeLocally(dir, policy, options, placementKeys, ToolFamilyGo, streams.Stderr) {
		return false, 0, nil
	}
	job, err := NewWorkOperationID()
	if err != nil {
		return true, 1, err
	}
	profile := remoteBuildProfile(argv[1], os.Getenv("CGO_ENABLED"))
	// One SSH connection serves every call of this job; the fleet probe
	// opens it (remote_ssh.go).
	conn := newRemoteSSH(job)
	if policy.Fleet != "" {
		lane, _, _, _ := remoteLane(options)
		binding, bindErr := runnerfleet.Assign(context.Background(), policy.Fleet, runnerfleet.Request{
			Key: "pressure-" + job, Identity: workdir + "\x00" + strings.Join(argv, "\x00"),
			Repository: "session-pressure-work", Profile: profile, Lane: lane,
		}, conn.probe())
		if bindErr != nil {
			return reject("fleet assignment: " + bindErr.Error())
		}
		policy.Host, policy.User = binding.Host.Name, binding.Host.User
	}
	var sourceUploadMode string
	var sourceFilesSent int
	var sourceBytesSent int64
	var sourceDelta remoteSourceUpload
	defer func() {
		finishStage()
		event := map[string]any{"event": "remote_work_finished", "duration_ms": time.Since(started).Milliseconds(), "timing_ms": timingMS, "source_bytes": sourceBytes, "delivery_id": os.Getenv("NDEV_COMMIT_DELIVERY_ID"), "operation": options.Command[1], "profile": profile, "runner_host": policy.Host, "runner_user": policy.User, "request_key": "pressure-" + job, "exit_code": exitCode, "failed": resultErr != nil || exitCode != 0, "placement_key": placementKey, "placement_family_key": placementKeys.Family}
		// The shared SQLite telemetry projection retains scalar attributes.
		for stage, ms := range timingMS {
			event[stage] = ms
		}
		if sourceUploadMode != "" {
			event["source_upload_mode"], event["source_files_sent"], event["source_bytes_sent"] = sourceUploadMode, sourceFilesSent, sourceBytesSent
			if sourceDelta.mode != "" {
				event["source_collect_ms"], event["source_hash_ms"], event["source_send_ms"], event["source_files_hashed"] = sourceDelta.collectMS, sourceDelta.hashMS, sourceDelta.sendMS, sourceDelta.hashed
			}
		}
		recordControlEvent(dir, event)
	}()
	remote := "/srv/build/candidates/pressure-" + job
	preflight, err := remoteBuildPreflight(profile, policy.Host, policy.DarwinCompilerSHA256, remote)
	if err != nil {
		return reject(err.Error())
	}
	for i := range argv {
		argv[i] = strings.ReplaceAll(argv[i], "/srv/build/candidates/OUTPUT", remote+"/artifact")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour+32*time.Minute)
	defer cancel()
	target := policy.User + "@" + policy.Host
	// Deferred first, so it runs after the cleanup call below.
	defer conn.close(target)
	ssh := func(command string) *exec.Cmd {
		return conn.command(ctx, "8", target, command)
	}
	// Keep the endpoint with the request key even after an ambiguous disconnect.
	requestDir := filepath.Join(dir, "remote-requests")
	if err := os.MkdirAll(requestDir, 0700); err != nil {
		return true, 1, err
	}
	request, _ := json.Marshal(map[string]any{"host": policy.Host, "user": policy.User, "request_key": "pressure-" + job, "profile": profile, "remote_path": remote, "started_at": started.UTC()})
	if err := atomicWrite(filepath.Join(requestDir, job+".json"), append(request, '\n'), 0600); err != nil {
		return true, 1, err
	}
	// Upload/extraction has one bounded host-wide preparation lane, separate
	// from the scheduler's execution slot. A stalled sender cannot hold it forever.
	nextStage("source_upload_ms")
	uploadMode, uploadFiles, uploadBytes := remoteUploadFull, 0, int64(0)
	var deltaUpload remoteSourceUpload
	useDelta := remoteSourceDeltaEnabled()
	if useDelta {
		up, deltaErr := uploadRemoteSourcesDelta(ssh, streams.Stderr, dir, policy.Host, remote, "pressure-"+job, common, modules, scope)
		switch {
		case errors.Is(deltaErr, errRemoteSourceUnsupported):
			useDelta = false
		case deltaErr != nil:
			return true, 1, fmt.Errorf("remote source upload failed (%s): %w", up.mode, deltaErr)
		default:
			deltaUpload = up
			uploadMode, uploadFiles, uploadBytes = up.mode, up.files, up.bytes
		}
	}
	if !useDelta {
		upload := ssh("timeout 600 flock -w 600 /srv/build/input-transfer.lock sh -c " + shellQuote("umask 077; mkdir -p "+shellQuote(remote+"/source")+" && printf '%s\n' "+shellQuote("pressure-"+job)+" > "+shellQuote(remote+".request")+" && tar --same-permissions -xzf - -C "+shellQuote(remote+"/source")))
		pipe, err := upload.StdinPipe()
		if err != nil {
			return true, 1, err
		}
		upload.Stderr = streams.Stderr
		if err = upload.Start(); err != nil {
			return true, 1, err
		}
		writeErr := writeRemoteSources(pipe, modules, common, scope)
		_ = pipe.Close()
		err = upload.Wait()
		if writeErr != nil || err != nil {
			return true, 1, fmt.Errorf("remote source upload failed: %w", errors.Join(writeErr, err))
		}
		uploadBytes = sourceBytes
	}
	sourceUploadMode, sourceFilesSent, sourceBytesSent, sourceDelta = uploadMode, uploadFiles, uploadBytes, deltaUpload
	cleanupSafe := false
	defer func() {
		nextStage("cleanup_ms")
		if !cleanupSafe {
			fmt.Fprintf(streams.Stderr, "session-pressure: retained remote inputs/artifacts at %s; request key pressure-%s (completion not confirmed)\n", remote, job)
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = conn.command(cleanupCtx, "5", target, "rm -rf -- "+shellQuote(remote)+" "+shellQuote(remote+".request")).Run()
	}()
	rel, _ := filepath.Rel(common, workdir)
	// A focused package test asks for the runner's light lane (1 CPU, 1 GiB,
	// 300 s) so it never queues behind a suite. The runner reruns it heavy if
	// it outgrows the lane, so a wrong guess costs time, never a verdict.
	lane, procs, memlimit, parallel := remoteLane(options)
	command := remoteRunnerPrelude() + preflight + "cd " + shellQuote("./"+filepath.ToSlash(rel)) + " && " + remoteGoInvocation(lane, procs, memlimit, parallel, os.Getenv("GOFLAGS"), remoteBuildEnvironment(profile, remote), argv)
	if output != "" && output != "/dev/null" {
		command += remoteBuildArtifactSeal(remote, output)
	}
	// The canonical runner retains source inputs before durable submission.
	// The command starts at the retained common root, preserving replacements.
	command = "env NCD_RUNNER_FRAMED=1 NCD_RUNNER_CLASS=" + lane + remoteDeliveryLineage(os.Getenv) + " NCD_RUNNER_KEY=" + shellQuote("pressure-"+job) + " /srv/build/run-job.sh session-pressure " + shellQuote("session-pressure:"+job) + " --candidate-dir " + shellQuote(remote+"/source") + " --timeout 1800 --shell " + shellQuote(command)
	command = "if test -x /srv/build/gaterunner && /srv/build/gaterunner inspect --key " + shellQuote("pressure-"+job) + " >/dev/null 2>&1; then exec env NCD_RUNNER_FRAMED=1 /srv/build/gaterunner wait --key " + shellQuote("pressure-"+job) + "; else exec " + command + "; fi"
	if streams.Stderr != nil {
		fmt.Fprintln(streams.Stderr, "session-pressure: routing Go work to "+policy.Host+" ("+profile+", durable runner queue, "+lane+" lane, parallelism "+parallel+")")
	}
	// Admission and execution are combined here. The runner receipt separates
	// capacity wait and command duration; source transfer is outside this stage.
	nextStage("runner_wait_and_run_ms")
	cleanupSafe, err = runRunnerSubscriber(ctx, func() *exec.Cmd { return ssh(command) }, streams.Stdout, streams.Stderr)
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return true, exit.ExitCode(), nil
		}
		return true, 1, err
	}
	if output != "" && output != "/dev/null" {
		// Keep the artifact on a fetch failure so the same finished job can be
		// recovered. Publish locally only after checking its retained manifest.
		cleanupSafe = false
		nextStage("artifact_download_ms")
		directoryOutput := remoteBuildDirectory(output)
		if !filepath.IsAbs(output) {
			output = filepath.Join(workdir, output)
		}
		if directoryOutput {
			output = filepath.Clean(output) + "/"
		}
		if err = os.MkdirAll(filepath.Dir(filepath.Clean(output)), 0755); err != nil {
			return true, 1, err
		}
		f, err := os.CreateTemp(filepath.Dir(filepath.Clean(output)), ".remote-go-artifact-*")
		if err != nil {
			return true, 1, err
		}
		defer os.Remove(f.Name())
		fetch := ssh("cat -- " + shellQuote(remote+"/artifact.sha256") + " " + shellQuote(remote+"/artifact"))
		fetch.Stderr = streams.Stderr
		sum, err := fetchRemoteArtifact(fetch, remote+"/artifact", f)
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			return true, 1, errors.Join(err, closeErr)
		}
		if remoteBuildDirectory(output) {
			if err = publishRemoteBuildDirectory(f.Name(), output, remoteBuildDirectoryReceipt(policy, profile, "pressure-"+job, sum, sourceBytes)); err != nil {
				return true, 1, err
			}
			cleanupSafe = true
			return true, 0, nil
		}
		if err = os.Chmod(f.Name(), 0755); err != nil {
			return true, 1, err
		}
		if err = os.Rename(f.Name(), output); err != nil {
			return true, 1, err
		}
		cleanupSafe = true
	}
	return true, 0, nil
}

// remoteRunnerAdvice ends a remote refusal that a runner profile can fix.
const remoteRunnerAdvice = "use the Hetzner gate-runner with an explicit compiler profile"

// remoteLocalOutputAdvice ends a refusal of a file the remote route cannot
// return: an explicit local placement skips the route and keeps the file.
const remoteLocalOutputAdvice = "run it with --placement local --placement-reason <slug> to keep the file on this host"

// rejectRemoteWork marks the command handled so callers cannot enter local
// admission after a profile or source rejection.
func rejectRemoteWork(dir, operation, reason string) (bool, int, error) {
	return refuseRemoteGo(dir, operation, reason, remoteRunnerAdvice, nil)
}

// rejectExplicitRemotePlacement refuses --placement remote when TryRemoteWork
// could not route the command, naming the actual cause (non-darwin host,
// disabled remote routing, disabled Node routing, or unsupported command)
// without falling back to local admission or repeating "local fallback is
// disabled".
func rejectExplicitRemotePlacement(dir string, command []string) (int, error) {
	cmd := ""
	if len(command) > 0 {
		cmd = filepath.Base(command[0])
	}
	policy, err := RemoteWorkConfig(dir)
	var kind, reason, advice string
	switch {
	case runtime.GOOS != "darwin":
		kind = "unsupported_host"
		reason = fmt.Sprintf("explicit remote placement is only supported on darwin hosts (current host is %s)", runtime.GOOS)
		advice = "use --placement auto or run directly on this host"
	case err == nil && !policy.Enabled:
		kind = "routing_disabled"
		reason = "explicit remote placement requires enabled remote routing in remote-work.json"
		advice = "enable remote routing or use --placement auto or --placement local --placement-reason <slug>"
	case err == nil && WorkToolFamily(command) == ToolFamilyNode && !policy.Node:
		kind = "node_routing_disabled"
		reason = fmt.Sprintf("explicit remote placement for Node command %q requires node routing enabled in remote-work.json", cmd)
		advice = "enable node in remote-work.json or use --placement auto or --placement local --placement-reason <slug>"
	default:
		kind = "unsupported_command"
		reason = fmt.Sprintf("explicit remote placement does not support command %q (only direct go build/test/vet and enabled Node verification route remotely)", cmd)
		advice = "run a supported Go or Node command directly or use --placement auto or --placement local --placement-reason <slug>"
	}
	_, code, refErr := refuseRemoteGo(dir, "work", reason, advice, map[string]any{
		"placement_decline_kind": kind,
		"command_bin":            cmd,
	})
	return code, refErr
}

// failRemoteReplaceTarget reports a missing local go.mod replace target as a
// plain command failure (exit 1) rather than a coordinator refusal. Go itself
// fails on a missing replace target, and tagging it with WorkRefusalMarker or
// "remote Go work blocked:" causes commit-delivery workers to hold and retry
// it as a host environment fault instead of attributing the broken source.
// Returning handled=true keeps the command out of local Mac admission.
func failRemoteReplaceTarget(dir, operation string, err *remoteReplaceTargetError) (bool, int, error) {
	recordControlEvent(dir, map[string]any{
		"event":          "remote_work_failed",
		"operation":      operation,
		"reason":         err.Error(),
		"go_mod":         err.goMod,
		"replace_old":    err.oldPath,
		"replace_new":    err.newPath,
		"missing_target": err.targetDir,
		"local_blocked":  true,
	})
	return true, 1, err
}

// refuseRemoteGo records the decline and returns the refusal. Callers match
// the marker and kind, and people read the "remote Go work blocked:" prefix,
// so only the reason and the advice change between refusals.
func refuseRemoteGo(dir, operation, reason, advice string, attrs map[string]any) (bool, int, error) {
	event := map[string]any{"event": "remote_work_declined", "operation": operation, "reason": reason, "local_blocked": true}
	for key, value := range attrs {
		event[key] = value
	}
	recordControlEvent(dir, event)
	return true, workExitPolicyDenied, workRefusal(WorkRefusalRemoteProfile, fmt.Errorf("remote Go work blocked: %s; %s; local fallback is disabled", reason, advice))
}

// remoteLane picks the runner lane and the Go limits that fit it. Only an
// express-test go test is light; older runners ignore the class.
func remoteLane(options WorkRunOptions) (lane, procs, memlimit, parallel string) {
	if options.Class == WorkClassExpressTest && len(options.Command) > 1 && options.Command[1] == "test" {
		return "light", "1", "768MiB", "1"
	}
	return "heavy", heavyProcs, heavyMemlimit, heavyParallel
}

// Heavy-lane Go limits for fixed-mode runners (elastic runners drop the
// GOMAXPROCS and -p pins and follow the unit's CPU quota).
const heavyProcs, heavyMemlimit, heavyParallel = "2", "3GiB", "2"

// remoteGoInvocation is the remote Go command with its CPU limits. A heavy
// job on a runner under elastic CPU shares (the scheduler sets
// NCD_CPU_MODE=elastic; KEP fleet-elastic-throughput S2) takes GOMAXPROCS
// and -p from its unit's CPU quota, which Go 1.25+ reads from cpu.max and
// follows; pinning 2 would cap it at 2 CPUs on a 16-thread host. Fixed-mode
// runners and the light lane keep the explicit pins.
//
// A light request does not always run light: the runner reruns a light
// attempt that outgrows its 1 CPU / 1 GiB / 300 s caps as heavy, and runs
// it heavy outright where it has no light lane (memory admission). The
// runner names the admitted lane in NCD_RUNNER_LANE, so the light pins
// apply only to a light attempt. Without the variable (an older runner) the
// requested lane decides, as before. On 2026-11-15 two heavy reruns of a
// cmd/ndev-go express-test kept GOMAXPROCS=1 / -p=1, ran silent at a
// fraction of a core, and were killed as stalled.
func remoteGoInvocation(lane, procs, memlimit, parallel, goflags, buildEnv string, argv []string) string {
	args := ""
	for _, arg := range argv {
		args += " " + shellQuote(arg)
	}
	pinned := func(procs, memlimit, parallel string) string {
		return "env GOWORK=off GOMAXPROCS=" + procs + " GOMEMLIMIT=" + memlimit + " GOFLAGS=" + shellQuote(strings.TrimSpace(goflags+" -p="+parallel)) + buildEnv + args
	}
	heavy := func(procs, memlimit, parallel string) string {
		elastic := "env -u GOMAXPROCS GOWORK=off GOMEMLIMIT=" + memlimit + " GOFLAGS=" + shellQuote(strings.TrimSpace(goflags)) + buildEnv + args
		return "if [ \"${NCD_CPU_MODE:-fixed}\" = elastic ]; then " + elastic + "; else " + pinned(procs, memlimit, parallel) + "; fi"
	}
	if lane == "heavy" {
		return heavy(procs, memlimit, parallel)
	}
	return "if [ \"${NCD_RUNNER_LANE:-" + lane + "}\" = " + lane + " ]; then " + pinned(procs, memlimit, parallel) + "; else " + heavy(heavyProcs, heavyMemlimit, heavyParallel) + "; fi"
}

// fetchRemoteArtifact reads the retained manifest line and then the
// artifact from one remote `cat -- <manifest> <artifact>` stream, writes the
// artifact to dst and checks it against the manifest's SHA-256. One stream
// replaces a manifest call and an artifact call. A missing or invalid
// manifest fails the fetch; the artifact stays on the runner for recovery.
func fetchRemoteArtifact(fetch *exec.Cmd, artifact string, dst io.Writer) (string, error) {
	out, err := fetch.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err = fetch.Start(); err != nil {
		return "", err
	}
	r := bufio.NewReaderSize(out, 64<<10)
	line, lineErr := r.ReadSlice('\n')
	fields := strings.Fields(string(line))
	if lineErr != nil || len(fields) != 2 || len(fields[0]) != 64 || strings.ToLower(fields[0]) != fields[0] || fields[1] != artifact {
		_ = fetch.Process.Kill()
		return "", fmt.Errorf("remote artifact manifest unavailable or invalid: %w", errors.Join(lineErr, fetch.Wait()))
	}
	digest := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(dst, digest), r)
	if err = errors.Join(copyErr, fetch.Wait()); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(digest.Sum(nil))
	if sum != fields[0] {
		return "", errors.New("remote artifact checksum mismatch")
	}
	return sum, nil
}

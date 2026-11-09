package sessionpressure

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/mod/modfile"
)

type RemoteWorkPolicy struct {
	Enabled bool `json:"enabled"`
}

func SetRemoteWork(dir string, enabled bool) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	body, _ := json.Marshal(RemoteWorkPolicy{Enabled: enabled})
	return atomicWrite(filepath.Join(dir, "remote-work.json"), append(body, '\n'), 0600)
}

func RemoteWorkEnabled(dir string) (bool, error) {
	body, err := os.ReadFile(filepath.Join(dir, "remote-work.json"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var policy RemoteWorkPolicy
	if err = json.Unmarshal(body, &policy); err != nil {
		return false, err
	}
	return policy.Enabled, nil
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
	for i := 2; i < len(out); i++ {
		if out[i] == "-o" && i+1 < len(out) {
			output := out[i+1]
			out[i+1] = "/srv/build/candidates/OUTPUT"
			return out, output, nil
		}
		if strings.HasPrefix(out[i], "-o=") {
			output := strings.TrimPrefix(out[i], "-o=")
			out[i] = "-o=/srv/build/candidates/OUTPUT"
			return out, output, nil
		}
	}
	for _, arg := range out[2:] {
		if strings.Contains(arg, "...") {
			return out, "", nil
		}
	}
	return out, "", errors.New("remote go build requires -o <local-output> so its executable can be returned; use go build -o ./bin/<name> <package>, or go build ./... for validation")
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

// Match the canonical run-job.sh environment in non-login SSH shells. The
// system Git is too old for merge-file --diff-algorithm. Missing pinned tools
// must refuse the job rather than silently select a different toolchain.
func remoteRunnerPrelude() string {
	return "{ test -x /srv/build/bin/git && test -x /usr/local/go/bin/go; } || { printf '%s\\n' " +
		shellQuote(WorkRefusalMarker+" kind="+WorkRefusalRemoteProfile+": pinned runner Git or Go is missing; local fallback is disabled") +
		fmt.Sprintf(" >&2; exit %d; }; ", workExitPolicyDenied) +
		"export PATH=/srv/build/bin:/usr/local/go/bin:\"$PATH\" " +
		"GOCACHE=/srv/build/cache/go-build GOMODCACHE=/srv/build/cache/go-mod GOPATH=/srv/build/go && " +
		"umask 0022 && "
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
			return nil, "", errors.New("remote Go work requires a module")
		}
		root = parent
	}
	seen := map[string]bool{}
	var modules []string
	var visit func(string) error
	visit = func(dir string) error {
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
			if err := visit(target); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(root); err != nil {
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

// remoteSourceLimit bounds one upload; larger closures are rejected.
const remoteSourceLimit = 512 << 20

// walkRemoteSources visits each uploadable regular file of the module closure
// once. Both the pre-upload size check and the upload use this one filter.
func walkRemoteSources(modules []string, common string, visit func(rel, path string, info os.FileInfo) error) error {
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
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.CommandContext(ctx, "git", "-C", repo, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
		body, err := cmd.Output()
		cancel()
		if err != nil {
			return fmt.Errorf("enumerate repository inputs: %w", err)
		}
		for _, name := range strings.Split(string(body), "\x00") {
			if name == "" || remoteExcludedRepositoryInput(name) {
				continue
			}
			path := filepath.Join(repo, name)
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

// remoteSourceSize returns the upload size, or an error once it passes limit.
func remoteSourceSize(modules []string, common string, limit int64) (int64, error) {
	var total int64
	err := walkRemoteSources(modules, common, func(_, _ string, info os.FileInfo) error {
		total += info.Size()
		if total > limit {
			return fmt.Errorf("remote source exceeds %dMiB; narrow the module", limit>>20)
		}
		return nil
	})
	return total, err
}

func writeRemoteSources(out io.Writer, modules []string, common string) error {
	tw := tar.NewWriter(out)
	defer tw.Close()
	var total int64
	err := walkRemoteSources(modules, common, func(rel, path string, info os.FileInfo) error {
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
		return err
	}
	return tw.Close()
}

// TryRemoteWork runs eligible commands on the tailnet runner. A failed route
// never silently falls back onto the overloaded Mac. The remote lock is shared
// by all these jobs; Go workers and compiler parallelism are bounded to two.
func TryRemoteWork(dir string, options WorkRunOptions, streams WorkRunStreams) (handled bool, exitCode int, resultErr error) {
	enabled, err := RemoteWorkEnabled(dir)
	if err != nil {
		return true, 1, err
	}
	if !enabled || runtime.GOOS != "darwin" || !RemoteGoCommand(options.Command) {
		return false, 0, nil
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
	// Reject unsupported profiles before upload. Enabled remote routing must
	// never turn an eligibility failure into heavy work on the Mac.
	reject := func(reason string) (bool, int, error) {
		return rejectRemoteWork(dir, options.Command[1], reason)
	}
	if reason := remoteProfileReason(options.Command, os.Getenv); reason != "" {
		return reject(reason)
	}
	if reason := remoteWorkIneligibleReason(options.Command, os.Getenv); reason != "" {
		return reject(reason)
	}
	modules, common, err := remoteModuleClosure(cwd)
	if err != nil {
		return reject(err.Error())
	}
	if _, err = remoteSourceSize(modules, common, remoteSourceLimit); err != nil {
		return reject(err.Error())
	}
	// Explicit outputs can be returned without guessing package/main names.
	argv := append([]string(nil), options.Command...)
	argv[0] = "go"
	argv, output, err := normalizeRemoteBuildArgv(argv)
	if err != nil {
		return reject(err.Error())
	}
	job, err := NewWorkOperationID()
	if err != nil {
		return true, 1, err
	}
	profile := remoteBuildProfile(argv[1], os.Getenv("CGO_ENABLED"))
	started := time.Now()
	defer func() {
		recordControlEvent(dir, map[string]any{"event": "remote_work_finished", "duration_ms": time.Since(started).Milliseconds(), "operation": options.Command[1], "profile": profile, "exit_code": exitCode, "failed": resultErr != nil || exitCode != 0})
	}()
	remote := "/srv/build/candidates/pressure-" + job
	for i := range argv {
		argv[i] = strings.ReplaceAll(argv[i], "/srv/build/candidates/OUTPUT", remote+"/artifact")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour+32*time.Minute)
	defer cancel()
	ssh := func(command string) *exec.Cmd {
		return exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=8", "builder@100.106.202.89", command)
	}
	// Upload/extraction has one bounded host-wide preparation lane, separate
	// from the scheduler's execution slot. A stalled sender cannot hold it forever.
	upload := ssh("timeout 600 flock -w 600 /srv/build/input-transfer.lock sh -c " + shellQuote("umask 077; mkdir -p "+shellQuote(remote+"/source")+" && tar -xf - -C "+shellQuote(remote+"/source")))
	pipe, err := upload.StdinPipe()
	if err != nil {
		return true, 1, err
	}
	upload.Stderr = streams.Stderr
	if err = upload.Start(); err != nil {
		return true, 1, err
	}
	writeErr := writeRemoteSources(pipe, modules, common)
	_ = pipe.Close()
	err = upload.Wait()
	if writeErr != nil || err != nil {
		return true, 1, fmt.Errorf("remote source upload failed: %w", errors.Join(writeErr, err))
	}
	cleanupSafe := false
	defer func() {
		if !cleanupSafe {
			fmt.Fprintf(streams.Stderr, "session-pressure: retained remote inputs/artifacts at %s; request key pressure-%s (completion not confirmed)\n", remote, job)
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanupCtx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "builder@100.106.202.89", "rm -rf -- "+shellQuote(remote)).Run()
	}()
	rel, _ := filepath.Rel(common, cwd)
	command := remoteRunnerPrelude() + remoteBuildPreflight(profile, remote) + "cd " + shellQuote("./"+filepath.ToSlash(rel)) + " && env GOWORK=off GOMAXPROCS=2 GOMEMLIMIT=3GiB GOFLAGS=" + shellQuote(strings.TrimSpace(os.Getenv("GOFLAGS")+" -p=2")) + remoteBuildEnvironment(profile, remote)
	for _, arg := range argv {
		command += " " + shellQuote(arg)
	}
	if output != "" && output != "/dev/null" {
		command += " && sha256sum " + shellQuote(remote+"/artifact") + " > " + shellQuote(remote+"/artifact.sha256")
	}
	// The canonical runner retains source inputs before durable submission.
	// The command starts at the retained common root, preserving replacements.
	command = "env NCD_RUNNER_FRAMED=1 NCD_RUNNER_KEY=" + shellQuote("pressure-"+job) + " /srv/build/run-job.sh session-pressure " + shellQuote("session-pressure:"+job) + " --candidate-dir " + shellQuote(remote+"/source") + " --timeout 1800 --shell " + shellQuote(command)
	command = "if test -x /srv/build/gaterunner && /srv/build/gaterunner inspect --key " + shellQuote("pressure-"+job) + " >/dev/null 2>&1; then exec env NCD_RUNNER_FRAMED=1 /srv/build/gaterunner wait --key " + shellQuote("pressure-"+job) + "; else exec " + command + "; fi"
	if streams.Stderr != nil {
		fmt.Fprintln(streams.Stderr, "session-pressure: routing Go work to gaterunner-1 ("+profile+", durable runner queue, parallelism 2)")
	}
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
		manifest, manifestErr := ssh("cat " + shellQuote(remote+"/artifact.sha256")).Output()
		fields := strings.Fields(string(manifest))
		if manifestErr != nil || len(fields) != 2 || len(fields[0]) != 64 || fields[1] != remote+"/artifact" {
			return true, 1, fmt.Errorf("remote artifact manifest unavailable or invalid: %w", manifestErr)
		}
		if !filepath.IsAbs(output) {
			output = filepath.Join(cwd, output)
		}
		if err = os.MkdirAll(filepath.Dir(output), 0755); err != nil {
			return true, 1, err
		}
		f, err := os.CreateTemp(filepath.Dir(output), ".remote-go-artifact-*")
		if err != nil {
			return true, 1, err
		}
		defer os.Remove(f.Name())
		fetch := ssh("cat " + shellQuote(remote+"/artifact"))
		digest := sha256.New()
		fetch.Stdout = io.MultiWriter(f, digest)
		fetch.Stderr = streams.Stderr
		err = fetch.Run()
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			return true, 1, errors.Join(err, closeErr)
		}
		if fmt.Sprintf("%x", digest.Sum(nil)) != fields[0] {
			return true, 1, errors.New("remote artifact checksum mismatch")
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

// rejectRemoteWork marks the command handled so callers cannot enter local
// admission after a profile or source rejection.
func rejectRemoteWork(dir, operation, reason string) (bool, int, error) {
	recordControlEvent(dir, map[string]any{"event": "remote_work_declined", "operation": operation, "reason": reason, "local_blocked": true})
	return true, workExitPolicyDenied, workRefusal(WorkRefusalRemoteProfile, fmt.Errorf("remote Go work blocked: %s; use the Hetzner gate-runner with an explicit compiler profile; local fallback is disabled", reason))
}

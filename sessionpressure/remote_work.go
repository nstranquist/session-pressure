package sessionpressure

import (
	"archive/tar"
	"context"
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

// normalizeRemoteBuildArgv rewrites a `go build` argv for remote execution.
// Explicit -o outputs are redirected to the remote artifact placeholder and
// reported so the artifact can be fetched back. Multi-package "..." builds
// stay check-only (output ""). A bare or single-package build with no -o
// becomes a validation build instead of an error: a placeholder -o is
// appended and the output is reported as "/dev/null", which the caller
// treats as discard (no fetch-back). Builds naming more than one package
// without -o keep the historical error. Non-build commands pass through.
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
	positional := 0
	for _, arg := range out[2:] {
		if !strings.HasPrefix(arg, "-") {
			positional++
		}
	}
	if positional > 1 {
		return out, "", errors.New("remote go build requires -o <local-output> (or a multi-package ... validation build)")
	}
	out = append(out, "-o=/srv/build/candidates/OUTPUT")
	return out, "/dev/null", nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

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
	for _, dir := range modules {
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

func writeRemoteSources(out io.Writer, modules []string, common string) error {
	tw := tar.NewWriter(out)
	defer tw.Close()
	seen := map[string]bool{}
	var total int64
	for _, module := range modules {
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
			if !info.Mode().IsRegular() {
				return nil
			}
			if info.Size() > 20<<20 {
				return nil
			}
			total += info.Size()
			if total > 512<<20 {
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
	started := time.Now()
	defer func() {
		recordControlEvent(dir, map[string]any{"event": "remote_work_finished", "duration_ms": time.Since(started).Milliseconds(), "operation": options.Command[1], "exit_code": exitCode, "failed": resultErr != nil})
	}()
	cwd, err := os.Getwd()
	if err != nil {
		return true, 1, err
	}
	modules, common, err := remoteModuleClosure(cwd)
	if err != nil {
		return true, 1, err
	}
	// Explicit outputs can be returned without guessing package/main names.
	argv := append([]string(nil), options.Command...)
	argv[0] = "go"
	argv, output, err := normalizeRemoteBuildArgv(argv)
	if err != nil {
		return true, 2, err
	}
	job, err := NewWorkOperationID()
	if err != nil {
		return true, 1, err
	}
	remote := "/srv/build/candidates/pressure-" + job
	for i := range argv {
		argv[i] = strings.ReplaceAll(argv[i], "/srv/build/candidates/OUTPUT", remote+"/artifact")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	ssh := func(command string) *exec.Cmd {
		return exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=8", "builder@100.106.202.89", command)
	}
	upload := ssh("umask 077; mkdir -p " + shellQuote(remote+"/source") + " && tar -xf - -C " + shellQuote(remote+"/source"))
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
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanupCtx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "builder@100.106.202.89", "rm -rf -- "+shellQuote(remote)).Run()
	}()
	rel, _ := filepath.Rel(common, cwd)
	command := "cd " + shellQuote(remote+"/source/"+filepath.ToSlash(rel)) + " && exec env GOWORK=off GOMAXPROCS=2 GOMEMLIMIT=3GiB GOFLAGS=" + shellQuote(strings.TrimSpace(os.Getenv("GOFLAGS")+" -p=2"))
	if argv[1] == "build" {
		command += " GOOS=darwin GOARCH=arm64 CGO_ENABLED=0"
	}
	for _, arg := range argv {
		command += " " + shellQuote(arg)
	}
	command = "flock -w 1800 /srv/build/global-work.lock timeout --kill-after=5s 1800s bash -c " + shellQuote(command)
	if streams.Stderr != nil {
		fmt.Fprintln(streams.Stderr, "session-pressure: routing Go work to gaterunner-1 (global slot, parallelism 2)")
	}
	child := ssh(command)
	child.Stdin = streams.Stdin
	child.Stdout = streams.Stdout
	child.Stderr = streams.Stderr
	if err = child.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return true, exit.ExitCode(), nil
		}
		return true, 1, err
	}
	if output != "" && output != "/dev/null" {
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
		fetch.Stdout = f
		fetch.Stderr = streams.Stderr
		err = fetch.Run()
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			return true, 1, errors.Join(err, closeErr)
		}
		if err = os.Chmod(f.Name(), 0755); err != nil {
			return true, 1, err
		}
		if err = os.Rename(f.Name(), output); err != nil {
			return true, 1, err
		}
	}
	return true, 0, nil
}

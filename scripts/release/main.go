// release builds from a clean committed checkout and promotes verified bundles.
// Run the build command on the gate runner; install runs on the target Mac.
package main

import (
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

type manifest struct {
	Revision   string            `json:"revision"`
	OS         string            `json:"os"`
	Arch       string            `json:"arch"`
	BuiltAt    time.Time         `json:"built_at"`
	CGOEnabled bool              `json:"cgo_enabled"`
	Files      map[string]string `json:"files"`
}

var names = []string{"session-pressure", "session-pressure-api", "session-pressure-helper", "gocache-lru", "session-pressure-release", "gaterunner-fleet"}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) > 0 && args[0] == "acceptance" {
		return acceptance(args[1:])
	}
	if len(args) > 0 && args[0] == "observe" {
		return observe(args[1:])
	}
	if len(args) == 0 {
		return errors.New("usage: release build|install|app-status|install-app [flags]")
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	out := f.String("out", "", "bundle directory")
	platform := f.String("platform", "darwin/arm64", "build target")
	prefix := f.String("prefix", "", "install bin directory")
	expected := f.String("expected-current", "", "observed current revision or app digest; none for first install")
	allowRollback := f.Bool("allow-rollback", false, "explicitly permit an older release after checking current authority")
	app := f.String("app", "", "signed .app source")
	destination := f.String("destination", "/Applications/NDev Pressure.app", "app destination")
	digestOnly := f.Bool("digest-only", false, "print only the app authority digest (app-status)")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	switch args[0] {
	case "build":
		return build(*out, *platform)
	case "install":
		return install(*out, *prefix, *expected, *allowRollback)
	case "install-app":
		return installApp(*app, *destination, *expected, *allowRollback)
	case "app-status":
		identity, err := readAppIdentity(*app)
		if err != nil {
			return err
		}
		if *digestOnly {
			fmt.Println(identity.authority())
			return nil
		}
		return json.NewEncoder(os.Stdout).Encode(identity)
	default:
		return errors.New("unknown release action")
	}
}
func command(name string, args ...string) (string, error) {
	c := exec.Command(name, args...)
	raw, err := c.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", name, err, raw)
	}
	return strings.TrimSpace(string(raw)), nil
}
func digest(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func writeJSON(path string, v any) error {
	raw, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return atomicFile(path, append(raw, '\n'), 0600)
}
func atomicFile(path string, raw []byte, mode os.FileMode) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".release-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(raw)
	}
	if e == nil {
		e = f.Sync()
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return e
	}
	if e = os.Rename(f.Name(), path); e != nil {
		return e
	}
	return syncDir(filepath.Dir(path))
}
func syncDir(path string) error {
	d, e := os.Open(path)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}

// Darwin releases require the native NVMe reader. A portable CGO-disabled
// binary can run, but is not a complete product release.
func releaseEnvironment(goos, goarch string) ([]string, error) {
	cgo := "0"
	if goos == "darwin" {
		if os.Getenv("CGO_ENABLED") == "0" {
			return nil, errors.New("Darwin releases require CGO_ENABLED=1 for native SMART support")
		}
		if (runtime.GOOS != goos || runtime.GOARCH != goarch) && os.Getenv("CC") == "" {
			return nil, errors.New("Darwin cross releases require an explicit target C compiler in CC")
		}
		cgo = "1"
	}
	return append(os.Environ(), "GOWORK=off", "CGO_ENABLED="+cgo, "GOOS="+goos, "GOARCH="+goarch), nil
}
func build(out, platform string) error {
	parts := strings.Split(platform, "/")
	if len(parts) != 2 || (parts[0] != "darwin" && parts[0] != "linux") || (parts[1] != "arm64" && parts[1] != "amd64") {
		return errors.New("unsupported platform")
	}
	if out == "" {
		return errors.New("--out is required")
	}
	buildEnv, e := releaseEnvironment(parts[0], parts[1])
	if e != nil {
		return e
	}
	abs, e := filepath.Abs(out)
	if e != nil {
		return e
	}
	root, e := command("git", "rev-parse", "--show-toplevel")
	if e != nil {
		return e
	}
	if abs == root || strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		return errors.New("bundle output must be outside the checkout")
	}
	dirty, e := command("git", "status", "--porcelain", "--untracked-files=all")
	if e != nil {
		return e
	}
	if dirty != "" {
		return errors.New("release requires a clean committed checkout")
	}
	rev, e := command("git", "rev-parse", "HEAD")
	if e != nil {
		return e
	}
	if e = os.MkdirAll(abs, 0700); e != nil {
		return e
	}
	m := manifest{Revision: rev, OS: parts[0], Arch: parts[1], BuiltAt: time.Now().UTC(), CGOEnabled: parts[0] == "darwin", Files: map[string]string{}}
	for _, name := range names {
		pkg := "./cmd/" + name
		if name == "session-pressure-release" {
			pkg = "./scripts/release"
		}
		c := exec.Command("go", "build", "-trimpath", "-buildvcs=true", "-o", filepath.Join(abs, name), pkg)
		c.Env = buildEnv
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if e = c.Run(); e != nil {
			return e
		}
		dirty, e = command("git", "status", "--porcelain", "--untracked-files=all")
		if e != nil {
			return e
		}
		if dirty != "" {
			return errors.New("compiler modified the committed checkout")
		}
		m.Files[name], e = digest(filepath.Join(abs, name))
		if e != nil {
			return e
		}
	}
	return writeJSON(filepath.Join(abs, "release.json"), m)
}
func verify(dir string) (manifest, error) { return verifyBundle(dir, false) }

// Older active releases predate the fleet selector. Verify every artifact they
// shipped; new candidate bundles must still contain the selector.
func verifyBundle(dir string, allowLegacy bool) (manifest, error) {
	var m manifest
	f, e := os.Open(filepath.Join(dir, "release.json"))
	if e != nil {
		return m, e
	}
	defer f.Close()
	raw, e := io.ReadAll(io.LimitReader(f, 65537))
	if e != nil {
		return m, e
	}
	if len(raw) > 65536 {
		return m, errors.New("oversized release manifest")
	}
	if e = json.Unmarshal(raw, &m); e != nil {
		return m, e
	}
	rev, e := hex.DecodeString(m.Revision)
	if e != nil || len(rev) != 20 || len(m.Files) != len(names) {
		return m, errors.New("invalid release identity")
	}
	if m.OS == "darwin" && !m.CGOEnabled {
		return m, errors.New("Darwin release lacks native SMART support")
	}
	for _, name := range names {
		if allowLegacy && name == "gaterunner-fleet" && m.Files[name] == "" {
			continue
		}
		path := filepath.Join(dir, name)
		info, e := os.Lstat(path)
		if e != nil {
			return m, e
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return m, errors.New("release binary is not regular executable")
		}
		sum, e := digest(path)
		if e != nil || sum != m.Files[name] {
			return m, fmt.Errorf("release digest mismatch: %s", name)
		}
		bi, e := buildinfo.ReadFile(path)
		if e != nil {
			return m, e
		}
		settings := map[string]string{}
		for _, s := range bi.Settings {
			settings[s.Key] = s.Value
		}
		if m.OS == "darwin" && settings["CGO_ENABLED"] != "1" {
			return m, fmt.Errorf("native SMART support missing: %s", name)
		}
		if bi.Main.Path != "github.com/nstranquist/session-pressure" || settings["vcs.revision"] != m.Revision || settings["vcs.modified"] != "false" || settings["GOOS"] != m.OS || settings["GOARCH"] != m.Arch {
			return m, fmt.Errorf("release provenance mismatch: %s", name)
		}
	}
	return m, nil
}
func copyFile(from, to string) error {
	in, e := os.Open(from)
	if e != nil {
		return e
	}
	defer in.Close()
	out, e := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
	if e != nil {
		return e
	}
	_, e = io.Copy(out, in)
	if e == nil {
		e = out.Sync()
	}
	return errors.Join(e, out.Close())
}
func replaceLink(target, path string) error {
	temp := path + fmt.Sprintf(".next-%d", os.Getpid())
	if e := os.Symlink(target, temp); e != nil {
		return e
	}
	defer os.Remove(temp)
	if e := os.Rename(temp, path); e != nil {
		return e
	}
	return syncDir(filepath.Dir(path))
}

// checkPromotion binds the operator's observed authority to this promotion.
// Build time is a downgrade fence, not a claim about Git ancestry.
func checkPromotion(candidate manifest, active *manifest, expected string, allowRollback bool) error {
	current := "none"
	if active != nil {
		current = active.Revision
	}
	if expected == "" || expected != current {
		return fmt.Errorf("release authority changed or was not pinned: expected %q, current %q; re-read current and pass --expected-current", expected, current)
	}
	if candidate.BuiltAt.IsZero() || (active != nil && active.BuiltAt.IsZero()) {
		return errors.New("release build timestamp is required for promotion")
	}
	if active != nil && candidate.Revision != active.Revision && candidate.BuiltAt.Before(active.BuiltAt) && !allowRollback {
		return errors.New("candidate predates active build; use --allow-rollback only for an intended rollback")
	}
	return nil
}

func install(dir, prefix, expected string, allowRollback bool) error {
	m, e := verify(dir)
	if e != nil {
		return e
	}
	if m.OS != runtime.GOOS || m.Arch != runtime.GOARCH {
		return errors.New("bundle platform differs from this host")
	}
	if prefix == "" {
		return errors.New("--prefix is required")
	}
	prefix, e = filepath.Abs(prefix)
	if e != nil {
		return e
	}
	store := filepath.Join(prefix, ".session-pressure-releases")
	if e = os.MkdirAll(store, 0700); e != nil {
		return e
	}
	// Kernel lock releases automatically if the installer exits or crashes.
	lock, e := os.OpenFile(filepath.Join(store, "install.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return fmt.Errorf("release install already active: %w", e)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	current := filepath.Join(store, "current")
	previous, readErr := os.Readlink(current)
	var active *manifest
	if readErr == nil {
		id, decodeErr := hex.DecodeString(previous)
		if decodeErr != nil || len(id) != 20 || filepath.Base(previous) != previous {
			return errors.New("invalid installed authority link")
		}
		a, err := verifyBundle(filepath.Join(store, previous), true)
		if err != nil {
			return fmt.Errorf("verify active release: %w", err)
		}
		if a.Revision != previous {
			return errors.New("active release identity mismatch")
		}
		active = &a
	} else if !os.IsNotExist(readErr) {
		return readErr
	}
	if e = checkPromotion(m, active, expected, allowRollback); e != nil {
		return e
	}
	stage, e := os.MkdirTemp(store, ".candidate-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(stage)
	for _, name := range names {
		if e = copyFile(filepath.Join(dir, name), filepath.Join(stage, name)); e != nil {
			return e
		}
	}
	if e = writeJSON(filepath.Join(stage, "release.json"), m); e != nil {
		return e
	}
	if _, e = verify(stage); e != nil {
		return e
	}
	// Exercise the candidate before the authority link changes.
	raw, e := command(filepath.Join(stage, "session-pressure"), "--json", "capabilities")
	if e != nil {
		return e
	}
	var probe struct {
		Capabilities struct {
			SourceRevision string `json:"source_revision"`
		} `json:"capabilities"`
	}
	if e = json.Unmarshal([]byte(raw), &probe); e != nil {
		return e
	}
	if probe.Capabilities.SourceRevision != m.Revision {
		return errors.New("candidate capability revision mismatch")
	}
	releaseDir := filepath.Join(store, m.Revision)
	if _, e = os.Stat(releaseDir); os.IsNotExist(e) {
		if e = os.Rename(stage, releaseDir); e != nil {
			return e
		}
	} else if e != nil {
		return e
	} else {
		existing, e := verify(releaseDir)
		if e != nil {
			return e
		}
		for _, n := range names {
			if existing.Files[n] != m.Files[n] {
				return errors.New("revision already installed with different bytes")
			}
		}
	}
	backups := map[string]string{}
	installed := []string{}
	rollback := func() {
		if previous != "" {
			_ = replaceLink(previous, current)
		} else {
			_ = os.Remove(current)
		}
		for _, name := range installed {
			path := filepath.Join(prefix, name)
			_ = os.Remove(path)
			if old := backups[name]; old != "" {
				_ = os.Rename(old, path)
			}
		}
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	if e = replaceLink(m.Revision, current); e != nil {
		return e
	}
	for _, name := range names {
		path := filepath.Join(prefix, name)
		wanted := filepath.Join(".session-pressure-releases", "current", name)
		if old, err := os.Readlink(path); err == nil && old == wanted {
			continue
		}
		if info, err := os.Lstat(path); err == nil {
			if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
				rollback()
				return errors.New("install target is not a file")
			}
			backup := filepath.Join(store, "previous-"+stamp+"-"+name)
			if e = os.Rename(path, backup); e != nil {
				rollback()
				return e
			}
			backups[name] = backup
		} else if !os.IsNotExist(err) {
			rollback()
			return err
		}
		installed = append(installed, name)
		if e = replaceLink(wanted, path); e != nil {
			rollback()
			return e
		}
	}
	if _, e = command(filepath.Join(prefix, "session-pressure"), "--json", "capabilities"); e != nil {
		rollback()
		return e
	}
	receipt := map[string]any{"installed_at": time.Now().UTC(), "release": m, "previous_release": previous, "expected_current": expected, "allow_rollback": allowRollback, "preserved_previous_paths": backups, "rollback": "use current installer with previous_release bundle, observed --expected-current and --allow-rollback; first migration backups remain preserved"}
	if e = writeJSON(filepath.Join(store, "install-"+stamp+".json"), receipt); e != nil {
		rollback()
		return e
	}
	fmt.Println("Installed", m.Revision, "at", prefix)
	return nil
}
func installApp(source, destination, expected string, allowRollback bool) error {
	if source == "" || !filepath.IsAbs(destination) || !strings.HasSuffix(destination, ".app") {
		return errors.New("--app and absolute .app destination required")
	}
	lock, e := os.OpenFile(destination+".install.lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return fmt.Errorf("app install already active: %w", e)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	current, e := readAppIdentity(destination)
	if e != nil {
		return e
	}
	candidate, e := readAppIdentity(source)
	if e != nil {
		return e
	}
	if e = validateAppPromotion(current, candidate, expected, allowRollback); e != nil {
		return e
	}
	if _, e := command("codesign", "--verify", "--deep", "--strict", source); e != nil {
		return e
	}
	stage, e := os.MkdirTemp(filepath.Dir(destination), ".pressure-install-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(stage)
	next := filepath.Join(stage, "NDev Pressure.app")
	if _, e = command("ditto", source, next); e != nil {
		return e
	}
	if _, e = command("codesign", "--verify", "--deep", "--strict", next); e != nil {
		return e
	}
	staged, e := readAppIdentity(next)
	if e != nil {
		return e
	}
	if staged.Digest != candidate.Digest {
		return errors.New("app source changed during staging")
	}
	latest, e := readAppIdentity(destination)
	if e != nil {
		return e
	}
	if e = validateAppPromotion(latest, staged, expected, allowRollback); e != nil {
		return e
	}
	backup := destination + ".rollback-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	prior := false
	if _, e = os.Lstat(destination); e == nil {
		if e = os.Rename(destination, backup); e != nil {
			return e
		}
		prior = true
	} else if !os.IsNotExist(e) {
		return e
	}
	if e = os.Rename(next, destination); e != nil {
		if prior {
			_ = os.Rename(backup, destination)
		}
		return e
	}
	if _, e = command("codesign", "--verify", "--deep", "--strict", destination); e != nil {
		_ = os.Rename(destination, next)
		if prior {
			_ = os.Rename(backup, destination)
		}
		return e
	}
	receipt := map[string]any{"installed_at": time.Now().UTC(), "app": staged, "previous_app": current, "expected_current": expected, "allow_rollback": allowRollback, "backup": backup}
	if e = writeJSON(destination+".install-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".json", receipt); e != nil {
		_ = os.Rename(destination, next)
		if prior {
			_ = os.Rename(backup, destination)
		}
		return e
	}
	fmt.Println("Installed", destination, "rollback", backup)
	return nil
}

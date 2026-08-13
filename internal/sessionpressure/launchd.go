package sessionpressure

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nstranquist/session-pressure/internal/filelock"
	"github.com/nstranquist/session-pressure/pkg/processtree"
)

const (
	LaunchdLabel           = "com.nicos.session-pressure"
	launchdInstallLockName = "launchd-install"
	ControlBinaryEnv       = "NDEV_SESSION_PRESSURE_CONTROL_BINARY"
	ControlBinaryDigestEnv = "NDEV_SESSION_PRESSURE_CONTROL_BINARY_SHA256"
)

var ErrLaunchAgentNotRunning = errors.New("session pressure LaunchAgent is not running")

var controlBinaryPlistPattern = regexp.MustCompile(`(?s)(<key>` + ControlBinaryEnv + `</key>\s*<string>)([^<]+)(</string>)`)
var controlBinaryDigestPlistPattern = regexp.MustCompile(`(?s)(<key>` + ControlBinaryDigestEnv + `</key>\s*<string>)([^<]+)(</string>)`)
var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func normalizedControlBinaryPlist(body []byte) []byte {
	body = controlBinaryPlistPattern.ReplaceAll(body, []byte(`${1}CONTROL_BINARY${3}`))
	return controlBinaryDigestPlistPattern.ReplaceAll(body, []byte(`${1}CONTROL_BINARY_SHA256${3}`))
}

func controlBinaryDigestFromPlist(body []byte) string {
	match := controlBinaryDigestPlistPattern.FindSubmatch(body)
	if len(match) != 4 {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(string(match[2])))
}

func controlBinaryFromPlist(body []byte) string {
	match := controlBinaryPlistPattern.FindSubmatch(body)
	if len(match) != 4 {
		return ""
	}
	return html.UnescapeString(string(match[2]))
}

type LaunchdStatus struct {
	OK                    bool   `json:"ok"`
	Label                 string `json:"label"`
	Installed             bool   `json:"installed"`
	Loaded                bool   `json:"loaded"`
	PID                   int    `json:"pid,omitempty"`
	PlistPath             string `json:"plist_path"`
	StdoutPath            string `json:"stdout_path"`
	StderrPath            string `json:"stderr_path"`
	ArtifactManifestPath  string `json:"artifact_manifest_path"`
	ArtifactPresent       bool   `json:"artifact_present"`
	ArtifactVerified      bool   `json:"artifact_verified"`
	ArtifactPath          string `json:"artifact_path,omitempty"`
	ArtifactSHA256        string `json:"artifact_sha256,omitempty"`
	ArtifactInstalledAt   string `json:"artifact_installed_at,omitempty"`
	ControlBinaryPath     string `json:"control_binary_path,omitempty"`
	ControlBinarySHA256   string `json:"control_binary_sha256,omitempty"`
	ControlBinaryVerified bool   `json:"control_binary_verified"`
	Detail                string `json:"detail,omitempty"`
}

type launchctlRunner func(ctx context.Context, args ...string) ([]byte, error)

type LaunchdManager struct {
	Home          string
	Binary        string
	ControlBinary string
	DataDir       string
	Launchctl     launchctlRunner
	SignalPID     func(int, os.Signal) error
}

func NewLaunchdManager(binary, dataDir string) (*LaunchdManager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	if binary == "" {
		var executable string
		executable, err = os.Executable()
		if err != nil {
			return nil, err
		}
		if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil {
			executable = resolved
		}
		binary = sessionPressureBinaryForExecutable(executable)
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return nil, err
	}
	controlBinary, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve cleanup control binary: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(controlBinary); resolveErr == nil {
		controlBinary = resolved
	}
	manager := &LaunchdManager{Home: home, Binary: binary, ControlBinary: controlBinary, DataDir: dataDir}
	manager.Launchctl = func(ctx context.Context, args ...string) ([]byte, error) {
		commandCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return processtree.CommandContext(commandCtx, "/bin/launchctl", args...).CombinedOutput()
	}
	return manager, nil
}

func sessionPressureBinaryForExecutable(executable string) string {
	if filepath.Base(executable) == "ndev-session-pressure" {
		return executable
	}
	dir := filepath.Dir(executable)
	if strings.HasSuffix(filepath.Base(dir), "-publish.artifacts") {
		// Atomic binary publication resolves the running CLI symlink to a
		// content-addressed file such as bin/ndev-publish.artifacts/sha256-… .
		// The helper is intentionally published through its own stable target
		// one directory above, not copied into every CLI revision directory.
		dir = filepath.Dir(dir)
	}
	return filepath.Join(dir, "ndev-session-pressure")
}

func (manager *LaunchdManager) PlistPath() string {
	return filepath.Join(manager.Home, "Library", "LaunchAgents", LaunchdLabel+".plist")
}

func (manager *LaunchdManager) stdoutPath() string {
	return filepath.Join(manager.DataDir, "monitor.stdout.log")
}
func (manager *LaunchdManager) stderrPath() string {
	return filepath.Join(manager.DataDir, "monitor.stderr.log")
}
func (manager *LaunchdManager) domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }
func (manager *LaunchdManager) target() string { return manager.domain() + "/" + LaunchdLabel }

func (manager *LaunchdManager) Install(ctx context.Context) (LaunchdStatus, error) {
	if _, err := os.Stat(manager.Binary); err != nil {
		return LaunchdStatus{}, fmt.Errorf("guard binary: %w", err)
	}
	unlock, err := filelock.AcquireContext(ctx, filepath.Join(manager.DataDir, launchdInstallLockName), 15*time.Second)
	if err != nil {
		return LaunchdStatus{}, fmt.Errorf("acquire launchd install lock: %w", err)
	}
	defer unlock()
	for _, dir := range []string{filepath.Dir(manager.PlistPath()), manager.DataDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return LaunchdStatus{}, err
		}
	}
	for _, path := range []string{manager.stdoutPath(), manager.stderrPath()} {
		file, openErr := os.OpenFile(path, os.O_CREATE|os.O_APPEND, 0o600)
		if openErr != nil {
			return LaunchdStatus{}, openErr
		}
		if closeErr := file.Close(); closeErr != nil {
			return LaunchdStatus{}, closeErr
		}
		if chmodErr := os.Chmod(path, 0o600); chmodErr != nil {
			return LaunchdStatus{}, chmodErr
		}
	}
	desiredDigest, err := fileSHA256(manager.Binary)
	if err != nil {
		return manager.Status(ctx), fmt.Errorf("hash guard binary: %w", err)
	}
	desiredControlDigest, err := ControlBinarySHA256(manager.ControlBinary)
	if err != nil {
		return manager.Status(ctx), fmt.Errorf("verify cleanup control binary: %w", err)
	}
	current := manager.Status(ctx)
	if current.OK && current.ArtifactVerified && current.ArtifactSHA256 == desiredDigest &&
		current.ControlBinarySHA256 == desiredControlDigest &&
		isPromotedControlArtifact(current.ControlBinaryPath, manager.DataDir, desiredControlDigest) {
		// Installing the exact healthy revision is a read-only convergence.
		// Avoid resetting resident baseline samples and interrupting live
		// monitoring merely because another operator repeated install.
		return current, nil
	}
	artifact, err := promoteArtifact(manager.Binary, manager.DataDir, time.Now())
	if err != nil {
		return manager.Status(ctx), err
	}
	controlArtifact, err := promoteControlArtifact(manager.ControlBinary, manager.DataDir)
	if err != nil {
		return manager.Status(ctx), err
	}
	plist := renderLaunchdPlist(artifact.Path, controlArtifact.Path, controlArtifact.SHA256, manager.Home, manager.DataDir, manager.stdoutPath(), manager.stderrPath())
	if err := atomicWrite(manager.PlistPath(), []byte(plist), 0o644); err != nil {
		return LaunchdStatus{}, err
	}
	if current.Loaded {
		if output, bootoutErr := manager.Launchctl(ctx, "bootout", manager.target()); bootoutErr != nil {
			afterBootout := manager.Status(ctx)
			if afterBootout.Loaded {
				return afterBootout, fmt.Errorf("bootout existing launch agent: %w: %s", bootoutErr, strings.TrimSpace(string(output)))
			}
		}
	}
	for attempts := 0; attempts < 20; attempts++ {
		if !manager.Status(ctx).Loaded {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if output, err := manager.Launchctl(ctx, "enable", manager.target()); err != nil {
		return manager.Status(ctx), fmt.Errorf("launchctl enable: %w: %s", err, strings.TrimSpace(string(output)))
	}
	var bootstrapOutput []byte
	var bootstrapErr error
	for attempts := 0; attempts < 4; attempts++ {
		bootstrapOutput, bootstrapErr = manager.Launchctl(ctx, "bootstrap", manager.domain(), manager.PlistPath())
		if bootstrapErr == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if bootstrapErr != nil {
		return manager.Status(ctx), fmt.Errorf("launchctl bootstrap: %w: %s", bootstrapErr, strings.TrimSpace(string(bootstrapOutput)))
	}
	status := manager.Status(ctx)
	for attempts := 0; status.Loaded && status.PID == 0 && attempts < 20; attempts++ {
		time.Sleep(100 * time.Millisecond)
		status = manager.Status(ctx)
	}
	if !status.OK {
		return status, errors.New("launch agent did not report an installed, loaded process with a live pid after install")
	}
	if _, err := pruneArtifactsUnlocked(manager.DataDir); err != nil {
		return status, fmt.Errorf("prune old guard artifacts: %w", err)
	}
	if err := pruneControlArtifacts(manager.DataDir, controlArtifact.SHA256, ArtifactRollbackCount); err != nil {
		return status, fmt.Errorf("prune old cleanup control artifacts: %w", err)
	}
	return status, nil
}

func (manager *LaunchdManager) Uninstall(ctx context.Context) (LaunchdStatus, error) {
	_, bootErr := manager.Launchctl(ctx, "bootout", manager.target())
	removeErr := os.Remove(manager.PlistPath())
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	status := manager.Status(ctx)
	for status.Loaded {
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return status, fmt.Errorf("wait for launch agent unload: %w", ctx.Err())
		case <-timer.C:
		}
		status = manager.Status(ctx)
		if ctx.Err() != nil {
			return status, fmt.Errorf("wait for launch agent unload: %w", ctx.Err())
		}
	}
	if removeErr != nil {
		return status, removeErr
	}
	// A non-zero bootout is harmless when the effective postcondition is still
	// confirmed: the job is absent. This also keeps repeated uninstall idempotent.
	_ = bootErr
	return status, nil
}

func (manager *LaunchdManager) Restart(ctx context.Context) (LaunchdStatus, error) {
	status := manager.Status(ctx)
	if !status.Loaded || status.PID == 0 {
		return status, ErrLaunchAgentNotRunning
	}
	signalPID := manager.SignalPID
	if signalPID == nil {
		signalPID = func(pid int, signal os.Signal) error {
			process, findErr := os.FindProcess(pid)
			if findErr != nil {
				return findErr
			}
			return process.Signal(signal)
		}
	}
	if err := signalPID(status.PID, os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return status, err
	}
	// KeepAlive owns the restart. Do not synchronously kickstart a running
	// process: launchctl can wait for the long-lived replacement and hit the
	// command timeout even though the service is healthy. Poll for a different
	// live PID so a policy command cannot claim that stale in-memory policy was
	// reloaded.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		current := manager.Status(ctx)
		if current.Loaded && current.PID > 0 && current.PID != status.PID {
			return current, nil
		}
		select {
		case <-ctx.Done():
			return current, fmt.Errorf("wait for launch agent restart: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// EnsureRunning reloads a healthy resident so it observes a new policy, or
// installs it when absent/degraded. A successful return always carries an OK
// status rather than merely a policy file that hopes launchd is running.
func (manager *LaunchdManager) EnsureRunning(ctx context.Context) (LaunchdStatus, error) {
	status := manager.Status(ctx)
	if status.OK {
		return manager.Restart(ctx)
	}
	return manager.Install(ctx)
}

var launchdPIDPattern = regexp.MustCompile(`(?m)^\s*pid\s*=\s*([0-9]+)\s*$`)

func (manager *LaunchdManager) Status(ctx context.Context) LaunchdStatus {
	status := LaunchdStatus{
		Label: LaunchdLabel, PlistPath: manager.PlistPath(),
		StdoutPath: manager.stdoutPath(), StderrPath: manager.stderrPath(),
		ArtifactManifestPath: ArtifactManifestPath(manager.DataDir),
	}
	plistVerified := false
	if artifact, found, artifactErr := LoadInstalledArtifact(manager.DataDir); artifactErr != nil {
		status.Detail = artifactErr.Error()
	} else if found {
		status.ArtifactPath = artifact.Path
		status.ArtifactSHA256 = artifact.SHA256
		status.ArtifactInstalledAt = artifact.InstalledAt.Format(time.RFC3339Nano)
		if info, statErr := os.Lstat(artifact.Path); statErr == nil && info.Mode().IsRegular() {
			status.ArtifactPresent = true
			if info.Mode().Perm() != artifactFileMode {
				status.Detail = fmt.Sprintf("installed artifact mode is %04o, want %04o", info.Mode().Perm(), artifactFileMode)
			} else if digest, hashErr := fileSHA256(artifact.Path); hashErr != nil {
				status.Detail = "verify installed artifact: " + hashErr.Error()
			} else if digest != artifact.SHA256 {
				status.Detail = "installed artifact digest does not match its manifest"
			} else {
				status.ArtifactVerified = true
			}
		} else if statErr == nil {
			status.Detail = "installed artifact is not a regular file"
		} else {
			status.Detail = "inspect installed artifact: " + statErr.Error()
		}
	}
	if plistBody, err := os.ReadFile(manager.PlistPath()); err == nil {
		status.Installed = true
		if status.ArtifactPath == "" {
			if status.Detail == "" {
				status.Detail = "launch agent plist exists without a valid installed artifact"
			}
		} else {
			expected := renderLaunchdPlist(status.ArtifactPath, manager.ControlBinary, "EXPECTED_CONTROL_DIGEST", manager.Home, manager.DataDir, manager.stdoutPath(), manager.stderrPath())
			controlBinary := controlBinaryFromPlist(plistBody)
			controlDigest := controlBinaryDigestFromPlist(plistBody)
			status.ControlBinaryPath = controlBinary
			status.ControlBinarySHA256 = controlDigest
			controlErr := VerifyControlBinary(controlBinary, controlDigest)
			controlValid := controlErr == nil
			if bytes.Equal(normalizedControlBinaryPlist(plistBody), normalizedControlBinaryPlist([]byte(expected))) && controlValid {
				plistVerified = true
				status.ControlBinaryVerified = true
			} else if !controlValid && status.Detail == "" {
				status.Detail = "launch agent cleanup control binary failed digest verification: " + controlErr.Error()
			} else if status.Detail == "" {
				status.Detail = "launch agent plist does not match the installed artifact and expected settings"
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) && status.Detail == "" {
		status.Detail = "read launch agent plist: " + err.Error()
	}
	output, err := manager.Launchctl(ctx, "print", manager.target())
	detail := strings.TrimSpace(string(output))
	if err == nil {
		status.Loaded = true
		if match := launchdPIDPattern.FindStringSubmatch(detail); len(match) == 2 {
			pid, parseErr := strconv.Atoi(match[1])
			if parseErr != nil {
				status.Detail = "parse launch agent pid: " + parseErr.Error()
			} else {
				status.PID = pid
			}
		}
	} else if status.Detail == "" {
		if len(detail) > 512 {
			detail = detail[:512]
		}
		status.Detail = detail
	}
	status.OK = status.Installed && plistVerified && status.Loaded && status.PID > 0 && status.ArtifactPresent && status.ArtifactVerified
	return status
}

func renderLaunchdPlist(binary, controlBinary, controlDigest, home, dataDir, stdoutPath, stderrPath string) string {
	escape := func(value string) string {
		return html.EscapeString(value)
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>HOME</key><string>%s</string>
    <key>%s</key><string>%s</string>
	<key>%s</key><string>%s</string>
	<key>%s</key><string>%s</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Standard</string>
  <key>LowPriorityIO</key><true/>
  <key>Umask</key><integer>63</integer>
  <key>Nice</key><integer>5</integer>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, LaunchdLabel, escape(binary), escape(home), DataDirEnv, escape(dataDir), ControlBinaryEnv, escape(controlBinary), ControlBinaryDigestEnv, escape(controlDigest), escape(stdoutPath), escape(stderrPath))
}

// ControlBinarySHA256 verifies the cleanup controller is an absolute regular
// executable and returns the digest the resident must bind before execution.
func ControlBinarySHA256(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("cleanup control binary path is not absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect cleanup control binary %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("cleanup control binary is not a non-symlink executable regular file")
	}
	digest, err := fileSHA256(path)
	if err != nil {
		return "", err
	}
	return digest, nil
}

// VerifyControlBinary closes the status-to-action gap: the long-lived resident
// hashes the controller again immediately before crossing the process bridge.
func VerifyControlBinary(path, expectedDigest string) error {
	digest, err := ControlBinarySHA256(path)
	if err != nil {
		return err
	}
	if !sha256Pattern.MatchString(expectedDigest) {
		return errors.New("cleanup control binary digest is missing or invalid")
	}
	if digest != expectedDigest {
		return fmt.Errorf("cleanup control binary digest mismatch: got %s want %s", digest, expectedDigest)
	}
	return nil
}

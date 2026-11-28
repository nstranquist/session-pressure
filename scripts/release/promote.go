package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/nstranquist/session-pressure/frontdoor"
)

type promoteOptions struct {
	Repo            string
	Ref             string
	Bundle          string
	Platform        string
	Prefix          string
	ExpectedCurrent string
	ExpectedApp     string
	Destination     string
	AllowRollback   bool
	SkipApp         bool
	SkipMonitor     bool
	SkipDoctor      bool
	SkipFrontDoor   bool
	VerifyLifecycle bool
	DryRun          bool
	JSONOutput      bool
}

type promoteReceipt struct {
	OK              bool              `json:"ok"`
	DryRun          bool              `json:"dry_run"`
	Headless        bool              `json:"headless"`
	Repo            string            `json:"repo"`
	Ref             string            `json:"ref"`
	TargetRevision  string            `json:"target_revision"`
	HeadRevision    string            `json:"head_revision"`
	BundleSource    string            `json:"bundle_source"`
	Prefix          string            `json:"prefix"`
	ExpectedCurrent string            `json:"expected_current"`
	InstalledCLI    string            `json:"installed_cli,omitempty"`
	MonitorPromoted bool              `json:"monitor_promoted"`
	AppPromoted     bool              `json:"app_promoted"`
	AppDestination  string            `json:"app_destination,omitempty"`
	ExpectedApp     string            `json:"expected_app,omitempty"`
	InstalledApp    *appIdentity      `json:"installed_app,omitempty"`
	DoctorChecked   bool              `json:"doctor_checked"`
	DoctorOK        bool              `json:"doctor_ok,omitempty"`
	SourceLagStatus string            `json:"source_lag_status,omitempty"`
	CommitsBehind   int               `json:"commits_behind,omitempty"`
	HeadMovedTo     string            `json:"head_moved_to,omitempty"`
	FrontDoor       *frontDoorReceipt `json:"front_door,omitempty"`
	LifecycleOK     bool              `json:"lifecycle_ok,omitempty"`
	Steps           []string          `json:"steps"`
	CompletedAt     time.Time         `json:"completed_at"`
}

func promote(args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	f := flag.NewFlagSet("promote", flag.ContinueOnError)
	var opts promoteOptions
	f.StringVar(&opts.Repo, "repo", "", "session-pressure source repository (default git toplevel)")
	f.StringVar(&opts.Ref, "ref", "HEAD", "committed git ref to build and promote from a clean clone")
	f.StringVar(&opts.Bundle, "bundle", "", "pre-built verified release bundle directory (skips local Go build)")
	f.StringVar(&opts.Platform, "platform", runtime.GOOS+"/"+runtime.GOARCH, "build target when --bundle is omitted")
	f.StringVar(&opts.Prefix, "prefix", filepath.Join(home, ".local", "bin"), "CLI install prefix")
	f.StringVar(&opts.ExpectedCurrent, "expected-current", "", "pinned current CLI revision (default: read from prefix before build)")
	f.StringVar(&opts.ExpectedApp, "expected-app", "", "pinned current app digest (default: read from destination before build)")
	f.StringVar(&opts.Destination, "destination", "/Applications/NDev Pressure.app", "macOS app bundle destination")
	f.BoolVar(&opts.AllowRollback, "allow-rollback", false, "explicitly permit an older release after checking current authority")
	f.BoolVar(&opts.SkipApp, "skip-app", false, "skip macOS desktop app build and headless install")
	f.BoolVar(&opts.SkipMonitor, "skip-monitor", false, "skip LaunchAgent resident monitor install")
	f.BoolVar(&opts.SkipDoctor, "skip-doctor", false, "skip post-install doctor verification (also skips the front door, which doctor names)")
	f.BoolVar(&opts.SkipFrontDoor, "skip-front-door", false, "do not rebuild or verify the recorded front door")
	f.BoolVar(&opts.VerifyLifecycle, "verify-lifecycle", false, "run scripts/verify-installed-lifecycle.py in isolated temp state")
	f.BoolVar(&opts.DryRun, "dry-run", false, "resolve clean target and authorities without mutating installed artifacts")
	f.BoolVar(&opts.JSONOutput, "json", false, "emit structured JSON receipt")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	receipt, err := runPromote(opts)
	if err != nil {
		return err
	}
	if opts.JSONOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(receipt)
	}
	mode := "promoted"
	if receipt.DryRun {
		mode = "dry-run"
	}
	fmt.Printf("session-pressure %s target=%s prefix=%s cli_authority=%s monitor=%v app=%v doctor_checked=%v source_lag=%s behind=%d\n",
		mode, shortSHA(receipt.TargetRevision), receipt.Prefix, shortSHA(receipt.ExpectedCurrent),
		receipt.MonitorPromoted, receipt.AppPromoted, receipt.DoctorChecked,
		firstStringOr(receipt.SourceLagStatus, "unchecked"), receipt.CommitsBehind)
	if receipt.HeadMovedTo != "" {
		fmt.Printf("session-pressure HEAD moved to %s during the promotion; %s is installed everywhere. Promote again to install the new HEAD.\n",
			shortSHA(receipt.HeadMovedTo), shortSHA(receipt.TargetRevision))
	}
	return nil
}

func runPromote(opts promoteOptions) (promoteReceipt, error) {
	if opts.Ref == "" {
		opts.Ref = "HEAD"
	}
	if opts.Platform == "" {
		opts.Platform = runtime.GOOS + "/" + runtime.GOARCH
	}
	repoRoot := strings.TrimSpace(opts.Repo)
	var err error
	if repoRoot == "" {
		repoRoot, err = command("git", "rev-parse", "--show-toplevel")
		if err != nil {
			return promoteReceipt{}, fmt.Errorf("resolve source repository: %w", err)
		}
	} else {
		repoRoot, err = filepath.Abs(repoRoot)
		if err != nil {
			return promoteReceipt{}, err
		}
		repoRoot, err = commandInDir(repoRoot, "git", "rev-parse", "--show-toplevel")
		if err != nil {
			return promoteReceipt{}, fmt.Errorf("verify source repository: %w", err)
		}
	}
	prefix, err := filepath.Abs(opts.Prefix)
	if err != nil {
		return promoteReceipt{}, err
	}
	targetRev, err := commandInDir(repoRoot, "git", "rev-parse", "--verify", opts.Ref+"^{commit}")
	if err != nil {
		return promoteReceipt{}, fmt.Errorf("resolve ref %q: %w", opts.Ref, err)
	}
	headRev, err := commandInDir(repoRoot, "git", "rev-parse", "HEAD")
	if err != nil {
		return promoteReceipt{}, fmt.Errorf("resolve HEAD: %w", err)
	}

	// Capture both authorities BEFORE building so a concurrent promotion fails
	// closed under the CLI and app kernel locks.
	expectedCurrent := strings.TrimSpace(opts.ExpectedCurrent)
	if expectedCurrent == "" {
		expectedCurrent, err = readCLIAuthority(prefix)
		if err != nil {
			return promoteReceipt{}, err
		}
	}
	wantApp := runtime.GOOS == "darwin" && !opts.SkipApp
	expectedApp := strings.TrimSpace(opts.ExpectedApp)
	if wantApp && expectedApp == "" {
		appID, err := readAppIdentity(opts.Destination)
		if err != nil {
			return promoteReceipt{}, fmt.Errorf("read current app authority: %w", err)
		}
		expectedApp = appID.authority()
	}

	bundleSource := "clean-head-clone"
	if strings.TrimSpace(opts.Bundle) != "" {
		bundleSource = "prebuilt-bundle"
	}
	receipt := promoteReceipt{
		OK:              true,
		DryRun:          opts.DryRun,
		Headless:        true,
		Repo:            repoRoot,
		Ref:             opts.Ref,
		TargetRevision:  targetRev,
		HeadRevision:    headRev,
		BundleSource:    bundleSource,
		Prefix:          prefix,
		ExpectedCurrent: expectedCurrent,
		ExpectedApp:     expectedApp,
		CompletedAt:     time.Now().UTC(),
	}
	if wantApp {
		receipt.AppDestination = opts.Destination
	}

	if opts.DryRun {
		if opts.Bundle != "" {
			m, err := verify(opts.Bundle)
			if err != nil {
				return promoteReceipt{}, fmt.Errorf("verify prebuilt bundle: %w", err)
			}
			if m.Revision != targetRev {
				return promoteReceipt{}, fmt.Errorf("prebuilt bundle revision %s does not match target %s", m.Revision, targetRev)
			}
			receipt.Steps = append(receipt.Steps, "verify-prebuilt-bundle")
		} else {
			receipt.Steps = append(receipt.Steps, "clone-clean-head", "build-cli-bundle")
		}
		receipt.Steps = append(receipt.Steps, "install-cli-guarded")
		if !opts.SkipMonitor {
			receipt.Steps = append(receipt.Steps, "promote-launchd-helper")
		}
		if wantApp {
			receipt.Steps = append(receipt.Steps, "build-signed-app-from-clean-clone", "install-app-headless-guarded")
		}
		if !opts.SkipDoctor {
			receipt.Steps = append(receipt.Steps, "verify-doctor-source-lag")
			if !opts.SkipFrontDoor {
				receipt.Steps = append(receipt.Steps, "republish-recorded-front-door", "verify-doctor-through-front-door")
			}
		}
		if opts.VerifyLifecycle {
			receipt.Steps = append(receipt.Steps, "verify-installed-lifecycle")
		}
		return receipt, nil
	}

	// Build and verify from an isolated clean clone of the exact committed target
	// outside the working checkout so dirty peer edits never contaminate a release.
	workDir, err := os.MkdirTemp("", "session-pressure-promote-*")
	if err != nil {
		return promoteReceipt{}, err
	}
	defer os.RemoveAll(workDir)

	needClone := strings.TrimSpace(opts.Bundle) == "" || wantApp || opts.VerifyLifecycle
	cleanDir := filepath.Join(workDir, "src")
	if needClone {
		if err := cloneCleanRevision(repoRoot, targetRev, cleanDir); err != nil {
			return promoteReceipt{}, err
		}
		receipt.Steps = append(receipt.Steps, "clone-clean-head")
	}

	bundleDir := strings.TrimSpace(opts.Bundle)
	if bundleDir == "" {
		bundleDir = filepath.Join(workDir, "bundle")
		if err := buildInDir(cleanDir, bundleDir, opts.Platform); err != nil {
			return promoteReceipt{}, fmt.Errorf("build clean bundle: %w", err)
		}
		receipt.Steps = append(receipt.Steps, "build-cli-bundle")
	} else {
		m, err := verify(bundleDir)
		if err != nil {
			return promoteReceipt{}, fmt.Errorf("verify prebuilt bundle: %w", err)
		}
		if m.Revision != targetRev {
			return promoteReceipt{}, fmt.Errorf("prebuilt bundle revision %s does not match target %s", m.Revision, targetRev)
		}
		receipt.Steps = append(receipt.Steps, "verify-prebuilt-bundle")
	}

	if err := install(bundleDir, prefix, expectedCurrent, opts.AllowRollback); err != nil {
		return promoteReceipt{}, fmt.Errorf("install cli bundle: %w", err)
	}
	receipt.InstalledCLI = targetRev
	receipt.Steps = append(receipt.Steps, "install-cli-guarded")

	cliBin := filepath.Join(prefix, "session-pressure")
	if !opts.SkipMonitor {
		cmd := exec.Command(cliBin, "--json", "monitor", "install")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return promoteReceipt{}, fmt.Errorf("promote resident monitor: %w: %s", err, strings.TrimSpace(string(out)))
		}
		receipt.MonitorPromoted = true
		receipt.Steps = append(receipt.Steps, "promote-launchd-helper")
	}

	if wantApp {
		appWorkDir := filepath.Join(cleanDir, "apps", "SessionPressure")
		if _, err := commandInDir(appWorkDir, "make", "build", "verify-signing"); err != nil {
			return promoteReceipt{}, fmt.Errorf("build signed app from clean clone: %w", err)
		}
		builtApp := filepath.Join(appWorkDir, ".build", "release", "NDev Pressure.app")
		// Headless promotion only: never launch, open, or activate the GUI app.
		if err := installApp(builtApp, opts.Destination, expectedApp, opts.AllowRollback); err != nil {
			return promoteReceipt{}, fmt.Errorf("install app headlessly: %w", err)
		}
		installedApp, err := readAppIdentity(opts.Destination)
		if err != nil {
			return promoteReceipt{}, fmt.Errorf("verify installed app identity: %w", err)
		}
		receipt.AppPromoted = true
		receipt.InstalledApp = &installedApp
		receipt.Steps = append(receipt.Steps, "build-signed-app-from-clean-clone", "install-app-headless-guarded")
	}

	if !opts.SkipDoctor {
		// Run the installed CLI directly, without the variables a wrapper
		// passes down: an inherited front-door revision describes whatever
		// wrapped this promotion, not what is installed. The front door gets
		// its own check below.
		env := append(frontdoor.WithoutWrapperEnv(os.Environ()), "SESSION_PRESSURE_SOURCE_REPO="+repoRoot)
		report, err := runDoctor(execRunner, []string{cliBin, "--json", "doctor"}, env)
		if err != nil {
			return promoteReceipt{}, err
		}
		receipt.DoctorChecked = true
		receipt.DoctorOK = report.OK
		if report.Doctor.SourceRevisions != nil && report.Doctor.SourceRevisions.Mismatch {
			return promoteReceipt{}, fmt.Errorf("post-install verification failed: installed binaries disagree on source revision (cli=%s helper=%s)",
				shortSHA(report.Doctor.SourceRevisions.CLI), shortSHA(report.Doctor.SourceRevisions.InstalledHelper))
		}
		if lag := report.Doctor.SourceLag; lag != nil {
			receipt.SourceLagStatus = lag.Status
			receipt.CommitsBehind = lag.CommitsBehind
			moved, err := checkPromotedSourceLag(lag, targetRev, headRev)
			if err != nil {
				return promoteReceipt{}, err
			}
			receipt.HeadMovedTo = moved
		}
		receipt.Steps = append(receipt.Steps, "verify-doctor-source-lag")

		if report.Doctor.FrontDoor != nil && !opts.SkipFrontDoor {
			frontDoor, err := promoteFrontDoor(execRunner, *report.Doctor.FrontDoor, targetRev, headRev, env)
			receipt.FrontDoor = &frontDoor
			if err != nil {
				return promoteReceipt{}, err
			}
			if frontDoor.Status == frontDoorRepublished {
				receipt.Steps = append(receipt.Steps, "republish-recorded-front-door", "verify-doctor-through-front-door")
			}
		}
	}

	if opts.VerifyLifecycle {
		lifecycleOut := filepath.Join(workDir, "lifecycle")
		cmd := exec.Command("python3", filepath.Join(cleanDir, "scripts", "verify-installed-lifecycle.py"), "--out", lifecycleOut)
		cmd.Env = append(os.Environ(), "PATH="+prefix+string(os.PathListSeparator)+os.Getenv("PATH"))
		if out, err := cmd.CombinedOutput(); err != nil {
			return promoteReceipt{}, fmt.Errorf("installed lifecycle verification failed: %w: %s", err, strings.TrimSpace(string(out)))
		}
		receipt.LifecycleOK = true
		receipt.Steps = append(receipt.Steps, "verify-installed-lifecycle")
	}

	receipt.CompletedAt = time.Now().UTC()
	return receipt, nil
}

func readCLIAuthority(prefix string) (string, error) {
	link := filepath.Join(prefix, ".session-pressure-releases", "current")
	target, err := os.Readlink(link)
	if os.IsNotExist(err) {
		return "none", nil
	}
	if err != nil {
		return "", fmt.Errorf("read current CLI authority: %w", err)
	}
	decoded, err := hex.DecodeString(target)
	if err != nil || len(decoded) != 20 || filepath.Base(target) != target {
		return "", errors.New("invalid installed CLI authority link")
	}
	return target, nil
}

// checkPromotedSourceLag fails when a promotion of HEAD left the installed
// binary behind the checkout. Doctor measures the lag against the
// checkout's HEAD now, which another commit can move during a promotion.
// When the installed binary links exactly the promoted revision, that lag
// is the new commit, not a failed install: it returns the new HEAD instead.
func checkPromotedSourceLag(lag *doctorSourceLag, targetRev, headRev string) (movedTo string, err error) {
	if targetRev != headRev || !lag.Checked || lag.CommitsBehind == 0 {
		return "", nil
	}
	if lag.InstalledRevision == targetRev && lag.SourceRevision != "" && lag.SourceRevision != targetRev {
		return lag.SourceRevision, nil
	}
	return "", fmt.Errorf("post-install verification failed: installed binary is still %d commit(s) behind HEAD", lag.CommitsBehind)
}

func cloneCleanRevision(repoRoot, targetRev, dest string) error {
	if _, err := command("git", "clone", "--quiet", "--no-checkout", repoRoot, dest); err != nil {
		return fmt.Errorf("clone clean checkout: %w", err)
	}
	if _, err := commandInDir(dest, "git", "checkout", "--quiet", "--detach", targetRev); err != nil {
		return fmt.Errorf("checkout committed revision %s: %w", targetRev, err)
	}
	gotRev, err := commandInDir(dest, "git", "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if gotRev != targetRev {
		return fmt.Errorf("clean clone revision mismatch: got %s want %s", gotRev, targetRev)
	}
	dirty, err := commandInDir(dest, "git", "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return err
	}
	if dirty != "" {
		return errors.New("clean clone unexpectedly dirty")
	}
	return nil
}

func shortSHA(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

func firstStringOr(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

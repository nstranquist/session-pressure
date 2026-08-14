package sessionpressure

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nstranquist/session-pressure/internal/filelock"
)

const (
	WorkBatchSchemaVersion = 1
	WorkBatchChildMode     = "work-batch-exec"

	workBatchMaximumBytes        = 256 << 10
	workBatchMaximumSteps        = 64
	workBatchMaximumEntries      = 50_000
	workBatchMaximumInputBytes   = int64(1 << 30)
	workBatchDefaultMaxAge       = 3600
	workBatchMaximumMaxAge       = 86_400
	workBatchResultSchema        = 1
	workReuseReceiptSchema       = 1
	workReuseKeyBytes            = 32
	workBatchSingleflightRetries = 3
)

const (
	WorkBatchReuseOff        = "off"
	WorkBatchReuseSuccessful = "successful"
	WorkBatchExitStatusOnly  = "exit_status_only"
	WorkBatchExternalNone    = "none"
)

// WorkBatchManifest is a strict, argv-only contract. It deliberately has no
// shell string, inherited stdin, arbitrary environment map, or undeclared
// cache inputs.
type WorkBatchManifest struct {
	SchemaVersion int                  `json:"schema_version"`
	Class         WorkClass            `json:"class"`
	Steps         []WorkBatchStep      `json:"steps"`
	Reuse         WorkBatchReusePolicy `json:"reuse"`
}

type WorkBatchStep struct {
	ID   string   `json:"id"`
	CWD  string   `json:"cwd"`
	Argv []string `json:"argv"`
}

type WorkBatchReusePolicy struct {
	Mode            string   `json:"mode"`
	ResultContract  string   `json:"result_contract,omitempty"`
	MaxAgeSeconds   int      `json:"max_age_seconds,omitempty"`
	CoveragePaths   []string `json:"coverage_paths,omitempty"`
	EnvironmentKeys []string `json:"environment_keys,omitempty"`
	ToolExecutables []string `json:"tool_executables,omitempty"`
	ExternalState   string   `json:"external_state,omitempty"`
}

type WorkBatchOptions struct {
	File          string
	Wait          time.Duration
	Progress      WorkProgressMode
	RetentionDays int
}

type workBatchResult struct {
	SchemaVersion  int `json:"schema_version"`
	CompletedSteps int `json:"completed_steps"`
}

type workReuseReceipt struct {
	SchemaVersion   int       `json:"schema_version"`
	ReceiptID       string    `json:"receipt_id"`
	KeyDigest       string    `json:"key_digest"`
	ManifestDigest  string    `json:"manifest_digest"`
	SourceDigest    string    `json:"source_digest"`
	ToolchainDigest string    `json:"toolchain_digest"`
	CreatedAt       time.Time `json:"created_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	Class           WorkClass `json:"class"`
	StepCount       int       `json:"step_count"`
	Outcome         string    `json:"outcome"`
}

type workBatchFingerprint struct {
	KeyDigest       string
	ManifestDigest  string
	SourceDigest    string
	ToolchainDigest string
}

func ParseWorkBatchArgs(args []string) (WorkBatchOptions, error) {
	options := WorkBatchOptions{Wait: 10 * time.Minute, Progress: WorkProgressHuman, RetentionDays: 14}
	for len(args) > 0 {
		switch args[0] {
		case "--file":
			if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
				return WorkBatchOptions{}, errors.New("work batch --file requires a path or -")
			}
			options.File = args[1]
			args = args[2:]
		case "--wait":
			if len(args) < 2 {
				return WorkBatchOptions{}, errors.New("work batch --wait requires a duration or 0")
			}
			if args[1] == "0" {
				options.Wait = 0
			} else {
				duration, err := time.ParseDuration(args[1])
				if err != nil || duration <= 0 {
					return WorkBatchOptions{}, errors.New("work batch --wait must be 0 or a positive duration such as 10m")
				}
				options.Wait = duration
			}
			args = args[2:]
		case "--progress":
			if len(args) < 2 {
				return WorkBatchOptions{}, errors.New("work batch --progress requires human, jsonl, or quiet")
			}
			mode, err := ParseWorkProgressMode(args[1])
			if err != nil {
				return WorkBatchOptions{}, err
			}
			options.Progress = mode
			args = args[2:]
		default:
			return WorkBatchOptions{}, fmt.Errorf("unknown work batch argument %s", strconv.Quote(args[0]))
		}
	}
	if options.File == "" {
		return WorkBatchOptions{}, errors.New("work batch requires --file <path|->")
	}
	return options, nil
}

func ReadWorkBatchManifest(filePath string, stdin io.Reader) (WorkBatchManifest, error) {
	var reader io.Reader
	var file *os.File
	if filePath == "-" {
		if stdin == nil {
			return WorkBatchManifest{}, errors.New("work batch manifest stdin is required")
		}
		reader = stdin
	} else {
		opened, err := os.Open(filePath)
		if err != nil {
			return WorkBatchManifest{}, fmt.Errorf("open work batch manifest: %w", err)
		}
		file = opened
		reader = opened
	}
	if file != nil {
		defer file.Close()
	}
	body, err := io.ReadAll(io.LimitReader(reader, workBatchMaximumBytes+1))
	if err != nil {
		return WorkBatchManifest{}, fmt.Errorf("read work batch manifest: %w", err)
	}
	if len(body) > workBatchMaximumBytes {
		return WorkBatchManifest{}, fmt.Errorf("work batch manifest exceeds %d bytes", workBatchMaximumBytes)
	}
	return DecodeWorkBatchManifest(body)
}

func DecodeWorkBatchManifest(body []byte) (WorkBatchManifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var manifest WorkBatchManifest
	if err := decoder.Decode(&manifest); err != nil {
		return WorkBatchManifest{}, fmt.Errorf("decode work batch manifest: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return WorkBatchManifest{}, err
	}
	manifest = normalizeWorkBatchManifest(manifest)
	if err := manifest.Validate(); err != nil {
		return WorkBatchManifest{}, err
	}
	return manifest, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("work batch manifest contains multiple JSON values")
		}
		return fmt.Errorf("decode trailing work batch manifest data: %w", err)
	}
	return nil
}

func normalizeWorkBatchManifest(manifest WorkBatchManifest) WorkBatchManifest {
	manifest.Reuse.Mode = strings.ToLower(strings.TrimSpace(manifest.Reuse.Mode))
	if manifest.Reuse.Mode == "" {
		manifest.Reuse.Mode = WorkBatchReuseOff
	}
	manifest.Reuse.ResultContract = strings.ToLower(strings.TrimSpace(manifest.Reuse.ResultContract))
	manifest.Reuse.ExternalState = strings.ToLower(strings.TrimSpace(manifest.Reuse.ExternalState))
	if manifest.Reuse.Mode == WorkBatchReuseSuccessful {
		if manifest.Reuse.ResultContract == "" {
			manifest.Reuse.ResultContract = WorkBatchExitStatusOnly
		}
		if manifest.Reuse.ExternalState == "" {
			manifest.Reuse.ExternalState = WorkBatchExternalNone
		}
		if manifest.Reuse.MaxAgeSeconds == 0 {
			manifest.Reuse.MaxAgeSeconds = workBatchDefaultMaxAge
		}
	}
	for index := range manifest.Steps {
		manifest.Steps[index].ID = strings.TrimSpace(manifest.Steps[index].ID)
		cwd := strings.ReplaceAll(strings.TrimSpace(manifest.Steps[index].CWD), "\\", "/")
		if cwd == "" {
			cwd = "."
		}
		manifest.Steps[index].CWD = path.Clean(cwd)
	}
	manifest.Reuse.CoveragePaths = normalizeBatchStringSet(manifest.Reuse.CoveragePaths, true)
	manifest.Reuse.EnvironmentKeys = normalizeBatchStringSet(manifest.Reuse.EnvironmentKeys, false)
	manifest.Reuse.ToolExecutables = normalizeBatchStringSet(manifest.Reuse.ToolExecutables, false)
	return manifest
}

func normalizeBatchStringSet(values []string, cleanPath bool) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if cleanPath {
			value = path.Clean(strings.ReplaceAll(value, "\\", "/"))
		}
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func (manifest WorkBatchManifest) Validate() error {
	if manifest.SchemaVersion != WorkBatchSchemaVersion {
		return fmt.Errorf("unsupported work batch schema_version %d; want %d", manifest.SchemaVersion, WorkBatchSchemaVersion)
	}
	if _, err := ParseWorkClass(string(manifest.Class)); err != nil {
		return err
	}
	if len(manifest.Steps) < 1 || len(manifest.Steps) > workBatchMaximumSteps {
		return fmt.Errorf("work batch requires between 1 and %d steps", workBatchMaximumSteps)
	}
	seenIDs := make(map[string]struct{}, len(manifest.Steps))
	for index, step := range manifest.Steps {
		if !validBatchStepID(step.ID) {
			return fmt.Errorf("work batch step %d id must contain 1-64 letters, digits, dots, dashes, or underscores", index+1)
		}
		if _, exists := seenIDs[step.ID]; exists {
			return fmt.Errorf("work batch step id %q is duplicated", step.ID)
		}
		seenIDs[step.ID] = struct{}{}
		if err := validateBatchRelativePath(step.CWD, "step cwd"); err != nil {
			return fmt.Errorf("work batch step %q: %w", step.ID, err)
		}
		if len(step.Argv) == 0 || strings.TrimSpace(step.Argv[0]) == "" {
			return fmt.Errorf("work batch step %q requires a non-empty argv", step.ID)
		}
		if len(step.Argv) > 128 {
			return fmt.Errorf("work batch step %q argv exceeds 128 entries", step.ID)
		}
		argumentBytes := 0
		for _, argument := range step.Argv {
			argumentBytes += len(argument)
			if strings.IndexByte(argument, 0) >= 0 {
				return fmt.Errorf("work batch step %q argv contains a NUL byte", step.ID)
			}
		}
		if argumentBytes > 128<<10 {
			return fmt.Errorf("work batch step %q argv exceeds 128 KiB", step.ID)
		}
		if isResidentWorkCommand(step.Argv) {
			return fmt.Errorf("work batch step %q is resident work; use `ndev dev` so capacity is released after startup health", step.ID)
		}
		if isInlineShellWorkCommand(step.Argv) {
			return fmt.Errorf("work batch step %q cannot use an inline shell command; declare the executable and argv directly", step.ID)
		}
	}
	switch manifest.Reuse.Mode {
	case WorkBatchReuseOff:
		if manifest.Reuse.ResultContract != "" || manifest.Reuse.MaxAgeSeconds != 0 || len(manifest.Reuse.CoveragePaths) != 0 || len(manifest.Reuse.EnvironmentKeys) != 0 || len(manifest.Reuse.ToolExecutables) != 0 || manifest.Reuse.ExternalState != "" {
			return errors.New("work batch reuse mode off cannot declare reuse inputs")
		}
	case WorkBatchReuseSuccessful:
		if manifest.Reuse.ResultContract != WorkBatchExitStatusOnly {
			return errors.New("successful reuse requires result_contract exit_status_only")
		}
		if manifest.Reuse.ExternalState != WorkBatchExternalNone {
			return errors.New("successful reuse requires external_state none")
		}
		if manifest.Reuse.MaxAgeSeconds < 1 || manifest.Reuse.MaxAgeSeconds > workBatchMaximumMaxAge {
			return fmt.Errorf("successful reuse max_age_seconds must be between 1 and %d", workBatchMaximumMaxAge)
		}
		if len(manifest.Reuse.CoveragePaths) < 1 || len(manifest.Reuse.CoveragePaths) > 32 {
			return errors.New("successful reuse requires between 1 and 32 coverage_paths")
		}
		if len(manifest.Reuse.EnvironmentKeys) > 64 || len(manifest.Reuse.ToolExecutables) > 32 {
			return errors.New("successful reuse exceeds the environment_keys or tool_executables limit")
		}
		for _, coveragePath := range manifest.Reuse.CoveragePaths {
			if err := validateBatchRelativePath(coveragePath, "coverage path"); err != nil {
				return err
			}
		}
		for _, key := range manifest.Reuse.EnvironmentKeys {
			if !validBatchEnvironmentKey(key) {
				return fmt.Errorf("invalid work batch environment key %q", key)
			}
		}
		for _, tool := range manifest.Reuse.ToolExecutables {
			if strings.TrimSpace(tool) == "" || strings.IndexByte(tool, 0) >= 0 {
				return errors.New("work batch tool executable cannot be empty or contain NUL")
			}
		}
	default:
		return fmt.Errorf("unknown work batch reuse mode %q; want off or successful", manifest.Reuse.Mode)
	}
	return nil
}

func isInlineShellWorkCommand(command []string) bool {
	if len(command) < 2 {
		return false
	}
	name := strings.ToLower(filepath.Base(command[0]))
	args := command[1:]
	if name == "env" {
		for len(args) > 0 && (strings.Contains(args[0], "=") || strings.HasPrefix(args[0], "-")) {
			args = args[1:]
		}
		if len(args) < 2 {
			return false
		}
		name, args = strings.ToLower(filepath.Base(args[0])), args[1:]
	}
	switch name {
	case "sh", "bash", "zsh", "dash", "fish", "ksh":
		for _, argument := range args {
			if argument == "-c" || argument == "--command" || (strings.HasPrefix(argument, "-") && strings.Contains(strings.TrimPrefix(argument, "-"), "c")) {
				return true
			}
		}
	}
	return false
}

func validBatchStepID(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '.' || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func validBatchEnvironmentKey(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || character == '_' || (index > 0 && character >= '0' && character <= '9') {
			continue
		}
		return false
	}
	return true
}

func validateBatchRelativePath(value, label string) error {
	if value == "" || path.IsAbs(value) || filepath.VolumeName(value) != "" || value == ".." || strings.HasPrefix(value, "../") {
		return fmt.Errorf("%s %q must stay within the repository", label, value)
	}
	for _, component := range strings.Split(value, "/") {
		if component == ".git" {
			return fmt.Errorf("%s %q cannot include .git", label, value)
		}
	}
	return nil
}

// RunWorkBatch executes every step under one weighted lease. Successful reuse
// is exact only within the manifest's declared source, environment, tool, and
// external-state boundary; no stdout or stderr is replayed from a receipt.
func RunWorkBatch(coordinator *WorkCoordinator, options WorkBatchOptions, manifest WorkBatchManifest, admissionCheck func() Admission, streams WorkRunStreams) (int, error) {
	if coordinator == nil {
		return 1, errors.New("work coordinator is required")
	}
	manifest = normalizeWorkBatchManifest(manifest)
	if err := manifest.Validate(); err != nil {
		return 2, err
	}
	if streams.Stdout == nil {
		streams.Stdout = os.Stdout
	}
	if streams.Stderr == nil {
		streams.Stderr = os.Stderr
	}
	repoRoot, err := findWorkBatchRepoRoot("")
	if err != nil {
		return 1, err
	}
	if manifest.Reuse.Mode == WorkBatchReuseOff {
		return runLeasedWorkBatch(coordinator, options, manifest, admissionCheck, streams, 0)
	}

	key, err := readOrCreateWorkReuseKey(coordinator.Dir)
	if err != nil {
		return 1, err
	}
	if err := pruneWorkReuseReceipts(coordinator.Dir, time.Now().UTC()); err != nil {
		return 1, fmt.Errorf("prune work reuse receipts: %w", err)
	}
	started := time.Now()
	var singleflightWait time.Duration
	for attempt := 0; attempt < workBatchSingleflightRetries; attempt++ {
		fingerprint, fingerprintErr := fingerprintWorkBatch(repoRoot, manifest, key)
		if fingerprintErr != nil {
			return 1, fingerprintErr
		}
		lockPath := workReuseLockPath(coordinator.Dir, fingerprint.KeyDigest)
		if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
			return 1, err
		}
		lockTimeout := options.Wait
		if lockTimeout > 0 {
			remaining := lockTimeout - time.Since(started)
			if remaining <= 0 {
				return workExitWaitTimeout, errors.New("wait for work batch singleflight: deadline exceeded")
			}
			lockTimeout = remaining
		} else {
			lockTimeout = time.Millisecond
		}
		waitStarted := time.Now()
		unlock, lockErr := filelock.AcquireContext(context.Background(), lockPath, lockTimeout)
		singleflightWait += time.Since(waitStarted)
		if lockErr != nil {
			return workExitWaitTimeout, fmt.Errorf("wait for identical work batch: %w", lockErr)
		}

		lockedFingerprint, fingerprintErr := fingerprintWorkBatch(repoRoot, manifest, key)
		if fingerprintErr != nil {
			unlock()
			return 1, fingerprintErr
		}
		if lockedFingerprint.KeyDigest != fingerprint.KeyDigest {
			unlock()
			continue
		}
		fingerprint = lockedFingerprint
		receipt, hit, receiptErr := readWorkReuseReceipt(coordinator.Dir, fingerprint, manifest, time.Now().UTC())
		if receiptErr != nil {
			unlock()
			return 1, receiptErr
		}
		if hit {
			eventErr := recordReusedWorkBatch(coordinator, options, manifest, fingerprint, singleflightWait)
			unlock()
			if eventErr != nil {
				return 1, eventErr
			}
			reportWorkBatchProgress(streams.Stderr, options.Progress, "reused", manifest, fmt.Sprintf("receipt=%s age=%s", receipt.KeyDigest, time.Since(receipt.CreatedAt).Round(time.Second)))
			return 0, nil
		}

		reportWorkBatchProgress(streams.Stderr, options.Progress, "reuse_miss", manifest, "acquiring one weighted lease")
		remainingOptions := options
		if options.Wait > 0 {
			remainingOptions.Wait = options.Wait - time.Since(started)
			if remainingOptions.Wait <= 0 {
				unlock()
				return 1, errors.New("wait for work batch capacity: deadline exceeded")
			}
		}
		code, runErr := runLeasedWorkBatch(coordinator, remainingOptions, manifest, admissionCheck, streams, singleflightWait)
		if runErr != nil || code != 0 {
			unlock()
			return code, runErr
		}
		after, afterErr := fingerprintWorkBatch(repoRoot, manifest, key)
		if afterErr != nil {
			unlock()
			return 1, afterErr
		}
		if after.KeyDigest != fingerprint.KeyDigest {
			unlock()
			reportWorkBatchProgress(streams.Stderr, options.Progress, "reuse_not_published", manifest, "declared inputs changed while the batch ran")
			return 0, nil
		}
		writeErr := writeWorkReuseReceipt(coordinator.Dir, manifest, fingerprint, time.Now().UTC())
		unlock()
		if writeErr != nil {
			return 1, writeErr
		}
		return 0, nil
	}
	return 1, errors.New("work batch inputs changed repeatedly while entering singleflight")
}

func runLeasedWorkBatch(coordinator *WorkCoordinator, options WorkBatchOptions, manifest WorkBatchManifest, admissionCheck func() Admission, streams WorkRunStreams, singleflightWait time.Duration) (int, error) {
	helper, err := os.Executable()
	if err != nil {
		return 1, fmt.Errorf("resolve work batch helper: %w", err)
	}
	privateDir := filepath.Join(coordinator.Dir, "work-batches")
	if err := os.MkdirAll(privateDir, 0o700); err != nil {
		return 1, fmt.Errorf("create private work batch directory: %w", err)
	}
	identity, err := NewWorkOperationID()
	if err != nil {
		return 1, err
	}
	manifestPath := filepath.Join(privateDir, identity+".json")
	resultPath := filepath.Join(privateDir, identity+".result.json")
	body, err := json.Marshal(manifest)
	if err != nil {
		return 1, err
	}
	if err := atomicWrite(manifestPath, append(body, '\n'), 0o600); err != nil {
		return 1, fmt.Errorf("write private work batch manifest: %w", err)
	}
	defer os.Remove(manifestPath)
	defer os.Remove(resultPath)
	progress := options.Progress
	if progress == "" {
		progress = WorkProgressHuman
	}
	completedSteps := func() int {
		result, readErr := readWorkBatchResult(resultPath)
		if readErr != nil {
			return 0
		}
		return result.CompletedSteps
	}
	return RunWorkCommand(coordinator, WorkRunOptions{
		Class: manifest.Class, Wait: options.Wait, Progress: progress, RetentionDays: options.RetentionDays,
		Command:        []string{helper, WorkBatchChildMode, "--file", manifestPath, "--result", resultPath, "--progress", string(progress)},
		BatchStepCount: len(manifest.Steps), CompletedSteps: completedSteps,
		ReuseStatus:        map[bool]string{true: "miss", false: "off"}[manifest.Reuse.Mode == WorkBatchReuseSuccessful],
		SingleflightWaitMS: max(int64(0), singleflightWait.Milliseconds()),
	}, admissionCheck, WorkAdmissionRetryInterval, streams)
}

func RunWorkBatchChild(args []string, stdout, stderr io.Writer) int {
	filePath, resultPath, progress, err := parseWorkBatchChildArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, "ndev session pressure:", err)
		return 2
	}
	dataDir, err := DataDir()
	if err != nil {
		fmt.Fprintln(stderr, "ndev session pressure:", err)
		return 1
	}
	privateDir := filepath.Join(dataDir, "work-batches")
	for _, candidate := range []string{filePath, resultPath} {
		if !pathContainedBy(privateDir, candidate) {
			fmt.Fprintln(stderr, "ndev session pressure: private batch paths must stay within the pressure data directory")
			return 2
		}
	}
	manifest, err := ReadWorkBatchManifest(filePath, nil)
	_ = os.Remove(filePath)
	if err != nil {
		fmt.Fprintln(stderr, "ndev session pressure:", err)
		return 2
	}
	repoRoot, err := findWorkBatchRepoRoot("")
	if err != nil {
		fmt.Fprintln(stderr, "ndev session pressure:", err)
		return 1
	}
	return executeWorkBatch(manifest, repoRoot, resultPath, progress, stdout, stderr)
}

func parseWorkBatchChildArgs(args []string) (string, string, WorkProgressMode, error) {
	var filePath, resultPath string
	progress := WorkProgressHuman
	for len(args) > 0 {
		if len(args) < 2 {
			return "", "", "", fmt.Errorf("internal work batch argument %s requires a value", strconv.Quote(args[0]))
		}
		switch args[0] {
		case "--file":
			filePath = args[1]
		case "--result":
			resultPath = args[1]
		case "--progress":
			mode, err := ParseWorkProgressMode(args[1])
			if err != nil {
				return "", "", "", err
			}
			progress = mode
		default:
			return "", "", "", fmt.Errorf("unknown internal work batch argument %s", strconv.Quote(args[0]))
		}
		args = args[2:]
	}
	if filePath == "" || resultPath == "" {
		return "", "", "", errors.New("internal work batch requires --file and --result")
	}
	return filePath, resultPath, progress, nil
}

func executeWorkBatch(manifest WorkBatchManifest, repoRoot, resultPath string, progress WorkProgressMode, stdout, stderr io.Writer) int {
	nullInput, err := os.Open(os.DevNull)
	if err != nil {
		fmt.Fprintln(stderr, "ndev session pressure: open null batch stdin:", err)
		return 125
	}
	defer nullInput.Close()
	completed := 0
	for index, step := range manifest.Steps {
		reportWorkBatchStep(stderr, progress, step.ID, index+1, len(manifest.Steps))
		workingDir := filepath.Join(repoRoot, filepath.FromSlash(step.CWD))
		if !pathContainedBy(repoRoot, workingDir) {
			fmt.Fprintln(stderr, "ndev session pressure: batch cwd escaped repository")
			return 125
		}
		command := exec.Command(step.Argv[0], step.Argv[1:]...)
		command.Dir = workingDir
		command.Stdin = nullInput
		command.Stdout = stdout
		command.Stderr = stderr
		if err := command.Run(); err != nil {
			if exitError, ok := err.(*exec.ExitError); ok {
				return exitError.ExitCode()
			}
			fmt.Fprintf(stderr, "ndev session pressure: start batch step %s: %v\n", step.ID, err)
			return 125
		}
		completed++
		if err := writeWorkBatchResult(resultPath, completed); err != nil {
			fmt.Fprintln(stderr, "ndev session pressure: persist batch progress:", err)
			return 125
		}
	}
	return 0
}

func reportWorkBatchStep(stderr io.Writer, progress WorkProgressMode, stepID string, index, total int) {
	if progress == WorkProgressQuiet || stderr == nil {
		return
	}
	if progress == WorkProgressJSONL {
		_ = json.NewEncoder(stderr).Encode(map[string]any{"schema_version": 1, "stage": "batch_step", "step_id": stepID, "step_index": index, "step_count": total})
		return
	}
	fmt.Fprintf(stderr, "ndev pressure work: batch step %d/%d id=%s\n", index, total, stepID)
}

func reportWorkBatchProgress(stderr io.Writer, progress WorkProgressMode, stage string, manifest WorkBatchManifest, reason string) {
	if progress == WorkProgressQuiet || stderr == nil {
		return
	}
	if progress == WorkProgressJSONL {
		_ = json.NewEncoder(stderr).Encode(map[string]any{"schema_version": 1, "stage": stage, "class": manifest.Class, "batch_step_count": len(manifest.Steps), "reason": reason})
		return
	}
	fmt.Fprintf(stderr, "ndev pressure work: stage=%s class=%s steps=%d %s\n", stage, manifest.Class, len(manifest.Steps), reason)
}

func writeWorkBatchResult(resultPath string, completed int) error {
	body, err := json.Marshal(workBatchResult{SchemaVersion: workBatchResultSchema, CompletedSteps: completed})
	if err != nil {
		return err
	}
	return atomicWrite(resultPath, append(body, '\n'), 0o600)
}

func readWorkBatchResult(resultPath string) (workBatchResult, error) {
	body, err := os.ReadFile(resultPath)
	if err != nil {
		return workBatchResult{}, err
	}
	var result workBatchResult
	if err := json.Unmarshal(body, &result); err != nil {
		return workBatchResult{}, err
	}
	if result.SchemaVersion != workBatchResultSchema || result.CompletedSteps < 0 || result.CompletedSteps > workBatchMaximumSteps {
		return workBatchResult{}, errors.New("invalid private work batch result")
	}
	return result, nil
}

func findWorkBatchRepoRoot(cwd string) (string, error) {
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	command := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel")
	body, err := command.Output()
	if err != nil {
		return "", errors.New("work batch must run from inside a git repository")
	}
	root := strings.TrimSpace(string(body))
	if root == "" {
		return "", errors.New("git returned an empty work batch repository root")
	}
	return filepath.Abs(root)
}

func pathContainedBy(root, candidate string) bool {
	rootAbs, rootErr := filepath.Abs(root)
	candidateAbs, candidateErr := filepath.Abs(candidate)
	if rootErr != nil || candidateErr != nil {
		return false
	}
	relative, err := filepath.Rel(rootAbs, candidateAbs)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func readOrCreateWorkReuseKey(dir string) ([]byte, error) {
	keyPath := filepath.Join(dir, "work-reuse-hmac.key")
	if key, err := readWorkReuseKey(keyPath); err == nil {
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	unlock, err := filelock.AcquireContext(ctx, keyPath+".create", 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("lock work reuse key creation: %w", err)
	}
	defer unlock()
	if key, err := readWorkReuseKey(keyPath); err == nil {
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, workReuseKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate work reuse key: %w", err)
	}
	if err := atomicWrite(keyPath, key, 0o600); err != nil {
		return nil, fmt.Errorf("persist work reuse key: %w", err)
	}
	return key, nil
}

func readWorkReuseKey(keyPath string) ([]byte, error) {
	info, err := os.Stat(keyPath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("work reuse key must be a private regular file")
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	if len(key) != workReuseKeyBytes {
		return nil, errors.New("work reuse key has invalid length")
	}
	return key, nil
}

func fingerprintWorkBatch(repoRoot string, manifest WorkBatchManifest, key []byte) (workBatchFingerprint, error) {
	canonicalManifest, err := json.Marshal(manifest)
	if err != nil {
		return workBatchFingerprint{}, err
	}
	sourceDigest, err := digestWorkBatchCoverage(repoRoot, manifest.Reuse.CoveragePaths)
	if err != nil {
		return workBatchFingerprint{}, err
	}
	toolchainDigest, err := digestWorkBatchToolchain(repoRoot, manifest)
	if err != nil {
		return workBatchFingerprint{}, err
	}
	manifestDigest := privateWorkDigest(key, canonicalManifest)
	environment := workBatchEnvironmentMaterial(manifest.Reuse.EnvironmentKeys)
	keyMaterial := bytes.NewBuffer(nil)
	writeWorkBatchFrame(keyMaterial, []byte("ndev-work-reuse-v1"))
	writeWorkBatchFrame(keyMaterial, canonicalManifest)
	writeWorkBatchFrame(keyMaterial, []byte(sourceDigest))
	writeWorkBatchFrame(keyMaterial, []byte(toolchainDigest))
	writeWorkBatchFrame(keyMaterial, environment)
	writeWorkBatchFrame(keyMaterial, []byte(runtime.GOOS+"/"+runtime.GOARCH))
	return workBatchFingerprint{
		KeyDigest: privateWorkDigest(key, keyMaterial.Bytes()), ManifestDigest: manifestDigest,
		SourceDigest: sourceDigest, ToolchainDigest: toolchainDigest,
	}, nil
}

func privateWorkDigest(key, material []byte) string {
	digest := hmac.New(sha256.New, key)
	_, _ = digest.Write(material)
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

type workBatchCoverageEntry struct {
	Path   string
	Kind   string
	Mode   uint32
	Size   int64
	Digest string
}

func digestWorkBatchCoverage(repoRoot string, coveragePaths []string) (string, error) {
	entries := make(map[string]workBatchCoverageEntry)
	var totalBytes int64
	for _, declared := range coveragePaths {
		rootPath := filepath.Join(repoRoot, filepath.FromSlash(declared))
		if !pathContainedBy(repoRoot, rootPath) {
			return "", fmt.Errorf("work batch coverage path %q escaped repository", declared)
		}
		if _, err := os.Lstat(rootPath); err != nil {
			return "", fmt.Errorf("read work batch coverage path %q: %w", declared, err)
		}
		err := filepath.WalkDir(rootPath, func(current string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			relative, err := filepath.Rel(repoRoot, current)
			if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return errors.New("work batch coverage walk escaped repository")
			}
			if entry.Name() == ".git" {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("work batch coverage does not support symlink %q", filepath.ToSlash(relative))
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			entryPath := filepath.ToSlash(relative)
			if entryPath == "." {
				entryPath = ""
			}
			if _, exists := entries[entryPath]; exists {
				return nil
			}
			if len(entries) >= workBatchMaximumEntries {
				return fmt.Errorf("work batch coverage exceeds %d entries", workBatchMaximumEntries)
			}
			covered := workBatchCoverageEntry{Path: entryPath, Mode: uint32(info.Mode().Perm())}
			switch {
			case info.IsDir():
				covered.Kind = "dir"
			case info.Mode().IsRegular():
				covered.Kind = "file"
				covered.Size = info.Size()
				totalBytes += info.Size()
				if totalBytes > workBatchMaximumInputBytes {
					return fmt.Errorf("work batch coverage exceeds %d bytes", workBatchMaximumInputBytes)
				}
				contentDigest, err := digestStableWorkBatchFile(current, info)
				if err != nil {
					return err
				}
				covered.Digest = contentDigest
			default:
				return fmt.Errorf("work batch coverage contains unsupported special file %q", entryPath)
			}
			entries[entryPath] = covered
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("fingerprint work batch coverage %q: %w", declared, err)
		}
	}
	ordered := make([]string, 0, len(entries))
	for entryPath := range entries {
		ordered = append(ordered, entryPath)
	}
	sort.Strings(ordered)
	digest := sha256.New()
	for _, entryPath := range ordered {
		entry := entries[entryPath]
		writeWorkBatchFrame(digest, []byte(entry.Path))
		writeWorkBatchFrame(digest, []byte(entry.Kind))
		writeWorkBatchFrame(digest, []byte(fmt.Sprintf("%o", entry.Mode)))
		writeWorkBatchFrame(digest, []byte(strconv.FormatInt(entry.Size, 10)))
		writeWorkBatchFrame(digest, []byte(entry.Digest))
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

func digestStableWorkBatchFile(filePath string, before os.FileInfo) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	_, copyErr := io.Copy(digest, file)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		return "", errors.Join(copyErr, closeErr)
	}
	after, err := os.Stat(filePath)
	if err != nil {
		return "", err
	}
	if before.Size() != after.Size() || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		return "", fmt.Errorf("work batch input %q changed while fingerprinting", filePath)
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

func digestWorkBatchToolchain(repoRoot string, manifest WorkBatchManifest) (string, error) {
	type toolRequest struct {
		name string
		cwd  string
	}
	requests := make([]toolRequest, 0, len(manifest.Steps)+len(manifest.Reuse.ToolExecutables)+1)
	for _, step := range manifest.Steps {
		requests = append(requests, toolRequest{name: step.Argv[0], cwd: filepath.Join(repoRoot, filepath.FromSlash(step.CWD))})
	}
	for _, tool := range manifest.Reuse.ToolExecutables {
		requests = append(requests, toolRequest{name: tool, cwd: repoRoot})
	}
	helper, err := os.Executable()
	if err != nil {
		return "", err
	}
	requests = append(requests, toolRequest{name: helper, cwd: repoRoot})
	digests := make(map[string]string)
	for _, request := range requests {
		resolved, err := resolveWorkBatchTool(request.cwd, request.name)
		if err != nil {
			return "", err
		}
		if _, exists := digests[resolved]; exists {
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("work batch tool %q is not a regular file", request.name)
		}
		toolDigest, err := digestStableWorkBatchFile(resolved, info)
		if err != nil {
			return "", err
		}
		digests[resolved] = toolDigest
	}
	paths := make([]string, 0, len(digests))
	for resolved := range digests {
		paths = append(paths, resolved)
	}
	sort.Strings(paths)
	digest := sha256.New()
	for _, resolved := range paths {
		writeWorkBatchFrame(digest, []byte(resolved))
		writeWorkBatchFrame(digest, []byte(digests[resolved]))
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

func resolveWorkBatchTool(cwd, name string) (string, error) {
	var resolved string
	var err error
	if filepath.IsAbs(name) {
		resolved = name
	} else if strings.ContainsRune(name, filepath.Separator) || strings.Contains(name, "/") {
		resolved = filepath.Join(cwd, filepath.FromSlash(name))
	} else {
		resolved, err = exec.LookPath(name)
		if err != nil {
			return "", fmt.Errorf("resolve work batch tool %q: %w", name, err)
		}
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", fmt.Errorf("resolve work batch tool %q: %w", name, err)
	}
	return resolved, nil
}

func workBatchEnvironmentMaterial(declared []string) []byte {
	keys := append([]string{"PATH"}, declared...)
	keys = normalizeBatchStringSet(keys, false)
	buffer := bytes.NewBuffer(nil)
	for _, key := range keys {
		value, present := os.LookupEnv(key)
		writeWorkBatchFrame(buffer, []byte(key))
		writeWorkBatchFrame(buffer, []byte(strconv.FormatBool(present)))
		writeWorkBatchFrame(buffer, []byte(value))
	}
	return buffer.Bytes()
}

func writeWorkBatchFrame(writer io.Writer, value []byte) {
	_, _ = fmt.Fprintf(writer, "%d:", len(value))
	_, _ = writer.Write(value)
}

func workReusePath(dir, keyDigest string) string {
	return filepath.Join(dir, "work-reuse", strings.TrimPrefix(keyDigest, "sha256:")+".json")
}

func workReuseLockPath(dir, keyDigest string) string {
	return filepath.Join(dir, "work-reuse-locks", strings.TrimPrefix(keyDigest, "sha256:"))
}

func readWorkReuseReceipt(dir string, fingerprint workBatchFingerprint, manifest WorkBatchManifest, now time.Time) (workReuseReceipt, bool, error) {
	path := workReusePath(dir, fingerprint.KeyDigest)
	info, statErr := os.Lstat(path)
	if statErr == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0) {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return workReuseReceipt{}, false, fmt.Errorf("remove unsafe work reuse receipt: %w", removeErr)
		}
		return workReuseReceipt{}, false, nil
	}
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return workReuseReceipt{}, false, fmt.Errorf("inspect work reuse receipt: %w", statErr)
	}
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return workReuseReceipt{}, false, nil
	}
	if err != nil {
		return workReuseReceipt{}, false, fmt.Errorf("read work reuse receipt: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var receipt workReuseReceipt
	if err := decoder.Decode(&receipt); err != nil || ensureJSONEOF(decoder) != nil || !validWorkReuseReceipt(receipt) {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return workReuseReceipt{}, false, fmt.Errorf("remove invalid work reuse receipt: %w", removeErr)
		}
		return workReuseReceipt{}, false, nil
	}
	if !now.Before(receipt.ExpiresAt) || receipt.KeyDigest != fingerprint.KeyDigest || receipt.ManifestDigest != fingerprint.ManifestDigest || receipt.SourceDigest != fingerprint.SourceDigest || receipt.ToolchainDigest != fingerprint.ToolchainDigest || receipt.Class != manifest.Class || receipt.StepCount != len(manifest.Steps) {
		_ = os.Remove(path)
		return workReuseReceipt{}, false, nil
	}
	return receipt, true, nil
}

func validWorkReuseReceipt(receipt workReuseReceipt) bool {
	return receipt.SchemaVersion == workReuseReceiptSchema && validPrivateID(receipt.ReceiptID) && validSHA256Digest(receipt.KeyDigest) && validSHA256Digest(receipt.ManifestDigest) && validSHA256Digest(receipt.SourceDigest) && validSHA256Digest(receipt.ToolchainDigest) && !receipt.CreatedAt.IsZero() && receipt.ExpiresAt.After(receipt.CreatedAt) && receipt.StepCount > 0 && receipt.StepCount <= workBatchMaximumSteps && receipt.Outcome == "successful"
}

func writeWorkReuseReceipt(dir string, manifest WorkBatchManifest, fingerprint workBatchFingerprint, now time.Time) error {
	receiptID, err := NewWorkOperationID()
	if err != nil {
		return err
	}
	receipt := workReuseReceipt{
		SchemaVersion: workReuseReceiptSchema, ReceiptID: receiptID, KeyDigest: fingerprint.KeyDigest,
		ManifestDigest: fingerprint.ManifestDigest, SourceDigest: fingerprint.SourceDigest, ToolchainDigest: fingerprint.ToolchainDigest,
		CreatedAt: now, ExpiresAt: now.Add(time.Duration(manifest.Reuse.MaxAgeSeconds) * time.Second),
		Class: manifest.Class, StepCount: len(manifest.Steps), Outcome: "successful",
	}
	body, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if err := atomicWrite(workReusePath(dir, fingerprint.KeyDigest), append(body, '\n'), 0o600); err != nil {
		return fmt.Errorf("persist work reuse receipt: %w", err)
	}
	return nil
}

func pruneWorkReuseReceipts(dir string, now time.Time) error {
	cacheDir := filepath.Join(dir, "work-reuse")
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
		path := filepath.Join(cacheDir, entry.Name())
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		var receipt workReuseReceipt
		if json.Unmarshal(body, &receipt) != nil || !validWorkReuseReceipt(receipt) || !now.Before(receipt.ExpiresAt) {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func recordReusedWorkBatch(coordinator *WorkCoordinator, options WorkBatchOptions, manifest WorkBatchManifest, fingerprint workBatchFingerprint, singleflightWait time.Duration) error {
	operationID, err := NewWorkOperationID()
	if err != nil {
		return err
	}
	weight, err := coordinator.Limits.Weight(manifest.Class)
	if err != nil {
		return err
	}
	helper, err := os.Executable()
	if err != nil {
		return err
	}
	store := NewWorkEventStore(coordinator.Dir)
	retentionDays := options.RetentionDays
	if retentionDays < 1 {
		retentionDays = 14
	}
	if err := store.Prune(retentionDays); err != nil {
		return fmt.Errorf("prune work event telemetry: %w", err)
	}
	return store.AppendDurable(WorkEvent{
		Event: WorkEventReused, OperationID: operationID, Class: manifest.Class, Weight: weight,
		SessionDigest: DetectedAgentSessionDigest(os.Environ()), CommandDigest: CommandShapeDigest(helper, len(manifest.Steps)),
		Outcome: "successful_receipt_reused", SchedulingPolicy: coordinator.schedulingPolicy(), SelectorSchemaVersion: workSelectorSchemaVersion,
		BatchStepCount: len(manifest.Steps), BatchCompletedSteps: len(manifest.Steps), ReuseStatus: "hit",
		ReceiptDigest: fingerprint.KeyDigest, SingleflightWaitMS: max(int64(0), singleflightWait.Milliseconds()),
	})
}

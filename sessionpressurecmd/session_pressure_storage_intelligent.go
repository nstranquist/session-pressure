package sessionpressurecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nstranquist/session-pressure/pkg/processtree"
)

const (
	defaultSimulatorAssetRoot = "/System/Library/AssetsV2/com_apple_MobileAsset_iOSSimulatorRuntime"
	simulatorReclaimPoll      = 2 * time.Second
	sipProtectedOrphanCode    = "SIP_PROTECTED_MOBILEASSET_ORPHAN"
)

type storageSimulatorEvidence struct {
	SchemaVersion           int    `json:"schema_version"`
	RuntimeCount            int    `json:"runtime_count"`
	DeviceCount             int    `json:"device_count"`
	DeletableRuntimeBytes   int64  `json:"deletable_runtime_bytes"`
	SIPProtectedOrphanCount int    `json:"sip_protected_orphan_count"`
	SIPProtectedOrphanBytes int64  `json:"sip_protected_orphan_bytes,omitempty"`
	ResidualCode            string `json:"residual_code,omitempty"`
	MutationAuthorized      bool   `json:"mutation_authorized"`
}

var storageSimctlRuntimeJSON = simctlRuntimeListJSON
var storageSimctlDevicesJSON = simctlDevicesJSON
var storageSimulatorAssetRoot = defaultSimulatorAssetRoot
var storageBrewLookPath = lookPathNamed("brew")
var storageUVLookPath = lookPathNamed("uv")
var storageBrewAutoremoveDryFormulae = brewAutoremoveDryFormulae
var storageBrewCellarPrefix = brewCellarPrefix

func inspectIOSSimulatorRuntimeProvider(measure bool) storageProviderReport {
	report := storageProviderReport{
		ID:             "ios-simulator-runtime",
		Classification: storageProviderOperator,
		Summary:        "Apple-deletable iOS Simulator runtimes and devices via simctl; SIP MobileAssets stay report-only",
		EstimateKind:   "simctl_runtime_list",
		Overlaps:       []string{"xcode-developer-data"},
	}
	runtimes, runtimeErr := decodeSimctlRuntimes()
	devices, deviceErr := decodeSimctlDevices()
	if runtimeErr != nil {
		report.BlockedReason = runtimeErr.Error()
		return report
	}
	if deviceErr != nil {
		report.BlockedReason = deviceErr.Error()
		return report
	}
	evidence := storageSimulatorEvidence{SchemaVersion: 1, RuntimeCount: len(runtimes), DeviceCount: len(devices)}
	for _, runtime := range runtimes {
		if runtime.Deletable {
			evidence.DeletableRuntimeBytes += max(int64(0), runtime.SizeBytes)
		}
	}
	sipCount, sipBytes, sipErr := inspectSimulatorSIPAssets(measure)
	if sipErr != nil && report.BlockedReason == "" {
		report.BlockedReason = sipErr.Error()
	}
	evidence.SIPProtectedOrphanCount = sipCount
	evidence.SIPProtectedOrphanBytes = sipBytes
	if sipCount > 0 {
		evidence.ResidualCode = sipProtectedOrphanCode
	}
	report.Present = evidence.RuntimeCount > 0 || evidence.DeviceCount > 0 || evidence.SIPProtectedOrphanCount > 0
	report.EstimatedBytes = evidence.DeletableRuntimeBytes
	mutable := evidence.RuntimeCount > 0 || evidence.DeviceCount > 0
	report.MutationSupported = mutable
	evidence.MutationAuthorized = mutable
	if report.Present && !mutable && evidence.SIPProtectedOrphanCount > 0 {
		report.BlockedReason = "SIP-protected MobileAsset residual; Xcode Settings is the supported delete surface"
		report.MutationSupported = false
	}
	if mutable {
		active, activeErr := storageCacheOwnerActive("ios-simulator-runtime")
		report.ActiveOwner = active
		if activeErr != nil {
			report.BlockedReason = activeErr.Error()
			report.MutationSupported = false
		} else if active {
			report.BlockedReason = "Simulator or Xcode owns these runtimes"
			report.MutationSupported = false
		}
	}
	report.SimulatorEvidence = &evidence
	report.DecisionEvidence = &storageProviderDecisionEvidence{
		SchemaVersion:      1,
		Scope:              "simctl_runtimes_and_sip_assets",
		Available:          true,
		ErrorCode:          evidence.ResidualCode,
		ItemCount:          evidence.RuntimeCount + evidence.DeviceCount,
		IdentityRedacted:   true,
		MutationAuthorized: evidence.MutationAuthorized,
	}
	report.Paths = []string{storageSimulatorAssetRoot}
	return report
}

type simctlRuntime struct {
	Identifier string `json:"identifier"`
	Version    string `json:"version"`
	Build      string `json:"build"`
	State      string `json:"state"`
	Deletable  bool   `json:"deletable"`
	SizeBytes  int64  `json:"sizeBytes"`
}

type simctlDevice struct {
	UDID  string `json:"udid"`
	Name  string `json:"name"`
	State string `json:"state"`
}

func decodeSimctlRuntimes() ([]simctlRuntime, error) {
	raw, err := storageSimctlRuntimeJSON()
	if err != nil {
		return nil, err
	}
	var asMap map[string]simctlRuntime
	if err := json.Unmarshal(raw, &asMap); err == nil {
		out := make([]simctlRuntime, 0, len(asMap))
		for id, runtime := range asMap {
			if strings.TrimSpace(runtime.Identifier) == "" {
				runtime.Identifier = id
			}
			out = append(out, runtime)
		}
		return out, nil
	}
	var asList struct {
		Runtimes []simctlRuntime `json:"runtimes"`
	}
	if err := json.Unmarshal(raw, &asList); err != nil {
		return nil, fmt.Errorf("decode simctl runtime list: %w", err)
	}
	return asList.Runtimes, nil
}

func decodeSimctlDevices() ([]simctlDevice, error) {
	raw, err := storageSimctlDevicesJSON()
	if err != nil {
		return nil, err
	}
	var payload struct {
		Devices map[string][]simctlDevice `json:"devices"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("decode simctl device list: %w", err)
	}
	out := make([]simctlDevice, 0)
	for _, group := range payload.Devices {
		out = append(out, group...)
	}
	return out, nil
}

func inspectSimulatorSIPAssets(measure bool) (int, int64, error) {
	root := storageSimulatorAssetRoot
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, nil
	}
	count := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".asset") {
			count++
		}
	}
	if count == 0 || !measure {
		return count, 0, nil
	}
	bytes, _, err := storageDirectoryBytes(root)
	if err != nil {
		return count, 0, err
	}
	return count, bytes, nil
}

func simctlRuntimeListJSON() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := processtree.CommandContext(ctx, "xcrun", "simctl", "runtime", "list", "-j")
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("simctl runtime list: %w", err)
	}
	return output, nil
}

func simctlDevicesJSON() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := processtree.CommandContext(ctx, "xcrun", "simctl", "list", "devices", "-j")
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("simctl list devices: %w", err)
	}
	return output, nil
}

func runIOSSimulatorReclaim(ctx context.Context) error {
	steps := [][]string{
		{"xcrun", "simctl", "shutdown", "all"},
		{"xcrun", "simctl", "delete", "all"},
		{"xcrun", "simctl", "runtime", "dyld_shared_cache", "remove", "--all"},
		{"xcrun", "simctl", "runtime", "delete", "all"},
	}
	for _, args := range steps {
		command := processtree.CommandContext(ctx, args[0], args[1:]...)
		var output bytes.Buffer
		command.Stdout = &output
		command.Stderr = &output
		if err := command.Run(); err != nil {
			message := strings.TrimSpace(output.String())
			if len(message) > 512 {
				message = message[:512]
			}
			return fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, message)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	deadline := time.Now().Add(5 * time.Minute)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		runtimes, err := decodeSimctlRuntimes()
		if err != nil {
			return err
		}
		if len(runtimes) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("simctl runtime delete still lists %d images", len(runtimes))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(simulatorReclaimPoll):
		}
	}
}

func inspectXcodeDerivedDataProvider(home string, measure bool) storageProviderReport {
	path := filepath.Join(home, "Library", "Developer", "Xcode", "DerivedData")
	report := storageProviderReport{
		ID:             "xcode-derived-data",
		Classification: storageProviderOperator,
		Summary:        "Xcode DerivedData only; never DeviceSupport or signing data",
		Path:           path,
		EstimateKind:   "inventory",
		Overlaps:       []string{"xcode-developer-data"},
	}
	present, pathErr := storageRealDirectoryPresent(path)
	report.Present = present
	report.MutationSupported = present
	if pathErr != nil {
		report.BlockedReason = pathErr.Error()
		report.MutationSupported = false
		return report
	}
	if measure && present {
		var err error
		report.EstimatedBytes, _, err = storageDirectoryBytes(path)
		if err != nil {
			report.BlockedReason = err.Error()
			report.MutationSupported = false
		}
	}
	if present {
		active, activeErr := storageCacheOwnerActive("xcode-derived-data")
		report.ActiveOwner = active
		if activeErr != nil {
			report.BlockedReason = activeErr.Error()
			report.MutationSupported = false
		} else if active {
			report.BlockedReason = "Xcode or xcodebuild is active"
			report.MutationSupported = false
		}
	}
	return report
}

func inspectXcodeDeviceSupportProvider(home string, measure bool) storageProviderReport {
	path := filepath.Join(home, "Library", "Developer", "Xcode", "iOS DeviceSupport")
	report := storageProviderReport{
		ID:                "xcode-device-support",
		Classification:    storageProviderReportOnly,
		Summary:           "Physical-device symbols; report-only unless the operator names a specific device",
		Path:              path,
		EstimateKind:      "presence_only",
		MutationSupported: false,
		Overlaps:          []string{"xcode-developer-data"},
	}
	present, pathErr := storageRealDirectoryPresent(path)
	report.Present = present
	if pathErr != nil {
		report.BlockedReason = pathErr.Error()
		return report
	}
	if measure && present {
		bytes, _, err := storageDirectoryBytes(path)
		if err != nil {
			report.BlockedReason = err.Error()
			return report
		}
		report.EstimatedBytes = bytes
		report.EstimateKind = "inventory"
	}
	return report
}

func inspectUVCacheProvider(home string, measure bool) storageProviderReport {
	path := filepath.Join(home, ".cache", "uv")
	report := storageProviderReport{
		ID:             "uv-cache",
		Classification: storageProviderOperator,
		Summary:        "uv content-addressed cache prune; keeps referenced wheels",
		Path:           path,
		EstimateKind:   "inventory",
	}
	present, pathErr := storageRealDirectoryPresent(path)
	report.Present = present
	if pathErr != nil {
		report.BlockedReason = pathErr.Error()
		return report
	}
	if !present {
		return report
	}
	if _, err := storageUVLookPath(); err != nil {
		report.BlockedReason = "uv executable is unavailable"
		report.MutationSupported = false
		return report
	}
	report.MutationSupported = true
	if measure {
		var err error
		report.EstimatedBytes, _, err = storageDirectoryBytes(path)
		if err != nil {
			report.BlockedReason = err.Error()
			report.MutationSupported = false
		}
	}
	active, activeErr := storageCacheOwnerActive("uv-cache")
	report.ActiveOwner = active
	if activeErr != nil {
		report.BlockedReason = activeErr.Error()
		report.MutationSupported = false
	} else if active {
		report.BlockedReason = "a uv process is active"
		report.MutationSupported = false
	}
	return report
}

func inspectBrewAutoremoveProvider(measure bool) storageProviderReport {
	report := storageProviderReport{
		ID:             "brew-autoremove",
		Classification: storageProviderOperator,
		Summary:        "Homebrew unused formulae via brew autoremove; not a bottle-cache wipe",
		EstimateKind:   "brew_autoremove_cellar",
	}
	if _, err := storageBrewLookPath(); err != nil {
		report.BlockedReason = "brew executable is unavailable"
		return report
	}
	if !measure {
		report.Present = true
		report.MutationSupported = true
		return report
	}
	formulae, dryErr := storageBrewAutoremoveDryFormulae()
	if dryErr != nil {
		report.BlockedReason = dryErr.Error()
		return report
	}
	report.Present = len(formulae) > 0
	report.MutationSupported = report.Present
	report.Paths = formulaeCellarPaths(formulae)
	if !report.Present {
		return report
	}
	bytes, _, measureErr := measureStoragePathSet(report.Paths)
	if measureErr != nil {
		report.BlockedReason = measureErr.Error()
		report.MutationSupported = false
		return report
	}
	report.EstimatedBytes = bytes
	active, activeErr := storageCacheOwnerActive("brew-cache")
	report.ActiveOwner = active
	if activeErr != nil {
		report.BlockedReason = activeErr.Error()
		report.MutationSupported = false
	} else if active {
		report.BlockedReason = "a brew process is active"
		report.MutationSupported = false
	}
	return report
}

func formulaeCellarPaths(formulae []string) []string {
	prefix, err := storageBrewCellarPrefix()
	if err != nil || strings.TrimSpace(prefix) == "" {
		return nil
	}
	paths := make([]string, 0, len(formulae))
	for _, name := range formulae {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		paths = append(paths, filepath.Join(prefix, name))
	}
	return paths
}

func lookPathNamed(name string) func() (string, error) {
	return func() (string, error) {
		return exec.LookPath(name)
	}
}

func brewCellarPrefix() (string, error) {
	path, err := storageBrewLookPath()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := processtree.CommandContext(ctx, path, "--cellar")
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func brewAutoremoveDryFormulae() ([]string, error) {
	path, err := storageBrewLookPath()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := processtree.CommandContext(ctx, path, "autoremove", "-n")
	command.Env = append(os.Environ(), "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ANALYTICS=1")
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("brew autoremove -n: %w", err)
	}
	return parseBrewAutoremoveDry(output), nil
}

func parseBrewAutoremoveDry(output []byte) []string {
	lines := strings.Split(string(output), "\n")
	collect := false
	formulae := make([]string, 0)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "Would autoremove") {
			collect = true
			continue
		}
		if !collect {
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "==>") {
			break
		}
		if strings.Contains(trimmed, " ") {
			continue
		}
		formulae = append(formulae, trimmed)
	}
	return formulae
}

func runBrewCleanup(ctx context.Context) error {
	return runBrewCommand(ctx, []string{"cleanup", "--prune=all", "-s"}, "brew cleanup")
}

func runBrewAutoremove(ctx context.Context) error {
	return runBrewCommand(ctx, []string{"autoremove"}, "brew autoremove")
}

func runBrewCommand(ctx context.Context, args []string, label string) error {
	path, err := storageBrewLookPath()
	if err != nil {
		return errors.New("brew executable is unavailable")
	}
	command := processtree.CommandContext(ctx, path, args...)
	command.Env = append(os.Environ(), "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ANALYTICS=1")
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(output.String())
		if len(message) > 512 {
			message = message[:512]
		}
		return fmt.Errorf("%s: %w: %s", label, err, message)
	}
	return ctx.Err()
}

func runUVCachePrune(ctx context.Context) error {
	path, err := storageUVLookPath()
	if err != nil {
		return errors.New("uv executable is unavailable")
	}
	command := processtree.CommandContext(ctx, path, "cache", "prune")
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(output.String())
		if len(message) > 512 {
			message = message[:512]
		}
		return fmt.Errorf("uv cache prune: %w: %s", err, message)
	}
	return ctx.Err()
}

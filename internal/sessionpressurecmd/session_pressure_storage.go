package sessionpressurecmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nstranquist/session-pressure/third_party/pageskein/browser"
	"github.com/nstranquist/session-pressure/internal/sessionpressure"
	"github.com/nstranquist/session-pressure/pkg/processtree"
)

type storageProviderClass string

const (
	storageProviderAutoSafe   storageProviderClass = "auto_safe"
	storageProviderOperator   storageProviderClass = "operator"
	storageProviderReportOnly storageProviderClass = "report_only"
)

type storageProviderReport struct {
	ID                string                           `json:"id"`
	Classification    storageProviderClass             `json:"classification"`
	Summary           string                           `json:"summary"`
	Path              string                           `json:"path,omitempty"`
	Paths             []string                         `json:"paths,omitempty"`
	Present           bool                             `json:"present"`
	EstimatedBytes    int64                            `json:"estimated_bytes"`
	EstimateKind      string                           `json:"estimate_kind"`
	MutationSupported bool                             `json:"mutation_supported"`
	ActiveOwner       bool                             `json:"active_owner"`
	BlockedReason     string                           `json:"blocked_reason,omitempty"`
	Overlaps          []string                         `json:"overlaps,omitempty"`
	DecisionEvidence  *storageProviderDecisionEvidence `json:"decision_evidence,omitempty"`
	HotnessEvidence   *storageProviderHotnessEvidence  `json:"hotness_evidence,omitempty"`
}

// storageProviderDecisionEvidence gives an operator enough bounded evidence to
// review personal state without projecting device names, identifiers, or file
// paths. It remains report-only and never expands cleanup authority.
type storageProviderDecisionEvidence struct {
	SchemaVersion      int    `json:"schema_version"`
	Scope              string `json:"scope"`
	Available          bool   `json:"available"`
	ErrorCode          string `json:"error_code,omitempty"`
	ItemCount          int    `json:"item_count"`
	CompletedItems     int    `json:"completed_items"`
	IncompleteItems    int    `json:"incomplete_items"`
	UnknownStateItems  int    `json:"unknown_state_items"`
	OldestModifiedAt   string `json:"oldest_modified_at,omitempty"`
	NewestModifiedAt   string `json:"newest_modified_at,omitempty"`
	OldestAgeDays      int64  `json:"oldest_age_days,omitempty"`
	NewestAgeDays      int64  `json:"newest_age_days,omitempty"`
	ModifiedAtSource   string `json:"modified_at_source,omitempty"`
	IdentityRedacted   bool   `json:"identity_redacted"`
	MutationAuthorized bool   `json:"mutation_authorized"`
}

type storagePlan struct {
	Sample             sessionpressure.StorageSnapshot `json:"sample"`
	TargetFreeBytes    int64                           `json:"target_free_bytes"`
	ShortfallBytes     int64                           `json:"shortfall_bytes"`
	EstimatedBytes     int64                           `json:"estimated_reclaimable_bytes"`
	ColdEligibleBytes  int64                           `json:"cold_eligible_reclaimable_bytes,omitempty"`
	ColdMeetsShortfall bool                            `json:"cold_buckets_meet_shortfall,omitempty"`
	Providers          []storageProviderReport         `json:"providers"`
}

var storageDirectoryBytes = directoryAllocatedBytes
var storagePNPMActive = pnpmProcessActive
var storageGradleActive = gradleProcessActive
var storageCacheOwnerActive = rebuildableCacheProcessActive
var storageRunPNPMPrune = runPNPMStorePrune
var storageRunGoBuildCacheClean = runGoBuildCacheClean
var storageRemoveAll = os.RemoveAll
var storageBrowserGC = browser.GC
var storageHostAdmission = sessionpressure.ConfiguredAdmission
var storageMobileSyncEvidence = inspectMobileSyncDecisionEvidence
var storageReadMobileSyncState = readMobileSyncSnapshotState
var storageEmptyTrash = emptyUserTrash
var storageCleanUpdaterCaches = cleanAppUpdaterCaches
var storageCleanBrowserMediaCaches = cleanBrowserMediaCaches
var storageDockerAvailable = dockerCLIAvailable
var storageDockerReclaimable = dockerReclaimableBytes
var storageRunDockerPrune = runDockerSystemPrune
var storageMeasureHotness = measureStorageHotness

func cmdSessionPressureStorage(g *Flags, args []string) int {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	switch sub {
	case "help", "--help", "-h":
		fmt.Print(`Usage: ndev [--json] session pressure storage <subcommand> [flags]

Subcommands:
  status                              Cheap live filesystem-capacity sample
  providers                           Inventory typed reclaim providers
  plan [--target-free SIZE]           Dry-run provider plan (default target from policy)
  apply [--provider ID|--auto-safe] [--target-free SIZE] [--apply]
                                      Dry-run by default; literal --apply mutates
  history [--since D] [--limit N]     Read bounded private reclaim receipts
  policy enable|observe               Toggle disk-growth admission independently

Sizes accept GiB, GB, MiB, MB, KiB, KB, or raw bytes.
`)
		return 0
	case "status":
		return cmdSessionPressureStorageStatus(g, args)
	case "providers":
		return cmdSessionPressureStorageProviders(g, args)
	case "plan":
		return cmdSessionPressureStoragePlan(g, args)
	case "apply":
		return cmdSessionPressureStorageApply(g, args)
	case "history":
		return cmdSessionPressureStorageHistory(g, args)
	case "policy":
		return cmdSessionPressureStoragePolicy(g, args)
	default:
		return sessionPressureError("unknown storage subcommand "+strconv.Quote(sub), 2)
	}
}

func loadStorageState() (pressureRuntime, sessionpressure.StorageSnapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runtime, err := loadPressureRuntime(ctx)
	if err != nil {
		return pressureRuntime{}, sessionpressure.StorageSnapshot{}, err
	}
	previous := sessionpressure.LevelNormal
	if latest, ok := runtime.store.ReadLatest(); ok {
		previous = latest.Storage.Level
	}
	sample := sessionpressure.SampleStorageCapacity(sessionpressure.DefaultStorageVolumePath(), time.Now())
	sample = sessionpressure.EvaluateStorage(sample, runtime.policy.Storage, previous)
	return runtime, sample, nil
}

func cmdSessionPressureStorageStatus(g *Flags, args []string) int {
	if len(args) != 0 {
		return sessionPressureError("storage status accepts no arguments", 2)
	}
	runtime, sample, err := loadStorageState()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	payload := map[string]any{"ok": sample.Available, "action": "storage.status", "storage": sample, "storage_policy": runtime.policy.Storage, "policy_persisted": runtime.persisted}
	text := fmt.Sprintf("storage effective=%s instantaneous=%s hysteresis=%v available=%.1fGiB total=%.1fGiB path=%s enforcement=%v\n", sample.Level, sample.InstantaneousLevel, sample.HysteresisActive, bytesToGiB(sample.AvailableBytes), bytesToGiB(sample.TotalBytes), sample.VolumePath, runtime.policy.Storage.EnforceAdmission)
	exit := 0
	if !sample.Available {
		exit = 1
	}
	return emitPressure(g, payload, text, exit)
}

func cmdSessionPressureStorageProviders(g *Flags, args []string) int {
	if len(args) != 0 {
		return sessionPressureError("storage providers accepts no arguments", 2)
	}
	runtime, sample, err := loadStorageState()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	providers, err := inspectStorageProviders(runtime.policy, false)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	payload := map[string]any{"ok": true, "action": "storage.providers", "storage": sample, "providers": providers}
	if g != nil && g.JSON {
		return emitPressure(g, payload, "", 0)
	}
	var text strings.Builder
	for _, provider := range providers {
		fmt.Fprintf(&text, "%-24s %-11s %8.1fGiB present=%v mutable=%v", provider.ID, provider.Classification, bytesToGiB(provider.EstimatedBytes), provider.Present, provider.MutationSupported)
		if provider.BlockedReason != "" {
			fmt.Fprintf(&text, " blocked=%q", provider.BlockedReason)
		}
		text.WriteByte('\n')
	}
	return emitPressure(g, payload, text.String(), 0)
}

func parseStorageTarget(args []string, defaultTarget int64) (target int64, remaining []string, err error) {
	target = defaultTarget
	for index := 0; index < len(args); index++ {
		if args[index] != "--target-free" {
			remaining = append(remaining, args[index])
			continue
		}
		index++
		if index >= len(args) {
			return 0, nil, errors.New("--target-free requires a size")
		}
		target, err = parseStorageBytes(args[index])
		if err != nil {
			return 0, nil, err
		}
	}
	return target, remaining, nil
}

func cmdSessionPressureStoragePlan(g *Flags, args []string) int {
	runtime, sample, err := loadStorageState()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	target, remaining, err := parseStorageTarget(args, runtime.policy.Storage.TargetFreeBytes)
	if err != nil || len(remaining) != 0 {
		if err == nil {
			err = fmt.Errorf("unknown storage plan argument %q", remaining[0])
		}
		return sessionPressureError(err.Error(), 2)
	}
	plan, err := buildStoragePlan(runtime.policy, sample, target)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	payload := map[string]any{"ok": true, "action": "storage.plan", "plan": plan, "apply": false}
	text := fmt.Sprintf("storage plan effective=%s instantaneous=%s hysteresis=%v available=%.1fGiB target=%.1fGiB shortfall=%.1fGiB estimated=%.1fGiB cold-eligible=%.1fGiB cold-meets-shortfall=%v\n", sample.Level, sample.InstantaneousLevel, sample.HysteresisActive, bytesToGiB(sample.AvailableBytes), bytesToGiB(target), bytesToGiB(plan.ShortfallBytes), bytesToGiB(plan.EstimatedBytes), bytesToGiB(plan.ColdEligibleBytes), plan.ColdMeetsShortfall)
	for _, provider := range plan.Providers {
		if provider.HotnessEvidence == nil || !provider.HotnessEvidence.Available {
			continue
		}
		text += fmt.Sprintf("provider %s hot-fraction=%.3f cold-eligible=%.1fGiB cold-meets-shortfall=%v\n", provider.ID, provider.HotnessEvidence.HotFraction, bytesToGiB(provider.HotnessEvidence.ColdEligibleBytes), provider.HotnessEvidence.ColdMeetsShortfall)
	}
	return emitPressure(g, payload, text, 0)
}

func buildStoragePlan(policy sessionpressure.Policy, sample sessionpressure.StorageSnapshot, target int64) (storagePlan, error) {
	providers, err := inspectStorageProviders(policy, true)
	if err != nil {
		return storagePlan{}, err
	}
	shortfall := max(int64(0), target-sample.AvailableBytes)
	estimated := int64(0)
	coldEligible := int64(0)
	for index := range providers {
		provider := &providers[index]
		eligible := (provider.Classification == storageProviderAutoSafe || provider.Classification == storageProviderOperator) && provider.MutationSupported && !provider.ActiveOwner
		if eligible {
			estimated += provider.EstimatedBytes
			if provider.HotnessEvidence != nil && provider.HotnessEvidence.Available && provider.HotnessEvidence.Complete {
				coldEligible += provider.HotnessEvidence.ColdEligibleBytes
			}
		}
	}
	for index := range providers {
		provider := &providers[index]
		if evidence := provider.HotnessEvidence; evidence != nil && evidence.Available && evidence.Complete {
			eligible := (provider.Classification == storageProviderAutoSafe || provider.Classification == storageProviderOperator) && provider.MutationSupported && !provider.ActiveOwner
			evidence.ColdMeetsShortfall = eligible && coldEligible >= shortfall
		}
	}
	return storagePlan{Sample: sample, TargetFreeBytes: target, ShortfallBytes: shortfall, EstimatedBytes: estimated, ColdEligibleBytes: coldEligible, ColdMeetsShortfall: coldEligible >= shortfall, Providers: providers}, nil
}

func inspectStorageProviders(policy sessionpressure.Policy, measure bool) ([]storageProviderReport, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user home for storage providers: %w", err)
	}
	if strings.TrimSpace(home) == "" || !filepath.IsAbs(home) {
		return nil, fmt.Errorf("resolve user home for storage providers: expected absolute path, got %q", home)
	}
	profilesReport := storageProviderReport{ID: "browser-dead-profiles", Classification: storageProviderAutoSafe, Summary: "Dead PageSkein profiles older than the lifecycle floor", MutationSupported: true, Present: true, EstimateKind: "exact_gc"}
	if report, err := storageBrowserGC(browser.GCOptions{OlderThan: time.Duration(policy.Storage.ProviderMinAgeSeconds) * time.Second}); err == nil {
		profilesReport.EstimatedBytes = report.ReclaimableBytes
	} else {
		profilesReport.BlockedReason = err.Error()
		profilesReport.MutationSupported = false
	}
	pnpmPath := filepath.Join(home, "Library", "pnpm", "store")
	pnpmBytes, pnpmPresent, pnpmErr := int64(0), storagePathExists(pnpmPath), error(nil)
	if measure {
		pnpmBytes, pnpmPresent, pnpmErr = storageDirectoryBytes(pnpmPath)
	}
	pnpmActive, activeErr := storagePNPMActive()
	pnpmReport := storageProviderReport{ID: "pnpm-store", Classification: storageProviderAutoSafe, Summary: "Canonical pnpm content-addressed store prune; estimate is a conservative store-size upper bound", Path: pnpmPath, Present: pnpmPresent, EstimatedBytes: pnpmBytes, EstimateKind: "upper_bound", MutationSupported: pnpmPresent, ActiveOwner: pnpmActive}
	if pnpmErr != nil {
		pnpmReport.BlockedReason = pnpmErr.Error()
		pnpmReport.MutationSupported = false
	} else if activeErr != nil {
		pnpmReport.BlockedReason = activeErr.Error()
		pnpmReport.MutationSupported = false
	} else if pnpmActive {
		pnpmReport.BlockedReason = "a pnpm process is active"
	}
	reports := []storageProviderReport{profilesReport, pnpmReport}
	reports = append(reports,
		inspectUserTrashProvider(home, measure),
		inspectAppUpdaterCachesProvider(home, measure),
		inspectBrowserMediaCachesProvider(home, measure),
	)
	if dockerReport, include := inspectDockerReclaimProvider(measure); include {
		reports = append(reports, dockerReport)
	}
	goBuildCachePath := filepath.Join(home, "Library", "Caches", "go-build")
	goBuildCacheBytes, goBuildCachePresent, goBuildCacheErr := int64(0), storagePathExists(goBuildCachePath), error(nil)
	if measure {
		goBuildCacheBytes, goBuildCachePresent, goBuildCacheErr = storageDirectoryBytes(goBuildCachePath)
	}
	goBuildCacheReport := storageProviderReport{
		ID:                "go-build-cache",
		Classification:    storageProviderOperator,
		Summary:           "Explicit operator reset of the rebuildable Go build cache; excluded from --auto-safe",
		Path:              goBuildCachePath,
		Present:           goBuildCachePresent,
		EstimatedBytes:    goBuildCacheBytes,
		EstimateKind:      "inventory",
		MutationSupported: goBuildCachePresent,
		Overlaps:          []string{"library-caches"},
	}
	goBuildCacheActive, goBuildCacheActiveErr := false, error(nil)
	if goBuildCachePresent {
		goBuildCacheActive, goBuildCacheActiveErr = storageCacheOwnerActive("go-build-cache")
	}
	goBuildCacheReport.ActiveOwner = goBuildCacheActive
	if goBuildCacheErr != nil {
		goBuildCacheReport.BlockedReason = goBuildCacheErr.Error()
		goBuildCacheReport.MutationSupported = false
	} else if goBuildCacheActiveErr != nil {
		goBuildCacheReport.BlockedReason = goBuildCacheActiveErr.Error()
		goBuildCacheReport.MutationSupported = false
	} else if goBuildCacheActive {
		goBuildCacheReport.BlockedReason = "an active Go build owns this cache"
		goBuildCacheReport.MutationSupported = false
	}
	if measure && goBuildCachePresent && goBuildCacheErr == nil {
		evidence, evidenceErr := storageMeasureHotness(goBuildCachePath, time.Now().UTC())
		if evidence != nil {
			goBuildCacheReport.HotnessEvidence = evidence
		}
		if evidenceErr != nil && goBuildCacheReport.BlockedReason == "" {
			goBuildCacheReport.BlockedReason = "hotness evidence unavailable: " + evidenceErr.Error()
		}
	}
	if measure && goBuildCacheReport.HotnessEvidence != nil {
		goBuildCacheReport.EstimateKind = "measured_age_buckets"
	}
	reports = append(reports, goBuildCacheReport)
	// Rebuildable developer caches: operator-classified like
	// go-build-cache (explicit `--provider` apply, excluded from --auto-safe)
	// so `storage plan`/`providers` is no longer blind to where dev-cache
	// space actually accumulates (2026-07-20 storage-red: plan estimated 0
	// while these caches held gigabytes).
	for _, definition := range []struct {
		id, summary, path string
		overlaps          []string
	}{
		{"yarn-cache", "Rebuildable Yarn download cache; explicit operator reset", filepath.Join(home, "Library", "Caches", "Yarn"), []string{"library-caches"}},
		{"npm-cache", "Rebuildable npm content-addressed cache (_cacache); explicit operator reset", filepath.Join(home, ".npm", "_cacache"), nil},
		{"brew-cache", "Rebuildable Homebrew download cache; explicit operator reset", filepath.Join(home, "Library", "Caches", "Homebrew"), []string{"library-caches"}},
		{"swiftpm-cache", "Rebuildable SwiftPM repository cache; explicit operator reset with active-owner revalidation", filepath.Join(home, "Library", "Caches", "org.swift.swiftpm", "repositories"), []string{"library-caches"}},
		{"playwright-cache", "Rebuildable Playwright browser runtime cache; explicit operator reset with active-owner revalidation", filepath.Join(home, "Library", "Caches", "ms-playwright"), []string{"library-caches"}},
	} {
		size, present, err := int64(0), storagePathExists(definition.path), error(nil)
		if measure {
			size, present, err = storageDirectoryBytes(definition.path)
		}
		report := storageProviderReport{
			ID:                definition.id,
			Classification:    storageProviderOperator,
			Summary:           definition.summary,
			Path:              definition.path,
			Present:           present,
			EstimatedBytes:    size,
			EstimateKind:      "inventory",
			MutationSupported: present,
			Overlaps:          definition.overlaps,
		}
		active, activeErr := false, error(nil)
		if present {
			active, activeErr = storageCacheOwnerActive(definition.id)
		}
		report.ActiveOwner = active
		if err != nil {
			report.BlockedReason = err.Error()
			report.MutationSupported = false
		} else if activeErr != nil {
			report.BlockedReason = activeErr.Error()
			report.MutationSupported = false
		} else if active {
			report.BlockedReason = "an active process owns this cache"
			report.MutationSupported = false
		}
		reports = append(reports, report)
	}
	gradlePath := filepath.Join(home, ".gradle", "caches")
	gradleBytes, gradlePresent, gradleErr := int64(0), storagePathExists(gradlePath), error(nil)
	if measure {
		gradleBytes, gradlePresent, gradleErr = storageDirectoryBytes(gradlePath)
	}
	gradleActive, gradleActiveErr := storageGradleActive()
	gradleReport := storageProviderReport{
		ID:                "gradle-caches",
		Classification:    storageProviderOperator,
		Summary:           "Rebuildable Gradle dependency and build caches; explicit operator reset with active-owner revalidation",
		Path:              gradlePath,
		Present:           gradlePresent,
		EstimatedBytes:    gradleBytes,
		EstimateKind:      "inventory",
		MutationSupported: gradlePresent,
		ActiveOwner:       gradleActive,
	}
	if gradleErr != nil {
		gradleReport.BlockedReason = gradleErr.Error()
		gradleReport.MutationSupported = false
	} else if gradleActiveErr != nil {
		gradleReport.BlockedReason = gradleActiveErr.Error()
		gradleReport.MutationSupported = false
	} else if gradleActive {
		gradleReport.BlockedReason = "a Gradle process is active"
		gradleReport.MutationSupported = false
	}
	reports = append(reports, gradleReport)
	for _, definition := range []struct {
		id, summary, path string
		overlaps          []string
	}{
		{"go-module-cache", "Useful Go module download cache preserved by operator preference", filepath.Join(home, "go", "pkg", "mod", "cache"), nil},
		{"library-caches", "Aggregate application caches; inspect by owner", filepath.Join(home, "Library", "Caches"), []string{"go-build-cache", "swiftpm-cache", "playwright-cache"}},
		{"downloads", "Operator-owned downloads", filepath.Join(home, "Downloads"), nil},
		{"mobile-sync", "Personal iPhone/iPad backups", filepath.Join(home, "Library", "Application Support", "MobileSync"), nil},
	} {
		size, present, err := int64(0), storagePathExists(definition.path), error(nil)
		report := storageProviderReport{ID: definition.id, Classification: storageProviderReportOnly, Summary: definition.summary, Path: definition.path, Present: present, EstimatedBytes: size, EstimateKind: "presence_only", Overlaps: definition.overlaps}
		if err != nil {
			report.BlockedReason = err.Error()
		}
		if definition.id == "mobile-sync" && present {
			evidence := storageMobileSyncEvidence(definition.path, time.Now().UTC())
			report.DecisionEvidence = &evidence
		}
		reports = append(reports, report)
	}
	return reports, nil
}

func inspectMobileSyncDecisionEvidence(path string, now time.Time) storageProviderDecisionEvidence {
	evidence := storageProviderDecisionEvidence{
		SchemaVersion: 1, Scope: "direct_backup_roots", IdentityRedacted: true,
		MutationAuthorized: false, ModifiedAtSource: "status_plist_or_backup_root",
	}
	entries, err := os.ReadDir(filepath.Join(path, "Backup"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			evidence.Available = true
			return evidence
		}
		evidence.ErrorCode = "inventory_unreadable"
		return evidence
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	oldest, newest := time.Time{}, time.Time{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		evidence.ItemCount++
		backupPath := filepath.Join(path, "Backup", entry.Name())
		statusPath := filepath.Join(backupPath, "Status.plist")
		info, statErr := os.Stat(statusPath)
		if statErr != nil {
			info, statErr = os.Stat(backupPath)
		}
		if statErr == nil {
			modified := info.ModTime().UTC()
			if oldest.IsZero() || modified.Before(oldest) {
				oldest = modified
			}
			if newest.IsZero() || modified.After(newest) {
				newest = modified
			}
		}
		state, stateErr := storageReadMobileSyncState(statusPath)
		if stateErr != nil {
			evidence.UnknownStateItems++
			continue
		}
		switch strings.ToLower(strings.TrimSpace(state)) {
		case "finished":
			evidence.CompletedItems++
		case "":
			evidence.UnknownStateItems++
		default:
			evidence.IncompleteItems++
		}
	}
	evidence.Available = true
	if !oldest.IsZero() {
		evidence.OldestModifiedAt = oldest.Format(time.RFC3339)
		evidence.OldestAgeDays = max(0, int64(now.Sub(oldest)/(24*time.Hour)))
	}
	if !newest.IsZero() {
		evidence.NewestModifiedAt = newest.Format(time.RFC3339)
		evidence.NewestAgeDays = max(0, int64(now.Sub(newest)/(24*time.Hour)))
	}
	return evidence
}

func readMobileSyncSnapshotState(path string) (string, error) {
	output, err := exec.Command("/usr/bin/plutil", "-extract", "SnapshotState", "raw", "-o", "-", path).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func cmdSessionPressureStorageApply(g *Flags, args []string) int {
	runtime, sample, err := loadStorageState()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	target, remaining, err := parseStorageTarget(args, runtime.policy.Storage.TargetFreeBytes)
	if err != nil {
		return sessionPressureError(err.Error(), 2)
	}
	apply := false
	autoSafe := false
	providerID := ""
	for index := 0; index < len(remaining); index++ {
		switch remaining[index] {
		case "--apply":
			apply = true
		case "--auto-safe":
			autoSafe = true
		case "--provider":
			index++
			if index >= len(remaining) {
				return sessionPressureError("--provider requires an ID", 2)
			}
			providerID = remaining[index]
		default:
			return sessionPressureError("unknown storage apply argument "+strconv.Quote(remaining[index]), 2)
		}
	}
	if autoSafe == (providerID != "") {
		return sessionPressureError("storage apply requires exactly one of --auto-safe or --provider ID", 2)
	}
	// Actual reclaim must not repeat the expensive report-only attribution walk.
	// The explicit plan/dry-run path owns size inventory; execution needs only
	// constant-cost provider presence/ownership checks and revalidates the
	// selected provider immediately before mutation.
	plan := storagePlan{
		Sample:          sample,
		TargetFreeBytes: target,
		ShortfallBytes:  max(int64(0), target-sample.AvailableBytes),
	}
	plan.Providers, err = inspectStorageProviders(runtime.policy, false)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	if !apply {
		plan, err = buildStoragePlan(runtime.policy, sample, target)
		if err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		return emitPressure(g, map[string]any{"ok": true, "action": "storage.apply", "apply": false, "plan": plan, "selected_provider": providerID, "auto_safe": autoSafe}, fmt.Sprintf("dry-run only: available=%.1fGiB target=%.1fGiB; pass --apply to mutate\n", bytesToGiB(sample.AvailableBytes), bytesToGiB(target)), 0)
	}
	if !runtime.persisted {
		return sessionPressureError("storage apply requires an initialized pressure policy", 1)
	}
	if autoSafe && !runtime.policy.Storage.EnforceAdmission {
		return sessionPressureError("--auto-safe requires explicit storage policy enable; named provider apply remains available for operator-controlled recovery", 1)
	}
	if providerID != "" {
		provider, found := storageProviderByID(plan.Providers, providerID)
		if !found {
			return sessionPressureError("unknown storage provider "+strconv.Quote(providerID), 2)
		}
		if provider.Classification == storageProviderReportOnly || !provider.MutationSupported {
			reason := provider.BlockedReason
			if reason == "" {
				reason = "classification is " + string(provider.Classification)
			}
			return sessionPressureError(fmt.Sprintf("provider %s is not mutable: %s", providerID, reason), 1)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	unlock, err := sessionpressure.AcquireStorageReclaimLock(ctx, runtime.dir, 5*time.Second)
	if err != nil {
		return sessionPressureError("acquire exclusive storage reclaim lock: "+err.Error(), 1)
	}
	defer unlock()
	coordinator := sessionpressure.NewWorkCoordinator(runtime.dir, runtime.policy.WorkLimits)
	lease, _, err := coordinator.WaitAcquire(ctx, sessionpressure.WorkClassReclaim)
	if err != nil {
		return sessionPressureError("acquire reclaim work capacity: "+err.Error(), 1)
	}
	defer func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()
		_ = lease.Release(releaseCtx)
	}()
	selected := []string{providerID}
	if autoSafe {
		selected = []string{"browser-dead-profiles", "pnpm-store"}
	}
	receipts := make([]sessionpressure.StorageReceipt, 0, len(selected)*2)
	skipped := make([]map[string]any, 0, len(selected))
	for _, id := range selected {
		current := sessionpressure.SampleStorageCapacity(sessionpressure.DefaultStorageVolumePath(), time.Now())
		if current.Available && current.AvailableBytes >= target {
			break
		}
		provider, found := storageProviderByID(plan.Providers, id)
		if !found {
			return sessionPressureError("unknown storage provider "+strconv.Quote(id), 2)
		}
		if provider.Classification == storageProviderReportOnly || !provider.MutationSupported {
			return sessionPressureError(fmt.Sprintf("provider %s is not mutable: %s", id, provider.BlockedReason), 1)
		}
		if autoSafe && provider.Classification != storageProviderAutoSafe {
			return sessionPressureError("provider "+id+" is not auto_safe", 1)
		}
		recent, historyErr := sessionpressure.NewStorageReceiptStore(runtime.dir).Read(time.Now().Add(-time.Duration(runtime.policy.Storage.ReclaimCooldownSeconds)*time.Second), 1000)
		if historyErr != nil {
			return sessionPressureError("refusing reclaim because cooldown history is unreadable: "+historyErr.Error(), 1)
		}
		if storageProviderInsideCooldown(recent, id) {
			reason := fmt.Sprintf("provider %s is inside its %s reclaim cooldown", id, time.Duration(runtime.policy.Storage.ReclaimCooldownSeconds)*time.Second)
			if autoSafe {
				skipped = append(skipped, map[string]any{"provider_id": id, "reason": reason})
				continue
			}
			return sessionPressureError(reason, 1)
		}
		if id == "go-build-cache" {
			if err := requireGoBuildCacheHotness(&provider); err != nil {
				return sessionPressureError("go-build-cache apply requires current complete hotness evidence: "+err.Error(), 1)
			}
		}
		providerReceipts, runErr := executeStorageProvider(ctx, runtime, provider)
		receipts = append(receipts, providerReceipts...)
		if runErr != nil {
			return emitPressure(g, map[string]any{"ok": false, "action": "storage.apply", "apply": true, "receipts": receipts, "error": runErr.Error()}, "storage reclaim failed: "+runErr.Error()+"\n", 1)
		}
	}
	after := sessionpressure.SampleStorageCapacity(sessionpressure.DefaultStorageVolumePath(), time.Now())
	after = sessionpressure.EvaluateStorage(after, runtime.policy.Storage, sample.Level)
	payload := map[string]any{"ok": true, "action": "storage.apply", "apply": true, "target_free_bytes": target, "storage": after, "receipts": receipts, "skipped_providers": skipped}
	return emitPressure(g, payload, fmt.Sprintf("storage reclaim complete: available=%.1fGiB target=%.1fGiB receipts=%d\n", bytesToGiB(after.AvailableBytes), bytesToGiB(target), len(receipts)), 0)
}

func storageProviderInsideCooldown(receipts []sessionpressure.StorageReceipt, providerID string) bool {
	for _, receipt := range receipts {
		if receipt.ProviderID == providerID && receipt.Mode == "result" && receipt.Outcome == "completed" {
			return true
		}
	}
	return false
}

func requireGoBuildCacheHotness(provider *storageProviderReport) error {
	if provider == nil || provider.ID != "go-build-cache" {
		return nil
	}
	if strings.TrimSpace(provider.Path) == "" {
		return errors.New("the provider path is unavailable")
	}
	evidence, err := storageMeasureHotness(provider.Path, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("measure hotness: %w", err)
	}
	if evidence == nil || !evidence.Available || !evidence.Complete {
		return errors.New("the bounded age-bucket measurement is unavailable or incomplete")
	}
	provider.HotnessEvidence = evidence
	provider.EstimateKind = "measured_age_buckets"
	return nil
}

func executeStorageProvider(ctx context.Context, runtime pressureRuntime, provider storageProviderReport) ([]sessionpressure.StorageReceipt, error) {
	store := sessionpressure.NewStorageReceiptStore(runtime.dir)
	attemptID, err := sessionpressure.NewWorkOperationID()
	if err != nil {
		return nil, err
	}
	started := time.Now().UTC()
	beforeStorage := sessionpressure.SampleStorageCapacity(sessionpressure.DefaultStorageVolumePath(), started)
	beforeProvider := int64(0)
	providerBytesMeasured := false
	if provider.ID == "browser-dead-profiles" {
		beforeProvider = provider.EstimatedBytes
		providerBytesMeasured = true
	}
	intent := sessionpressure.StorageReceipt{SchemaVersion: sessionpressure.StorageReceiptSchemaVersion, AttemptID: attemptID, ProviderID: provider.ID, Mode: "intent", StartedAt: started, FinishedAt: started, BeforeAvailableBytes: beforeStorage.AvailableBytes, AfterAvailableBytes: beforeStorage.AvailableBytes, BeforeProviderBytes: beforeProvider, AfterProviderBytes: beforeProvider, ProviderBytesMeasured: providerBytesMeasured, Outcome: "intent_persisted"}
	if err := store.Append(intent); err != nil {
		return nil, fmt.Errorf("persist reclaim intent: %w", err)
	}
	receipts := []sessionpressure.StorageReceipt{intent}
	runErr := error(nil)
	switch provider.ID {
	case "browser-dead-profiles":
		_, runErr = storageBrowserGC(browser.GCOptions{OlderThan: time.Duration(runtime.policy.Storage.ProviderMinAgeSeconds) * time.Second, Apply: true})
	case "user-trash":
		runErr = storageEmptyTrash(ctx, provider.Path)
	case "app-updater-caches":
		active, activeErr := storageCacheOwnerActive("app-updater-caches")
		if activeErr != nil {
			runErr = activeErr
		} else if active {
			runErr = errors.New("updater provider refused because an updater process is active")
		} else {
			runErr = storageCleanUpdaterCaches(ctx, provider.Path)
		}
	case "browser-media-caches":
		active, activeErr := storageCacheOwnerActive("browser-media-caches")
		if activeErr != nil {
			runErr = activeErr
		} else if active {
			runErr = errors.New("browser media provider refused because a Spotify, Arc, or Brave process is active")
		} else {
			paths := provider.Paths
			if len(paths) == 0 {
				if home, homeErr := os.UserHomeDir(); homeErr != nil {
					runErr = homeErr
				} else {
					paths = browserMediaCachePaths(home)
				}
			}
			if runErr == nil {
				runErr = storageCleanBrowserMediaCaches(ctx, paths)
			}
		}
	case "docker-reclaim":
		runErr = storageRunDockerPrune(ctx)
	case "pnpm-store":
		active, activeErr := storagePNPMActive()
		if activeErr != nil {
			runErr = activeErr
		} else if active {
			runErr = errors.New("pnpm provider refused because a pnpm process is active")
		} else {
			admission := storageHostAdmission(ctx)
			if !admission.Allowed {
				runErr = fmt.Errorf("pnpm provider deferred by host pressure: %s", sessionpressure.AdmissionReason(admission))
			} else {
				runErr = storageRunPNPMPrune(ctx)
			}
		}
	case "gradle-caches":
		active, activeErr := storageGradleActive()
		if activeErr != nil {
			runErr = activeErr
		} else if active {
			runErr = errors.New("Gradle provider refused because a Gradle process is active")
		} else {
			runErr = storageRunGoBuildCacheClean(ctx, provider.Path)
		}
	case "go-build-cache", "yarn-cache", "npm-cache", "brew-cache", "swiftpm-cache", "playwright-cache":
		active, activeErr := storageCacheOwnerActive(provider.ID)
		if activeErr != nil {
			runErr = activeErr
		} else if active {
			runErr = fmt.Errorf("%s provider refused because an owning process is active", provider.ID)
		} else {
			// These are rebuildable cache directories; the quarantine-rename
			// clean is content-agnostic.
			runErr = storageRunGoBuildCacheClean(ctx, provider.Path)
		}
	default:
		runErr = fmt.Errorf("provider %s has no executor", provider.ID)
	}
	finished := time.Now().UTC()
	afterStorage := sessionpressure.SampleStorageCapacity(sessionpressure.DefaultStorageVolumePath(), finished)
	afterProvider := int64(0)
	if provider.ID == "browser-dead-profiles" {
		if report, gcErr := storageBrowserGC(browser.GCOptions{OlderThan: time.Duration(runtime.policy.Storage.ProviderMinAgeSeconds) * time.Second}); gcErr == nil {
			afterProvider = report.ReclaimableBytes
		} else if runErr == nil {
			runErr = gcErr
		}
	}
	result := sessionpressure.StorageReceipt{SchemaVersion: sessionpressure.StorageReceiptSchemaVersion, AttemptID: attemptID, ProviderID: provider.ID, Mode: "result", StartedAt: started, FinishedAt: finished, BeforeAvailableBytes: beforeStorage.AvailableBytes, AfterAvailableBytes: afterStorage.AvailableBytes, BeforeProviderBytes: beforeProvider, AfterProviderBytes: afterProvider, ProviderBytesMeasured: providerBytesMeasured, ReclaimedBytes: max(int64(0), afterStorage.AvailableBytes-beforeStorage.AvailableBytes), Outcome: "completed"}
	if runErr != nil {
		result.Outcome = "failed"
		result.Error = runErr.Error()
	}
	if err := store.Append(result); err != nil {
		return receipts, errors.Join(runErr, fmt.Errorf("persist reclaim result: %w", err))
	}
	receipts = append(receipts, result)
	return receipts, runErr
}

func runGoBuildCacheClean(ctx context.Context, path string) error {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || !filepath.IsAbs(path) || path == string(filepath.Separator) {
		return fmt.Errorf("go build cache path must be a non-root absolute path: %q", path)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect Go build cache: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("Go build cache is not a real directory: %s", path)
	}
	quarantine := fmt.Sprintf("%s.reclaim-%d-%d", path, os.Getpid(), time.Now().UnixNano())
	if err := os.Rename(path, quarantine); err != nil {
		return fmt.Errorf("quarantine Go build cache: %w", err)
	}
	if err := os.MkdirAll(path, info.Mode().Perm()); err != nil {
		rollbackErr := os.Rename(quarantine, path)
		return errors.Join(fmt.Errorf("recreate Go build cache: %w", err), rollbackErr)
	}
	if err := storageRemoveAll(quarantine); err != nil {
		if rollbackErr := restoreQuarantinedCache(path, quarantine); rollbackErr != nil {
			return errors.Join(fmt.Errorf("remove quarantined Go build cache: %w", err), rollbackErr)
		}
		return fmt.Errorf("remove quarantined Go build cache: %w; cache restored", err)
	}
	return ctx.Err()
}

func restoreQuarantinedCache(path, quarantine string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("inspect replacement Go build cache for rollback: %w; quarantine preserved at %s", err, quarantine)
	}
	if len(entries) != 0 {
		return fmt.Errorf("replacement Go build cache is not empty; quarantine preserved at %s", quarantine)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove replacement Go build cache for rollback: %w; quarantine preserved at %s", err, quarantine)
	}
	if err := os.Rename(quarantine, path); err != nil {
		return fmt.Errorf("restore quarantined Go build cache: %w; quarantine remains at %s", err, quarantine)
	}
	return nil
}

func cmdSessionPressureStorageHistory(g *Flags, args []string) int {
	since := time.Now().Add(-14 * 24 * time.Hour)
	limit := 100
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--since":
			index++
			if index >= len(args) {
				return sessionPressureError("storage history --since requires a duration", 2)
			}
			duration, err := time.ParseDuration(args[index])
			if err != nil || duration <= 0 {
				return sessionPressureError("storage history --since must be a positive duration", 2)
			}
			since = time.Now().Add(-duration)
		case "--limit":
			index++
			if index >= len(args) {
				return sessionPressureError("storage history --limit requires an integer", 2)
			}
			value, err := strconv.Atoi(args[index])
			if err != nil || value < 1 || value > 10000 {
				return sessionPressureError("storage history --limit must be between 1 and 10000", 2)
			}
			limit = value
		default:
			return sessionPressureError("unknown storage history argument "+strconv.Quote(args[index]), 2)
		}
	}
	dir, err := sessionpressure.DataDir()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	receipts, err := sessionpressure.NewStorageReceiptStore(dir).Read(since, limit)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	payload := map[string]any{"ok": true, "action": "storage.history", "receipts": receipts, "count": len(receipts), "since": since.UTC()}
	if g != nil && g.JSON {
		return emitPressure(g, payload, "", 0)
	}
	var text strings.Builder
	for _, receipt := range receipts {
		fmt.Fprintf(&text, "%s %-24s %-8s %-18s reclaimed=%.1fGiB error=%q\n", receipt.FinishedAt.Local().Format(time.RFC3339), receipt.ProviderID, receipt.Mode, receipt.Outcome, bytesToGiB(receipt.ReclaimedBytes), receipt.Error)
	}
	return emitPressure(g, payload, text.String(), 0)
}

func cmdSessionPressureStoragePolicy(g *Flags, args []string) int {
	if len(args) != 1 || (args[0] != "enable" && args[0] != "observe") {
		return sessionPressureError("storage policy requires enable or observe", 2)
	}
	dir, err := sessionpressure.DataDir()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	mutation, err := beginPressurePolicyMutation(dir)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	defer mutation.Close()
	runtime := mutation.Runtime
	if !runtime.persisted {
		return sessionPressureError("storage policy requires initialization via session pressure policy init", 1)
	}
	runtime.policy.Storage.Enabled = true
	runtime.policy.Storage.EnforceAdmission = args[0] == "enable"
	if err := sessionpressure.SavePolicy(runtime.path, runtime.policy); err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	if _, err := ensurePressureMonitor(runtime.dir); err != nil {
		runtime.policy.Storage.EnforceAdmission = false
		fallbackErr := sessionpressure.SavePolicy(runtime.path, runtime.policy)
		if fallbackErr == nil {
			fallbackErr = restartPressureMonitor(runtime.dir)
		}
		return sessionPressureError(fmt.Sprintf("storage policy activation failed: %v; observe-only fallback: %v", err, fallbackErr), 1)
	}
	payload := map[string]any{"ok": true, "action": "storage.policy." + args[0], "storage_policy": runtime.policy.Storage, "path": runtime.path}
	return emitPressure(g, payload, fmt.Sprintf("storage admission enforcement=%v\n", runtime.policy.Storage.EnforceAdmission), 0)
}

func storageProviderByID(providers []storageProviderReport, id string) (storageProviderReport, bool) {
	for _, provider := range providers {
		if provider.ID == id {
			return provider, true
		}
	}
	return storageProviderReport{}, false
}

func directoryAllocatedBytes(path string) (int64, bool, error) {
	if strings.TrimSpace(path) == "" {
		return 0, false, nil
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, err
	}
	// Measurement is foreground-only and bounded. A red host must not spend
	// minutes recursively attributing every report-only tree before an operator
	// can see the typed plan.
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := processtree.CommandContext(ctx, "/usr/bin/du", "-sk", path)
	output, err := command.Output()
	if err != nil {
		return 0, true, fmt.Errorf("measure %s: %w", path, err)
	}
	fields := bytes.Fields(output)
	if len(fields) == 0 {
		return 0, true, fmt.Errorf("measure %s: empty du output", path)
	}
	kib, err := strconv.ParseInt(string(fields[0]), 10, 64)
	if err != nil || kib < 0 {
		return 0, true, fmt.Errorf("measure %s: invalid du size", path)
	}
	return kib * 1024, true, nil
}

func storagePathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func pnpmProcessActive() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	command := processtree.CommandContext(ctx, "/usr/bin/pgrep", "-f", `(^|/)(pnpm|pnpx)( |$)`)
	err := command.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("inspect active pnpm owners: %w", err)
}

func gradleProcessActive() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	command := processtree.CommandContext(ctx, "/usr/bin/pgrep", "-f", `(^|/)(gradle|gradlew)( |$)|org\.gradle\.`)
	err := command.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("inspect active Gradle owners: %w", err)
}

func rebuildableCacheProcessActive(providerID string) (bool, error) {
	pattern := ""
	switch providerID {
	case "go-build-cache":
		pattern = `(^|/)go( |$).*(build|clean|generate|install|list|test|tool|vet)( |$)|(^|/)(asm|compile|link)( |$)`
	case "yarn-cache":
		pattern = `(^|/)(yarn|yarnpkg)( |$)`
	case "npm-cache":
		pattern = `(^|/)(npm|npx)( |$)`
	case "brew-cache":
		pattern = `(^|/)brew( |$)`
	case "swiftpm-cache":
		pattern = `(^|/)(swift-build|swift-package|xcodebuild)( |$)|(^|/)swift( |$).*(build|package|resolve|test)( |$)`
	case "playwright-cache":
		pattern = `ms-playwright|(^|/)(playwright)( |$)`
	case "app-updater-caches":
		pattern = `(^|/)[^/]*[Uu]pdater( |$)|(^|/)(ShipIt|shipit)( |$)`
	case "browser-media-caches":
		pattern = `(^|/)(Spotify|Arc|Brave|BraveSoftware)( |$)`
	default:
		return false, fmt.Errorf("inspect active owner for unknown cache provider %q", providerID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	command := processtree.CommandContext(ctx, "/usr/bin/pgrep", "-f", pattern)
	err := command.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("inspect active owner for %s: %w", providerID, err)
}

func runPNPMStorePrune(ctx context.Context) error {
	path, err := exec.LookPath("pnpm")
	if err != nil {
		return errors.New("pnpm executable is unavailable")
	}
	command := processtree.CommandContext(ctx, path, "store", "prune")
	command.Stdin = nil
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(output.String())
		if len(message) > 512 {
			message = message[:512]
		}
		return fmt.Errorf("pnpm store prune: %w: %s", err, message)
	}
	return nil
}

func parseStorageBytes(raw string) (int64, error) {
	value := strings.TrimSpace(strings.ToUpper(raw))
	units := []struct {
		suffix string
		factor float64
	}{
		{"GIB", 1 << 30}, {"GB", 1e9}, {"MIB", 1 << 20}, {"MB", 1e6}, {"KIB", 1 << 10}, {"KB", 1e3}, {"B", 1},
	}
	factor := float64(1)
	hadUnit := false
	for _, unit := range units {
		if strings.HasSuffix(value, unit.suffix) {
			value = strings.TrimSpace(strings.TrimSuffix(value, unit.suffix))
			factor = unit.factor
			hadUnit = true
			break
		}
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || number < 0 || (number == 0 && !hadUnit) || number*factor > float64(^uint64(0)>>1) {
		return 0, fmt.Errorf("invalid storage size %q", raw)
	}
	return int64(number * factor), nil
}

func bytesToGiB(value int64) float64 { return float64(value) / (1 << 30) }

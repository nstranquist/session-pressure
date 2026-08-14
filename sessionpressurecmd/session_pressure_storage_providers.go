package sessionpressurecmd

import (
	"bufio"
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
	storageHotnessSchemaVersion = 1
	storageHotnessAgeFloor      = 7 * 24 * time.Hour
	storageHotnessMaxItems      = 1_000_000
)

type storageAgeBucket struct {
	Label     string `json:"label"`
	MinAge    string `json:"min_age"`
	MaxAge    string `json:"max_age,omitempty"`
	ItemCount int64  `json:"item_count"`
	Bytes     int64  `json:"bytes"`
}

// storageProviderHotnessEvidence is deliberately advisory. It records the
// mtime distribution of a foreground-measured cache so an operator can see the
// rebuild cost before selecting an operator provider. It never grants a
// cold-only delete authority.
type storageProviderHotnessEvidence struct {
	SchemaVersion      int                `json:"schema_version"`
	Scope              string             `json:"scope"`
	Available          bool               `json:"available"`
	Complete           bool               `json:"complete"`
	ErrorCode          string             `json:"error_code,omitempty"`
	MeasuredAt         string             `json:"measured_at,omitempty"`
	ItemCount          int64              `json:"item_count"`
	TotalBytes         int64              `json:"total_bytes"`
	HotAge             string             `json:"hot_age"`
	HotBytes           int64              `json:"hot_bytes"`
	HotFraction        float64            `json:"hot_fraction"`
	ColdEligibleBytes  int64              `json:"cold_eligible_bytes"`
	ColdMeetsShortfall bool               `json:"cold_meets_shortfall"`
	AgeBuckets         []storageAgeBucket `json:"age_buckets"`
}

func measureStorageHotness(path string, now time.Time) (*storageProviderHotnessEvidence, error) {
	evidence := &storageProviderHotnessEvidence{
		SchemaVersion: storageHotnessSchemaVersion,
		Scope:         "regular_file_mtime",
		HotAge:        storageHotnessAgeFloor.String(),
		AgeBuckets: []storageAgeBucket{
			{Label: "0-2d", MinAge: "0s", MaxAge: (48 * time.Hour).String()},
			{Label: "2-7d", MinAge: (48 * time.Hour).String(), MaxAge: storageHotnessAgeFloor.String()},
			{Label: "7-30d", MinAge: storageHotnessAgeFloor.String(), MaxAge: (30 * 24 * time.Hour).String()},
			{Label: "30d+", MinAge: (30 * 24 * time.Hour).String()},
		},
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	evidence.MeasuredAt = now.Format(time.RFC3339)

	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		evidence.ErrorCode = "path_missing"
		return evidence, nil
	}
	if err != nil {
		evidence.ErrorCode = "path_unreadable"
		return evidence, fmt.Errorf("inspect hotness root %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		evidence.ErrorCode = "path_not_directory"
		return evidence, fmt.Errorf("hotness root is not a real directory: %s", path)
	}
	evidence.Available = true

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	entryCount := 0
	err = filepath.WalkDir(path, func(entryPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entryCount >= storageHotnessMaxItems {
			evidence.ErrorCode = "item_limit"
			return errStorageHotnessItemLimit
		}
		entryCount++
		fileInfo, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if !fileInfo.Mode().IsRegular() {
			return nil
		}
		age := now.Sub(fileInfo.ModTime())
		if age < 0 {
			age = 0
		}
		bucket := storageAgeBucketIndex(age)
		bytes := fileInfo.Size()
		if bytes < 0 {
			bytes = 0
		}
		evidence.ItemCount++
		evidence.TotalBytes += bytes
		evidence.AgeBuckets[bucket].ItemCount++
		evidence.AgeBuckets[bucket].Bytes += bytes
		if age < storageHotnessAgeFloor {
			evidence.HotBytes += bytes
		} else {
			evidence.ColdEligibleBytes += bytes
		}
		return nil
	})
	if evidence.TotalBytes > 0 {
		evidence.HotFraction = float64(evidence.HotBytes) / float64(evidence.TotalBytes)
	}
	if err != nil {
		evidence.Complete = false
		if evidence.ErrorCode == "" {
			evidence.ErrorCode = "walk_failed"
		}
		return evidence, fmt.Errorf("measure hotness %s: %w", path, err)
	}
	evidence.Complete = true
	return evidence, nil
}

var errStorageHotnessItemLimit = errors.New("hotness measurement item limit reached")

func storageAgeBucketIndex(age time.Duration) int {
	switch {
	case age < 48*time.Hour:
		return 0
	case age < storageHotnessAgeFloor:
		return 1
	case age < 30*24*time.Hour:
		return 2
	default:
		return 3
	}
}

func inspectUserTrashProvider(home string, measure bool) storageProviderReport {
	path := filepath.Join(home, ".Trash")
	report := storageProviderReport{
		ID:             "user-trash",
		Classification: storageProviderOperator,
		Summary:        "Empty only the user Trash directory; never a general home-directory delete",
		Path:           path,
		EstimateKind:   "allowlisted_directory",
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
		report.EstimatedBytes, _, pathErr = storageDirectoryBytes(path)
		if pathErr != nil {
			report.BlockedReason = pathErr.Error()
			report.MutationSupported = false
		}
	}
	return report
}

func inspectAppUpdaterCachesProvider(home string, measure bool) storageProviderReport {
	root := filepath.Join(home, "Library", "Caches")
	report := storageProviderReport{
		ID:             "app-updater-caches",
		Classification: storageProviderOperator,
		Summary:        "Remove only top-level ShipIt/updater cache entries from Library/Caches",
		Path:           root,
		EstimateKind:   "allowlisted_top_level",
		Overlaps:       []string{"library-caches"},
	}
	rootPresent, rootErr := storageRealDirectoryPresent(root)
	if rootErr != nil {
		report.BlockedReason = rootErr.Error()
		report.MutationSupported = false
		return report
	}
	if !rootPresent {
		return report
	}
	paths, listErr := appUpdaterCachePaths(root)
	if listErr != nil {
		if errors.Is(listErr, os.ErrNotExist) {
			return report
		}
		report.BlockedReason = listErr.Error()
		report.MutationSupported = false
		return report
	}
	report.Paths = paths
	report.Present = len(paths) > 0
	report.MutationSupported = report.Present
	if !report.Present {
		return report
	}
	if measure {
		report.EstimatedBytes, _, listErr = measureStoragePathSet(paths)
		if listErr != nil {
			report.BlockedReason = listErr.Error()
			report.MutationSupported = false
		}
	}
	active, activeErr := storageCacheOwnerActive("app-updater-caches")
	report.ActiveOwner = active
	if activeErr != nil {
		report.BlockedReason = activeErr.Error()
		report.MutationSupported = false
	} else if active {
		report.BlockedReason = "an updater process is active"
		report.MutationSupported = false
	}
	return report
}

func inspectBrowserMediaCachesProvider(home string, measure bool) storageProviderReport {
	paths := browserMediaCachePaths(home)
	report := storageProviderReport{
		ID:             "browser-media-caches",
		Classification: storageProviderOperator,
		Summary:        "Remove named Spotify, Arc, or Brave media/web cache roots only; never browser profiles or password stores",
		EstimateKind:   "allowlisted_paths",
		Overlaps:       []string{"library-caches"},
	}
	presentPaths := storageExistingPaths(paths)
	report.Paths = presentPaths
	report.Present = len(presentPaths) > 0
	report.MutationSupported = report.Present
	if !report.Present {
		return report
	}
	if measure {
		var err error
		report.EstimatedBytes, _, err = measureStoragePathSet(presentPaths)
		if err != nil {
			report.BlockedReason = err.Error()
			report.MutationSupported = false
		}
	}
	active, activeErr := storageCacheOwnerActive("browser-media-caches")
	report.ActiveOwner = active
	if activeErr != nil {
		report.BlockedReason = activeErr.Error()
		report.MutationSupported = false
	} else if active {
		report.BlockedReason = "a Spotify, Arc, or Brave process is active"
		report.MutationSupported = false
	}
	return report
}

func inspectDockerReclaimProvider(measure bool) (storageProviderReport, bool) {
	path, available, err := storageDockerAvailable()
	if err != nil {
		return storageProviderReport{
			ID:                "docker-reclaim",
			Classification:    storageProviderOperator,
			Summary:           "Docker system prune without -a or --volumes; operator-only and skipped when Docker is absent",
			Path:              path,
			Present:           true,
			MutationSupported: false,
			EstimateKind:      "docker_system_df",
			BlockedReason:     err.Error(),
		}, true
	}
	if !available {
		return storageProviderReport{}, false
	}
	report := storageProviderReport{
		ID:                "docker-reclaim",
		Classification:    storageProviderOperator,
		Summary:           "Docker system prune without -a or --volumes; operator-only and skipped when Docker is absent",
		Path:              path,
		Present:           true,
		MutationSupported: true,
		EstimateKind:      "docker_system_df",
	}
	if measure {
		report.EstimatedBytes, err = storageDockerReclaimable()
		if err != nil {
			report.BlockedReason = err.Error()
			report.MutationSupported = false
		}
	}
	return report, true
}

func storageRealDirectoryPresent(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return true, fmt.Errorf("refusing symlink storage root %s", path)
	}
	if !info.IsDir() {
		return true, fmt.Errorf("storage root is not a directory: %s", path)
	}
	return true, nil
}

func measureStoragePathSet(paths []string) (int64, bool, error) {
	total := int64(0)
	present := false
	for _, path := range paths {
		bytes, exists, err := storageDirectoryBytes(path)
		if err != nil {
			return total, present, err
		}
		if !exists {
			continue
		}
		present = true
		total += bytes
	}
	return total, present, nil
}

func storageExistingPaths(paths []string) []string {
	present := make([]string, 0, len(paths))
	for _, path := range paths {
		exists, err := storageRealDirectoryPresent(path)
		if err == nil && exists {
			present = append(present, path)
		}
	}
	return present
}

func appUpdaterCachePaths(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0)
	for _, entry := range entries {
		if !appUpdaterCacheName(entry.Name()) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, infoErr := os.Lstat(path)
		if infoErr != nil {
			return nil, infoErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("refusing symlink updater cache %s", path)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func appUpdaterCacheName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return strings.Contains(name, "shipit") || strings.Contains(name, "updater")
}

func browserMediaCachePaths(home string) []string {
	return []string{
		filepath.Join(home, "Library", "Caches", "com.spotify.client"),
		filepath.Join(home, "Library", "Application Support", "Spotify", "PersistentCache"),
		filepath.Join(home, "Library", "Caches", "Arc"),
		filepath.Join(home, "Library", "Application Support", "Arc", "Default", "Cache"),
		filepath.Join(home, "Library", "Application Support", "Arc", "Default", "Code Cache"),
		filepath.Join(home, "Library", "Application Support", "Arc", "Default", "GPUCache"),
		filepath.Join(home, "Library", "Caches", "BraveSoftware"),
		filepath.Join(home, "Library", "Application Support", "BraveSoftware", "Brave-Browser", "Default", "Cache"),
		filepath.Join(home, "Library", "Application Support", "BraveSoftware", "Brave-Browser", "Default", "Code Cache"),
		filepath.Join(home, "Library", "Application Support", "BraveSoftware", "Brave-Browser", "Default", "GPUCache"),
		filepath.Join(home, "Library", "Application Support", "BraveSoftware", "Brave-Browser", "Default", "Service Worker", "CacheStorage"),
	}
}

func storagePathUnderAllowlist(path string, allowed []string) bool {
	clean := filepath.Clean(path)
	for _, candidate := range allowed {
		if clean == filepath.Clean(candidate) {
			return true
		}
	}
	return false
}

func validateStorageRoot(path, expected string) error {
	if filepath.Clean(path) != filepath.Clean(expected) {
		return fmt.Errorf("storage root is outside the provider allowlist: %s", path)
	}
	_, err := storageRealDirectoryPresent(expected)
	return err
}

func removeStoragePath(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	return ctx.Err()
}

func emptyUserTrash(ctx context.Context, path string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	root := filepath.Join(home, ".Trash")
	if err := validateStorageRoot(path, root); err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := removeStoragePath(ctx, filepath.Join(root, entry.Name())); err != nil {
			return fmt.Errorf("empty user Trash: %w", err)
		}
	}
	return nil
}

func cleanAppUpdaterCaches(ctx context.Context, root string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	expected := filepath.Join(home, "Library", "Caches")
	if err := validateStorageRoot(root, expected); err != nil {
		return err
	}
	paths, err := appUpdaterCachePaths(expected)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, path := range paths {
		if err := removeStoragePath(ctx, path); err != nil {
			return fmt.Errorf("remove updater cache %s: %w", filepath.Base(path), err)
		}
	}
	return nil
}

func cleanBrowserMediaCaches(ctx context.Context, paths []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	allowed := browserMediaCachePaths(home)
	for _, path := range paths {
		if !storagePathUnderAllowlist(path, allowed) {
			return fmt.Errorf("browser media cache path is outside the allowlist: %s", path)
		}
	}
	for _, path := range paths {
		if err := removeStoragePath(ctx, path); err != nil {
			return fmt.Errorf("remove browser media cache %s: %w", filepath.Base(path), err)
		}
	}
	return nil
}

func dockerCLIAvailable() (string, bool, error) {
	path, err := exec.LookPath("docker")
	if errors.Is(err, exec.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return path, true, nil
}

type dockerSystemDFRow struct {
	Type        string `json:"Type"`
	Reclaimable string `json:"Reclaimable"`
}

func dockerReclaimableBytes() (int64, error) {
	path, available, err := dockerCLIAvailable()
	if err != nil {
		return 0, err
	}
	if !available {
		return 0, errors.New("docker executable is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := processtree.CommandContext(ctx, path, "system", "df", "--format", "{{json .}}")
	output, err := command.Output()
	if err != nil {
		return 0, fmt.Errorf("docker system df: %w", err)
	}
	return parseDockerSystemDFOutput(output)
}

func parseDockerSystemDFOutput(output []byte) (int64, error) {
	total := int64(0)
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var row dockerSystemDFRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return 0, fmt.Errorf("decode docker system df: %w", err)
		}
		if strings.Contains(strings.ToLower(row.Type), "volume") {
			continue
		}
		fields := strings.Fields(row.Reclaimable)
		if len(fields) == 0 {
			continue
		}
		value := strings.TrimSpace(fields[0])
		bytes, parseErr := parseStorageBytes(value)
		if parseErr != nil {
			return 0, fmt.Errorf("parse Docker reclaimable %q: %w", row.Reclaimable, parseErr)
		}
		total += bytes
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return total, nil
}

func runDockerSystemPrune(ctx context.Context) error {
	path, available, err := dockerCLIAvailable()
	if err != nil {
		return err
	}
	if !available {
		return errors.New("docker executable is unavailable")
	}
	command := processtree.CommandContext(ctx, path, "system", "prune", "--force")
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(output.String())
		if len(message) > 512 {
			message = message[:512]
		}
		return fmt.Errorf("docker system prune: %w: %s", err, message)
	}
	return ctx.Err()
}

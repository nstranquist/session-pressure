package sessionpressure

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const StorageVolumePathEnv = "NDEV_STORAGE_VOLUME_PATH"

type StorageCapacity struct {
	TotalBytes     int64
	FreeBytes      int64
	AvailableBytes int64
}

func saturatingBlockBytes(blocks uint64, blockSize int64) int64 {
	if blockSize <= 0 || blocks == 0 {
		return 0
	}
	if blocks > uint64(math.MaxInt64)/uint64(blockSize) {
		return math.MaxInt64
	}
	return int64(blocks) * blockSize
}

func DefaultStorageVolumePath() string {
	if override := strings.TrimSpace(os.Getenv(StorageVolumePathEnv)); override != "" {
		if absolute, err := filepath.Abs(override); err == nil {
			return absolute
		}
		return override
	}
	if runtime.GOOS == "darwin" {
		const dataVolume = "/System/Volumes/Data"
		if info, err := os.Stat(dataVolume); err == nil && info.IsDir() {
			return dataVolume
		}
	}
	return string(filepath.Separator)
}

func SampleStorageCapacity(path string, now time.Time) StorageSnapshot {
	if strings.TrimSpace(path) == "" {
		path = DefaultStorageVolumePath()
	}
	sample := StorageSnapshot{VolumePath: path, Source: "statfs", CapturedAt: now.UTC(), Level: LevelNormal}
	capacity, err := nativeStorageCapacity(path)
	if err != nil {
		sample.Error = boundedText(err.Error(), 512)
		return sample
	}
	if capacity.TotalBytes <= 0 || capacity.AvailableBytes < 0 {
		sample.Error = "filesystem capacity probe returned invalid values"
		return sample
	}
	sample.Available = true
	sample.TotalBytes = capacity.TotalBytes
	sample.FreeBytes = capacity.FreeBytes
	sample.AvailableBytes = capacity.AvailableBytes
	sample.FreePercent = 100 * float64(capacity.AvailableBytes) / float64(capacity.TotalBytes)
	return sample
}

func (sampler *Sampler) sampleStorage(policy Policy, previous Level) StorageSnapshot {
	now := sampler.now()
	path := DefaultStorageVolumePath()
	if sampler.storageSource == nil {
		return StorageSnapshot{VolumePath: path, Source: "unavailable", CapturedAt: now.UTC(), Level: LevelNormal, Error: "storage sampler unavailable"}
	}
	capacity, err := sampler.storageSource(path)
	if err != nil {
		return StorageSnapshot{VolumePath: path, Source: "statfs", CapturedAt: now.UTC(), Level: LevelNormal, Error: boundedText(err.Error(), 512)}
	}
	sample := StorageSnapshot{
		Available: true, VolumePath: path, Source: "statfs", CapturedAt: now.UTC(),
		TotalBytes: capacity.TotalBytes, FreeBytes: capacity.FreeBytes, AvailableBytes: capacity.AvailableBytes,
	}
	if capacity.TotalBytes <= 0 || capacity.AvailableBytes < 0 {
		sample.Available = false
		sample.Error = "filesystem capacity probe returned invalid values"
		return sample
	}
	sample.FreePercent = 100 * float64(capacity.AvailableBytes) / float64(capacity.TotalBytes)
	return EvaluateStorage(sample, policy.Storage, previous)
}

// StorageAdmissionForClass evaluates only storage authority for a calibrated
// work class. Reclaim is the relief path and can never be storage-blocked.
// Install is network/disk-bound and deliberately admitted under storage-red
// (KEP P2). Express classes deliberately admit under storage-red as the
// sanctioned degraded-mode packing path (KEP P3).
func StorageAdmissionForClass(snapshot StorageSnapshot, policy Policy, class WorkClass, source string) Admission {
	if !policy.Storage.Enabled || !policy.Storage.EnforceAdmission || class == WorkClassReclaim || class == WorkClassInstall || !storageGrowingClass(class) {
		return Admission{Allowed: true, Level: snapshot.Level, Source: source, Snapshot: &Snapshot{Storage: snapshot}}
	}
	// P3: express classes are the sanctioned degraded-mode path under storage-red.
	if class == WorkClassExpressTest || class == WorkClassExpressBuild {
		return Admission{Allowed: true, Level: snapshot.Level, Source: source + "+express-degraded", Snapshot: &Snapshot{Storage: snapshot}}
	}
	if !snapshot.Available {
		warning := "storage sampling unavailable; storage admission failed open"
		if snapshot.Error != "" {
			warning += ": " + snapshot.Error
		}
		return Admission{Allowed: true, Level: LevelNormal, Source: "fail-open", Warning: warning, Snapshot: &Snapshot{Storage: snapshot}}
	}
	allowed := !snapshot.Level.AtLeast(policy.Storage.BlockNewAt)
	return Admission{Allowed: allowed, Level: snapshot.Level, Source: source, Reasons: append([]string(nil), snapshot.Reasons...), Snapshot: &Snapshot{Storage: snapshot}}
}

// storageGrowingClass is true for compiler/test-shaped work that grows disk
// under storage-red enforcement. Express and install are intentionally excluded
// so package-scoped and I/O-bound work can progress under a latched red.
func storageGrowingClass(class WorkClass) bool {
	switch class {
	case WorkClassTest, WorkClassBuild, WorkClassEmulator, WorkClassBrowser, WorkClassHeavy, WorkClassBenchmark, WorkClassBenchmarkExclusive:
		return true
	default:
		// express-test, express-build, install, reclaim → not storage-growing for gate purposes
		return false
	}
}

// FormatStorageDeadlockAdvice returns a single actionable line for storage-red
// waiters (KEP P1): free bytes, release threshold, and the lowest-cost typed
// reclaim levers before the high-rebuild-cost Go cache.
func FormatStorageDeadlockAdvice(snapshot StorageSnapshot, policy StoragePolicy) string {
	free := snapshot.AvailableBytes
	release := policy.RedReleaseBytes
	if release <= 0 {
		release = 30 << 30
	}
	need := release - free
	if need < 0 {
		need = 0
	}
	return fmt.Sprintf(
		"storage-red deadlock: free=%s release=%s need≈%s; reclaim levers: load skill storage-reclaim; ndev session pressure storage status; ndev session pressure storage plan --target-free 50GiB; ndev session pressure storage apply --auto-safe --apply; ndev session pressure storage apply --provider user-trash --target-free 50GiB --apply; ndev session pressure storage apply --provider app-updater-caches --target-free 50GiB --apply; ndev session pressure storage apply --provider browser-media-caches --target-free 50GiB --apply; ndev session pressure storage apply --provider brew-cache --target-free 50GiB --apply; ndev session pressure storage apply --provider go-build-cache --target-free 50GiB --apply (last resort); catalog skill.storage-reclaim",
		formatBytesIEC(free), formatBytesIEC(release), formatBytesIEC(need),
	)
}

func formatBytesIEC(n int64) string {
	if n < 0 {
		n = 0
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// ExpressAdmittedUnderStorageRed documents the P3 decision for tests and KB.
func ExpressAdmittedUnderStorageRed() bool { return true }

func ConfiguredWorkAdmission(ctx context.Context, class WorkClass) Admission {
	host := ConfiguredWorkHostAdmission(ctx)
	if !host.Allowed {
		return host
	}
	storage := ConfiguredStorageAdmission(ctx, class)
	if !storage.Allowed || storage.Warning != "" {
		return storage
	}
	return host
}

func ConfiguredStorageAdmission(ctx context.Context, class WorkClass) Admission {
	dir, err := DataDir()
	if err != nil {
		return Admission{Allowed: true, Level: LevelNormal, Source: "fail-open", Warning: err.Error()}
	}
	policy, persisted, err := LoadPolicy(PolicyPath(dir), 0)
	if err != nil || !persisted {
		return Admission{Allowed: true, Level: LevelNormal, Source: "unconfigured"}
	}
	previous := LevelNormal
	if latest, ok := NewTelemetryStore(dir).ReadLatest(); ok {
		previous = latest.Storage.Level
	}
	sample := NewSampler().sampleStorage(policy, previous)
	storage := StorageAdmissionForClass(sample, policy, class, "live-storage-probe")
	return storage
}

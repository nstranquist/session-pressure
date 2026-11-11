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
// work class. Reclaim must remain possible at every level. Express and install
// may progress under red pressure, but not through the critical reserve or its
// recovery hysteresis. Diagnostic warning targets are not admission targets.
func StorageAdmissionForClass(snapshot StorageSnapshot, policy Policy, class WorkClass, source string) Admission {
	if !policy.Storage.Enabled || !policy.Storage.EnforceAdmission || class == WorkClassReclaim || !storageGrowingClass(class) {
		return Admission{Allowed: true, Level: snapshot.Level, Source: source, Snapshot: &Snapshot{Storage: snapshot}}
	}
	if !snapshot.Available {
		warning := "storage sampling unavailable; storage admission failed open"
		if snapshot.Error != "" {
			warning += ": " + snapshot.Error
		}
		return Admission{Allowed: true, Level: LevelNormal, Source: "fail-open", Warning: warning, Snapshot: &Snapshot{Storage: snapshot}}
	}
	blockAt := policy.Storage.BlockNewAt
	if storageRedDegradedClass(class) {
		blockAt = LevelCritical
		source += "+red-degraded"
	}
	allowed := !snapshot.Level.AtLeast(blockAt)
	if !allowed {
		// A critical diagnostic may recover to red before a broad build can
		// start. Return the release needed by THIS admission, not merely the
		// currently latched diagnostic band's release.
		snapshot.ReleaseBytes = max(snapshot.ReleaseBytes, storageAdmissionReleaseBytes(policy.Storage, blockAt), storageAdmissionReleaseBytes(policy.Storage, snapshot.Level))
	}
	return Admission{Allowed: allowed, Level: snapshot.Level, Source: source, Reasons: append([]string(nil), snapshot.Reasons...), Snapshot: &Snapshot{Storage: snapshot}}
}

func storageRedDegradedClass(class WorkClass) bool {
	return class == WorkClassExpressTest || class == WorkClassExpressBuild || class == WorkClassInstall
}

// Even package-scoped compilation and installation can grow disk. They have
// a different admission band, not an exemption from the emergency reserve.
func storageGrowingClass(class WorkClass) bool {
	switch class {
	case WorkClassTest, WorkClassBuild, WorkClassEmulator, WorkClassBrowser, WorkClassHeavy, WorkClassBenchmark, WorkClassBenchmarkExclusive,
		WorkClassExpressTest, WorkClassExpressBuild, WorkClassInstall:
		return true
	default:
		return false
	}
}

func storageAdmissionReleaseBytes(policy StoragePolicy, blockAt Level) int64 {
	switch blockAt {
	case LevelCritical:
		return max(policy.CriticalFreeBytes, policy.CriticalReleaseBytes)
	case LevelWarning:
		return max(policy.WarningFreeBytes, policy.WarningReleaseBytes)
	default:
		return max(policy.RedFreeBytes, policy.RedReleaseBytes)
	}
}

// FormatStorageDeadlockAdvice asks only for the headroom needed to resume this
// admission. The separate cleanup target is not a prerequisite. Keep this a
// read-only plan: deleting personal data or expensive caches is not an implied
// next step, and report-only providers must never be advertised as apply paths.
func FormatStorageDeadlockAdvice(snapshot StorageSnapshot, policy StoragePolicy) string {
	if !snapshot.Available {
		return "storage capacity unavailable; cannot calculate recovery shortage; run ndev session pressure storage status before planning reclaim"
	}
	free := snapshot.AvailableBytes
	release := snapshot.ReleaseBytes
	if release <= 0 {
		release = storageAdmissionReleaseBytes(policy, policy.BlockNewAt)
	}
	need := max(int64(0), release-free)
	target := fmt.Sprintf("%d", release)
	if release > 0 && release%(1<<30) == 0 {
		target = fmt.Sprintf("%dGiB", release>>30)
	}
	return fmt.Sprintf(
		"storage admission blocked: physical_free=%s (%.1f GB) release=%s need≈%s; cleanup target=%s is optional, not an admission requirement; inspect ndev session pressure storage status; load skill storage-reclaim; preview ndev session pressure storage plan --target-free %s; review provider eligibility and deletion authority before apply; catalog skill.storage-reclaim",
		formatBytesIEC(free), float64(free)/1e9, formatBytesIEC(release), formatBytesIEC(need), formatBytesIEC(policy.TargetFreeBytes), target,
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

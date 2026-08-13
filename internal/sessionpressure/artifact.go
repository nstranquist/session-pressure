package sessionpressure

import (
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nstranquist/session-pressure/internal/filelock"
)

const artifactManifestSchemaVersion = 2
const artifactManifestMinimumSchemaVersion = 1
const artifactFileMode = 0o555
const ArtifactRollbackCount = 2

const Version = "0.4.0"

// InstalledArtifact is the immutable helper revision selected for launchd.
// Ordinary repository builds never mutate this content-addressed path.
type InstalledArtifact struct {
	SchemaVersion int       `json:"schema_version"`
	SHA256        string    `json:"sha256"`
	Path          string    `json:"path"`
	InstalledAt   time.Time `json:"installed_at"`
	SizeBytes     int64     `json:"size_bytes,omitempty"`
	Version       string    `json:"version,omitempty"`
	GoVersion     string    `json:"go_version,omitempty"`
	ModulePath    string    `json:"module_path,omitempty"`
	ModuleVersion string    `json:"module_version,omitempty"`
	VCSRevision   string    `json:"vcs_revision,omitempty"`
	VCSModified   bool      `json:"vcs_modified,omitempty"`
	VCSBuildTime  string    `json:"vcs_build_time,omitempty"`
}

type ArtifactPruneEntry struct {
	SHA256     string    `json:"sha256"`
	Path       string    `json:"path"`
	SizeBytes  int64     `json:"size_bytes"`
	ModifiedAt time.Time `json:"modified_at"`
	Active     bool      `json:"active"`
	Retained   bool      `json:"retained"`
	Prune      bool      `json:"prune"`
	Pruned     bool      `json:"pruned"`
	SkipReason string    `json:"skip_reason,omitempty"`
}

type ArtifactPruneReport struct {
	SchemaVersion  int                  `json:"schema_version"`
	Applied        bool                 `json:"applied"`
	ActiveSHA256   string               `json:"active_sha256,omitempty"`
	RollbackCount  int                  `json:"rollback_count"`
	PruneCount     int                  `json:"prune_count"`
	PrunedCount    int                  `json:"pruned_count"`
	ReclaimBytes   int64                `json:"reclaim_bytes"`
	ReclaimedBytes int64                `json:"reclaimed_bytes"`
	Entries        []ArtifactPruneEntry `json:"entries"`
}

func ArtifactManifestPath(dir string) string {
	return filepath.Join(dir, "installed-artifact.json")
}

// ArtifactDrift compares the binary a promotion would install against the one
// currently installed. Coordinated work executes the installed artifact, not
// the repository build, so without this a rebuilt helper can sit unpromoted
// while every measurement silently describes the older revision.
type ArtifactDrift struct {
	Checked         bool   `json:"checked"`
	Drifted         bool   `json:"drifted"`
	CandidatePath   string `json:"candidate_path,omitempty"`
	CandidateSHA256 string `json:"candidate_sha256,omitempty"`
	InstalledSHA256 string `json:"installed_sha256,omitempty"`
	Detail          string `json:"detail,omitempty"`
}

// InspectArtifactDrift is report-only and never fails closed: an unreadable
// candidate leaves Checked false rather than blocking artifact inspection.
func InspectArtifactDrift(candidateBinary, dir string) ArtifactDrift {
	drift := ArtifactDrift{CandidatePath: candidateBinary}
	installed, found, err := LoadInstalledArtifact(dir)
	if err != nil {
		drift.Detail = err.Error()
		return drift
	}
	if !found {
		drift.Detail = "no installed artifact to compare"
		return drift
	}
	drift.InstalledSHA256 = installed.SHA256
	digest, hashErr := fileSHA256(candidateBinary)
	if hashErr != nil {
		drift.Detail = "hash promotion candidate: " + hashErr.Error()
		return drift
	}
	drift.CandidateSHA256 = digest
	drift.Checked = true
	drift.Drifted = digest != installed.SHA256
	if drift.Drifted {
		drift.Detail = "coordinated work runs the installed artifact; promote this build with `ndev session pressure monitor install`"
	}
	return drift
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func promoteArtifact(source, dir string, now time.Time) (InstalledArtifact, error) {
	digest, err := fileSHA256(source)
	if err != nil {
		return InstalledArtifact{}, fmt.Errorf("hash guard binary: %w", err)
	}
	artifactDir := filepath.Join(dir, "artifacts", "sha256-"+digest)
	artifactPath := filepath.Join(artifactDir, "ndev-session-pressure")
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		return InstalledArtifact{}, err
	}
	existingInfo, statErr := os.Lstat(artifactPath)
	if statErr == nil && !existingInfo.Mode().IsRegular() {
		return InstalledArtifact{}, fmt.Errorf("promoted guard binary is not a regular file")
	}
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return InstalledArtifact{}, fmt.Errorf("inspect promoted guard binary: %w", statErr)
	}
	existingDigest := ""
	if statErr == nil {
		existingDigest, err = fileSHA256(artifactPath)
		if err != nil {
			return InstalledArtifact{}, fmt.Errorf("verify promoted guard binary: %w", err)
		}
	}
	if existingDigest != digest {
		body, readErr := os.ReadFile(source)
		if readErr != nil {
			return InstalledArtifact{}, readErr
		}
		if writeErr := atomicWrite(artifactPath, body, artifactFileMode); writeErr != nil {
			return InstalledArtifact{}, writeErr
		}
		writtenDigest, verifyErr := fileSHA256(artifactPath)
		if verifyErr != nil || writtenDigest != digest {
			return InstalledArtifact{}, fmt.Errorf("promoted guard binary digest mismatch")
		}
	}
	// Content addressing prevents replacement-by-build; a read/execute-only
	// mode also rejects accidental in-place writes by ordinary tooling.
	if chmodErr := os.Chmod(artifactPath, artifactFileMode); chmodErr != nil {
		return InstalledArtifact{}, chmodErr
	}
	artifact := InstalledArtifact{
		SchemaVersion: artifactManifestSchemaVersion,
		SHA256:        digest,
		Path:          artifactPath,
		InstalledAt:   now.UTC(),
	}
	populateArtifactProvenance(&artifact)
	body, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return InstalledArtifact{}, err
	}
	body = append(body, '\n')
	if err := atomicWrite(ArtifactManifestPath(dir), body, 0o600); err != nil {
		return InstalledArtifact{}, err
	}
	return artifact, nil
}

func LoadInstalledArtifact(dir string) (InstalledArtifact, bool, error) {
	body, err := os.ReadFile(ArtifactManifestPath(dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return InstalledArtifact{}, false, nil
		}
		return InstalledArtifact{}, false, err
	}
	var artifact InstalledArtifact
	if err := json.Unmarshal(body, &artifact); err != nil {
		return InstalledArtifact{}, true, fmt.Errorf("decode installed artifact: %w", err)
	}
	decodedDigest, decodeErr := hex.DecodeString(artifact.SHA256)
	expectedPath := filepath.Join(dir, "artifacts", "sha256-"+artifact.SHA256, "ndev-session-pressure")
	if artifact.SchemaVersion < artifactManifestMinimumSchemaVersion || artifact.SchemaVersion > artifactManifestSchemaVersion || decodeErr != nil || len(decodedDigest) != sha256.Size || !filepath.IsAbs(artifact.Path) || filepath.Clean(artifact.Path) != filepath.Clean(expectedPath) {
		return InstalledArtifact{}, true, fmt.Errorf("invalid installed artifact manifest")
	}
	return artifact, true, nil
}

func populateArtifactProvenance(artifact *InstalledArtifact) {
	info, err := os.Stat(artifact.Path)
	if err == nil {
		artifact.SizeBytes = info.Size()
	}
	artifact.Version = Version
	build, err := buildinfo.ReadFile(artifact.Path)
	if err != nil {
		return
	}
	artifact.GoVersion = build.GoVersion
	artifact.ModulePath = build.Main.Path
	artifact.ModuleVersion = build.Main.Version
	for _, setting := range build.Settings {
		switch setting.Key {
		case "vcs.revision":
			artifact.VCSRevision = setting.Value
		case "vcs.modified":
			artifact.VCSModified = setting.Value == "true"
		case "vcs.time":
			artifact.VCSBuildTime = setting.Value
		}
	}
}

// PruneArtifacts retains the active helper plus two newest verified rollback
// revisions. The default is a read-only plan; apply holds the install lock and
// removes only exact, content-addressed directories containing one regular
// helper file.
func PruneArtifacts(ctx context.Context, dir string, apply bool) (ArtifactPruneReport, error) {
	if !apply {
		return planArtifactPrune(dir)
	}
	unlock, err := filelock.AcquireContext(ctx, filepath.Join(dir, launchdInstallLockName), 15*time.Second)
	if err != nil {
		return ArtifactPruneReport{}, fmt.Errorf("acquire artifact prune lock: %w", err)
	}
	defer unlock()
	report, err := planArtifactPrune(dir)
	if err != nil {
		return report, err
	}
	return applyArtifactPrune(dir, report)
}

func pruneArtifactsUnlocked(dir string) (ArtifactPruneReport, error) {
	report, err := planArtifactPrune(dir)
	if err != nil {
		return report, err
	}
	return applyArtifactPrune(dir, report)
}

func applyArtifactPrune(dir string, report ArtifactPruneReport) (ArtifactPruneReport, error) {
	report.Applied = true
	for index := range report.Entries {
		entry := &report.Entries[index]
		if !entry.Prune {
			continue
		}
		if err := removeVerifiedArtifactDirectory(dir, *entry); err != nil {
			return report, err
		}
		entry.Pruned = true
		report.PrunedCount++
		report.ReclaimedBytes += entry.SizeBytes
	}
	return report, nil
}

func planArtifactPrune(dir string) (ArtifactPruneReport, error) {
	report := ArtifactPruneReport{SchemaVersion: 1, RollbackCount: ArtifactRollbackCount}
	manifest, found, err := LoadInstalledArtifact(dir)
	if err != nil {
		return report, err
	}
	if found {
		report.ActiveSHA256 = manifest.SHA256
	}
	root, err := filepath.Abs(filepath.Join(dir, "artifacts"))
	if err != nil {
		return report, err
	}
	children, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return report, nil
	}
	if err != nil {
		return report, err
	}
	for _, child := range children {
		entry := inspectArtifactDirectory(root, child, report.ActiveSHA256)
		report.Entries = append(report.Entries, entry)
	}
	sort.Slice(report.Entries, func(left, right int) bool {
		if report.Entries[left].Active != report.Entries[right].Active {
			return report.Entries[left].Active
		}
		if !report.Entries[left].ModifiedAt.Equal(report.Entries[right].ModifiedAt) {
			return report.Entries[left].ModifiedAt.After(report.Entries[right].ModifiedAt)
		}
		return report.Entries[left].SHA256 < report.Entries[right].SHA256
	})
	rollbackRetained := 0
	for index := range report.Entries {
		entry := &report.Entries[index]
		if entry.SkipReason != "" {
			continue
		}
		if entry.Active {
			entry.Retained = true
			continue
		}
		if rollbackRetained < ArtifactRollbackCount {
			entry.Retained = true
			rollbackRetained++
			continue
		}
		entry.Prune = true
		report.PruneCount++
		report.ReclaimBytes += entry.SizeBytes
	}
	return report, nil
}

func inspectArtifactDirectory(root string, child os.DirEntry, activeDigest string) ArtifactPruneEntry {
	entry := ArtifactPruneEntry{Path: filepath.Join(root, child.Name())}
	if !child.IsDir() || child.Type()&os.ModeSymlink != 0 || !strings.HasPrefix(child.Name(), "sha256-") {
		entry.SkipReason = "not an exact content-addressed artifact directory"
		return entry
	}
	entry.SHA256 = strings.TrimPrefix(child.Name(), "sha256-")
	decoded, err := hex.DecodeString(entry.SHA256)
	if err != nil || len(decoded) != sha256.Size {
		entry.SkipReason = "invalid artifact digest directory"
		return entry
	}
	children, err := os.ReadDir(entry.Path)
	if err != nil || len(children) != 1 || children[0].Name() != "ndev-session-pressure" || children[0].Type()&os.ModeSymlink != 0 {
		entry.SkipReason = "artifact directory has unexpected contents"
		return entry
	}
	binaryPath := filepath.Join(entry.Path, "ndev-session-pressure")
	info, err := os.Lstat(binaryPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != artifactFileMode {
		entry.SkipReason = "artifact binary is missing, non-regular, or has the wrong mode"
		return entry
	}
	digest, err := fileSHA256(binaryPath)
	if err != nil || digest != entry.SHA256 {
		entry.SkipReason = "artifact binary digest does not match its directory"
		return entry
	}
	entry.Path = binaryPath
	entry.SizeBytes = info.Size()
	entry.ModifiedAt = info.ModTime().UTC()
	entry.Active = entry.SHA256 == activeDigest
	return entry
}

func removeVerifiedArtifactDirectory(dir string, entry ArtifactPruneEntry) error {
	root, err := filepath.Abs(filepath.Join(dir, "artifacts"))
	if err != nil {
		return err
	}
	expectedDirectory := filepath.Join(root, "sha256-"+entry.SHA256)
	expectedBinary := filepath.Join(expectedDirectory, "ndev-session-pressure")
	if filepath.Clean(entry.Path) != expectedBinary || entry.Active || !entry.Prune {
		return errors.New("refuse unsafe artifact prune target")
	}
	verified := inspectArtifactDirectory(root, artifactDirectoryEntry{name: filepath.Base(expectedDirectory)}, "")
	if verified.SkipReason != "" || verified.SHA256 != entry.SHA256 || verified.Path != expectedBinary {
		return fmt.Errorf("artifact changed before prune: %s", entry.SHA256)
	}
	if err := os.Remove(expectedBinary); err != nil {
		return err
	}
	if err := os.Remove(expectedDirectory); err != nil {
		return err
	}
	return nil
}

type artifactDirectoryEntry struct{ name string }

func (entry artifactDirectoryEntry) Name() string         { return entry.name }
func (artifactDirectoryEntry) IsDir() bool                { return true }
func (artifactDirectoryEntry) Type() os.FileMode          { return os.ModeDir }
func (artifactDirectoryEntry) Info() (os.FileInfo, error) { return nil, errors.New("not implemented") }

// VerifyInstalledArtifact validates the exact executable selected by the
// manifest before a launchd install or CLI handoff trusts it.
func VerifyInstalledArtifact(dir string) (InstalledArtifact, error) {
	artifact, found, err := LoadInstalledArtifact(dir)
	if err != nil {
		return InstalledArtifact{}, err
	}
	if !found {
		return InstalledArtifact{}, errors.New("installed guard artifact is missing; run ndev session pressure monitor install")
	}
	info, err := os.Lstat(artifact.Path)
	if err != nil {
		return InstalledArtifact{}, fmt.Errorf("inspect installed guard artifact: %w", err)
	}
	if !info.Mode().IsRegular() {
		return InstalledArtifact{}, errors.New("installed guard artifact is not a regular file")
	}
	if info.Mode().Perm() != artifactFileMode {
		return InstalledArtifact{}, fmt.Errorf("installed guard artifact mode is %04o, want %04o", info.Mode().Perm(), artifactFileMode)
	}
	digest, err := fileSHA256(artifact.Path)
	if err != nil {
		return InstalledArtifact{}, fmt.Errorf("hash installed guard artifact: %w", err)
	}
	if digest != artifact.SHA256 {
		return InstalledArtifact{}, errors.New("installed guard artifact digest mismatch")
	}
	return artifact, nil
}

func currentExecutableSHA256() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	digest, err := fileSHA256(path)
	if err != nil {
		return ""
	}
	return digest
}

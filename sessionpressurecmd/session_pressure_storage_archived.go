package sessionpressurecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// The archived-projects provider reports projects that moved out of the
// nicos-tools tree into standalone archives (~/archive/nicos-tools/<id>). They
// are source-only and rebuildable, and their owner marked them as candidates
// for an external drive. Storage reclaim never deletes an archive: the only
// mutation is an operator offload through the catalog, which verifies the copy
// on the drive before it moves the local copy to the Trash.
//
// The provider reads a projection written by nicos-tools
// (`ndev catalog external archive status --write-projection`), so this repo
// never parses nicos-tools configuration.

const (
	archivedProjectsProviderID = "archived-projects"
	archiveProjectionSchema    = "nicos.catalog.archive-candidates.v1"
	// archiveProjectionStaleAfter marks a projection old enough that its byte
	// counts and tiers may no longer match the disk.
	archiveProjectionStaleAfter = 7 * 24 * time.Hour
)

type archiveProjectionRow struct {
	ID             string `json:"id"`
	Path           string `json:"path"`
	Format         string `json:"format"`
	Tier           string `json:"tier"`
	Online         bool   `json:"online"`
	Bytes          int64  `json:"bytes"`
	Candidate      bool   `json:"external_drive_candidate"`
	OffloadCommand string `json:"offload_command,omitempty"`
}

type archiveProjection struct {
	Schema      string                 `json:"schema"`
	GeneratedAt string                 `json:"generated_at"`
	Producer    string                 `json:"producer"`
	Archives    []archiveProjectionRow `json:"archives"`
}

// storageArchiveEvidence is the per-archive decision payload.
type storageArchiveEvidence struct {
	Projection     string                 `json:"projection"`
	GeneratedAt    string                 `json:"generated_at,omitempty"`
	Stale          bool                   `json:"stale"`
	Refresh        string                 `json:"refresh"`
	CandidateCount int                    `json:"candidate_count"`
	OffloadedCount int                    `json:"offloaded_count"`
	Candidates     []archiveProjectionRow `json:"candidates,omitempty"`
}

func storageArchiveProjectionPath(home string) string {
	return filepath.Join(home, ".nicos-dev", "storage", "archive-candidates.v1.json")
}

// storageArchivedProjectsProvider is report_only. EstimatedBytes counts local,
// online candidates; the inventory estimate never enters the reclaim headline
// because offload needs a mounted drive and an operator decision.
func storageArchivedProjectsProvider(home string, now time.Time) storageProviderReport {
	path := storageArchiveProjectionPath(home)
	report := storageProviderReport{
		ID:             archivedProjectsProviderID,
		Classification: storageProviderReportOnly,
		Summary:        "Source-only project archives moved out of nicos-tools; external-drive candidates (offload through the catalog, never deleted here)",
		Path:           path,
		EstimateKind:   "inventory",
		BlockedReason:  "offload is an operator step: run each candidate's offload_command with the drive mounted",
	}
	evidence := storageArchiveEvidence{
		Projection: path,
		Refresh:    "ndev catalog external archive status --write-projection",
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return report
	}
	var proj archiveProjection
	if json.Unmarshal(raw, &proj) != nil || proj.Schema != archiveProjectionSchema {
		report.BlockedReason = "archive projection is unreadable or has an unknown schema; refresh it with " + evidence.Refresh
		return report
	}
	evidence.GeneratedAt = proj.GeneratedAt
	if generated, err := time.Parse(time.RFC3339, proj.GeneratedAt); err != nil || now.Sub(generated) > archiveProjectionStaleAfter {
		evidence.Stale = true
	}
	for _, row := range proj.Archives {
		switch {
		case row.Tier == "external-drive":
			evidence.OffloadedCount++
		case row.Candidate && row.Online:
			evidence.CandidateCount++
			report.EstimatedBytes += row.Bytes
			report.Paths = append(report.Paths, row.Path)
			evidence.Candidates = append(evidence.Candidates, row)
		}
	}
	sort.Slice(evidence.Candidates, func(i, j int) bool { return evidence.Candidates[i].Bytes > evidence.Candidates[j].Bytes })
	sort.Strings(report.Paths)
	report.Present = evidence.CandidateCount > 0
	report.ArchiveEvidence = &evidence
	return report
}

package sessionpressurecmd

import (
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Retention providers (repo restructure program S2, ST6). Each one selects
// old copies of state that a newer copy supersedes. The selection is a pure
// function of the directory listing, the pins and the clock, so a dry run and
// an apply select the same paths. Apply re-plans and removes only paths that
// the fresh plan still selects.

const (
	storageRetentionSchema = 1
	// Matches nicos-commit-delivery binarypublish: three newest revisions
	// plus the newest clean VCS build.
	storageBinaryRevisionsRetain      = 3
	storageBinaryRevisionsRetainClean = 1
	storageTursoBackupsRetain         = 2
)

var (
	storageRevisionFilePattern  = regexp.MustCompile(`^sha256-[0-9a-f]{64}$`)
	storageReleaseDirPattern    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	storageTursoDayPattern      = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	storageGoOrphanPattern      = regexp.MustCompile(`^go-build-.+-(\d+)$`)
	storageRevisionPinReference = regexp.MustCompile(`/\.local/bin/\.[A-Za-z0-9._-]+/(sha256-[0-9a-f]{64}|[0-9a-f]{40})`)
)

// Test seams.
var (
	storageProcessArgs = func() ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, "ps", "-axww", "-o", "args=").Output()
	}
	storageProcessAlive = func(pid int) bool {
		if pid <= 0 {
			return false
		}
		err := syscall.Kill(pid, 0)
		return err == nil || errors.Is(err, syscall.EPERM)
	}
	storageGoEnv = func() (gocache, gocacheprog string, err error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, "go", "env", "-json", "GOCACHE", "GOCACHEPROG").Output()
		if err != nil {
			return "", "", fmt.Errorf("go env: %w", err)
		}
		var env struct{ GOCACHE, GOCACHEPROG string }
		if err := json.Unmarshal(output, &env); err != nil {
			return "", "", fmt.Errorf("decode go env: %w", err)
		}
		return env.GOCACHE, env.GOCACHEPROG, nil
	}
	storageCleanVCSBuild = func(path string) bool {
		info, err := buildinfo.ReadFile(path)
		if err != nil {
			return false
		}
		revision, modified := "", ""
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				revision = setting.Value
			case "vcs.modified":
				modified = setting.Value
			}
		}
		return revision != "" && modified == "false"
	}
	storageRetentionNow = time.Now
)

type storageRetentionEvidence struct {
	SchemaVersion  int                     `json:"schema_version"`
	Rule           string                  `json:"rule"`
	Stores         []storageRetentionStore `json:"stores"`
	CandidateCount int                     `json:"candidate_count"`
	CandidateBytes int64                   `json:"candidate_bytes"`
	ProtectedCount int                     `json:"protected_count"`
}

type storageRetentionStore struct {
	Root           string                      `json:"root"`
	Entries        int                         `json:"entries"`
	Kept           int                         `json:"kept"`
	CandidateBytes int64                       `json:"candidate_bytes"`
	Candidates     []storageRetentionCandidate `json:"candidates,omitempty"`
	Protected      map[string]string           `json:"protected,omitempty"`
	Error          string                      `json:"error,omitempty"`
}

type storageRetentionCandidate struct {
	Path    string    `json:"path"`
	Bytes   int64     `json:"bytes"`
	ModTime time.Time `json:"mod_time"`
	Dir     bool      `json:"dir"`
}

type storageRetentionEntry struct {
	name    string
	path    string
	modTime time.Time
	dir     bool
}

func (evidence *storageRetentionEvidence) add(store storageRetentionStore) {
	evidence.Stores = append(evidence.Stores, store)
	evidence.CandidateCount += len(store.Candidates)
	evidence.CandidateBytes += store.CandidateBytes
	evidence.ProtectedCount += len(store.Protected)
}

func (evidence *storageRetentionEvidence) candidates() []storageRetentionCandidate {
	var out []storageRetentionCandidate
	for _, store := range evidence.Stores {
		out = append(out, store.Candidates...)
	}
	return out
}

// storageRetentionBytes returns allocated bytes for a candidate. Files use
// their block count; directories use the shared measured walker.
func storageRetentionBytes(path string, dir bool) int64 {
	if dir {
		size, _, err := storageDirectoryBytes(path)
		if err != nil {
			return 0
		}
		return size
	}
	info, err := os.Lstat(path)
	if err != nil {
		return 0
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return stat.Blocks * 512
	}
	return info.Size()
}

// storageRevisionPins finds revision paths that something outside the store
// names directly: launchd agents (live and backup plists), top-level
// ~/.local/bin links, and running process arguments.
func storageRevisionPins(home string) (map[string]string, error) {
	pins := map[string]string{}
	addMatches := func(text, reason string) {
		for _, match := range storageRevisionPinReference.FindAllString(text, -1) {
			pins[filepath.Join(home, strings.TrimPrefix(match, "/"))] = reason
		}
	}
	agents := filepath.Join(home, "Library", "LaunchAgents")
	if entries, err := os.ReadDir(agents); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			data, readErr := os.ReadFile(filepath.Join(agents, entry.Name()))
			if readErr == nil {
				addMatches(string(data), "launchd:"+entry.Name())
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read launch agents: %w", err)
	}
	binDir := filepath.Join(home, ".local", "bin")
	if entries, err := os.ReadDir(binDir); err == nil {
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink == 0 {
				continue
			}
			target, linkErr := os.Readlink(filepath.Join(binDir, entry.Name()))
			if linkErr != nil {
				continue
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(binDir, target)
			}
			addMatches(target, "link:"+entry.Name())
		}
	}
	args, err := storageProcessArgs()
	if err != nil {
		return nil, fmt.Errorf("read process arguments: %w", err)
	}
	addMatches(string(args), "process")
	// The regex anchors on /.local/bin; rebuild each pin under this home so a
	// path written with another home prefix cannot pin a foreign file.
	normalized := map[string]string{}
	for path, reason := range pins {
		index := strings.Index(path, string(filepath.Separator)+".local"+string(filepath.Separator)+"bin"+string(filepath.Separator))
		if index < 0 {
			continue
		}
		normalized[filepath.Join(home, path[index+1:])] = reason
	}
	return normalized, nil
}

// planRevisionStore applies the binary revision rule to one store. It keeps
// every symlink target in the store (current, previous-*), every pin, every
// entry younger than minAge, the newest `retain` entries and the newest
// `retainClean` clean VCS builds. Everything else is a candidate.
func planRevisionStore(root string, dirs bool, pins map[string]string, now time.Time, minAge time.Duration, retain, retainClean int) storageRetentionStore {
	store := storageRetentionStore{Root: root, Protected: map[string]string{}}
	entries, err := os.ReadDir(root)
	if err != nil {
		store.Error = err.Error()
		return store
	}
	var revisions []storageRetentionEntry
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(root, name)
		info, infoErr := os.Lstat(path)
		if infoErr != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, linkErr := os.Readlink(path)
			if linkErr != nil {
				continue
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(root, target)
			}
			target = filepath.Clean(target)
			if filepath.Dir(target) == root {
				store.Protected[target] = "link:" + name
			}
			continue
		}
		if dirs {
			if !info.IsDir() || !storageReleaseDirPattern.MatchString(name) {
				continue
			}
		} else if !info.Mode().IsRegular() || !storageRevisionFilePattern.MatchString(name) {
			continue
		}
		revisions = append(revisions, storageRetentionEntry{name: name, path: path, modTime: info.ModTime(), dir: dirs})
	}
	store.Entries = len(revisions)
	sort.Slice(revisions, func(i, j int) bool {
		if revisions[i].modTime.Equal(revisions[j].modTime) {
			return revisions[i].name > revisions[j].name
		}
		return revisions[i].modTime.After(revisions[j].modTime)
	})
	for path, reason := range pins {
		if filepath.Dir(path) == root {
			if _, linked := store.Protected[path]; !linked {
				store.Protected[path] = reason
			}
		}
	}
	keptClean := 0
	for index, revision := range revisions {
		if _, protected := store.Protected[revision.path]; protected {
			store.Kept++
			continue
		}
		if index < retain {
			store.Kept++
			continue
		}
		if now.Sub(revision.modTime) < minAge {
			store.Protected[revision.path] = "younger_than_min_age"
			store.Kept++
			continue
		}
		if !dirs && keptClean < retainClean && storageCleanVCSBuild(revision.path) {
			keptClean++
			store.Protected[revision.path] = "newest_clean_vcs_build"
			store.Kept++
			continue
		}
		bytes := storageRetentionBytes(revision.path, revision.dir)
		store.Candidates = append(store.Candidates, storageRetentionCandidate{Path: revision.path, Bytes: bytes, ModTime: revision.modTime, Dir: revision.dir})
		store.CandidateBytes += bytes
	}
	if len(store.Protected) == 0 {
		store.Protected = nil
	}
	return store
}

func binaryRevisionStores(home string) (files, dirs []string) {
	binDir := filepath.Join(home, ".local", "bin")
	entries, err := os.ReadDir(binDir)
	if err != nil {
		return nil, nil
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() {
			continue
		}
		switch {
		case strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".revisions"):
			files = append(files, filepath.Join(binDir, name))
		case name == ".session-pressure-releases":
			dirs = append(dirs, filepath.Join(binDir, name))
		}
	}
	return files, dirs
}

func planBinaryRevisions(home string, minAge time.Duration) (storageRetentionEvidence, error) {
	evidence := storageRetentionEvidence{SchemaVersion: storageRetentionSchema, Rule: fmt.Sprintf("keep current, links, launchd/process pins, entries younger than %s, newest %d and newest %d clean VCS build per store", minAge, storageBinaryRevisionsRetain, storageBinaryRevisionsRetainClean)}
	pins, err := storageRevisionPins(home)
	if err != nil {
		return evidence, err
	}
	now := storageRetentionNow()
	files, dirs := binaryRevisionStores(home)
	for _, root := range files {
		evidence.add(planRevisionStore(root, false, pins, now, minAge, storageBinaryRevisionsRetain, storageBinaryRevisionsRetainClean))
	}
	for _, root := range dirs {
		evidence.add(planRevisionStore(root, true, pins, now, minAge, storageBinaryRevisionsRetain, 0))
	}
	return evidence, nil
}

// planTursoBackups keeps the newest dated daily copies. `pinned` and any
// other non-dated entry stay untouched.
func planTursoBackups(root string, retain int) storageRetentionEvidence {
	evidence := storageRetentionEvidence{SchemaVersion: storageRetentionSchema, Rule: fmt.Sprintf("keep the newest %d dated daily copies; never touch pinned", retain)}
	store := storageRetentionStore{Root: root}
	entries, err := os.ReadDir(root)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			store.Error = err.Error()
		}
		evidence.add(store)
		return evidence
	}
	var days []storageRetentionEntry
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !storageTursoDayPattern.MatchString(entry.Name()) {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		days = append(days, storageRetentionEntry{name: entry.Name(), path: filepath.Join(root, entry.Name()), modTime: info.ModTime(), dir: true})
	}
	sort.Slice(days, func(i, j int) bool { return days[i].name > days[j].name })
	store.Entries = len(days)
	for index, day := range days {
		if index < retain {
			store.Kept++
			continue
		}
		bytes := storageRetentionBytes(day.path, true)
		store.Candidates = append(store.Candidates, storageRetentionCandidate{Path: day.path, Bytes: bytes, ModTime: day.modTime, Dir: true})
		store.CandidateBytes += bytes
	}
	evidence.add(store)
	return evidence
}

// planGoBuildOrphans selects per-process Go caches (~/Library/Caches/
// go-build-<name>-<pid>) whose process is gone. Active caches, the capped
// cache and caches without a pid suffix stay.
func planGoBuildOrphans(cachesRoot string, exclude []string, now time.Time, minAge time.Duration) storageRetentionEvidence {
	evidence := storageRetentionEvidence{SchemaVersion: storageRetentionSchema, Rule: fmt.Sprintf("remove go-build-<name>-<pid> caches whose pid is not alive and that are older than %s", minAge)}
	store := storageRetentionStore{Root: cachesRoot, Protected: map[string]string{}}
	excluded := map[string]bool{}
	for _, path := range exclude {
		if path != "" {
			excluded[filepath.Clean(path)] = true
		}
	}
	entries, err := os.ReadDir(cachesRoot)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			store.Error = err.Error()
		}
		evidence.add(store)
		return evidence
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "go-build-") || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		path := filepath.Join(cachesRoot, name)
		store.Entries++
		if excluded[path] {
			store.Protected[path] = "active_go_cache"
			store.Kept++
			continue
		}
		match := storageGoOrphanPattern.FindStringSubmatch(name)
		if match == nil {
			store.Protected[path] = "no_pid_suffix"
			store.Kept++
			continue
		}
		pid, _ := strconv.Atoi(match[1])
		if storageProcessAlive(pid) {
			store.Protected[path] = "pid_alive"
			store.Kept++
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil || now.Sub(info.ModTime()) < minAge {
			store.Protected[path] = "younger_than_min_age"
			store.Kept++
			continue
		}
		bytes := storageRetentionBytes(path, true)
		store.Candidates = append(store.Candidates, storageRetentionCandidate{Path: path, Bytes: bytes, ModTime: info.ModTime(), Dir: true})
		store.CandidateBytes += bytes
	}
	if len(store.Protected) == 0 {
		store.Protected = nil
	}
	evidence.add(store)
	return evidence
}

// resolveGoCaches returns the GOCACHE directory and, when GOCACHEPROG runs
// gocache-lru, its directory and cap. The 2026-11-09 finding: the provider
// measured ~/Library/Caches/go-build while builds wrote go-build-capped.
func resolveGoCaches(home string) (gocache, cappedDir, cappedMax string, err error) {
	gocache = filepath.Join(home, "Library", "Caches", "go-build")
	value, prog, envErr := storageGoEnv()
	if envErr != nil {
		return gocache, "", "", envErr
	}
	if filepath.IsAbs(value) {
		gocache = filepath.Clean(value)
	}
	fields := strings.Fields(prog)
	if len(fields) == 0 || filepath.Base(fields[0]) != "gocache-lru" {
		return gocache, "", "", nil
	}
	cappedDir = filepath.Join(home, "Library", "Caches", "go-build-capped")
	cappedMax = "16GiB"
	for index := 1; index < len(fields); index++ {
		flag, value, hasValue := strings.Cut(strings.TrimLeft(fields[index], "-"), "=")
		if !hasValue && index+1 < len(fields) {
			value = fields[index+1]
		}
		switch flag {
		case "dir":
			if filepath.IsAbs(value) {
				cappedDir = filepath.Clean(value)
			}
		case "max":
			cappedMax = value
		}
	}
	return gocache, cappedDir, cappedMax, nil
}

func inspectGoBuildCappedProvider(dir, max string, envErr error, measure bool) storageProviderReport {
	report := storageProviderReport{
		ID:                "go-build-capped",
		Classification:    storageProviderReportOnly,
		Summary:           "Active Go build cache served by GOCACHEPROG=gocache-lru; the LRU caps it",
		Path:              dir,
		EstimateKind:      "presence_only",
		MutationSupported: false,
		BlockedReason:     "gocache-lru owns this cache and trims it to -max " + max,
		Overlaps:          []string{"library-caches"},
	}
	if envErr != nil {
		report.BlockedReason = "resolve Go cache: " + envErr.Error()
		return report
	}
	if dir == "" {
		report.Summary = "GOCACHEPROG does not run gocache-lru; Go uses GOCACHE (go-build-cache)"
		report.BlockedReason = "not configured"
		return report
	}
	report.Present = storagePathExists(dir)
	if measure && report.Present {
		size, present, err := storageDirectoryBytes(dir)
		report.Present, report.EstimatedBytes, report.EstimateKind = present, size, "inventory"
		if err != nil {
			report.BlockedReason = err.Error()
		}
	}
	return report
}

func retentionProviderReport(id, summary, path string, evidence storageRetentionEvidence, err error) storageProviderReport {
	report := storageProviderReport{
		ID:                id,
		Classification:    storageProviderOperator,
		Summary:           summary,
		Path:              path,
		Present:           storagePathExists(path),
		EstimatedBytes:    evidence.CandidateBytes,
		EstimateKind:      "exact_gc",
		MutationSupported: evidence.CandidateCount > 0,
		RetentionEvidence: &evidence,
	}
	for _, store := range evidence.Stores {
		if store.Error != "" && report.BlockedReason == "" {
			report.BlockedReason = store.Error
		}
	}
	if err != nil {
		report.BlockedReason = err.Error()
		report.MutationSupported = false
		report.EstimatedBytes = 0
	} else if evidence.CandidateCount == 0 && report.BlockedReason == "" {
		report.BlockedReason = "no entries exceed the retention rule"
	}
	return report
}

func storageRetentionMinAge(minAgeSeconds int) time.Duration {
	minAge := time.Duration(minAgeSeconds) * time.Second
	if minAge < time.Hour {
		minAge = time.Hour
	}
	return minAge
}

// inspectRetentionProviders appends the S2 providers. Retention providers
// always measure their candidates: the sets are small and an unmeasured
// retention provider was the 0-byte bug.
func inspectRetentionProviders(home string, minAgeSeconds int, measure bool) []storageProviderReport {
	minAge := storageRetentionMinAge(minAgeSeconds)
	binaries, binariesErr := planBinaryRevisions(home, minAge)
	turso := filepath.Join(home, ".local", "state", "turso-backups")
	reports := []storageProviderReport{
		retentionProviderReport("binary-revisions", "Superseded installed binary revisions in ~/.local/bin stores", filepath.Join(home, ".local", "bin"), binaries, binariesErr),
		retentionProviderReport("turso-backups", "Older Turso daily backup copies", turso, planTursoBackups(turso, storageTursoBackupsRetain), nil),
	}
	for _, definition := range []struct{ id, summary, path string }{
		{"nicos-dev-tmp", "ndev scratch files (retention 7 days; value review before removal)", filepath.Join(home, ".nicos-dev", "tmp")},
		{"openbook-worktrees", "Openbook worktrees (retention: merged or abandoned; value review)", filepath.Join(home, "tools", "openbook-worktrees")},
		{"docgraph-generations", "Docgraph build generations (retention: last 2)", filepath.Join(home, ".nicos-dev", "docgraph")},
		{"session-workspaces", "Agent session workspaces (retention: closed more than 14 days)", filepath.Join(home, ".nicos-dev", "session-workspaces")},
		{"context-builds", "Context index builds", filepath.Join(home, ".nicos-dev", "context", "builds")},
	} {
		reports = append(reports, inventoryReportOnlyProvider(definition.id, definition.summary, definition.path, measure))
	}
	perf, _ := filepath.Glob(filepath.Join(home, "dev", "perf-*"))
	perfReport := storageProviderReport{ID: "perf-copies", Classification: storageProviderReportOnly, Summary: "Perf and verify repo copies under ~/dev/perf-* (value review before removal)", Path: filepath.Join(home, "dev"), Paths: perf, Present: len(perf) > 0, EstimateKind: "presence_only"}
	if measure && len(perf) > 0 {
		size, _, err := measureStoragePathSet(perf)
		perfReport.EstimatedBytes, perfReport.EstimateKind = size, "inventory"
		if err != nil {
			perfReport.BlockedReason = err.Error()
		}
	}
	return append(reports, perfReport)
}

func inventoryReportOnlyProvider(id, summary, path string, measure bool) storageProviderReport {
	report := storageProviderReport{ID: id, Classification: storageProviderReportOnly, Summary: summary, Path: path, Present: storagePathExists(path), EstimateKind: "presence_only", BlockedReason: "report only: removal needs a value review (G2 packet)"}
	if measure && report.Present {
		size, present, err := storageDirectoryBytes(path)
		report.Present, report.EstimatedBytes, report.EstimateKind = present, size, "inventory"
		if err != nil {
			report.BlockedReason = err.Error()
		}
	}
	return report
}

// executeRetentionProvider re-plans and removes the fresh candidates. Each
// path must sit directly in a planned store root and match the store's name
// pattern; anything else is refused.
func executeRetentionProvider(ctx context.Context, providerID, home string, minAgeSeconds int) error {
	var (
		evidence storageRetentionEvidence
		err      error
	)
	switch providerID {
	case "binary-revisions":
		evidence, err = planBinaryRevisions(home, storageRetentionMinAge(minAgeSeconds))
	case "turso-backups":
		evidence = planTursoBackups(filepath.Join(home, ".local", "state", "turso-backups"), storageTursoBackupsRetain)
	case "go-build-orphans":
		gocache, capped, _, envErr := resolveGoCaches(home)
		if envErr != nil {
			return envErr
		}
		evidence = planGoBuildOrphans(filepath.Join(home, "Library", "Caches"), []string{gocache, capped}, storageRetentionNow(), storageRetentionMinAge(minAgeSeconds))
	default:
		return fmt.Errorf("provider %s is not a retention provider", providerID)
	}
	if err != nil {
		return err
	}
	var failures []error
	for _, store := range evidence.Stores {
		for _, candidate := range store.Candidates {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return errors.Join(append(failures, ctxErr)...)
			}
			if filepath.Dir(candidate.Path) != store.Root || !storageRetentionNameAllowed(providerID, filepath.Base(candidate.Path)) {
				failures = append(failures, fmt.Errorf("refusing %s: outside the store contract", candidate.Path))
				continue
			}
			if candidate.Dir {
				err = storageRemoveAll(candidate.Path)
			} else {
				err = os.Remove(candidate.Path)
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				failures = append(failures, fmt.Errorf("remove %s: %w", candidate.Path, err))
			}
		}
	}
	return errors.Join(failures...)
}

func storageRetentionNameAllowed(providerID, name string) bool {
	switch providerID {
	case "binary-revisions":
		return storageRevisionFilePattern.MatchString(name) || storageReleaseDirPattern.MatchString(name)
	case "turso-backups":
		return storageTursoDayPattern.MatchString(name)
	case "go-build-orphans":
		return storageGoOrphanPattern.MatchString(name)
	}
	return false
}

func storageIsRetentionProvider(id string) bool {
	switch id {
	case "binary-revisions", "turso-backups", "go-build-orphans":
		return true
	}
	return false
}

var storageRunRetention = executeRetentionProvider

// readDeliveryGoCacheTrimBytes reads the commit-delivery worker's last trim
// receipt so `providers` reports a non-zero size without walking the cache.
func readDeliveryGoCacheTrimBytes(receiptPath, cacheDir string) (int64, bool) {
	data, err := os.ReadFile(receiptPath)
	if err != nil {
		return 0, false
	}
	var receipt struct {
		Dir        string `json:"dir"`
		AfterBytes int64  `json:"after_bytes"`
	}
	if json.Unmarshal(data, &receipt) != nil || filepath.Clean(receipt.Dir) != filepath.Clean(cacheDir) || receipt.AfterBytes <= 0 {
		return 0, false
	}
	return receipt.AfterBytes, true
}

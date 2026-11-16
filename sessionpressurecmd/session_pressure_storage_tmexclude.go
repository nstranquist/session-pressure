package sessionpressurecmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// storage tm-exclude plan (repo restructure program S2, ST3). The plan lists
// the fixed Time Machine exclusions (sudo, Nico runs them) and the rebuildable
// folders under the code roots (sticky, no sudo), measures each, and estimates
// the backed-up bytes. It never changes an exclusion.

const tmExcludeSchema = 1

// tmExcludeRebuildableNames are folder names that a build or install
// recreates. A match stops the descent.
var tmExcludeRebuildableNames = map[string]bool{
	"node_modules": true, "target": true, ".build": true, ".next": true, ".venv": true, "venv": true,
	"Pods": true, "DerivedData": true, ".turbo": true, ".wrangler": true, ".svelte-kit": true, ".expo": true,
}

// Folders the walk never enters: VCS metadata and the matched folders.
var tmExcludeSkipNames = map[string]bool{".git": true, ".hg": true, ".jj": true}

type tmExcludeEntry struct {
	Path      string `json:"path"`
	Bytes     int64  `json:"bytes"`
	Kind      string `json:"kind"` // fixed_sudo, rebuildable_sticky
	Present   bool   `json:"present"`
	Error     string `json:"error,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Directive string `json:"directive"`
}

type tmExcludePlan struct {
	SchemaVersion      int              `json:"schema_version"`
	GeneratedAt        time.Time        `json:"generated_at"`
	VolumeUsedBytes    int64            `json:"volume_used_bytes"`
	DefaultExcluded    int64            `json:"default_excluded_bytes"`
	FixedBytes         int64            `json:"fixed_excluded_bytes"`
	RebuildableBytes   int64            `json:"rebuildable_excluded_bytes"`
	EstimatedIncluded  int64            `json:"estimated_included_bytes"`
	TargetIncluded     int64            `json:"target_included_bytes"`
	MeetsTarget        bool             `json:"meets_target"`
	Method             string           `json:"method"`
	Fixed              []tmExcludeEntry `json:"fixed"`
	Rebuildable        []tmExcludeEntry `json:"rebuildable"`
	RebuildableByRoot  map[string]int64 `json:"rebuildable_by_root"`
	WalkErrors         int              `json:"walk_errors"`
	WalkIncompleteRoot []string         `json:"walk_incomplete_roots,omitempty"`
}

func tmExcludeFixedPaths(home string) []string {
	h := func(parts ...string) string { return filepath.Join(append([]string{home}, parts...)...) }
	paths := []string{
		"/Applications", "/opt/homebrew", "/Library/Developer",
		h("go"), h(".nvm"), h(".npm"), h(".cargo"), h(".rustup"), h(".cache"), h(".vscode"), h(".cursor"),
		h("Library", "pnpm"), h("Library", "Developer"), h(".local", "bin"), h(".local", "state", "turso-backups"),
		h(".nicos-dev", "commit-delivery", "v1", "go-build-cache"), h(".nicos-dev", "tmp"), h(".nicos-dev", "docgraph"),
		h(".nicos-dev", "context", "builds"), h("archive"), h("dev", "_worktrees"),
		h("Library", "Application Support", "Steam"),
		// Browser and editor caches outside ~/Library/Caches.
		h("Library", "Application Support", "Google", "Chrome", "Default", "Service Worker", "CacheStorage"),
		h("Library", "Application Support", "Code", "CachedData"), h("Library", "Application Support", "Cursor", "CachedData"),
	}
	return paths
}

func tmExcludeCodeRoots(home string) []string {
	var roots []string
	for _, name := range []string{"dev", "tools", "crypto", "clients", "code", "hackathons", "projects"} {
		roots = append(roots, filepath.Join(home, name))
	}
	return roots
}

// tmExcludeDiscover walks one code root and returns rebuildable folders plus
// ~/dev/perf-* and ~/dev/.ndev-work* copies at the root's top level.
func tmExcludeDiscover(ctx context.Context, root string) ([]string, int, error) {
	var found []string
	errorsSeen := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if walkErr != nil {
			errorsSeen++
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.IsDir() || path == root {
			return nil
		}
		name := entry.Name()
		if tmExcludeSkipNames[name] {
			return fs.SkipDir
		}
		if filepath.Dir(path) == root && filepath.Base(root) == "dev" && (strings.HasPrefix(name, "perf-") || strings.HasPrefix(name, ".ndev-work")) {
			found = append(found, path)
			return fs.SkipDir
		}
		if tmExcludeRebuildableNames[name] {
			found = append(found, path)
			return fs.SkipDir
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil, errorsSeen, nil
	}
	return found, errorsSeen, err
}

func buildTMExcludePlan(ctx context.Context, home string, volumeUsed int64, target int64) tmExcludePlan {
	plan := tmExcludePlan{SchemaVersion: tmExcludeSchema, GeneratedAt: time.Now().UTC(), VolumeUsedBytes: volumeUsed, TargetIncluded: target, RebuildableByRoot: map[string]int64{},
		Method: "estimated_included = data volume used - default exclusions (~/Library/Caches, ~/.Trash) - fixed exclusions - rebuildable folders; system files and APFS clones make this an upper bound"}
	measure := func(path, kind, directive string) tmExcludeEntry {
		entry := tmExcludeEntry{Path: path, Kind: kind, Directive: directive}
		size, present, err := storageDirectoryBytes(path)
		entry.Bytes, entry.Present = size, present
		if err != nil {
			entry.Error = err.Error()
		}
		return entry
	}
	for _, path := range []string{filepath.Join(home, "Library", "Caches"), filepath.Join(home, ".Trash")} {
		plan.DefaultExcluded += measure(path, "default", "").Bytes
	}
	for _, path := range tmExcludeFixedPaths(home) {
		entry := measure(path, "fixed_sudo", "sudo tmutil addexclusion -p "+shellQuote(path))
		if entry.Present {
			plan.FixedBytes += entry.Bytes
		}
		plan.Fixed = append(plan.Fixed, entry)
	}
	for _, root := range tmExcludeCodeRoots(home) {
		found, walkErrors, err := tmExcludeDiscover(ctx, root)
		plan.WalkErrors += walkErrors
		if err != nil {
			plan.WalkIncompleteRoot = append(plan.WalkIncompleteRoot, root+": "+err.Error())
		}
		for _, path := range found {
			entry := measure(path, "rebuildable_sticky", "tmutil addexclusion "+shellQuote(path))
			entry.Reason = filepath.Base(path)
			plan.RebuildableBytes += entry.Bytes
			plan.RebuildableByRoot[root] += entry.Bytes
			plan.Rebuildable = append(plan.Rebuildable, entry)
		}
	}
	sort.Slice(plan.Rebuildable, func(i, j int) bool { return plan.Rebuildable[i].Bytes > plan.Rebuildable[j].Bytes })
	plan.EstimatedIncluded = max(int64(0), volumeUsed-plan.DefaultExcluded-plan.FixedBytes-plan.RebuildableBytes)
	plan.MeetsTarget = plan.EstimatedIncluded <= target
	return plan
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func cmdSessionPressureStorageTMExclude(g *Flags, args []string) int {
	if len(args) == 0 || args[0] != "plan" {
		return sessionPressureError("usage: storage tm-exclude plan [--limit N]  (apply ships after the NicosTM volume exists)", 2)
	}
	limit := 40
	for index := 1; index < len(args); index++ {
		if args[index] == "--limit" && index+1 < len(args) {
			if _, err := fmt.Sscanf(args[index+1], "%d", &limit); err != nil || limit < 0 {
				return sessionPressureError("--limit requires a non-negative integer", 2)
			}
			index++
			continue
		}
		return sessionPressureError("unknown tm-exclude argument "+args[index], 2)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	_, sample, err := loadStorageState()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	plan := buildTMExcludePlan(ctx, home, sample.TotalBytes-sample.AvailableBytes, 300<<30)
	full := len(plan.Rebuildable)
	if limit < full {
		plan.Rebuildable = plan.Rebuildable[:limit]
	}
	text := fmt.Sprintf("tm-exclude plan: used=%.0fGiB default=%.0fGiB fixed=%.0fGiB rebuildable=%.0fGiB (%d folders) estimated_included=%.0fGiB target=%.0fGiB meets=%v\n",
		bytesToGiB(plan.VolumeUsedBytes), bytesToGiB(plan.DefaultExcluded), bytesToGiB(plan.FixedBytes), bytesToGiB(plan.RebuildableBytes), full, bytesToGiB(plan.EstimatedIncluded), bytesToGiB(plan.TargetIncluded), plan.MeetsTarget)
	return emitPressure(g, map[string]any{"ok": true, "action": "storage.tm_exclude.plan", "plan": plan, "rebuildable_total": full}, text, 0)
}

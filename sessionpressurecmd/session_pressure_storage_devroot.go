package sessionpressurecmd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The dev-root-leftovers provider inventories agent scratch left directly in
// ~/dev: mktemp trees, orphaned or stray worktrees, and clones of local repos.
// It is report_only. Its value is the per-entry proof that an operator needs
// before removal: for each entry it resolves an owning repository and counts
// the files whose content was never committed there.
//
// The 2026-11-04 dev-root review did this proof by hand for 60 entries
// (~14 GiB). Every file was hashed with `git hash-object` and looked up in the
// owning repository's object store. A missing object is content that was never
// committed, which is the only place unique work can hide. This provider makes
// that review repeatable.

const (
	devRootEvidenceSchema = 1
	// devRootMaxEntries bounds the evidence payload for one plan.
	devRootMaxEntries = 200
	// devRootMaxFiles bounds one entry's hash audit. A larger tree reports an
	// unknown verdict instead of a partial proof.
	devRootMaxFiles = 400_000
	// devRootSampleLimit bounds the never-committed paths shown per entry.
	devRootSampleLimit = 5
	devRootGitTimeout  = 5 * time.Minute
)

// Closed reason vocabulary for dev-root-leftovers entries.
const (
	devRootReasonOrphanedWorktree = "orphaned-worktree"
	devRootReasonStrayWorktree    = "dev-root-worktree"
	devRootReasonUnfilledTemplate = "unfilled-mktemp-template"
	devRootReasonGoMkdirTemp      = "go-mkdirtemp-name"
	devRootReasonShellMktemp      = "shell-mktemp-name"
	devRootReasonTimestamped      = "timestamped-hidden-scratch"
	devRootReasonLocalClone       = "local-clone"
)

// Closed verdict vocabulary.
const (
	devRootVerdictNoUniqueWork = "no-unique-work"
	devRootVerdictReview       = "review"
	devRootVerdictUnknown      = "unknown"
	devRootVerdictUnmeasured   = "unmeasured"
)

type storageDevRootEvidence struct {
	SchemaVersion     int                   `json:"schema_version"`
	Root              string                `json:"root"`
	Measured          bool                  `json:"measured"`
	EntryCount        int                   `json:"entry_count"`
	NoUniqueWorkCount int                   `json:"no_unique_work_count"`
	ReviewCount       int                   `json:"review_count"`
	UnknownCount      int                   `json:"unknown_count"`
	Truncated         bool                  `json:"truncated,omitempty"`
	Entries           []storageDevRootEntry `json:"entries"`
}

type storageDevRootEntry struct {
	Name       string `json:"name"`
	Reason     string `json:"reason"`
	Kind       string `json:"kind"`
	ModifiedAt string `json:"modified_at,omitempty"`
	Bytes      int64  `json:"bytes,omitempty"`
	Owner      string `json:"owner,omitempty"`
	OwnerBasis string `json:"owner_basis,omitempty"`
	Files      int    `json:"files,omitempty"`
	// IgnoredFiles match the owner's ignore rules. They are derived or local
	// state by the owner's own policy, so they are counted, not proven.
	IgnoredFiles int `json:"ignored_files,omitempty"`
	// NeverCommittedFiles is -1 when no proof ran.
	NeverCommittedFiles  int      `json:"never_committed_files"`
	NeverCommittedSample []string `json:"never_committed_sample,omitempty"`
	MissingCommits       int      `json:"missing_commits,omitempty"`
	Verdict              string   `json:"verdict"`
	Detail               string   `json:"detail,omitempty"`
}

// storageDevRootInspect is replaced in tests so inventory tests never scan the
// real ~/dev.
var storageDevRootInspect = inspectDevRootLeftovers

func storageDevRootProvider(home string, measure bool) storageProviderReport {
	root := filepath.Join(home, "dev")
	report := storageProviderReport{
		ID:                "dev-root-leftovers",
		Classification:    storageProviderReportOnly,
		Summary:           "Agent scratch left directly in ~/dev (mktemp trees, orphaned worktrees, local clones) with per-entry never-committed proof",
		Path:              root,
		Present:           storagePathExists(root),
		EstimateKind:      "inventory",
		MutationSupported: false,
		BlockedReason:     "each entry needs owner review of its never-committed proof; remove worktrees with git worktree remove",
	}
	if !report.Present {
		report.EstimateKind = "presence_only"
		return report
	}
	evidence, err := storageDevRootInspect(context.Background(), root, measure)
	if err != nil {
		report.BlockedReason = err.Error()
		return report
	}
	var total int64
	for _, entry := range evidence.Entries {
		total += entry.Bytes
	}
	report.EstimatedBytes = total
	if !measure {
		report.EstimateKind = "presence_only"
	}
	report.DevRootEvidence = &evidence
	return report
}

var (
	devRootGoTempRE    = regexp.MustCompile(`\.\d{8,12}(\.[A-Za-z0-9]+)?$`)
	devRootShellTempRE = regexp.MustCompile(`\.([A-Za-z0-9]{6})(\.[A-Za-z0-9]+)?$`)
	devRootStampRE     = regexp.MustCompile(`\d{8}T\d{4,6}Z`)
)

// devRootKeepNames are dev-root children that belong there.
var devRootKeepNames = map[string]bool{
	".git": true, ".DS_Store": true, ".claude": true, ".cursor": true, ".vscode": true,
	".idea": true, ".ignore": true, ".codex": true, ".agents": true, "_worktrees": true,
}

// devRootRandomSuffix reports whether a six-character mktemp suffix looks
// random: it mixes at least two of upper case, lower case, and digits. A
// real name such as `notes.backup` or `app.config` does not.
func devRootRandomSuffix(suffix string) bool {
	var upper, lower, digit bool
	for _, r := range suffix {
		switch {
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= '0' && r <= '9':
			digit = true
		}
	}
	classes := 0
	for _, present := range []bool{upper, lower, digit} {
		if present {
			classes++
		}
	}
	return classes >= 2
}

// devRootNameReason classifies an entry by name alone.
func devRootNameReason(name string) string {
	switch {
	case strings.Contains(name, "XXXXXX"):
		return devRootReasonUnfilledTemplate
	case devRootGoTempRE.MatchString(name):
		return devRootReasonGoMkdirTemp
	}
	if match := devRootShellTempRE.FindStringSubmatch(name); match != nil && devRootRandomSuffix(match[1]) {
		return devRootReasonShellMktemp
	}
	if strings.HasPrefix(name, ".") && devRootStampRE.MatchString(name) {
		return devRootReasonTimestamped
	}
	return ""
}

// devRootGitdir reads a `.git` indirection file and returns its absolute
// target, or "" when the entry has no such file.
func devRootGitdir(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, ".git"))
	if err != nil || len(data) > 16<<10 {
		return ""
	}
	line := strings.TrimSpace(strings.SplitN(string(data), "\n", 2)[0])
	target, ok := strings.CutPrefix(line, "gitdir:")
	if !ok {
		return ""
	}
	target = strings.TrimSpace(target)
	if !filepath.IsAbs(target) {
		target = filepath.Join(dir, target)
	}
	return filepath.Clean(target)
}

func devRootIsWorktreeAdmin(gitdir string) bool {
	slash := filepath.ToSlash(gitdir)
	return strings.Contains(slash, "/.git/worktrees/") && !strings.Contains(slash, "/.git/modules/")
}

// devRootLocalOrigin returns the local repository a clone came from, or "".
func devRootLocalOrigin(ctx context.Context, dir string) string {
	out, err := devRootGit(ctx, dir, nil, "config", "--get", "remote.origin.url")
	if err != nil {
		return ""
	}
	origin := strings.TrimSpace(string(out))
	origin = strings.TrimPrefix(origin, "file://")
	if !filepath.IsAbs(origin) {
		return ""
	}
	return filepath.Clean(strings.TrimSuffix(origin, "/."))
}

type devRootCandidate struct {
	entry storageDevRootEntry
	path  string
	// gitReadable is true when git can open the entry's repository.
	gitReadable bool
}

// inspectDevRootLeftovers classifies root's direct children and, when measure
// is set, sizes each leftover and proves which files were never committed.
func inspectDevRootLeftovers(ctx context.Context, root string, measure bool) (storageDevRootEvidence, error) {
	evidence := storageDevRootEvidence{SchemaVersion: devRootEvidenceSchema, Root: root, Measured: measure, Entries: []storageDevRootEntry{}}
	children, err := os.ReadDir(root)
	if err != nil {
		return evidence, fmt.Errorf("read dev root: %w", err)
	}
	var candidates []devRootCandidate
	var owners []string
	for _, child := range children {
		name := child.Name()
		if devRootKeepNames[name] {
			continue
		}
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&fs.ModeSymlink != 0 {
			continue
		}
		candidate := devRootCandidate{path: path, entry: storageDevRootEntry{Name: name, Kind: "file", ModifiedAt: info.ModTime().UTC().Format(time.RFC3339), NeverCommittedFiles: -1}}
		if info.IsDir() {
			candidate.entry.Kind = "dir"
			if gitdir := devRootGitdir(path); gitdir != "" && devRootIsWorktreeAdmin(gitdir) {
				candidate.entry.Kind = "worktree"
				if _, statErr := os.Stat(gitdir); errors.Is(statErr, fs.ErrNotExist) {
					candidate.entry.Reason = devRootReasonOrphanedWorktree
					candidate.entry.Detail = "worktree admin dir " + gitdir + " is missing"
				} else {
					candidate.entry.Reason = devRootReasonStrayWorktree
					candidate.gitReadable = true
					// The admin dir is <repo>/.git/worktrees/<name>.
					candidate.entry.Owner = filepath.Dir(filepath.Dir(filepath.Dir(gitdir)))
					candidate.entry.OwnerBasis = "worktree-common-dir"
				}
			} else if gitInfo, statErr := os.Stat(filepath.Join(path, ".git")); statErr == nil && gitInfo.IsDir() {
				candidate.entry.Kind = "git-repo"
				candidate.gitReadable = true
				if origin := devRootLocalOrigin(ctx, path); origin != "" {
					candidate.entry.Reason = devRootReasonLocalClone
					candidate.entry.Owner = origin
					candidate.entry.OwnerBasis = "local-origin"
				} else if reason := devRootNameReason(name); reason == "" {
					owners = append(owners, path)
				}
			}
		}
		if candidate.entry.Reason == "" {
			candidate.entry.Reason = devRootNameReason(name)
		}
		if candidate.entry.Reason == "" {
			continue
		}
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].entry.Name < candidates[j].entry.Name })
	if len(candidates) > devRootMaxEntries {
		candidates = candidates[:devRootMaxEntries]
		evidence.Truncated = true
	}
	owners = append(owners, devRootToolRepos(root)...)
	ownerLayouts := map[string]map[string]bool{}
	for _, owner := range owners {
		ownerLayouts[owner] = devRootTopLevel(owner)
	}
	for _, candidate := range candidates {
		entry := candidate.entry
		if entry.Owner == "" && entry.Kind != "file" {
			if owner := devRootLayoutOwner(devRootTopLevel(candidate.path), ownerLayouts); owner != "" {
				entry.Owner = owner
				entry.OwnerBasis = "layout-match"
			}
		}
		if !measure {
			entry.Verdict = devRootVerdictUnmeasured
		} else {
			if size, _, err := storageDirectoryBytes(candidate.path); err == nil {
				entry.Bytes = size
			} else if info, statErr := os.Stat(candidate.path); statErr == nil && !info.IsDir() {
				entry.Bytes = info.Size()
			}
			devRootProve(ctx, candidate, &entry)
		}
		switch entry.Verdict {
		case devRootVerdictNoUniqueWork:
			evidence.NoUniqueWorkCount++
		case devRootVerdictReview:
			evidence.ReviewCount++
		case devRootVerdictUnknown:
			evidence.UnknownCount++
		}
		evidence.Entries = append(evidence.Entries, entry)
	}
	evidence.EntryCount = len(evidence.Entries)
	return evidence, nil
}

// devRootToolRepos lists the repositories under ~/tools, a second home for
// the owners of dev-root copies (release staging copies tools/ repos).
func devRootToolRepos(root string) []string {
	tools := filepath.Join(filepath.Dir(root), "tools")
	children, err := os.ReadDir(tools)
	if err != nil {
		return nil
	}
	var repos []string
	for _, child := range children {
		path := filepath.Join(tools, child.Name())
		if info, err := os.Stat(filepath.Join(path, ".git")); err == nil && info.IsDir() {
			repos = append(repos, path)
		}
	}
	return repos
}

func devRootTopLevel(dir string) map[string]bool {
	names := map[string]bool{}
	children, err := os.ReadDir(dir)
	if err != nil {
		return names
	}
	for _, child := range children {
		if child.Name() == ".git" {
			continue
		}
		names[child.Name()] = true
	}
	return names
}

// devRootLayoutOwner picks the owner whose top-level layout best matches the
// candidate's. A copy of a repository keeps the same top-level names, so a
// Jaccard index of at least 0.6 over at least five shared names is a strong
// signal. Ties and weak matches return "" (unknown owner).
func devRootLayoutOwner(layout map[string]bool, owners map[string]map[string]bool) string {
	best, bestScore, tie := "", 0.0, false
	for owner, ownerLayout := range owners {
		shared := 0
		for name := range layout {
			if ownerLayout[name] {
				shared++
			}
		}
		union := len(layout) + len(ownerLayout) - shared
		if shared < 5 || union == 0 {
			continue
		}
		score := float64(shared) / float64(union)
		switch {
		case score > bestScore:
			best, bestScore, tie = owner, score, false
		case score == bestScore:
			tie = true
		}
	}
	if bestScore < 0.6 || tie {
		return ""
	}
	return best
}

// devRootProve fills the never-committed proof for one entry.
func devRootProve(ctx context.Context, candidate devRootCandidate, entry *storageDevRootEntry) {
	entry.Verdict = devRootVerdictUnknown
	if entry.Owner == "" {
		entry.Detail = strings.TrimSpace(entry.Detail + "; no owning repository found")
		entry.Detail = strings.TrimPrefix(entry.Detail, "; ")
		return
	}
	if info, err := os.Stat(filepath.Join(entry.Owner, ".git")); err != nil || !info.IsDir() {
		entry.Detail = "owning repository " + entry.Owner + " is not a readable git checkout"
		return
	}
	files, err := devRootFiles(candidate.path)
	if err != nil {
		entry.Detail = err.Error()
		return
	}
	entry.Files = len(files)
	tracked, ignored, err := devRootSplitIgnored(ctx, entry.Owner, files)
	if err != nil {
		entry.Detail = "check-ignore: " + err.Error()
		return
	}
	entry.IgnoredFiles = ignored
	missing, err := devRootMissingBlobs(ctx, entry.Owner, candidate.path, tracked)
	if err != nil {
		entry.Detail = "hash audit: " + err.Error()
		return
	}
	entry.NeverCommittedFiles = len(missing)
	if len(missing) > devRootSampleLimit {
		missing = missing[:devRootSampleLimit]
	}
	entry.NeverCommittedSample = missing
	if candidate.gitReadable {
		missingCommits, err := devRootMissingCommits(ctx, entry.Owner, candidate.path)
		if err != nil {
			entry.Detail = "commit audit: " + err.Error()
			return
		}
		entry.MissingCommits = missingCommits
	}
	if entry.NeverCommittedFiles == 0 && entry.MissingCommits == 0 {
		entry.Verdict = devRootVerdictNoUniqueWork
	} else {
		entry.Verdict = devRootVerdictReview
	}
}

// devRootFiles lists regular files below dir (or dir itself when it is a
// file) as slash-separated relative paths. Nested .git directories are git
// metadata, not work, and are skipped.
func devRootFiles(path string) ([]string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{filepath.Base(path)}, nil
	}
	var files []string
	walkErr := filepath.WalkDir(path, func(current string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		// `.git` is metadata: a directory in a clone, a gitdir pointer file in
		// a worktree. Neither is work.
		if d.Name() == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(path, current)
		if relErr != nil {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		if len(files) > devRootMaxFiles {
			return fmt.Errorf("more than %d files; proof skipped", devRootMaxFiles)
		}
		return nil
	})
	return files, walkErr
}

// devRootSplitIgnored separates files that the owner's ignore rules match.
func devRootSplitIgnored(ctx context.Context, owner string, files []string) ([]string, int, error) {
	if len(files) == 0 {
		return nil, 0, nil
	}
	out, err := devRootGit(ctx, owner, strings.NewReader(strings.Join(files, "\n")+"\n"), "check-ignore", "--no-index", "--stdin")
	if err != nil {
		var exitErr *exec.ExitError
		// Exit 1 means no path is ignored.
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			return nil, 0, err
		}
	}
	ignored := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			ignored[line] = true
		}
	}
	var tracked []string
	for _, file := range files {
		if !ignored[file] {
			tracked = append(tracked, file)
		}
	}
	return tracked, len(files) - len(tracked), nil
}

// devRootMissingBlobs hashes each file and returns those whose content is not
// in the owner's object store.
func devRootMissingBlobs(ctx context.Context, owner, base string, files []string) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}
	info, err := os.Stat(base)
	if err != nil {
		return nil, err
	}
	var paths strings.Builder
	for _, file := range files {
		if info.IsDir() {
			paths.WriteString(filepath.Join(base, filepath.FromSlash(file)))
		} else {
			paths.WriteString(base)
		}
		paths.WriteByte('\n')
	}
	hashes, err := devRootGit(ctx, owner, strings.NewReader(paths.String()), "hash-object", "--no-filters", "--stdin-paths")
	if err != nil {
		return nil, err
	}
	hashLines := strings.Fields(string(hashes))
	if len(hashLines) != len(files) {
		return nil, fmt.Errorf("hash-object returned %d hashes for %d files", len(hashLines), len(files))
	}
	types, err := devRootGit(ctx, owner, strings.NewReader(strings.Join(hashLines, "\n")+"\n"), "cat-file", "--batch-check=%(objecttype)")
	if err != nil {
		return nil, err
	}
	var missing []string
	scanner := bufio.NewScanner(bytes.NewReader(types))
	index := 0
	for scanner.Scan() {
		if index >= len(files) {
			break
		}
		if strings.HasSuffix(scanner.Text(), " missing") {
			missing = append(missing, files[index])
		}
		index++
	}
	if index != len(files) {
		return nil, fmt.Errorf("cat-file returned %d rows for %d hashes", index, len(files))
	}
	return missing, nil
}

// devRootMissingCommits counts the entry's ref tips and HEAD that the owner's
// object store does not contain.
func devRootMissingCommits(ctx context.Context, owner, dir string) (int, error) {
	refs, err := devRootGit(ctx, dir, nil, "for-each-ref", "--format=%(objectname)")
	if err != nil {
		return 0, err
	}
	commits := map[string]bool{}
	for _, ref := range strings.Fields(string(refs)) {
		commits[ref] = true
	}
	if head, err := devRootGit(ctx, dir, nil, "rev-parse", "--verify", "-q", "HEAD"); err == nil {
		commits[strings.TrimSpace(string(head))] = true
	}
	if len(commits) == 0 {
		return 0, nil
	}
	var input strings.Builder
	for commit := range commits {
		input.WriteString(commit + "\n")
	}
	out, err := devRootGit(ctx, owner, strings.NewReader(input.String()), "cat-file", "--batch-check=%(objecttype)")
	if err != nil {
		return 0, err
	}
	return strings.Count(string(out), " missing"), nil
}

func devRootGit(ctx context.Context, dir string, stdin *strings.Reader, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, devRootGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return out, err
		}
		return out, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

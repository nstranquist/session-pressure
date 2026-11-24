package sessionpressure

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// SourceRepoEnv explicitly points doctor at the session-pressure source
	// checkout for commit-lag reporting. Set to "off" or "none" to disable.
	SourceRepoEnv = "SESSION_PRESSURE_SOURCE_REPO"

	sourceLagTimeout = 2 * time.Second
)

// SourceLag compares the installed session-pressure commit against committed
// HEAD in the local session-pressure source repository. When no source
// repository or installed commit identity is available, Checked is false and
// doctor stays quiet (no false alarms on packaged or non-git hosts).
type SourceLag struct {
	Checked             bool   `json:"checked"`
	Status              string `json:"status"`
	RepoRoot            string `json:"repo_root,omitempty"`
	SourceRevision      string `json:"source_revision,omitempty"`
	InstalledRevision   string `json:"installed_revision,omitempty"`
	InstalledDirty      bool   `json:"installed_dirty,omitempty"`
	CommitsBehind       int    `json:"commits_behind,omitempty"`
	CommitsAhead        int    `json:"commits_ahead,omitempty"`
	SourceCommitTime    string `json:"source_commit_time,omitempty"`
	InstalledCommitTime string `json:"installed_commit_time,omitempty"`
	Detail              string `json:"detail,omitempty"`
	Remediation         string `json:"remediation,omitempty"`
}

// DiscoverSourceRepo locates a local git checkout of this module without ever
// mistaking another repository (such as nicos-tools) for session-pressure.
func DiscoverSourceRepo(hints ...string) string {
	if env, ok := os.LookupEnv(SourceRepoEnv); ok {
		trimmed := strings.TrimSpace(env)
		switch strings.ToLower(trimmed) {
		case "", "off", "none", "0", "false":
			return ""
		default:
			if isSessionPressureGitRepo(trimmed) {
				return filepath.Clean(trimmed)
			}
			return ""
		}
	}
	candidates := make([]string, 0, len(hints)+3)
	candidates = append(candidates, hints...)
	if cwd, err := os.Getwd(); err == nil && cwd != "" {
		if top := gitToplevel(context.Background(), cwd); top != "" {
			candidates = append(candidates, top)
		}
		candidates = append(candidates, cwd)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, filepath.Join(home, "dev", "tools", "session-pressure"))
	}
	seen := map[string]bool{}
	for _, raw := range candidates {
		candidate := strings.TrimSpace(raw)
		if candidate == "" {
			continue
		}
		cleaned := filepath.Clean(candidate)
		if seen[cleaned] {
			continue
		}
		seen[cleaned] = true
		if isSessionPressureGitRepo(cleaned) {
			return cleaned
		}
	}
	return ""
}

func isSessionPressureGitRepo(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return false
	}
	body, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		return line == "module "+sourceRevisionModule
	}
	return false
}

func gitToplevel(ctx context.Context, dir string) string {
	out, err := runGit(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	return out
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmdCtx, cancel := context.WithTimeout(ctx, sourceLagTimeout)
	defer cancel()
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(cmdCtx, "git", full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(stdout.String()), nil
}

// InspectSourceLag checks how far behind (or ahead of) committed source HEAD
// the installed session-pressure revision is. It fails open (Checked=false)
// when no valid source repo, git binary, or commit ancestry is available so
// packaged hosts never see false alarms.
func InspectSourceLag(ctx context.Context, installedRevision string, hints ...string) SourceLag {
	repo := DiscoverSourceRepo(hints...)
	if repo == "" {
		return SourceLag{
			Status: "unavailable",
			Detail: "no local session-pressure source repository detected",
		}
	}
	return InspectSourceLagAtRepo(ctx, installedRevision, repo)
}

// InspectSourceLagAtRepo compares installedRevision against HEAD in repo.
func InspectSourceLagAtRepo(ctx context.Context, installedRevision, repo string) SourceLag {
	lag := SourceLag{
		Status:            "unknown",
		RepoRoot:          filepath.Clean(repo),
		InstalledRevision: strings.TrimSpace(installedRevision),
	}
	if !isSessionPressureGitRepo(lag.RepoRoot) {
		lag.Status = "unavailable"
		lag.RepoRoot = ""
		lag.Detail = "source repository is not a session-pressure git checkout"
		return lag
	}
	head, err := runGit(ctx, lag.RepoRoot, "rev-parse", "HEAD")
	if err != nil || head == "" {
		lag.Status = "unavailable"
		lag.Detail = "unable to read git HEAD from source repository"
		return lag
	}
	lag.SourceRevision = head
	if headTime, err := runGit(ctx, lag.RepoRoot, "show", "-s", "--format=%cI", head); err == nil {
		lag.SourceCommitTime = headTime
	}
	if lag.InstalledRevision == "" {
		lag.Detail = "installed binary carries no source revision"
		return lag
	}
	base, dirty := strings.CutSuffix(lag.InstalledRevision, "-dirty")
	lag.InstalledDirty = dirty
	if base == head {
		lag.Checked = true
		lag.InstalledCommitTime = lag.SourceCommitTime
		if dirty {
			lag.Status = "dirty"
			lag.Detail = fmt.Sprintf("installed session-pressure (%s) matches source HEAD but was built from a dirty checkout", ShortRevision(lag.InstalledRevision))
			lag.Remediation = sourceLagRemediation(lag.RepoRoot)
		} else {
			lag.Status = "current"
			lag.Detail = fmt.Sprintf("installed session-pressure (%s) matches source HEAD", ShortRevision(head))
		}
		return lag
	}
	kind, err := runGit(ctx, lag.RepoRoot, "cat-file", "-t", base)
	if err != nil || kind != "commit" {
		lag.Detail = fmt.Sprintf("installed revision %s is not present in local git history", ShortRevision(lag.InstalledRevision))
		return lag
	}
	if installedTime, err := runGit(ctx, lag.RepoRoot, "show", "-s", "--format=%cI", base); err == nil {
		lag.InstalledCommitTime = installedTime
	}
	counts, err := runGit(ctx, lag.RepoRoot, "rev-list", "--left-right", "--count", base+"..."+head)
	if err != nil {
		lag.Detail = "unable to compute commit distance from installed revision to source HEAD"
		return lag
	}
	fields := strings.Fields(counts)
	if len(fields) != 2 {
		lag.Detail = "unexpected git rev-list output"
		return lag
	}
	ahead, errA := strconv.Atoi(fields[0])
	behind, errB := strconv.Atoi(fields[1])
	if errA != nil || errB != nil {
		lag.Detail = "unable to parse commit distance from installed revision to source HEAD"
		return lag
	}
	lag.Checked = true
	lag.CommitsAhead = ahead
	lag.CommitsBehind = behind
	switch {
	case behind > 0 && ahead == 0:
		lag.Status = "behind"
		lag.Detail = fmt.Sprintf("installed session-pressure (%s) is %d %s behind source HEAD (%s)",
			ShortRevision(lag.InstalledRevision), behind, pluralWord(behind, "commit", "commits"), ShortRevision(head))
		lag.Remediation = sourceLagRemediation(lag.RepoRoot)
	case behind == 0 && ahead > 0:
		lag.Status = "ahead"
		lag.Detail = fmt.Sprintf("installed session-pressure (%s) is %d %s ahead of local source HEAD (%s)",
			ShortRevision(lag.InstalledRevision), ahead, pluralWord(ahead, "commit", "commits"), ShortRevision(head))
	case behind > 0 && ahead > 0:
		lag.Status = "diverged"
		lag.Detail = fmt.Sprintf("installed session-pressure (%s) diverged from source HEAD (%s): %d behind, %d ahead",
			ShortRevision(lag.InstalledRevision), ShortRevision(head), behind, ahead)
		lag.Remediation = sourceLagRemediation(lag.RepoRoot)
	default:
		lag.Status = "current"
		lag.Detail = fmt.Sprintf("installed session-pressure (%s) matches source HEAD", ShortRevision(head))
	}
	return lag
}

func sourceLagRemediation(repoRoot string) string {
	if command := strings.TrimSpace(os.Getenv(PromoteCommandEnv)); command != "" {
		return command
	}
	if repoRoot != "" {
		return fmt.Sprintf("make -C %s release-promote", repoRoot)
	}
	return "make release-promote"
}

func pluralWord(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

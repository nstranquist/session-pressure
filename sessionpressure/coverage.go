package sessionpressure

import (
	"os"
	"path/filepath"
	"strings"
)

type CoverageState string

const (
	CoverageEnforced    CoverageState = "enforced"
	CoverageCoordinated CoverageState = "coordinated"
	CoverageObserved    CoverageState = "observed"
	CoverageAttention   CoverageState = "attention"
)

// CoverageSurface makes prevention and observation boundaries machine-readable
// so a healthy monitor cannot be mistaken for universal command interception.
type CoverageSurface struct {
	ID     string        `json:"id"`
	Label  string        `json:"label"`
	State  CoverageState `json:"state"`
	Scope  string        `json:"scope"`
	Detail string        `json:"detail"`
}

type CoverageReport struct {
	Status      string            `json:"status"`
	RepoRoot    string            `json:"repo_root,omitempty"`
	Surfaces    []CoverageSurface `json:"surfaces"`
	Limitations []string          `json:"limitations"`
}

// CoverageAssessment optionally carries live process/tree evidence so the
// agent-identity surface can report unlabeled basename misses.
type CoverageAssessment struct {
	RepoRoot  string
	Policy    Policy
	Health    GuardHealth
	Home      string
	Trees     []AgentTree
	Processes []Process
	Catalog   *AgentIdentityCatalog
}

func AssessCoverage(repoRoot string, policy Policy, health GuardHealth) CoverageReport {
	return AssessCoverageDetailed(CoverageAssessment{RepoRoot: repoRoot, Policy: policy, Health: health})
}

// AssessCoverageDetailed is the full coverage path used by doctor/board when a
// latest snapshot is available.
func AssessCoverageDetailed(in CoverageAssessment) CoverageReport {
	repoRoot := in.RepoRoot
	policy := in.Policy
	health := in.Health
	report := CoverageReport{
		Status:   "ready-with-explicit-boundaries",
		RepoRoot: repoRoot,
		Limitations: []string{
			"Direct external agent launches and unrelated applications are observed through host metrics but are not intercepted.",
			"Core automatic relief is limited to freshly revalidated agent trees; separately governed typed cleanup requires native provider identity plus active/stale resource claims.",
			"When every live host probe fails and no recent resident evidence exists, admission remains visibly fail-open to avoid making the guard a host-wide availability dependency.",
			"Agent tree ownership is catalog-driven (exact names, install roots, path probes, optional operator overlay, session-state hints); unknown install layouts need an overlay under agent-identity.json.",
		},
	}

	hostState := CoverageAttention
	hostDetail := "resident monitor health or immutable artifact verification needs attention"
	if health.MonitorHealthy {
		hostState = CoverageObserved
		hostDetail = "machine-wide memory, CPU, swap, storage, and privacy-safe process attribution are sampled by the resident"
	}
	report.Surfaces = append(report.Surfaces, CoverageSurface{
		ID: "host_observation", Label: "Whole-host observation", State: hostState, Scope: "machine", Detail: hostDetail,
	})

	launchState := CoverageObserved
	launchDetail := "canonical launch checks are observe-only under the effective policy"
	if policy.EnforceAdmission {
		launchState = CoverageEnforced
		launchDetail = "canonical ndev agent launches block at the configured red threshold"
	}
	report.Surfaces = append(report.Surfaces,
		CoverageSurface{ID: "canonical_agent_launch", Label: "Canonical agent launches", State: launchState, Scope: "ndev launch paths", Detail: launchDetail},
		CoverageSurface{ID: "coordinated_work", Label: "Weighted heavy work", State: CoverageCoordinated, Scope: "ndev work run", Detail: "build, test, browser, emulator, benchmark, reclaim, and heavy classes share one host capacity queue"},
	)
	storageState := CoverageObserved
	storageDetail := "storage is measured separately; disk-growing work admission is disabled by explicit policy"
	if policy.Storage.EnforceAdmission {
		storageState = CoverageEnforced
		storageDetail = "configured disk-growing work classes block at the storage threshold; storage never grants process-termination authority"
	}
	report.Surfaces = append(report.Surfaces, CoverageSurface{
		ID: "storage_admission", Label: "Storage pressure", State: storageState, Scope: "disk-growing work", Detail: storageDetail,
	})

	claude := assessToolguard(repoRoot, ".claude/settings.json", ".claude/hooks/toolguard.sh")
	claude.ID, claude.Label, claude.Scope = "claude_toolguard", "Claude repo toolguard", "optional factory adapter"
	codex := assessToolguard(repoRoot, ".codex/hooks.json", ".codex/hooks/toolguard.sh")
	codex.ID, codex.Label, codex.Scope = "codex_toolguard", "Codex repo toolguard", "optional factory adapter"
	home := strings.TrimSpace(in.Home)
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	catalog := in.Catalog
	if catalog == nil {
		catalog = ActiveAgentIdentityCatalog()
	}
	report.Surfaces = append(report.Surfaces, claude, codex, assessAgentShims(home),
		AssessAgentIdentityCoverage(catalog, home, in.Trees, in.Processes),
		CoverageSurface{ID: "direct_external_launch", Label: "Direct external launches", State: CoverageObserved, Scope: "machine", Detail: "visible in whole-host metrics and attribution; not globally intercepted"},
		CoverageSurface{ID: "external_applications", Label: "Unrelated applications", State: CoverageObserved, Scope: "machine", Detail: "visible in privacy-safe host-consumer buckets; no automatic termination authority"},
		reliefCoverage(policy, health),
		probeFailureCoverage(policy),
	)

	for _, surface := range report.Surfaces {
		if surface.State == CoverageAttention {
			report.Status = "attention"
			break
		}
	}
	if !health.OperatorReady {
		report.Status = "attention"
	}
	return report
}

func reliefCoverage(policy Policy, health GuardHealth) CoverageSurface {
	surface := CoverageSurface{
		ID: "relief_authority", Label: "Automatic relief authority", State: CoverageObserved, Scope: "agent trees plus separately governed typed resources",
		Detail: "core agent-tree relief is disabled by explicit policy; typed resource cleanup has an independent observe/enforce policy",
	}
	if !policy.AutoShedCritical {
		return surface
	}
	if !health.DailyDriverReady {
		surface.State = CoverageAttention
		surface.Detail = "automatic relief is configured but the resident has not earned daily-driver action authority"
		return surface
	}
	surface.State = CoverageEnforced
	surface.Detail = "one old quiescent agent tree at sustained critical; typed resource cleanup is independently policy- and claim-governed at memory red"
	return surface
}

func probeFailureCoverage(policy Policy) CoverageSurface {
	surface := CoverageSurface{
		ID: "probe_failure_fallback", Label: "Probe-failure fallback", State: CoverageObserved, Scope: "canonical admission",
		Detail: "recent resident evidence remains visible after a live probe failure, but admission enforcement is disabled",
	}
	if policy.EnforceAdmission {
		surface.State = CoverageEnforced
		surface.Detail = "recent resident red or critical evidence remains enforceable when a live probe fails; otherwise failure is explicit and fail-open"
	}
	return surface
}

func assessAgentShims(home string) CoverageSurface {
	surface := CoverageSurface{
		ID: "shell_agent_shims", Label: "Shell agent commands", State: CoverageAttention,
		Scope: "interactive PATH", Detail: "one or more canonical agent shims are missing or not executable",
	}
	if strings.TrimSpace(home) == "" {
		return surface
	}
	dir := filepath.Join(home, ".nicos-dev", "agent-shims")
	for _, name := range []string{"cdx", "codex", "cld", "claude", "grok", "kimi"} {
		if !isExecutableFile(filepath.Join(dir, name)) {
			return surface
		}
	}
	surface.State = CoverageEnforced
	surface.Detail = "canonical interactive command names route through ndev admission; absolute provider paths and GUI launches can still bypass"
	return surface
}

func assessToolguard(repoRoot, settingsRelative, hookRelative string) CoverageSurface {
	// Toolguard stays in nicos-tools. Missing wiring is observed, not an
	// extract attention item that tells operators to build the factory binary.
	surface := CoverageSurface{State: CoverageObserved, Detail: "Toolguard stays in nicos-tools; the open extract does not require it"}
	if strings.TrimSpace(repoRoot) == "" {
		return surface
	}
	settingsPath := filepath.Join(repoRoot, settingsRelative)
	hookPath := filepath.Join(repoRoot, hookRelative)
	toolPath := filepath.Join(repoRoot, "nicos-dev", "bin", "toolguard")
	body, err := os.ReadFile(settingsPath)
	if err != nil || !strings.Contains(string(body), "toolguard.sh") || !isExecutableFile(hookPath) || !isExecutableFile(toolPath) {
		return surface
	}
	surface.State = CoverageEnforced
	surface.Detail = "repo hook routes raw heavy commands through the canonical weighted-work guard"
	return surface
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

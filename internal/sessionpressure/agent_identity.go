package sessionpressure

import (
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Agent identity is the long-term process ownership catalog for coding-agent
// trees. Exact basenames, install roots, and path-probe shapes live in one
// table so Claude SemVer binaries, Grok versioned downloads, node wrappers,
// and operator install overlays do not accumulate one-off if-ladders.
//
// Safety: only processes that match exact names, node script basenames, or a
// resolved path under a trusted home-relative install root become agent-owned
// (and therefore relief-eligible). Overlays may only expand that set under
// fail-closed validation.

const (
	agentIdentitySchemaVersion = 1
	agentIdentityOverlayName   = "agent-identity.json"
)

// AgentIdentityRule is one coding-agent host identity.
type AgentIdentityRule struct {
	// Agent is the stable agent id projected on trees (codex, claude, grok, kimi).
	Agent string `json:"agent"`
	// ExactBasenames match p_comm / argv basenames without a path syscall.
	ExactBasenames []string `json:"exact_basenames,omitempty"`
	// PathProbeSemVer resolves the executable path when p_comm is pure SemVer
	// (Claude provider binaries named 2.1.211).
	PathProbeSemVer bool `json:"path_probe_semver,omitempty"`
	// PathProbePrefixes resolve the executable path when p_comm starts with one
	// of these prefixes (Grok p_comm "grok-0.2.118-mac").
	PathProbePrefixes []string `json:"path_probe_prefixes,omitempty"`
	// PathProbePrefixRequiresDigit requires the character after the matched
	// prefix to be a digit so "grok-helper" stays out.
	PathProbePrefixRequiresDigit bool `json:"path_probe_prefix_requires_digit,omitempty"`
	// InstallPathPrefixes are home-relative directory prefixes for trusted
	// resolved binaries (slash-normalized, no leading slash).
	InstallPathPrefixes []string `json:"install_path_prefixes,omitempty"`
	// InstallPathExact are home-relative exact paths (for stable symlinks).
	InstallPathExact []string `json:"install_path_exact,omitempty"`
	// NodeScriptBasenames are first-positional node scripts that establish
	// identity (node /opt/bin/codex).
	NodeScriptBasenames []string `json:"node_script_basenames,omitempty"`
}

// AgentIdentityOverlay is the optional operator file under the session-pressure
// state directory. It only expands the shipped catalog.
type AgentIdentityOverlay struct {
	SchemaVersion int                 `json:"schema_version"`
	Rules         []AgentIdentityRule `json:"rules"`
}

// AgentIdentityCatalog is the compiled, fail-closed identity table.
type AgentIdentityCatalog struct {
	SchemaVersion int
	Rules         []AgentIdentityRule
	OverlayLoaded bool
	OverlayPath   string
	OverlayError  string

	exact          map[string]string // basename -> agent
	nodeScripts    map[string]string // basename -> agent
	pathProbeExact map[string]string // basename needing path probe (unused reserved)
	pathPrefixes   []pathProbePrefix
	pathSemVer     bool
	installPrefix  []installPathRule
	installExact   map[string]string // home-relative lower path -> agent
	agents         map[string]struct{}
}

type pathProbePrefix struct {
	prefix       string
	requireDigit bool
	agent        string
}

type installPathRule struct {
	prefix string // home-relative lower, with trailing /
	agent  string
}

var (
	agentIdentityMu       sync.RWMutex
	agentIdentityCached   *AgentIdentityCatalog
	agentIdentityCacheKey string
	agentIdentityCacheAt  time.Time
)

// DefaultAgentIdentityRules is the shipped catalog. Version numbers are never
// hard-coded; only install layout shapes and stable names are.
func DefaultAgentIdentityRules() []AgentIdentityRule {
	return []AgentIdentityRule{
		{
			Agent:               "codex",
			ExactBasenames:      []string{"codex"},
			NodeScriptBasenames: []string{"codex"},
		},
		{
			Agent:               "claude",
			ExactBasenames:      []string{"claude"},
			PathProbeSemVer:     true,
			InstallPathPrefixes: []string{".local/share/claude/versions/"},
			NodeScriptBasenames: []string{"claude"},
		},
		{
			Agent:                        "grok",
			ExactBasenames:               []string{"grok"},
			PathProbePrefixes:            []string{"grok-"},
			PathProbePrefixRequiresDigit: true,
			InstallPathPrefixes:          []string{".grok/downloads/"},
			InstallPathExact:             []string{".grok/bin/grok", ".grok/bin/agent"},
			NodeScriptBasenames:          []string{"grok"},
		},
		{
			// Host evidence (2026-08-04): ~/.kimi-code/bin/kimi is a real Mach-O
			// install; ~/.kimi holds config/sessions only (no versioned download
			// tree observed). Keep exact/node names and the install exact path.
			Agent:               "kimi",
			ExactBasenames:      []string{"kimi"},
			NodeScriptBasenames: []string{"kimi"},
			InstallPathExact:    []string{".kimi-code/bin/kimi"},
		},
	}
}

// CompileAgentIdentityCatalog builds indexes from rules. Invalid entries are
// skipped only when building from defaults (defaults must be valid); callers
// validating overlays should use ValidateAgentIdentityRule first.
func CompileAgentIdentityCatalog(rules []AgentIdentityRule) *AgentIdentityCatalog {
	catalog := &AgentIdentityCatalog{
		SchemaVersion:  agentIdentitySchemaVersion,
		Rules:          append([]AgentIdentityRule(nil), rules...),
		exact:          make(map[string]string),
		nodeScripts:    make(map[string]string),
		pathProbeExact: make(map[string]string),
		installExact:   make(map[string]string),
		agents:         make(map[string]struct{}),
	}
	for _, rule := range rules {
		agent := normalizeAgentID(rule.Agent)
		if agent == "" {
			continue
		}
		catalog.agents[agent] = struct{}{}
		for _, name := range rule.ExactBasenames {
			if base := normalizeBasename(name); base != "" {
				catalog.exact[base] = agent
			}
		}
		for _, name := range rule.NodeScriptBasenames {
			if base := normalizeBasename(name); base != "" {
				catalog.nodeScripts[base] = agent
			}
		}
		if rule.PathProbeSemVer {
			catalog.pathSemVer = true
		}
		for _, prefix := range rule.PathProbePrefixes {
			prefix = strings.ToLower(strings.TrimSpace(prefix))
			if prefix == "" {
				continue
			}
			catalog.pathPrefixes = append(catalog.pathPrefixes, pathProbePrefix{
				prefix:       prefix,
				requireDigit: rule.PathProbePrefixRequiresDigit,
				agent:        agent,
			})
		}
		for _, prefix := range rule.InstallPathPrefixes {
			if normalized, ok := normalizeHomeRelativePath(prefix, true); ok {
				catalog.installPrefix = append(catalog.installPrefix, installPathRule{prefix: normalized, agent: agent})
			}
		}
		for _, exact := range rule.InstallPathExact {
			if normalized, ok := normalizeHomeRelativePath(exact, false); ok {
				catalog.installExact[normalized] = agent
			}
		}
	}
	return catalog
}

// ActiveAgentIdentityCatalog returns the process-wide catalog (defaults +
// optional overlay under the session-pressure dir). Overlay mtime changes
// reload the catalog; a corrupt overlay falls back to defaults and records
// OverlayError without granting extra ownership.
func ActiveAgentIdentityCatalog() *AgentIdentityCatalog {
	dir, err := DataDir()
	if err != nil {
		dir = ""
	}
	return loadAgentIdentityCatalogCached(dir)
}

func loadAgentIdentityCatalogCached(dir string) *AgentIdentityCatalog {
	key, mtime := agentIdentityCacheFingerprint(dir)
	agentIdentityMu.RLock()
	if agentIdentityCached != nil && agentIdentityCacheKey == key && time.Since(agentIdentityCacheAt) < 5*time.Second {
		catalog := agentIdentityCached
		agentIdentityMu.RUnlock()
		return catalog
	}
	agentIdentityMu.RUnlock()

	catalog := CompileAgentIdentityCatalog(DefaultAgentIdentityRules())
	if dir != "" {
		overlayPath := AgentIdentityOverlayPath(dir)
		overlay, loadErr := LoadAgentIdentityOverlay(overlayPath)
		if loadErr != nil {
			catalog.OverlayPath = overlayPath
			catalog.OverlayError = loadErr.Error()
		} else if overlay != nil {
			merged, mergeErr := MergeAgentIdentityOverlay(catalog, overlay, overlayPath)
			if mergeErr != nil {
				catalog.OverlayPath = overlayPath
				catalog.OverlayError = mergeErr.Error()
			} else {
				catalog = merged
				_ = mtime
			}
		}
	}

	agentIdentityMu.Lock()
	agentIdentityCached = catalog
	agentIdentityCacheKey = key
	agentIdentityCacheAt = time.Now()
	agentIdentityMu.Unlock()
	return catalog
}

// ResetAgentIdentityCatalogCache is for tests.
func ResetAgentIdentityCatalogCache() {
	agentIdentityMu.Lock()
	agentIdentityCached = nil
	agentIdentityCacheKey = ""
	agentIdentityCacheAt = time.Time{}
	agentIdentityMu.Unlock()
}

func agentIdentityCacheFingerprint(dir string) (key string, mtime time.Time) {
	if dir == "" {
		return "default", time.Time{}
	}
	path := AgentIdentityOverlayPath(dir)
	info, err := statRegularFile(path)
	if err != nil {
		return "default:" + path, time.Time{}
	}
	return path + ":" + info.ModTime().UTC().Format(time.RFC3339Nano) + ":" + itoa64(info.Size()), info.ModTime()
}

func itoa64(value int64) string {
	if value == 0 {
		return "0"
	}
	var buf [32]byte
	i := len(buf)
	neg := value < 0
	if neg {
		value = -value
	}
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// MatchExactBasename reports an agent for a direct p_comm / basename match.
func (catalog *AgentIdentityCatalog) MatchExactBasename(basename string) (agent, executable string, ok bool) {
	if catalog == nil {
		return "", "", false
	}
	base := normalizeBasename(basename)
	if base == "" {
		return "", "", false
	}
	agent, ok = catalog.exact[base]
	if !ok {
		return "", "", false
	}
	return agent, agent, true
}

// NeedsPathProbe reports whether basename should trigger an executable-path
// lookup (Claude SemVer names, Grok versioned p_comm truncations).
func (catalog *AgentIdentityCatalog) NeedsPathProbe(basename string) bool {
	if catalog == nil {
		return false
	}
	base := normalizeBasename(basename)
	if base == "" {
		return false
	}
	if catalog.pathSemVer && isVersionExecutable(base) {
		return true
	}
	for _, probe := range catalog.pathPrefixes {
		if strings.HasPrefix(base, probe.prefix) {
			rest := base[len(probe.prefix):]
			if !probe.requireDigit {
				return true
			}
			if rest != "" && rest[0] >= '0' && rest[0] <= '9' {
				return true
			}
		}
	}
	return false
}

// MatchPath classifies a resolved executable path under trusted home install
// roots only.
func (catalog *AgentIdentityCatalog) MatchPath(path, home string) (agent, executable string, ok bool) {
	if catalog == nil {
		return "", "", false
	}
	normalizedHome := normalizeHome(home)
	if normalizedHome == "" {
		return "", "", false
	}
	normalized := strings.ToLower(filepath.ToSlash(filepath.Clean(path)))
	if normalized == "" || normalized == "." {
		return "", "", false
	}
	homePrefix := normalizedHome + "/"
	if normalized != normalizedHome && !strings.HasPrefix(normalized, homePrefix) {
		return "", "", false
	}
	relative := strings.TrimPrefix(normalized, homePrefix)
	if relative == normalized {
		// Path equals home only — never an agent binary.
		return "", "", false
	}
	if agent, ok = catalog.installExact[relative]; ok {
		return agent, agent, true
	}
	base := normalizeBasename(relative)
	for _, rule := range catalog.installPrefix {
		if !strings.HasPrefix(relative, rule.prefix) {
			continue
		}
		// Trusted root is necessary but not sufficient: reject unrelated files
		// dropped into the install directory (for example not-grok-helper).
		if catalog.basenameBelongsToAgent(base, rule.agent) {
			return rule.agent, rule.agent, true
		}
	}
	return "", "", false
}

// basenameBelongsToAgent reports whether a leaf name is a plausible binary for
// the agent (exact name, path-probe prefix, or pure SemVer when that agent
// uses SemVer provider binaries).
func (catalog *AgentIdentityCatalog) basenameBelongsToAgent(base, agent string) bool {
	if base == "" || agent == "" {
		return false
	}
	if matched, ok := catalog.exact[base]; ok && matched == agent {
		return true
	}
	if base == agent {
		return true
	}
	for _, probe := range catalog.pathPrefixes {
		if probe.agent != agent || !strings.HasPrefix(base, probe.prefix) {
			continue
		}
		rest := base[len(probe.prefix):]
		if !probe.requireDigit {
			return true
		}
		if rest != "" && rest[0] >= '0' && rest[0] <= '9' {
			return true
		}
	}
	if catalog.pathSemVer && isVersionExecutable(base) {
		// Pure SemVer basenames only map to agents that declared PathProbeSemVer.
		for _, rule := range catalog.Rules {
			if normalizeAgentID(rule.Agent) == agent && rule.PathProbeSemVer {
				return true
			}
		}
	}
	return false
}

// MatchNodeScript classifies the first positional node script basename.
func (catalog *AgentIdentityCatalog) MatchNodeScript(basename string) (agent, executable string, ok bool) {
	if catalog == nil {
		return "", "", false
	}
	base := normalizeBasename(basename)
	if base == "" {
		return "", "", false
	}
	agent, ok = catalog.nodeScripts[base]
	if !ok {
		return "", "", false
	}
	return agent, agent, true
}

// MatchCommand is the ps/command-line identity path (privacy-sensitive; only
// used when native identity is unavailable or as a fallback).
func (catalog *AgentIdentityCatalog) MatchCommand(command string) (agent, executable string, ok bool) {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return "", "", false
	}
	first := normalizeBasename(filepath.Base(fields[0]))
	if first == "node" && len(fields) > 1 {
		// First non-flag positional after node.
		for index := 1; index < len(fields) && index < 5; index++ {
			if strings.HasPrefix(fields[index], "-") {
				continue
			}
			if agent, executable, ok = catalog.MatchNodeScript(filepath.Base(fields[index])); ok {
				return agent, executable, true
			}
			if agent, executable, ok = catalog.MatchExactBasename(filepath.Base(fields[index])); ok {
				return agent, executable, true
			}
			// Versioned Grok-style basenames on the script path.
			if catalog.NeedsPathProbe(filepath.Base(fields[index])) {
				// Without a home path we can still treat path-probe prefixes
				// that encode the agent name (grok-*) as that agent.
				if agent = catalog.agentForPathProbeBasename(filepath.Base(fields[index])); agent != "" {
					return agent, agent, true
				}
			}
			break
		}
	}
	if agent, executable, ok = catalog.MatchExactBasename(first); ok {
		return agent, executable, true
	}
	if catalog.NeedsPathProbe(first) {
		if agent = catalog.agentForPathProbeBasename(first); agent != "" {
			return agent, agent, true
		}
	}
	// Full path argv0 under home install roots is rare on the ps path but
	// cheap when the first field is absolute.
	if strings.HasPrefix(fields[0], "/") || strings.HasPrefix(fields[0], "~/") {
		home, _ := userHomeDir()
		if agent, executable, ok = catalog.MatchPath(expandHomePath(fields[0], home), home); ok {
			return agent, executable, true
		}
	}
	return "", "", false
}

func (catalog *AgentIdentityCatalog) agentForPathProbeBasename(basename string) string {
	base := normalizeBasename(basename)
	for _, probe := range catalog.pathPrefixes {
		if strings.HasPrefix(base, probe.prefix) {
			rest := base[len(probe.prefix):]
			if probe.requireDigit && (rest == "" || rest[0] < '0' || rest[0] > '9') {
				continue
			}
			return probe.agent
		}
	}
	// Pure SemVer alone does not name an agent without a path.
	return ""
}

// IsAgentExecutable reports whether a privacy-safe executable basename belongs
// to a known agent (for host-consumer category / disk attribution).
func (catalog *AgentIdentityCatalog) IsAgentExecutable(executable string) bool {
	if catalog == nil {
		return false
	}
	base := normalizeBasename(executable)
	if base == "" {
		return false
	}
	if _, ok := catalog.exact[base]; ok {
		return true
	}
	if catalog.NeedsPathProbe(base) && catalog.agentForPathProbeBasename(base) != "" {
		return true
	}
	// After identity resolution Executable is the agent id itself.
	if _, ok := catalog.agents[base]; ok {
		return true
	}
	return false
}

// KnownAgents returns sorted agent ids.
func (catalog *AgentIdentityCatalog) KnownAgents() []string {
	if catalog == nil {
		return nil
	}
	out := make([]string, 0, len(catalog.agents))
	for agent := range catalog.agents {
		out = append(out, agent)
	}
	sort.Strings(out)
	return out
}

// InstallPresence describes on-disk install roots for miss detection.
type AgentInstallPresence struct {
	Agent  string `json:"agent"`
	Path   string `json:"path"`
	Kind   string `json:"kind"` // exact | prefix
	Present bool  `json:"present"`
}

// InstallPresences reports which trusted install roots exist under home.
func (catalog *AgentIdentityCatalog) InstallPresences(home string) []AgentInstallPresence {
	if catalog == nil {
		return nil
	}
	normalizedHome := normalizeHome(home)
	if normalizedHome == "" {
		return nil
	}
	var out []AgentInstallPresence
	for exact, agent := range catalog.installExact {
		abs := normalizedHome + "/" + exact
		out = append(out, AgentInstallPresence{
			Agent: agent, Path: abs, Kind: "exact",
			Present: isExecutableFile(abs) || pathExists(abs),
		})
	}
	for _, rule := range catalog.installPrefix {
		abs := normalizedHome + "/" + strings.TrimSuffix(rule.prefix, "/")
		out = append(out, AgentInstallPresence{
			Agent: rule.agent, Path: abs, Kind: "prefix",
			Present: pathExists(abs),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Agent != out[j].Agent {
			return out[i].Agent < out[j].Agent
		}
		return out[i].Path < out[j].Path
	})
	return out
}

func normalizeAgentID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return ""
	}
	if len(value) > 32 {
		return ""
	}
	return value
}

func normalizeBasename(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	return strings.ToLower(filepath.Base(value))
}

func normalizeHome(home string) string {
	home = strings.TrimSpace(home)
	if home == "" {
		return ""
	}
	return strings.TrimSuffix(strings.ToLower(filepath.ToSlash(filepath.Clean(home))), "/")
}

// normalizeHomeRelativePath accepts " .grok/bin/grok " or "/.grok/bin/grok"
// and returns a lower slash path without a leading slash. directory=true forces
// a trailing slash for prefix rules.
func normalizeHomeRelativePath(value string, directory bool) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	value = strings.ReplaceAll(value, "\\", "/")
	value = strings.TrimPrefix(value, "~/")
	value = strings.TrimPrefix(value, "/")
	value = strings.Trim(value, "/")
	if value == "" || value == "." || value == ".." {
		return "", false
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", false
		}
	}
	// Reject overly broad roots that would classify half the home directory.
	if len(parts) < 2 {
		return "", false
	}
	normalized := strings.ToLower(strings.Join(parts, "/"))
	if directory {
		normalized += "/"
	}
	return normalized, true
}

func expandHomePath(path, home string) string {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "~/") && home != "" {
		return filepath.Join(home, path[2:])
	}
	return path
}

func pathExists(path string) bool {
	_, err := statPath(path)
	return err == nil
}

// isVersionExecutable reports pure dotted numeric names (Claude SemVer p_comm).
func isVersionExecutable(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// userHomeDir is overridable in tests via the same os path; kept thin here.
func userHomeDir() (string, error) {
	return osUserHomeDir()
}

package sessionpressure

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Memory levels are the free, swap, tree and agent-total rungs of the memory
// ladder. They come from a JSON config, not from code: the embedded
// memory_levels.json, or a full replacement at <data dir>/memory-levels.json.
// JSON matches policy.json and every other state file here, decodes strictly
// with the standard library (no new dependency in the 30 MB resident), and is
// readable with jq. Each anchor is a host shape measured from its own monitor
// history; see corpus/memory-levels-calibration-2026-11-17.md.

// MemoryLevelsSchemaVersion is the only memory-levels.json schema accepted.
const MemoryLevelsSchemaVersion = 1

// MemoryLevelsFileName is the operator replacement for the embedded config.
const MemoryLevelsFileName = "memory-levels.json"

// Policy.MemoryLevels modes. Empty is manual, so a policy written before this
// field keeps its own thresholds until the operator opts in.
const (
	MemoryLevelsCalibrated = "calibrated"
	MemoryLevelsManual     = "manual"
)

//go:embed memory_levels.json
var embeddedMemoryLevelsJSON []byte

// LevelBand is one warning < red < critical ladder.
type LevelBand struct {
	Warning  float64 `json:"warning"`
	Red      float64 `json:"red"`
	Critical float64 `json:"critical"`
}

// MemoryLevelAnchor is the calibrated ladder for one measured host shape.
type MemoryLevelAnchor struct {
	Name                     string    `json:"name"`
	MemoryGiB                float64   `json:"memory_gib"`
	Evidence                 string    `json:"evidence"`
	FreePercent              LevelBand `json:"free_percent"`
	SwapMB                   LevelBand `json:"swap_mb"`
	TreeMB                   LevelBand `json:"tree_mb"`
	AgentTotalMB             LevelBand `json:"agent_total_mb"`
	RSSEscalationFreePercent float64   `json:"rss_escalation_free_percent"`
	GuardMaxSelfRSSMB        float64   `json:"guard_max_self_rss_mb"`
}

// MemoryLevelHost overrides single resolved values for one host. Absent
// fields keep the anchor result.
type MemoryLevelHost struct {
	Reason                   string     `json:"reason"`
	FreePercent              *LevelBand `json:"free_percent,omitempty"`
	SwapMB                   *LevelBand `json:"swap_mb,omitempty"`
	TreeMB                   *LevelBand `json:"tree_mb,omitempty"`
	AgentTotalMB             *LevelBand `json:"agent_total_mb,omitempty"`
	RSSEscalationFreePercent *float64   `json:"rss_escalation_free_percent,omitempty"`
	GuardMaxSelfRSSMB        *float64   `json:"guard_max_self_rss_mb,omitempty"`
}

// MemoryLevelsConfig is the decoded memory-levels.json.
type MemoryLevelsConfig struct {
	SchemaVersion int                        `json:"schema_version"`
	Note          string                     `json:"note,omitempty"`
	Anchors       []MemoryLevelAnchor        `json:"anchors"`
	Hosts         map[string]MemoryLevelHost `json:"hosts,omitempty"`
}

// MemoryLevels is the resolved ladder for one host, with its provenance.
type MemoryLevels struct {
	Source                   string    `json:"source"`
	Host                     string    `json:"host,omitempty"`
	HostOverride             bool      `json:"host_override"`
	PhysicalMemoryMB         float64   `json:"physical_memory_mb"`
	Rule                     string    `json:"rule"`
	FreePercent              LevelBand `json:"free_percent"`
	SwapMB                   LevelBand `json:"swap_mb"`
	TreeMB                   LevelBand `json:"tree_mb"`
	AgentTotalMB             LevelBand `json:"agent_total_mb"`
	RSSEscalationFreePercent float64   `json:"rss_escalation_free_percent"`
	GuardMaxSelfRSSMB        float64   `json:"guard_max_self_rss_mb"`
}

var hostKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ParseMemoryLevels decodes and validates one memory-levels.json body.
// Unknown fields, trailing data and inconsistent ladders are errors that name
// the field, so a bad edit fails loudly instead of loosening protection.
func ParseMemoryLevels(body []byte) (MemoryLevelsConfig, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var config MemoryLevelsConfig
	if err := decoder.Decode(&config); err != nil {
		return MemoryLevelsConfig{}, fmt.Errorf("decode: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return MemoryLevelsConfig{}, errors.New("decode: trailing data after the config object")
	}
	if err := config.Validate(); err != nil {
		return MemoryLevelsConfig{}, err
	}
	return config, nil
}

func validateBand(field string, band LevelBand, ascending bool, maxValue float64) error {
	values := []float64{band.Warning, band.Red, band.Critical}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
			return fmt.Errorf("%s: every value must be a positive number (got warning %g, red %g, critical %g)", field, band.Warning, band.Red, band.Critical)
		}
		if maxValue > 0 && value > maxValue {
			return fmt.Errorf("%s: values must be at most %g (got warning %g, red %g, critical %g)", field, maxValue, band.Warning, band.Red, band.Critical)
		}
	}
	if ascending && !(band.Warning < band.Red && band.Red < band.Critical) {
		return fmt.Errorf("%s: must satisfy warning < red < critical (got %g, %g, %g)", field, band.Warning, band.Red, band.Critical)
	}
	if !ascending && !(band.Warning > band.Red && band.Red > band.Critical) {
		return fmt.Errorf("%s: free memory must satisfy warning > red > critical (got %g, %g, %g)", field, band.Warning, band.Red, band.Critical)
	}
	return nil
}

func validateEscalation(field string, value float64) error {
	if math.IsNaN(value) || value <= 0 || value > 100 {
		return fmt.Errorf("%s: must be in (0, 100]; 100 means the RSS rungs always escalate (got %g)", field, value)
	}
	return nil
}

func validateGuardRSS(field string, value float64) error {
	if math.IsNaN(value) || value < 10 || value > 256 {
		return fmt.Errorf("%s: must be between 10 and 256 MB (got %g)", field, value)
	}
	return nil
}

func (levels MemoryLevels) validate(prefix string) error {
	if err := validateBand(prefix+"free_percent", levels.FreePercent, false, 100); err != nil {
		return err
	}
	if err := validateBand(prefix+"swap_mb", levels.SwapMB, true, 0); err != nil {
		return err
	}
	if err := validateBand(prefix+"tree_mb", levels.TreeMB, true, 0); err != nil {
		return err
	}
	if err := validateBand(prefix+"agent_total_mb", levels.AgentTotalMB, true, 0); err != nil {
		return err
	}
	if err := validateEscalation(prefix+"rss_escalation_free_percent", levels.RSSEscalationFreePercent); err != nil {
		return err
	}
	return validateGuardRSS(prefix+"guard_max_self_rss_mb", levels.GuardMaxSelfRSSMB)
}

func (anchor MemoryLevelAnchor) levels() MemoryLevels {
	return MemoryLevels{
		FreePercent:              anchor.FreePercent,
		SwapMB:                   anchor.SwapMB,
		TreeMB:                   anchor.TreeMB,
		AgentTotalMB:             anchor.AgentTotalMB,
		RSSEscalationFreePercent: anchor.RSSEscalationFreePercent,
		GuardMaxSelfRSSMB:        anchor.GuardMaxSelfRSSMB,
	}
}

// Validate checks the schema, every anchor ladder and every host override.
func (config MemoryLevelsConfig) Validate() error {
	if config.SchemaVersion != MemoryLevelsSchemaVersion {
		return fmt.Errorf("schema_version: want %d, got %d", MemoryLevelsSchemaVersion, config.SchemaVersion)
	}
	if len(config.Anchors) == 0 {
		return errors.New("anchors: at least one measured host shape is required")
	}
	names := map[string]bool{}
	for index, anchor := range config.Anchors {
		prefix := fmt.Sprintf("anchors[%d] (%s).", index, anchor.Name)
		if strings.TrimSpace(anchor.Name) == "" {
			return fmt.Errorf("anchors[%d].name: required", index)
		}
		if names[anchor.Name] {
			return fmt.Errorf("anchors[%d].name: duplicate %q", index, anchor.Name)
		}
		names[anchor.Name] = true
		if math.IsNaN(anchor.MemoryGiB) || anchor.MemoryGiB < 1 || anchor.MemoryGiB > 4096 {
			return fmt.Errorf("%smemory_gib: must be between 1 and 4096 (got %g)", prefix, anchor.MemoryGiB)
		}
		if index > 0 && anchor.MemoryGiB <= config.Anchors[index-1].MemoryGiB {
			return fmt.Errorf("%smemory_gib: anchors must be in strictly ascending memory order (%g after %g)", prefix, anchor.MemoryGiB, config.Anchors[index-1].MemoryGiB)
		}
		if strings.TrimSpace(anchor.Evidence) == "" {
			return fmt.Errorf("%sevidence: required; say which host and monitor history the values come from", prefix)
		}
		if err := anchor.levels().validate(prefix); err != nil {
			return err
		}
	}
	hosts := make([]string, 0, len(config.Hosts))
	for host := range config.Hosts {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	for _, host := range hosts {
		override := config.Hosts[host]
		prefix := fmt.Sprintf("hosts[%q].", host)
		if !hostKeyPattern.MatchString(host) {
			return fmt.Errorf("hosts[%q]: key must be a lowercase short hostname (letters, digits, '-')", host)
		}
		if strings.TrimSpace(override.Reason) == "" {
			return fmt.Errorf("%sreason: required; say why this host differs from its memory size", prefix)
		}
		for field, band := range map[string]*LevelBand{"swap_mb": override.SwapMB, "tree_mb": override.TreeMB, "agent_total_mb": override.AgentTotalMB} {
			if band != nil {
				if err := validateBand(prefix+field, *band, true, 0); err != nil {
					return err
				}
			}
		}
		if override.FreePercent != nil {
			if err := validateBand(prefix+"free_percent", *override.FreePercent, false, 100); err != nil {
				return err
			}
		}
		if override.RSSEscalationFreePercent != nil {
			if err := validateEscalation(prefix+"rss_escalation_free_percent", *override.RSSEscalationFreePercent); err != nil {
				return err
			}
		}
		if override.GuardMaxSelfRSSMB != nil {
			if err := validateGuardRSS(prefix+"guard_max_self_rss_mb", *override.GuardMaxSelfRSSMB); err != nil {
				return err
			}
		}
	}
	return nil
}

func lerp(a, b, t float64) float64 { return a + (b-a)*t }

func lerpBand(a, b LevelBand, t float64) LevelBand {
	return LevelBand{Warning: lerp(a.Warning, b.Warning, t), Red: lerp(a.Red, b.Red, t), Critical: lerp(a.Critical, b.Critical, t)}
}

func scaleBand(band LevelBand, factor float64) LevelBand {
	return LevelBand{Warning: band.Warning * factor, Red: band.Red * factor, Critical: band.Critical * factor}
}

// Resolve returns the ladder for a host with physicalMemoryMB of memory.
// Between anchors every value is interpolated on memory. Below the first or
// above the last anchor, MB ladders scale with memory and the percent and
// guard values stay at that anchor. A host entry then replaces single values.
func (config MemoryLevelsConfig) Resolve(physicalMemoryMB float64, host string) (MemoryLevels, error) {
	if physicalMemoryMB <= 0 {
		physicalMemoryMB = 16 * 1024
	}
	memoryGiB := physicalMemoryMB / 1024
	anchors := config.Anchors
	var levels MemoryLevels
	switch {
	case memoryGiB <= anchors[0].MemoryGiB:
		first := anchors[0]
		factor := memoryGiB / first.MemoryGiB
		levels = first.levels()
		levels.SwapMB, levels.TreeMB, levels.AgentTotalMB = scaleBand(first.SwapMB, factor), scaleBand(first.TreeMB, factor), scaleBand(first.AgentTotalMB, factor)
		levels.Rule = fmt.Sprintf("anchor %s scaled x%.3f", first.Name, factor)
		if factor == 1 {
			levels.Rule = "anchor " + first.Name
		}
	case memoryGiB >= anchors[len(anchors)-1].MemoryGiB:
		last := anchors[len(anchors)-1]
		factor := memoryGiB / last.MemoryGiB
		levels = last.levels()
		levels.SwapMB, levels.TreeMB, levels.AgentTotalMB = scaleBand(last.SwapMB, factor), scaleBand(last.TreeMB, factor), scaleBand(last.AgentTotalMB, factor)
		levels.Rule = fmt.Sprintf("anchor %s scaled x%.3f", last.Name, factor)
		if factor == 1 {
			levels.Rule = "anchor " + last.Name
		}
	default:
		upper := sort.Search(len(anchors), func(i int) bool { return anchors[i].MemoryGiB >= memoryGiB })
		low, high := anchors[upper-1], anchors[upper]
		t := (memoryGiB - low.MemoryGiB) / (high.MemoryGiB - low.MemoryGiB)
		levels = MemoryLevels{
			FreePercent:              lerpBand(low.FreePercent, high.FreePercent, t),
			SwapMB:                   lerpBand(low.SwapMB, high.SwapMB, t),
			TreeMB:                   lerpBand(low.TreeMB, high.TreeMB, t),
			AgentTotalMB:             lerpBand(low.AgentTotalMB, high.AgentTotalMB, t),
			RSSEscalationFreePercent: lerp(low.RSSEscalationFreePercent, high.RSSEscalationFreePercent, t),
			GuardMaxSelfRSSMB:        lerp(low.GuardMaxSelfRSSMB, high.GuardMaxSelfRSSMB, t),
			Rule:                     fmt.Sprintf("interpolated %.2f between anchors %s and %s", t, low.Name, high.Name),
		}
	}
	levels.PhysicalMemoryMB = physicalMemoryMB
	levels.Host = NormalizeMemoryLevelsHost(host)
	if override, ok := config.Hosts[levels.Host]; ok && levels.Host != "" {
		levels.HostOverride = true
		levels.Rule += "; host override " + levels.Host
		if override.FreePercent != nil {
			levels.FreePercent = *override.FreePercent
		}
		if override.SwapMB != nil {
			levels.SwapMB = *override.SwapMB
		}
		if override.TreeMB != nil {
			levels.TreeMB = *override.TreeMB
		}
		if override.AgentTotalMB != nil {
			levels.AgentTotalMB = *override.AgentTotalMB
		}
		if override.RSSEscalationFreePercent != nil {
			levels.RSSEscalationFreePercent = *override.RSSEscalationFreePercent
		}
		if override.GuardMaxSelfRSSMB != nil {
			levels.GuardMaxSelfRSSMB = *override.GuardMaxSelfRSSMB
		}
	}
	// Small hosts scale MB ladders far down; only the ordering matters there.
	if err := levels.validateOrdering(); err != nil {
		return MemoryLevels{}, fmt.Errorf("resolved levels for %s (%.0f MB): %w", firstNonEmpty(levels.Host, "this host"), physicalMemoryMB, err)
	}
	return levels, nil
}

func (levels MemoryLevels) validateOrdering() error {
	if !(levels.FreePercent.Warning > levels.FreePercent.Red && levels.FreePercent.Red > levels.FreePercent.Critical) {
		return errors.New("free_percent must satisfy warning > red > critical")
	}
	for field, band := range map[string]LevelBand{"swap_mb": levels.SwapMB, "tree_mb": levels.TreeMB, "agent_total_mb": levels.AgentTotalMB} {
		if !(band.Warning < band.Red && band.Red < band.Critical) {
			return fmt.Errorf("%s must satisfy warning < red < critical (got %g, %g, %g)", field, band.Warning, band.Red, band.Critical)
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// NormalizeMemoryLevelsHost turns os.Hostname output ("Example-MacBook-Pro.local")
// into the hosts key form ("example-macbook-pro").
func NormalizeMemoryLevelsHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if index := strings.IndexByte(host, '.'); index >= 0 {
		host = host[:index]
	}
	return host
}

// Apply writes the resolved ladder into a policy's memory thresholds and
// guard RSS budget. CPU, swap-out rate and swap-lag knobs stay the policy's.
func (levels MemoryLevels) Apply(policy *Policy) {
	t := &policy.Thresholds
	t.FreeWarningPercent, t.FreeRedPercent, t.FreeCriticalPercent = levels.FreePercent.Warning, levels.FreePercent.Red, levels.FreePercent.Critical
	t.SwapWarningMB, t.SwapRedMB, t.SwapCriticalMB = levels.SwapMB.Warning, levels.SwapMB.Red, levels.SwapMB.Critical
	t.TreeWarningMB, t.TreeRedMB, t.TreeCriticalMB = levels.TreeMB.Warning, levels.TreeMB.Red, levels.TreeMB.Critical
	t.AgentTotalWarningMB, t.AgentTotalRedMB, t.AgentTotalCriticalMB = levels.AgentTotalMB.Warning, levels.AgentTotalMB.Red, levels.AgentTotalMB.Critical
	t.RSSEscalationFreePercent = levels.RSSEscalationFreePercent
	if levels.RSSEscalationFreePercent >= 100 {
		t.RSSEscalationFreePercent = 0
	}
	policy.ResourceBudgets.MaxSelfRSSMB = levels.GuardMaxSelfRSSMB
}

var embeddedMemoryLevels = sync.OnceValues(func() (MemoryLevelsConfig, error) {
	config, err := ParseMemoryLevels(embeddedMemoryLevelsJSON)
	if err != nil {
		return MemoryLevelsConfig{}, fmt.Errorf("embedded memory_levels.json: %w", err)
	}
	return config, nil
})

// EmbeddedMemoryLevels is the shipped calibration. A parse failure is a build
// defect; TestEmbeddedMemoryLevelsValid keeps it from shipping.
func EmbeddedMemoryLevels() MemoryLevelsConfig {
	config, err := embeddedMemoryLevels()
	if err != nil {
		panic(err)
	}
	return config
}

// MemoryLevelsPath is the operator replacement file in the data directory.
func MemoryLevelsPath(dir string) string { return filepath.Join(dir, MemoryLevelsFileName) }

// LoadMemoryLevels returns the operator file in dir when present, otherwise
// the embedded calibration, plus a source label for status output.
func LoadMemoryLevels(dir string) (MemoryLevelsConfig, string, error) {
	if dir != "" {
		path := MemoryLevelsPath(dir)
		body, err := os.ReadFile(path)
		switch {
		case err == nil:
			config, parseErr := ParseMemoryLevels(body)
			if parseErr != nil {
				return MemoryLevelsConfig{}, path, fmt.Errorf("%s: %w", path, parseErr)
			}
			return config, path, nil
		case !errors.Is(err, os.ErrNotExist):
			return MemoryLevelsConfig{}, path, err
		}
	}
	config, err := embeddedMemoryLevels()
	return config, "embedded", err
}

// hostMemoryMB is the native memory size; tests replace it.
var hostMemoryMB = nativePhysicalMemoryMB

// hostName is the short hostname for host overrides; tests replace it.
var hostName = os.Hostname

// ResolveHostMemoryLevels loads the config for dir and resolves it for this
// host. physicalMemoryMB <= 0 reads hw.memsize natively.
func ResolveHostMemoryLevels(dir string, physicalMemoryMB float64) (MemoryLevels, error) {
	config, source, err := LoadMemoryLevels(dir)
	if err != nil {
		return MemoryLevels{}, fmt.Errorf("memory levels: %w", err)
	}
	if physicalMemoryMB <= 0 {
		native, nativeErr := hostMemoryMB()
		if nativeErr != nil || native <= 0 {
			native, nativeErr = procMemTotalMB("/proc/meminfo")
		}
		if nativeErr != nil || native <= 0 {
			return MemoryLevels{}, fmt.Errorf("memory levels: read physical memory: %v", nativeErr)
		}
		physicalMemoryMB = native
	}
	host, _ := hostName()
	levels, err := config.Resolve(physicalMemoryMB, host)
	if err != nil {
		return MemoryLevels{}, fmt.Errorf("memory levels from %s: %w", source, err)
	}
	levels.Source = source
	return levels, nil
}

// procMemTotalMB reads MemTotal from a Linux /proc/meminfo, the fallback for
// hosts without hw.memsize (gate runners).
func procMemTotalMB(path string) (float64, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			var kib float64
			if _, err := fmt.Sscanf(fields[1], "%g", &kib); err != nil || kib <= 0 {
				return 0, fmt.Errorf("%s: bad MemTotal %q", path, fields[1])
			}
			return kib / 1024, nil
		}
	}
	return 0, fmt.Errorf("%s: no MemTotal line", path)
}

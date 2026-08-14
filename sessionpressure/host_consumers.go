package sessionpressure

import (
	"math"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const maxProjectedHostConsumers = 16

// buildHostConsumers aggregates the complete process inventory into bounded,
// prompt-free executable buckets. The caller may persist this projection: the
// function deliberately discards command tails, paths, and process identity.
func buildHostConsumers(processes []Process, trees []AgentTree) []HostConsumer {
	agentPIDs := make(map[int]struct{})
	for _, tree := range trees {
		for _, pid := range tree.PIDs {
			agentPIDs[pid] = struct{}{}
		}
	}

	catalog := ActiveAgentIdentityCatalog()
	byExecutable := make(map[string]HostConsumer)
	for _, process := range processes {
		executable := privacySafeExecutable(process.Executable)
		if executable == "unknown" {
			executable = privacySafeCommandExecutable(process.Command)
		}
		consumer, exists := byExecutable[executable]
		if !exists {
			consumer.CPUAvailable = true
		}
		consumer.Executable = executable
		consumer.Category = hostConsumerCategoryWithCatalog(executable, catalog)
		consumer.ProcessCount++
		consumer.RSSSumMB += float64(process.RSSKB) / 1024
		consumer.CPUPercentSum += process.CPUPercent
		if !process.CPUAvailable {
			consumer.CPUAvailable = false
		}
		if _, ok := agentPIDs[process.PID]; ok {
			consumer.AgentProcessCount++
		}
		byExecutable[executable] = consumer
	}

	consumers := make([]HostConsumer, 0, len(byExecutable))
	for _, consumer := range byExecutable {
		consumer.RSSSumMB = math.Round(consumer.RSSSumMB*10) / 10
		consumer.CPUPercentSum = math.Round(consumer.CPUPercentSum*100) / 100
		consumers = append(consumers, consumer)
	}
	sort.Slice(consumers, func(i, j int) bool {
		if consumers[i].RSSSumMB != consumers[j].RSSSumMB {
			return consumers[i].RSSSumMB > consumers[j].RSSSumMB
		}
		if consumers[i].CPUPercentSum != consumers[j].CPUPercentSum {
			return consumers[i].CPUPercentSum > consumers[j].CPUPercentSum
		}
		return consumers[i].Executable < consumers[j].Executable
	})
	if len(consumers) > maxProjectedHostConsumers {
		consumers = consumers[:maxProjectedHostConsumers]
	}
	return consumers
}

// privacySafeExecutable normalizes one already-isolated executable basename.
// It is safe for native comm values, including names containing spaces.
func privacySafeExecutable(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	name := filepath.Base(value)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "unknown"
	}
	runes := make([]rune, 0, min(len([]rune(name)), 64))
	for _, r := range name {
		if len(runes) >= 64 {
			break
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._+-()[]", r) {
			runes = append(runes, r)
		} else {
			runes = append(runes, '_')
		}
	}
	result := strings.Trim(string(runes), "_")
	if result == "" {
		return "unknown"
	}
	return result
}

func privacySafeCommandExecutable(command string) string {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return "unknown"
	}
	return privacySafeExecutable(fields[0])
}

func hostConsumerCategory(executable string) string {
	return hostConsumerCategoryWithCatalog(executable, ActiveAgentIdentityCatalog())
}

func hostConsumerCategoryWithCatalog(executable string, catalog *AgentIdentityCatalog) string {
	lower := strings.ToLower(executable)
	switch {
	case catalog != nil && catalog.IsAgentExecutable(lower):
		return "agent"
	case strings.Contains(lower, "chrome") || strings.Contains(lower, "safari") || strings.Contains(lower, "firefox") || strings.Contains(lower, "webkit"):
		return "browser"
	case strings.Contains(lower, "simulator") || strings.Contains(lower, "qemu") || strings.Contains(lower, "emulator"):
		return "emulator"
	case strings.Contains(lower, "docker") || strings.Contains(lower, "orb") || strings.Contains(lower, "virtual") || strings.Contains(lower, "vmware"):
		return "container_vm"
	case lower == "sqlite3" || strings.Contains(lower, "postgres") || lower == "mysqld" || strings.HasPrefix(lower, "redis") || strings.Contains(lower, "mongod"):
		return "database"
	case lower == "go" || lower == "swift" || lower == "clang" || lower == "rustc" || lower == "node" || lower == "python" || lower == "python3" || lower == "xcodebuild":
		return "developer"
	case lower == "kernel_task" || lower == "launchd" || lower == "windowserver" || lower == "mds" || strings.HasPrefix(lower, "com.apple."):
		return "system"
	default:
		return "other"
	}
}

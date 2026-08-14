package sessionpressure

import "strings"

// CommandLeaf returns the categorical session-pressure leaf for a positional
// subcommand path *after* `session pressure` (e.g. ["work","run"] → "work.run").
// Empty path means the default snapshot leaf. Unknown tops map to "unknown".
//
// This is the single vocabulary authority for:
//   - live `session.pressure.command` telemetry attributes
//   - cli-invocations `action` attrs on operation_id=session.pressure
func CommandLeaf(args []string) string {
	if len(args) == 0 {
		return "snapshot"
	}
	sub := args[0]
	switch sub {
	case "help", "--help", "-h":
		return "help"
	case "snapshot", "check", "doctor", "status", "self-test", "telemetry", "idle", "audit", "board":
		return sub
	case "recovery":
		if len(args) > 1 && args[1] == "clear" {
			return "recovery.clear"
		}
		return "recovery.show"
	case "artifact":
		return AllowedLeaf("artifact", args[1:], "status", []string{"status", "plan", "prune"})
	case "storage":
		leaf := AllowedLeaf("storage", args[1:], "help", []string{"help", "status", "providers", "plan", "apply", "history", "policy"})
		if leaf == "storage.policy" {
			return AllowedLeaf("storage.policy", args[2:], "unknown", []string{"enable", "observe"})
		}
		return leaf
	case "io":
		leaf := AllowedLeaf("io", args[1:], "status", []string{"help", "status", "top", "history", "policy", "trace"})
		if leaf == "io.policy" {
			return AllowedLeaf("io.policy", args[2:], "show", []string{"show", "observe", "enable-alerts", "disable"})
		}
		return leaf
	case "work":
		return AllowedLeaf("work", args[1:], "status", []string{"help", "status", "run", "batch", "override", "history", "stats", "report", "evaluate"})
	case "policy":
		leaf := AllowedLeaf("policy", args[1:], "show", []string{"show", "init", "migrate", "enable", "observe", "profile"})
		if leaf == "policy.profile" {
			return AllowedLeaf("policy.profile", args[2:], "show", []string{"show", "apply"})
		}
		return leaf
	case "monitor":
		return AllowedLeaf("monitor", args[1:], "status", []string{"once", "run", "install", "status", "uninstall"})
	case "cleanup":
		leaf := AllowedLeaf("cleanup", args[1:], "help", []string{"help", "status", "plan", "history", "policy", "claim", "enforce"})
		switch leaf {
		case "cleanup.policy":
			return AllowedLeaf("cleanup.policy", args[2:], "show", []string{"show", "init", "schedule", "enable", "observe", "disable"})
		case "cleanup.claim":
			return AllowedLeaf("cleanup.claim", args[2:], "list", []string{"list", "acquire", "heartbeat", "release"})
		default:
			return leaf
		}
	default:
		return "unknown"
	}
}

// AllowedLeaf appends the first allowed positional token under prefix, or the
// fallback when none is present, or prefix+".unknown" when the token is not
// in the allow-list.
func AllowedLeaf(prefix string, args []string, fallback string, allowed []string) string {
	if len(args) == 0 {
		return prefix + "." + fallback
	}
	for _, candidate := range allowed {
		if args[0] == candidate || ((args[0] == "--help" || args[0] == "-h") && candidate == "help") {
			return prefix + "." + candidate
		}
	}
	return prefix + ".unknown"
}

// CommandLeafFromCLIArgs extracts the leaf from a full CLI argv (excluding
// program name). Stops at `--` so nested command text cannot be mistaken for a
// leaf. Returns "" when the path is not `session pressure …`.
func CommandLeafFromCLIArgs(args []string) string {
	path := make([]string, 0, 8)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if arg == "" {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			switch arg {
			case "--wait", "--since", "--limit", "--class", "--event", "--operation-id",
				"--include", "--progress", "--file", "--priority", "--claim-id":
				if i+1 < len(args) && args[i+1] != "--" && !strings.HasPrefix(args[i+1], "-") {
					i++
				}
			}
			continue
		}
		path = append(path, arg)
	}
	for len(path) > 0 {
		switch path[0] {
		case "--json", "--yes", "--use-daemon", "--verbose", "--debug":
			path = path[1:]
			continue
		}
		break
	}
	if len(path) < 2 || path[0] != "session" || path[1] != "pressure" {
		return ""
	}
	return CommandLeaf(path[2:])
}

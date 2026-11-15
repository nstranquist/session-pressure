package sessionpressure

import (
	"path/filepath"
	"sort"
	"strings"
)

// Light commands are finite and cheap by construction. Callers classify
// coarsely (an agent hook sends every `xcrun` as "build"), so without this the
// queue charged a read-only device query five units of a busy 8-unit Mac and
// held it behind real builds. On 2026-11-15 `xcrun devicectl device info
// detail` waited five minutes as a weight-5 build and `npm ci` as a test.
//
// The mapping uses existing weight-1 classes so policy, storage exemptions and
// the app need no new vocabulary:
//   - dependency installs are network and disk bound: install
//   - read-only Apple device and simulator queries: express-test, the short
//     light envelope that the fast lane and storage floor already exempt
//
// Only exact, known-read-only shapes match. Anything that can launch, build,
// boot or install onto a device keeps its caller's class.

// classifyLightWorkArgv reports the weight-1 class for a light command.
func classifyLightWorkArgv(argv []string) (WorkClass, bool) {
	tokens := lightCommandTokens(argv)
	if len(tokens) < 2 {
		return "", false
	}
	base := strings.ToLower(filepath.Base(tokens[0]))
	rest := tokens[1:]
	switch base {
	case "npm", "pnpm", "yarn", "bun":
		if nodeDependencyInstall(base, rest) {
			return WorkClassInstall, true
		}
	case "go":
		if len(rest) >= 2 && rest[0] == "mod" && rest[1] == "download" {
			return WorkClassInstall, true
		}
	case "xcrun":
		if appleReadOnlyQuery(rest) {
			return WorkClassExpressTest, true
		}
	}
	return "", false
}

// lightCommandTokens drops leading environment assignments and a bare `env`.
func lightCommandTokens(argv []string) []string {
	tokens := argv
	if len(tokens) > 0 && filepath.Base(tokens[0]) == "env" {
		tokens = tokens[1:]
	}
	for len(tokens) > 0 && isShellAssignment(tokens[0]) {
		tokens = tokens[1:]
	}
	return tokens
}

func isShellAssignment(token string) bool {
	name, _, found := strings.Cut(token, "=")
	if !found || name == "" {
		return false
	}
	for index, r := range name {
		if r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (index > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

// nodeDependencyInstall matches a whole-project dependency install. Adding a
// named package (`npm install left-pad`) can run arbitrary lifecycle scripts
// for code the project does not yet pin, so it keeps the caller's class.
func nodeDependencyInstall(tool string, rest []string) bool {
	if len(rest) == 0 {
		return false
	}
	command := rest[0]
	switch command {
	case "ci", "install", "i":
	default:
		return false
	}
	return onlyFlags(rest[1:])
}

func onlyFlags(args []string) bool {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			return false
		}
	}
	return true
}

// appleReadOnlyQuery matches xcrun forms that only read tool, SDK, device or
// simulator state.
func appleReadOnlyQuery(rest []string) bool {
	if len(rest) == 0 {
		return false
	}
	switch rest[0] {
	case "--find", "-f", "--show-sdk-path", "--show-sdk-version", "--show-sdk-platform-path", "--show-sdk-platform-version", "--show-sdk-build-version":
		return true
	case "devicectl":
		return devicectlReadOnly(rest[1:])
	case "simctl":
		return len(rest) >= 2 && (rest[1] == "list" || rest[1] == "getenv" || rest[1] == "help")
	}
	return false
}

func devicectlReadOnly(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "list":
		return true
	case "device":
		// `device info <query>` reads; `device install|process|copy|reboot`
		// change the device and keep the caller's class.
		return len(args) >= 2 && args[1] == "info"
	}
	return false
}

// Tool families are a closed vocabulary recorded on work events. They name
// the toolchain only, never arguments, paths or environment, so they are as
// safe to persist as the command digest. Closed-loop placement needs them: a
// digest cannot say whether queued local work was Go (remote-capable today),
// Node (remote-capable with a profile) or Swift (Mac-only).
const (
	ToolFamilyGo         = "go"
	ToolFamilyNode       = "node"
	ToolFamilyRust       = "rust"
	ToolFamilySwift      = "swift"
	ToolFamilyXcodebuild = "xcodebuild"
	ToolFamilyXcrun      = "xcrun"
	ToolFamilyMake       = "make"
	ToolFamilyPython     = "python"
	ToolFamilyNDev       = "ndev"
	ToolFamilyShell      = "shell"
	ToolFamilyOther      = "other"
)

var toolFamilies = map[string]bool{
	ToolFamilyGo: true, ToolFamilyNode: true, ToolFamilyRust: true, ToolFamilySwift: true,
	ToolFamilyXcodebuild: true, ToolFamilyXcrun: true, ToolFamilyMake: true, ToolFamilyPython: true,
	ToolFamilyNDev: true, ToolFamilyShell: true, ToolFamilyOther: true,
}

// ValidToolFamily reports whether family belongs to the closed vocabulary.
func ValidToolFamily(family string) bool { return toolFamilies[family] }

// WorkToolFamily names the toolchain of a work command. A shell-wrapped
// compound command takes the family of its heaviest classified statement,
// else of its first real statement; an unparseable script is "shell".
func WorkToolFamily(argv []string) string {
	if statement, ok := shellCommandStatement(argv); ok {
		commands, parsed := tokenizeSafeShellSequence(statement)
		if !parsed {
			return ToolFamilyShell
		}
		family, strongest := "", -1
		for _, command := range commands {
			if safeShellSetCommand(command) || (len(command) == 2 && command[0] == "cd") {
				continue
			}
			candidate := directToolFamily(command)
			weight := -1
			if class, ok := classifyDirectWorkArgvAt(command, ""); ok {
				weight = workClassRank(class)
			}
			if family == "" || weight > strongest {
				family, strongest = candidate, weight
			}
		}
		if family == "" {
			return ToolFamilyShell
		}
		return family
	}
	return directToolFamily(argv)
}

// workClassRank orders classes by default weight for compound commands.
func workClassRank(class WorkClass) int {
	// A fixed reference host keeps the ranking deterministic across machines.
	weight, err := defaultWorkLimits(8).Weight(class)
	if err != nil {
		return 0
	}
	return weight
}

func directToolFamily(argv []string) string {
	tokens := lightCommandTokens(argv)
	if len(tokens) == 0 {
		return ToolFamilyOther
	}
	switch base := strings.ToLower(filepath.Base(tokens[0])); base {
	case "go", "gofmt":
		return ToolFamilyGo
	case "node", "npm", "npx", "pnpm", "yarn", "bun", "vitest", "jest", "tsc", "eslint", "vite", "next":
		return ToolFamilyNode
	case "cargo", "rustc":
		return ToolFamilyRust
	case "swift", "swiftc":
		return ToolFamilySwift
	case "xcodebuild":
		return ToolFamilyXcodebuild
	case "xcrun":
		if len(tokens) > 1 {
			switch strings.ToLower(filepath.Base(tokens[1])) {
			case "xcodebuild":
				return ToolFamilyXcodebuild
			case "swift", "swiftc":
				return ToolFamilySwift
			}
		}
		return ToolFamilyXcrun
	case "make", "gmake":
		return ToolFamilyMake
	case "python", "python3", "uv", "pytest", "pip", "pip3":
		return ToolFamilyPython
	case "ndev", "ndev-commit-delivery":
		return ToolFamilyNDev
	case "bash", "sh", "zsh":
		return ToolFamilyShell
	}
	return ToolFamilyOther
}

// WorkToolFamilyStats summarizes one tool family's operations in a window.
type WorkToolFamilyStats struct {
	ToolFamily  string `json:"tool_family"`
	Operations  int    `json:"operations"`
	Acquired    int    `json:"acquired"`
	Completed   int    `json:"completed"`
	Failed      int    `json:"failed"`
	Cancelled   int    `json:"cancelled"`
	Expired     int    `json:"expired"`
	WaitTotalMS int64  `json:"wait_total_ms"`
	WaitP95MS   int64  `json:"wait_p95_ms"`
	RuntimeMS   int64  `json:"runtime_total_ms"`
}

// summarizeToolFamilies groups operations by recorded tool family. Rows from
// before the field existed are counted under "unrecorded" so the share of
// evidence is visible instead of silently dropped.
func summarizeToolFamilies(events []WorkEvent) []WorkToolFamilyStats {
	type operation struct {
		family    string
		acquired  bool
		terminal  WorkEventType
		waitMS    int64
		runtimeMS int64
	}
	operations := map[string]*operation{}
	order := []string{}
	for _, event := range events {
		if event.OperationID == "" {
			continue
		}
		op := operations[event.OperationID]
		if op == nil {
			op = &operation{}
			operations[event.OperationID] = op
			order = append(order, event.OperationID)
		}
		if event.ToolFamily != "" {
			op.family = event.ToolFamily
		}
		switch event.Event {
		case WorkEventAcquired:
			op.acquired = true
			op.waitMS = max(op.waitMS, event.WaitMilliseconds)
		case WorkEventCompleted, WorkEventFailed, WorkEventCancelled, WorkEventExpired:
			op.terminal = event.Event
			op.runtimeMS += max(int64(0), event.RuntimeMillis)
		}
	}
	byFamily := map[string]*WorkToolFamilyStats{}
	waits := map[string][]int64{}
	for _, id := range order {
		op := operations[id]
		family := op.family
		if family == "" {
			family = "unrecorded"
		}
		row := byFamily[family]
		if row == nil {
			row = &WorkToolFamilyStats{ToolFamily: family}
			byFamily[family] = row
		}
		row.Operations++
		if op.acquired {
			row.Acquired++
			row.WaitTotalMS += op.waitMS
			waits[family] = append(waits[family], op.waitMS)
		}
		switch op.terminal {
		case WorkEventCompleted:
			row.Completed++
		case WorkEventFailed:
			row.Failed++
		case WorkEventCancelled:
			row.Cancelled++
		case WorkEventExpired:
			row.Expired++
		}
		row.RuntimeMS += op.runtimeMS
	}
	result := make([]WorkToolFamilyStats, 0, len(byFamily))
	for family, row := range byFamily {
		values := waits[family]
		sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
		if len(values) > 0 {
			row.WaitP95MS = values[(len(values)*95+99)/100-1]
		}
		result = append(result, *row)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].WaitTotalMS != result[j].WaitTotalMS {
			return result[i].WaitTotalMS > result[j].WaitTotalMS
		}
		return result[i].ToolFamily < result[j].ToolFamily
	})
	return result
}

package sessionpressure

import (
	"path/filepath"
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

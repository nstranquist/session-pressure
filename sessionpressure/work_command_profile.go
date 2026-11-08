package sessionpressure

import (
	"path/filepath"
	"strings"
)

// unsupportedWrappedGoOperation identifies Go operations hidden from the direct-Go
// remote selector. This is conservative admission, not shell evaluation: an
// environment wrapper must have a reviewed remote profile before it can run.
// In particular a Mac executable named by an environment variable cannot be
// uploaded to a Linux test runner and treated as equivalent test coverage.
func unsupportedWrappedGoOperation(argv []string) string {
	if len(argv) > 1 && filepath.Base(argv[0]) == "go" && !RemoteGoCommand(argv) {
		return goOperationAfterGlobalFlags(argv)
	}
	if len(argv) < 2 || filepath.Base(argv[0]) != "env" {
		return ""
	}
	args := argv[1:]
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-u" || arg == "--unset" || arg == "-C" || arg == "--chdir" || arg == "-P":
			i++
		case arg == "--" || arg == "-" || arg == "-i" || arg == "--ignore-environment" || arg == "-v" || arg == "-0" || strings.HasPrefix(arg, "--unset=") || strings.HasPrefix(arg, "--chdir=") || strings.Contains(arg, "=") && !strings.HasPrefix(arg, "-"):
			continue
		case strings.HasPrefix(arg, "-"):
			// env -S has its own escape and variable-expansion language.
			// Unknown and combined options also need a reviewed profile;
			// guessing their argv boundary could admit a hidden Go command.
			return "wrapped"
		default:
			if filepath.Base(arg) == "env" {
				return unsupportedWrappedGoOperation(args[i:])
			}
			return goOperationAfterGlobalFlags(args[i:])
		}
	}
	return ""
}

func goOperationAfterGlobalFlags(argv []string) string {
	if len(argv) < 2 || filepath.Base(argv[0]) != "go" {
		return ""
	}
	args := argv[1:]
	if args[0] == "-C" && len(args) >= 3 {
		args = args[2:]
	} else if strings.HasPrefix(args[0], "-C=") && len(args) >= 2 {
		args = args[1:]
	}
	switch args[0] {
	case "build", "test", "vet":
		return args[0]
	}
	return ""
}

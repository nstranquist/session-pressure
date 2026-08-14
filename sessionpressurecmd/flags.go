package sessionpressurecmd

// ProductCLIMarker is embedded in the product CLI binary so the nicos-tools
// wrapper can refuse the old ndev-pressure forwarder.
const ProductCLIMarker = "github.com/nstranquist/session-pressure/sessionpressurecmd"

// Flags is the product-CLI subset of ndev global flags.
type Flags struct {
	JSON bool
}

// Main runs the SessionPressure dispatcher. jsonOutput is the ndev --json flag.
func Main(jsonOutput bool, args []string) int {
	return cmdSessionPressure(&Flags{JSON: jsonOutput}, StripSessionPressurePrefix(args))
}

// HelpText is the product CLI help. The ndev wrapper prints the same text.
func HelpText() string {
	return sessionPressureHelp
}

// ParseArgs strips --json and an optional `session pressure` wrapper prefix.
// The desktop client always sends `--json session pressure <leaf>` so the same
// argv works against ndev (which requires the prefix) and ndev-pressure.
// Scanning stops at `--` so a child `ndev --json …` does not flip work run
// into product JSON mode.
func ParseArgs(args []string) (jsonOutput bool, rest []string) {
	rest = make([]string, 0, len(args))
	for i, arg := range args {
		if arg == "--" {
			rest = append(rest, args[i:]...)
			return jsonOutput, StripSessionPressurePrefix(rest)
		}
		if arg == "--json" {
			jsonOutput = true
			continue
		}
		rest = append(rest, arg)
	}
	return jsonOutput, StripSessionPressurePrefix(rest)
}

// StripSessionPressurePrefix removes a leading `session pressure` pair so the
// product CLI accepts the desktop/ndev argv shape.
func StripSessionPressurePrefix(args []string) []string {
	if len(args) >= 2 && args[0] == "session" && args[1] == "pressure" {
		out := make([]string, len(args)-2)
		copy(out, args[2:])
		return out
	}
	return args
}

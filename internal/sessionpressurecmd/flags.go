package sessionpressurecmd

// Flags is the product-CLI subset of ndev global flags.
type Flags struct {
	JSON bool
}

// Main runs the SessionPressure dispatcher. jsonOutput is the ndev --json flag.
func Main(jsonOutput bool, args []string) int {
	return cmdSessionPressure(&Flags{JSON: jsonOutput}, args)
}

// HelpText is the product CLI help. The ndev wrapper prints the same text.
func HelpText() string {
	return sessionPressureHelp
}

// ParseArgs strips a leading --json flag so both `ndev --json session pressure`
// and `ndev-pressure --json status` share one dispatcher.
func ParseArgs(args []string) (jsonOutput bool, rest []string) {
	rest = make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--json" {
			jsonOutput = true
			continue
		}
		rest = append(rest, arg)
	}
	return jsonOutput, rest
}

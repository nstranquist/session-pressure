// Command ndev-pressure is the SessionPressure product CLI.
// ndev session pressure is a compatibility wrapper that execs this binary.
package main

import (
	"os"

	"github.com/nstranquist/session-pressure/sessionpressurecmd"
)

func main() {
	jsonOutput, args := sessionpressurecmd.ParseArgs(os.Args[1:])
	if code := sessionpressurecmd.Main(jsonOutput, args); code != 0 {
		os.Exit(code)
	}
}

// release-cc keeps compiler feature probes from writing -.o into the committed
// checkout. Compile this small adapter on the runner; set PRESSURE_TARGET_CC to
// the target compiler and CC to this executable. Ordinary compile/link output
// paths and caller flags pass through unchanged.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func main() {
	compiler := os.Getenv("PRESSURE_TARGET_CC")
	if compiler == "" {
		fmt.Fprintln(os.Stderr, "PRESSURE_TARGET_CC is required")
		os.Exit(2)
	}
	args := os.Args[1:]
	stdin, compile, output := false, false, false
	for _, arg := range args {
		stdin = stdin || arg == "-"
		compile = compile || arg == "-c"
		output = output || arg == "-o" || strings.HasPrefix(arg, "-o") && len(arg) > 2
	}
	if stdin && compile && !output {
		args = append(args, "-o", os.DevNull)
	}
	cmd := exec.Command(compiler, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			os.Exit(exit.ExitCode())
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

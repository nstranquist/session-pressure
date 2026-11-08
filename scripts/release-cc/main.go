// release-cc keeps compiler feature probes from writing -.o into the committed
// checkout. Compile this small adapter on the runner; set PRESSURE_TARGET_CC to
// the target compiler and CC to this executable. Ordinary compile/link output
// paths and caller flags pass through unchanged.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	scratch, err := os.MkdirTemp("", "pressure-cc-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer os.RemoveAll(scratch)
	// Go and Zig also issue feature probes without -c. Isolate every compiler
	// invocation, preserving relative include, input and explicit output paths.
	args = append([]string(nil), args...)
	for i, arg := range args {
		if filepath.IsAbs(arg) || arg == "-" {
			continue
		}
		if i > 0 && (args[i-1] == "-o" || args[i-1] == "-I" || args[i-1] == "-L") {
			args[i] = filepath.Join(cwd, arg)
			continue
		}
		if (strings.HasPrefix(arg, "-I") || strings.HasPrefix(arg, "-L")) && len(arg) > 2 {
			if !filepath.IsAbs(arg[2:]) {
				args[i] = arg[:2] + filepath.Join(cwd, arg[2:])
			}
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			if _, e := os.Stat(arg); e == nil {
				args[i] = filepath.Join(cwd, arg)
			}
		}
	}
	args = append(args, "-I", cwd)
	cmd := exec.Command(compiler, args...)
	cmd.Dir = scratch
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			_ = os.RemoveAll(scratch)
			os.Exit(exit.ExitCode())
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

package sessionpressurecmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func cmdSessionPressureAPI(g *Flags, args []string) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Print(`Usage: session-pressure [--json] api <serve|status> [flags]

Run or inspect the explicit local SessionPressure control plane.
The API is foreground-only and is never installed or started automatically.

Subcommands:
  serve   Owner-only Unix socket by default; --http accepts loopback only
  status  Probe the Unix socket and report API health

Flags are forwarded to session-pressure-api. See session-pressure-api --help.
`)
		return 0
	}
	if args[0] != "serve" && args[0] != "status" {
		return sessionPressureError("api requires serve or status", 2)
	}
	path, err := resolvePressureAPI()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	forwarded := append([]string(nil), args...)
	if g != nil && g.JSON && args[0] == "status" {
		forwarded = append(forwarded, "--json")
	}
	command := exec.Command(path, forwarded...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	command.Env = os.Environ()
	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				return status.ExitStatus()
			}
		}
		return sessionPressureError(err.Error(), 1)
	}
	return 0
}

func resolvePressureAPI() (string, error) {
	if path := os.Getenv("NDEV_PRESSURE_API_BIN"); executable(path) {
		return path, nil
	}
	if self, err := os.Executable(); err == nil {
		dir := filepath.Dir(self)
		for _, name := range []string{"ndev-pressure-api", "ndev-pressure-api-go"} {
			path := filepath.Join(dir, name)
			if executable(path) {
				return path, nil
			}
		}
	}
	if dir, err := nicosDevDir(); err == nil {
		for _, path := range []string{filepath.Join(dir, "bin", "ndev-pressure-api"), filepath.Join(dir, "bin", "ndev-pressure-api-go")} {
			if executable(path) {
				return path, nil
			}
		}
	}
	if path, err := exec.LookPath("ndev-pressure-api"); err == nil && executable(path) {
		return path, nil
	}
	return "", errors.New("ndev-pressure-api not found; build/install it or set NDEV_PRESSURE_API_BIN")
}

// Command ndev-pressure-api runs the explicit, foreground SessionPressure
// control plane. It owns transport and approval plumbing only; ndev remains
// the canonical authority for pressure projections and mutations.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nstranquist/session-pressure/internal/sessionpressure"
	control "github.com/nstranquist/session-pressure/internal/sessionpressurecontrol"
)

func main() {
	if code := run(os.Args[1:]); code != 0 {
		os.Exit(code)
	}
}

func run(args []string) int {
	sub := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	if sub == "help" || sub == "--help" || sub == "-h" {
		printHelp()
		return 0
	}
	set := flag.NewFlagSet("ndev-pressure-api "+sub, flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	stateDir := set.String("state-dir", "", "SessionPressure state directory; also pins canonical ndev authority")
	socket := set.String("socket", "", "Unix socket path")
	httpAddr := set.String("http", "", "explicit loopback HTTP address")
	tokenPath := set.String("token", "", "loopback bearer token file")
	ndevBin := set.String("ndev-bin", "", "canonical ndev binary")
	jsonOutput := set.Bool("json", false, "emit JSON for status")
	if err := set.Parse(args); err != nil {
		return 2
	}
	if set.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "ndev-pressure-api: unexpected argument %q\n", set.Arg(0))
		return 2
	}
	if *stateDir == "" {
		var err error
		*stateDir, err = sessionpressure.DataDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "ndev-pressure-api:", err)
			return 1
		}
	}
	if *socket == "" {
		*socket = filepath.Join(*stateDir, "api", "session-pressure.sock")
	}
	if *tokenPath == "" {
		*tokenPath = filepath.Join(*stateDir, "api", "http.token")
	}
	cfg := control.Config{StateDir: *stateDir, SocketPath: *socket, HTTPAddr: *httpAddr, TokenPath: *tokenPath, NDevBin: *ndevBin}
	server, err := control.New(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ndev-pressure-api:", err)
		return 1
	}
	switch sub {
	case "serve":
		return serve(server)
	case "status":
		return status(*socket, *jsonOutput)
	default:
		fmt.Fprintf(os.Stderr, "ndev-pressure-api: unknown subcommand %q\n", sub)
		printHelp()
		return 2
	}
}

func serve(server *control.Server) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if server.Config().HTTPAddr != "" {
		fmt.Fprintf(os.Stderr, "ndev-pressure-api: foreground loopback listener at %s; token file %s\n", server.Config().HTTPAddr, server.Config().TokenPath)
	} else {
		fmt.Fprintf(os.Stderr, "ndev-pressure-api: foreground Unix listener at %s\n", server.Config().SocketPath)
	}
	if err := server.Serve(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "ndev-pressure-api:", err)
		return 1
	}
	return 0
}

func status(socket string, jsonOutput bool) int {
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	response, err := client.Get("http://session-pressure/v1/health")
	if err != nil {
		if jsonOutput {
			fmt.Printf("{\"ok\":false,\"action\":\"api.status\",\"socket\":%q,\"error\":%q}\n", socket, err.Error())
		} else {
			fmt.Fprintf(os.Stderr, "ndev-pressure-api: unavailable at %s: %v\n", socket, err)
		}
		return 1
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if readErr != nil {
		fmt.Fprintln(os.Stderr, "ndev-pressure-api: read status:", readErr)
		return 1
	}
	if jsonOutput {
		fmt.Println(string(body))
	} else {
		fmt.Printf("SessionPressure API %s at %s\n", response.Status, socket)
	}
	if response.StatusCode >= 400 {
		return 1
	}
	return 0
}

func printHelp() {
	fmt.Print(`Usage: ndev-pressure-api <serve|status> [flags]

Explicit foreground control plane for local SessionPressure.

Subcommands:
  serve   Listen on the owner-only Unix socket, or explicit loopback HTTP with --http
  status  Probe the default/configured Unix socket

Flags:
  --state-dir DIR  SessionPressure state directory and canonical ndev authority state
  --socket PATH    Unix socket path
  --http ADDR      loopback address such as 127.0.0.1:8765; never public
  --token PATH     bearer token file for loopback HTTP
  --ndev-bin PATH  canonical ndev authority binary
  --json           JSON output for status
`)
}

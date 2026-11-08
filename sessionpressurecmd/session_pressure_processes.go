package sessionpressurecmd

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nstranquist/session-pressure/sessionpressure"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const processIncidentsHelp = `Usage: session-pressure [--json] processes <subcommand>

  record --receipt FILE        Record an operator-confirmed historical stop
  list                         Show up to 200 retained process incidents
  stop --id ID --confirm       Stop exactly the selected process with SIGTERM
  stop --id ID --force --confirm  Escalate an earlier Stop to SIGKILL
  run --timeout D -- COMMAND [ARGS...]  Bound a scan to at most 1 hour

The resident alerts after an orphaned process is at least 10 minutes old and
uses >=60% of one core for 2 minutes. Other processes need 30 minutes of age
and 5 minutes of sustained CPU use. Gaps beyond two inventory intervals plus
one sampling interval reset the evidence (minimum tolerance: 3 minutes).
No process is stopped automatically by detection. Stop verifies kernel birth
identity and ownership, records intent before signaling, and checks exit.
Run creates a private process group. Its deadline or cancellation stops that
owned group and records the incident. Use a narrow scan root as well:
  session-pressure processes run --timeout 2m -- rg --files /path/to/project
For expensive scans, wrap this command in work run --class heavy --.
Command arguments and output are not stored in incident history.
A historical receipt contains pid, ppid, executable (basename), at (RFC3339),
and signal (TERM or KILL). It is marked operator-reported and cannot be stopped.
`

func cmdSessionPressureProcesses(g *Flags, args []string) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" {
		fmt.Print(processIncidentsHelp)
		return 0
	}
	dir, err := sessionpressure.DataDir()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return sessionPressureError("processes list takes no arguments", 2)
		}
		report, err := sessionpressure.ReadProcessIncidents(dir)
		if err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		return emitPressure(g, map[string]any{"ok": true, "report": report}, fmt.Sprintf("%d retained process incidents\n", len(report.Incidents)), 0)
	case "stop":
		id := ""
		confirm, force := false, false
		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "--id":
				i++
				if i >= len(args) || id != "" {
					return sessionPressureError("stop requires one --id value", 2)
				}
				id = args[i]
			case "--confirm":
				confirm = true
			case "--force":
				force = true
			default:
				return sessionPressureError("unknown processes stop flag: "+args[i], 2)
			}
		}
		if id == "" || !confirm {
			return sessionPressureError("processes stop requires --id ID --confirm; it signals the selected live process", 2)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		incident, err := sessionpressure.StopProcessIncident(ctx, dir, id, force)
		if err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		return emitPressure(g, map[string]any{"ok": true, "incident": incident}, fmt.Sprintf("PID %d: %s\n", incident.PID, incident.State), 0)
	case "record":
		if len(args) != 3 || args[1] != "--receipt" {
			return sessionPressureError("processes record requires --receipt FILE", 2)
		}
		f, err := os.Open(args[2])
		if err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		body, readErr := io.ReadAll(io.LimitReader(f, 16*1024+1))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil || len(body) > 16*1024 {
			return sessionPressureError("cannot read receipt (limit 16 KiB)", 1)
		}
		var receipt sessionpressure.ResolvedProcessReceipt
		decoder := json.NewDecoder(strings.NewReader(string(body)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&receipt); err != nil {
			return sessionPressureError("invalid process receipt: "+err.Error(), 2)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return sessionPressureError("receipt must contain exactly one JSON object", 2)
		}
		incident, err := sessionpressure.RecordResolvedProcessIncident(context.Background(), dir, receipt)
		if err != nil {
			return sessionPressureError(err.Error(), 1)
		}
		return emitPressure(g, map[string]any{"ok": true, "incident": incident}, fmt.Sprintf("Recorded historical stop of PID %d (operator report)\n", incident.PID), 0)
	case "run":
		if len(args) < 5 || args[1] != "--timeout" || args[3] != "--" {
			return sessionPressureError("processes run requires --timeout D -- COMMAND [ARGS...]", 2)
		}
		duration, err := time.ParseDuration(args[2])
		if err != nil || duration <= 0 || duration > time.Hour {
			return sessionPressureError("--timeout must be positive and at most 1h", 2)
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		code, err := sessionpressure.RunBoundedProcess(ctx, dir, duration, args[4:], os.Stdin, os.Stdout, os.Stderr)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		return code
	default:
		return sessionPressureError("unknown processes subcommand; use processes --help", 2)
	}
}

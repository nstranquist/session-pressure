package jsonl

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestAppendLineExclusiveAcrossProcesses runs AppendLine from several
// processes back to back and checks from a ledger that no two appends were
// inside the lock at the same time. Back-to-back releases overlap other
// processes' create attempts, which is the window where a waiter could
// delete a successor's live sidecar.
//
// JSONL_STRESS_PROCS, JSONL_STRESS_ROUNDS and JSONL_STRESS_HOLD scale the
// run for a stress A/B; the defaults keep it to about one second.
func TestAppendLineExclusiveAcrossProcesses(t *testing.T) {
	procs := envInt(t, "JSONL_STRESS_PROCS", 12)
	rounds := envInt(t, "JSONL_STRESS_ROUNDS", 20)
	hold := os.Getenv("JSONL_STRESS_HOLD")
	if hold == "" {
		hold = "1ms"
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "events.jsonl")
	ledger := filepath.Join(dir, "critical-section.log")
	started := time.Now()
	commands := make([]*exec.Cmd, 0, procs)
	for index := 0; index < procs; index++ {
		command := exec.Command(os.Args[0], "-test.run=^TestAppendLineProcessHelper$")
		command.Env = append(os.Environ(),
			"JSONL_HELPER_TARGET="+target,
			"JSONL_HELPER_LEDGER="+ledger,
			"JSONL_HELPER_ROUNDS="+strconv.Itoa(rounds),
			"JSONL_HELPER_HOLD="+hold,
		)
		var stderr strings.Builder
		command.Stdout = &stderr
		command.Stderr = &stderr
		if err := command.Start(); err != nil {
			t.Fatalf("start helper %d: %v", index, err)
		}
		commands = append(commands, command)
		t.Cleanup(func() {
			if t.Failed() && stderr.Len() > 0 {
				t.Logf("helper %d output: %s", index, stderr.String())
			}
		})
	}
	for index, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("wait for helper %d: %v", index, err)
		}
	}
	elapsed := time.Since(started)
	body, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	inside := map[string]bool{}
	overlaps, completed := 0, 0
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("malformed ledger line %q", line)
		}
		switch fields[0] {
		case "start":
			if len(inside) > 0 {
				overlaps++
			}
			inside[fields[1]] = true
		case "end":
			if !inside[fields[1]] {
				t.Fatalf("ledger end without start: %s", fields[1])
			}
			delete(inside, fields[1])
			completed++
		default:
			t.Fatalf("unknown ledger row %q", line)
		}
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	rows := strings.Count(string(data), "\n")
	t.Logf("procs=%d rounds=%d hold=%s appends=%d rows=%d overlaps=%d wall=%s per_append=%s",
		procs, rounds, hold, completed, rows, overlaps, elapsed.Round(time.Millisecond), (elapsed / time.Duration(max(completed, 1))).Round(time.Microsecond))
	if overlaps != 0 {
		t.Fatalf("cross-process appends overlapped %d times in %d appends", overlaps, completed)
	}
	if completed != procs*rounds || rows != procs*rounds {
		t.Fatalf("appends=%d rows=%d, want %d", completed, rows, procs*rounds)
	}
}

func TestAppendLineProcessHelper(t *testing.T) {
	target := os.Getenv("JSONL_HELPER_TARGET")
	ledger := os.Getenv("JSONL_HELPER_LEDGER")
	if target == "" || ledger == "" {
		return
	}
	rounds := envInt(t, "JSONL_HELPER_ROUNDS", 1)
	hold, err := time.ParseDuration(os.Getenv("JSONL_HELPER_HOLD"))
	if err != nil {
		t.Fatalf("JSONL_HELPER_HOLD: %v", err)
	}
	ledgerFile, err := os.OpenFile(ledger, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer ledgerFile.Close()
	var id string
	testHookInsideLock = func() {
		_, _ = fmt.Fprintf(ledgerFile, "start %s\n", id)
		spin(hold)
		_, _ = fmt.Fprintf(ledgerFile, "end %s\n", id)
	}
	for round := 0; round < rounds; round++ {
		id = fmt.Sprintf("%d.%d", os.Getpid(), round)
		// The test checks exclusion, not latency: a long wait budget keeps a
		// loaded host from failing a helper on the 5 s default.
		if err := AppendLineWithin(target, []byte(fmt.Sprintf(`{"id":%q}`, id)), 0o600, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
}

// spin holds the CPU for d. A sleep under 1 ms rounds up on some kernels.
func spin(d time.Duration) {
	for end := time.Now().Add(d); time.Now().Before(end); {
	}
}

func envInt(t *testing.T, name string, fallback int) int {
	t.Helper()
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		t.Fatalf("%s must be a positive integer, got %q", name, raw)
	}
	return value
}

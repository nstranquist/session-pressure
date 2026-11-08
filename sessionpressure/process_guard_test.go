package sessionpressure

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

func guardFixture(t *testing.T) (*ProcessGuard, []Process, time.Time) {
	t.Helper()
	g := NewProcessGuard(t.TempDir())
	g.identity = func(int) (string, int, error) { return "test-birth", os.Getuid(), nil }
	return g, []Process{{PID: 987654, PPID: 1, CPUPercent: 76, CPUAvailable: true, ElapsedSeconds: 10800, Executable: "python3", RSSKB: 2048}}, time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
}

func TestProcessGuardSustainedOrphanAndAlertDedup(t *testing.T) {
	g, rows, at := guardFixture(t)
	alerts := 0
	g.Notify = func(p ProcessIncident) error {
		alerts++
		if p.Reason != "init_parent_sustained_cpu" {
			t.Fatal(p.Reason)
		}
		return nil
	}
	for _, delta := range []time.Duration{0, time.Minute, 2 * time.Minute, 3 * time.Minute} {
		if err := g.Observe(context.Background(), at.Add(delta), rows); err != nil {
			t.Fatal(err)
		}
	}
	r, err := ReadProcessIncidents(g.Dir)
	if err != nil || len(r.Incidents) != 1 || alerts != 1 {
		t.Fatalf("report=%+v alerts=%d err=%v", r, alerts, err)
	}
	p := r.Incidents[0]
	if p.PID != 987654 || p.CPUPercent != 76 || p.SustainedSeconds != 180 || p.RSSMB != 2 {
		t.Fatal(p)
	}
	// Restart does not send another notification for the same birth identity.
	restart := NewProcessGuard(g.Dir)
	restart.identity = g.identity
	restart.Notify = g.Notify
	for _, delta := range []time.Duration{4 * time.Minute, 5 * time.Minute, 6 * time.Minute} {
		if err := restart.Observe(context.Background(), at.Add(delta), rows); err != nil {
			t.Fatal(err)
		}
	}
	if alerts != 1 {
		t.Fatal("restart duplicated alert")
	}
	if err := g.Observe(context.Background(), at.Add(7*time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	r, _ = ReadProcessIncidents(g.Dir)
	if r.Incidents[0].State != "recovered" {
		t.Fatal(r.Incidents[0])
	}
}

func TestProcessGuardRejectsSpikesStaleUnknownAndGaps(t *testing.T) {
	g, rows, at := guardFixture(t)
	for _, delta := range []time.Duration{0, 0, time.Minute, 5 * time.Minute, 6 * time.Minute} {
		if err := g.Observe(context.Background(), at.Add(delta), rows); err != nil {
			t.Fatal(err)
		}
	}
	r, _ := ReadProcessIncidents(g.Dir)
	if len(r.Incidents) != 0 {
		t.Fatal("gap counted as continuous CPU")
	}
	rows[0].CPUAvailable = false
	if err := g.Observe(context.Background(), at.Add(7*time.Minute), rows); err != nil {
		t.Fatal(err)
	}
	rows[0].CPUAvailable = true
	if err := g.Observe(context.Background(), at.Add(8*time.Minute), rows); err != nil {
		t.Fatal(err)
	}
	r, _ = ReadProcessIncidents(g.Dir)
	if len(r.Incidents) != 0 {
		t.Fatal("unknown CPU counted as hot")
	}
}

func TestProcessGuardOrphanedShellAndNormalLongWork(t *testing.T) {
	for _, shell := range []bool{false, true} {
		t.Run(fmt.Sprint(shell), func(t *testing.T) {
			g, rows, at := guardFixture(t)
			rows[0].PPID = 44
			parent := Process{PID: 44, PPID: 1, Executable: "daemon"}
			window := 5 * time.Minute
			if shell {
				parent.Executable = "zsh"
				window = 2 * time.Minute
			}
			rows = append(rows, parent)
			for delta := time.Duration(0); delta <= window; delta += time.Minute {
				if err := g.Observe(context.Background(), at.Add(delta), rows); err != nil {
					t.Fatal(err)
				}
			}
			r, _ := ReadProcessIncidents(g.Dir)
			if len(r.Incidents) != 1 {
				t.Fatal(r)
			}
			if shell && r.Incidents[0].Reason != "init_parent_sustained_cpu" {
				t.Fatal(r)
			}
		})
	}
}

func TestProcessGuardRejectsForeignOwnerAndReusedBirth(t *testing.T) {
	g, rows, at := guardFixture(t)
	g.identity = func(int) (string, int, error) { return "other", os.Getuid() + 1, nil }
	for i := 0; i < 4; i++ {
		if err := g.Observe(context.Background(), at.Add(time.Duration(i)*time.Minute), rows); err != nil {
			t.Fatal(err)
		}
	}
	r, _ := ReadProcessIncidents(g.Dir)
	if len(r.Incidents) != 0 {
		t.Fatal("foreign owner detected as actionable")
	}
	g.identity = func(int) (string, int, error) { return fmt.Sprintf("new-%d", g.lastInventory.Unix()), os.Getuid(), nil }
	for i := 4; i < 8; i++ {
		if err := g.Observe(context.Background(), at.Add(time.Duration(i)*time.Minute), rows); err != nil {
			t.Fatal(err)
		}
	}
	r, _ = ReadProcessIncidents(g.Dir)
	if len(r.Incidents) != 0 {
		t.Fatal("reused PID inherited CPU evidence")
	}
}

func activeGuardIncident(t *testing.T) ProcessIncident {
	t.Helper()
	return ProcessIncident{ID: "fixture", PID: 987654, UID: os.Getuid(), StartIdentity: "birth", Executable: "fixture", State: "active", FirstSeen: time.Now(), LastSeen: time.Now()}
}
func seedGuardIncident(t *testing.T, dir string, p ProcessIncident) {
	t.Helper()
	if err := updateProcessIncidents(context.Background(), dir, func(r *ProcessIncidentReport) error { r.Incidents = append(r.Incidents, p); return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestProcessIncidentStopRevalidatesAndPersistsBeforeSignal(t *testing.T) {
	dir := t.TempDir()
	p := activeGuardIncident(t)
	seedGuardIncident(t, dir, p)
	calls := 0
	signalCalls := 0
	identity := func(int) (string, int, error) {
		calls++
		if calls >= 3 {
			return "", 0, errProcessGuardExited
		}
		return "birth", os.Getuid(), nil
	}
	signal := func(pid int, s syscall.Signal) error {
		signalCalls++
		r, err := ReadProcessIncidents(dir)
		if err != nil || len(r.Incidents[0].Actions) != 1 || r.Incidents[0].Actions[0].Result != "requested" {
			t.Fatal("signal preceded durable intent", r, err)
		}
		if pid != p.PID || s != syscall.SIGTERM {
			t.Fatal("wrong target", pid, s)
		}
		return nil
	}
	got, err := stopProcessIncident(context.Background(), dir, p.ID, false, identity, signal)
	if err != nil || got.State != "stopped" || signalCalls != 1 || got.Actions[0].Result != "exit_confirmed" {
		t.Fatalf("%+v %v calls=%d", got, err, signalCalls)
	}
}

func TestProcessIncidentStopRejectsIdentityRacesAndForceFirst(t *testing.T) {
	for _, race := range []int{1, 2} {
		t.Run(fmt.Sprint(race), func(t *testing.T) {
			dir := t.TempDir()
			p := activeGuardIncident(t)
			seedGuardIncident(t, dir, p)
			calls := 0
			signaled := false
			identity := func(int) (string, int, error) {
				calls++
				if calls == race {
					return "reused", os.Getuid(), nil
				}
				return "birth", os.Getuid(), nil
			}
			signal := func(int, syscall.Signal) error { signaled = true; return nil }
			if _, err := stopProcessIncident(context.Background(), dir, p.ID, false, identity, signal); err == nil || signaled {
				t.Fatal("identity race signaled")
			}
		})
	}
	dir := t.TempDir()
	p := activeGuardIncident(t)
	seedGuardIncident(t, dir, p)
	if _, err := stopProcessIncident(context.Background(), dir, p.ID, true, processGuardIdentity, func(int, syscall.Signal) error { t.Fatal("force first signaled"); return nil }); err == nil {
		t.Fatal("force first accepted")
	}
}

func TestProcessIncidentStorageFailurePreventsSignal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(processIncidentPath(dir), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := stopProcessIncident(context.Background(), dir, "fixture", false, processGuardIdentity, func(int, syscall.Signal) error { t.Fatal("corrupt ledger signaled"); return nil })
	if err == nil {
		t.Fatal("corrupt ledger accepted")
	}
}

func TestProcessIncidentsConcurrentUpdates(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := updateProcessIncidents(context.Background(), dir, func(r *ProcessIncidentReport) error {
				r.Incidents = append(r.Incidents, ProcessIncident{ID: fmt.Sprint(i)})
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	r, err := ReadProcessIncidents(dir)
	if err != nil || len(r.Incidents) != 8 {
		t.Fatal(r, err)
	}
	info, err := os.Stat(processIncidentPath(dir))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private permissions", info, err)
	}
}

func TestProcessGuardStopsOnlyOwnedFixture(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	start, uid, err := processGuardIdentity(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := activeGuardIncident(t)
	p.PID = cmd.Process.Pid
	p.StartIdentity = start
	p.UID = uid
	seedGuardIncident(t, dir, p)
	got, err := StopProcessIncident(context.Background(), dir, p.ID, false)
	if err != nil || got.State != "stopped" {
		t.Fatal(got, err)
	}
}

func TestBoundedProcessDeadlineAndSuccess(t *testing.T) {
	dir := t.TempDir()
	code, err := RunBoundedProcess(context.Background(), dir, 100*time.Millisecond, []string{"/bin/sleep", "60"}, nil, nil, nil)
	if code != 124 || err != nil {
		t.Fatal(code, err)
	}
	r, err := ReadProcessIncidents(dir)
	if err != nil || len(r.Incidents) != 1 || r.Incidents[0].State != "stopped" || r.Incidents[0].Reason != "guard_deadline" {
		t.Fatal(r, err)
	}
	code, err = RunBoundedProcess(context.Background(), dir, time.Second, []string{"/bin/sh", "-c", "exit 0"}, nil, nil, nil)
	if code != 0 || err != nil {
		t.Fatal(code, err)
	}
	if _, err = os.Stat(filepath.Join(dir, "process-incidents.v1.json")); err != nil {
		t.Fatal(err)
	}
}

func BenchmarkProcessGuardInventory(b *testing.B) {
	rows := make([]Process, 4000)
	for i := range rows {
		rows[i] = Process{PID: i + 2, CPUAvailable: true, CPUPercent: 1, ElapsedSeconds: 1000}
	}
	g := NewProcessGuard(b.TempDir())
	at := time.Now()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := g.Observe(context.Background(), at.Add(time.Duration(i)*time.Minute), rows); err != nil {
			b.Fatal(err)
		}
	}
}

func TestProcessGuardAlertIsDurableAndRetriesFailure(t *testing.T) {
	g, rows, at := guardFixture(t)
	calls := 0
	g.Notify = func(p ProcessIncident) error {
		r, err := ReadProcessIncidents(g.Dir)
		if err != nil || len(r.Incidents) != 1 || r.Incidents[0].ID != p.ID {
			t.Fatal("alert preceded durable incident", r, err)
		}
		calls++
		if calls == 1 {
			return fmt.Errorf("fixture delivery failure")
		}
		return nil
	}
	for i := 0; i < 4; i++ {
		err := g.Observe(context.Background(), at.Add(time.Duration(i)*time.Minute), rows)
		if (i == 2) != (err != nil) {
			t.Fatalf("sample %d: %v", i, err)
		}
	}
	if calls != 2 {
		t.Fatal(calls)
	}
	r, err := ReadProcessIncidents(g.Dir)
	if err != nil || r.Incidents[0].AlertedAt == nil {
		t.Fatal(r, err)
	}
}

func TestProcessGuardIdleDoesNotRewriteLedger(t *testing.T) {
	g, _, at := guardFixture(t)
	if err := g.Observe(context.Background(), at, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(processIncidentPath(g.Dir)); !os.IsNotExist(err) {
		t.Fatal("idle guard wrote an empty ledger", err)
	}
	p := activeGuardIncident(t)
	p.State = "stopped"
	seedGuardIncident(t, g.Dir, p)
	before, err := os.Stat(processIncidentPath(g.Dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Observe(context.Background(), at.Add(time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(processIncidentPath(g.Dir))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("unchanged ledger was replaced")
	}
}

func TestProcessIncidentHistoricalRecordCannotSignal(t *testing.T) {
	dir := t.TempDir()
	receipt := ResolvedProcessReceipt{PID: 8523, PPID: 8475, Executable: "python3", At: time.Now().Add(-time.Hour), Signal: "TERM"}
	p, err := RecordResolvedProcessIncident(context.Background(), dir, receipt)
	if err != nil || p.Source != "operator_record" || p.State != "recorded_closed" || p.StartIdentity != "" {
		t.Fatal(p, err)
	}
	if _, err := RecordResolvedProcessIncident(context.Background(), dir, receipt); err != nil {
		t.Fatal(err)
	}
	r, err := ReadProcessIncidents(dir)
	if err != nil || len(r.Incidents) != 1 {
		t.Fatal(r, err)
	}
	if _, err := stopProcessIncident(context.Background(), dir, p.ID, false, processGuardIdentity, func(int, syscall.Signal) error { t.Fatal("historical record signaled"); return nil }); err == nil {
		t.Fatal("historical record was actionable")
	}
	receipt.Executable = "/private/path/python3"
	if _, err := RecordResolvedProcessIncident(context.Background(), dir, receipt); err == nil {
		t.Fatal("accepted full path")
	}
}

func TestProcessIncidentObservedExitDoesNotClaimSignal(t *testing.T) {
	receipt := ResolvedProcessReceipt{PID: 5897, PPID: 5883, Executable: "Google Chrome", At: time.Now().Add(-time.Minute), Signal: "NONE"}
	p, err := RecordResolvedProcessIncident(context.Background(), t.TempDir(), receipt)
	if err != nil {
		t.Fatal(err)
	}
	if p.Reason != "operator_recorded_exit" || p.State != "recorded_closed" || p.Actions[0].Signal != "NONE" || p.Actions[0].Result != "operator_observed_exit" {
		t.Fatal("observed exit was reported as a signal", p)
	}
}

func TestBoundedProcessCancellationIsRecorded(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(100*time.Millisecond, cancel)
	defer timer.Stop()
	defer cancel()
	code, err := RunBoundedProcess(ctx, dir, time.Minute, []string{"/bin/sleep", "60"}, nil, nil, nil)
	if code != 124 || err != nil {
		t.Fatal(code, err)
	}
	r, err := ReadProcessIncidents(dir)
	if err != nil || len(r.Incidents) != 1 || r.Incidents[0].Reason != "guard_cancelled" || r.Incidents[0].State != "stopped" {
		t.Fatal(r, err)
	}
}

func TestProcessGuardNativeInventoryBirthMustMatch(t *testing.T) {
	g, rows, at := guardFixture(t)
	rows[0].StartedAtNS = at.UnixNano()
	g.identity = func(int) (string, int, error) { return "darwin:1:2", os.Getuid(), nil }
	for i := 0; i < 4; i++ {
		if err := g.Observe(context.Background(), at.Add(time.Duration(i)*time.Minute), rows); err != nil {
			t.Fatal(err)
		}
	}
	r, err := ReadProcessIncidents(g.Dir)
	if err != nil || len(r.Incidents) != 0 {
		t.Fatal("recycled PID received stale CPU evidence", r, err)
	}
}

func TestProcessIncidentIntentWriteFailurePreventsSignal(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "ledger")
	p := activeGuardIncident(t)
	seedGuardIncident(t, dir, p)
	identity := func(int) (string, int, error) {
		if err := os.Rename(dir, filepath.Join(parent, "moved")); err != nil {
			t.Fatal(err)
		}
		return p.StartIdentity, p.UID, nil
	}
	if _, err := stopProcessIncident(context.Background(), dir, p.ID, false, identity, func(int, syscall.Signal) error { t.Fatal("failed intent write signaled"); return nil }); err == nil {
		t.Fatal("intent storage failure ignored")
	}
}

func TestProcessIncidentForceRequiresPriorStopAndChecksExit(t *testing.T) {
	dir := t.TempDir()
	p := activeGuardIncident(t)
	p.State = "stop_requested"
	seedGuardIncident(t, dir, p)
	signaled := false
	identity := func(int) (string, int, error) {
		if signaled {
			return "", 0, errProcessGuardExited
		}
		return p.StartIdentity, p.UID, nil
	}
	got, err := stopProcessIncident(context.Background(), dir, p.ID, true, identity, func(pid int, s syscall.Signal) error {
		if pid != p.PID || s != syscall.SIGKILL {
			t.Fatal(pid, s)
		}
		signaled = true
		return nil
	})
	if err != nil || got.State != "stopped" || len(got.Actions) != 1 || got.Actions[0].Signal != "KILL" {
		t.Fatal(got, err)
	}
}

func TestProcessGuardUsesResidentInventoryCadence(t *testing.T) {
	for _, interval := range []int{180, 240} {
		t.Run(fmt.Sprint(interval), func(t *testing.T) {
			guard := NewProcessGuard(t.TempDir())
			policy := DefaultPolicy(16384)
			policy.ProcessInventoryIntervalSeconds = interval
			guard.maxInventoryGap = processIncidentEvidenceGap(policy)
			guard.identity = func(int) (string, int, error) { return "cadence-birth", os.Getuid(), nil }
			at := time.Now().UTC()
			process := Process{PID: 812345, PPID: 1, Executable: "python3", ElapsedSeconds: 20000, CPUAvailable: true, CPUPercent: 76}
			if err := guard.Observe(context.Background(), at, []Process{process}); err != nil {
				t.Fatal(err)
			}
			if err := guard.Observe(context.Background(), at.Add(time.Duration(interval+1)*time.Second), []Process{process}); err != nil {
				t.Fatal(err)
			}
			report, err := ReadProcessIncidents(guard.Dir)
			if err != nil || len(report.Incidents) != 1 || report.Incidents[0].State != "active" {
				t.Fatalf("cadence incident: %+v %v", report, err)
			}
		})
	}
}

func TestBoundedProcessPreservesFastFailureExit(t *testing.T) {
	for range 5 {
		code, err := RunBoundedProcess(context.Background(), t.TempDir(), time.Second, []string{"sh", "-c", "exit 7"}, nil, io.Discard, io.Discard)
		if err != nil || code != 7 {
			t.Fatalf("exit=%d err=%v", code, err)
		}
	}
}

// Run the precompiled Darwin test with PRESSURE_PROCESS_CONTRACT_BIN set to
// the installed CLI. CPU/age observations are test data; process birth and
// signalling are real and limited to this test's owned child and private ledger.
func TestProcessIncidentInstalledCLIStopsOwnedChild(t *testing.T) {
	binary := os.Getenv("PRESSURE_PROCESS_CONTRACT_BIN")
	if binary == "" {
		t.Skip("installed CLI contract is opt-in")
	}
	child := exec.Command("/bin/sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	guard := NewProcessGuard(t.TempDir())
	row := Process{PID: child.Process.Pid, PPID: 1, Executable: "sleep", CPUAvailable: true, CPUPercent: 76, ElapsedSeconds: 10800}
	now := time.Now().UTC()
	for _, at := range []time.Time{now.Add(-6 * time.Minute), now.Add(-3 * time.Minute), now} {
		if err := guard.Observe(context.Background(), at, []Process{row}); err != nil {
			t.Fatal(err)
		}
	}
	report, err := ReadProcessIncidents(guard.Dir)
	if err != nil || len(report.Incidents) != 1 {
		t.Fatalf("fixture: %+v %v", report, err)
	}
	run := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, args...)
		command.Env = append(os.Environ(), "NDEV_SESSION_PRESSURE_HOME="+guard.Dir)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("installed CLI: %v %s", err, output)
		}
		return output
	}
	var response struct {
		OK       bool            `json:"ok"`
		Incident ProcessIncident `json:"incident"`
	}
	if err := json.Unmarshal(run("--json", "processes", "stop", "--id", report.Incidents[0].ID, "--confirm"), &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.Incident.PID != child.Process.Pid || response.Incident.State != "stopped" {
		t.Fatalf("stop response: %+v", response)
	}
	if action := response.Incident.Actions[len(response.Incident.Actions)-1]; action.Signal != "TERM" || action.Result != "exit_confirmed" {
		t.Fatalf("action: %+v", action)
	}
	var history struct {
		OK     bool                  `json:"ok"`
		Report ProcessIncidentReport `json:"report"`
	}
	if err := json.Unmarshal(run("--json", "processes", "list"), &history); err != nil {
		t.Fatal(err)
	}
	if !history.OK || len(history.Report.Incidents) != 1 || history.Report.Incidents[0].State != "stopped" {
		t.Fatalf("history: %+v", history)
	}
}

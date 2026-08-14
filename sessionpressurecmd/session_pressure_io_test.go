package sessionpressurecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/internal/operationcontract"
	"github.com/nstranquist/session-pressure/sessionpressure"
)

func seedPressureIOTest(t *testing.T) (string, sessionpressure.DiskWriteSummary) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	policy := sessionpressure.DefaultPolicy(16 * 1024)
	if err := sessionpressure.SavePolicy(sessionpressure.PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	summary := sessionpressure.DiskWriteSummary{
		SchemaVersion:        sessionpressure.DiskWriteSummarySchemaVersion,
		ModelVersion:         sessionpressure.DiskWriteProfileQuietAdaptiveV1,
		CapturedAt:           now,
		State:                sessionpressure.DiskWriteStateNormal,
		Confidence:           sessionpressure.DiskWriteConfidenceConfident,
		Source:               "test",
		DeviceScope:          "internal_ssd",
		AttributionScope:     "all_disk_io_best_effort",
		Context:              "uncoordinated",
		Window15mBytes:       64 << 20,
		Bytes24h:             512 << 20,
		BaselineP99Bytes15m:  32 << 20,
		BaselineRatio:        2,
		BaselineSamples:      7_000,
		DeviceCount:          1,
		TotalPIDCount:        10,
		AccessiblePIDCount:   9,
		AttributionAvailable: true,
		WriterAvailableCount: 7,
	}
	writer := sessionpressure.DiskWriter{DiskWriterSummary: sessionpressure.DiskWriterSummary{
		Executable: "sqlite3", Category: "database", ProcessCount: 1,
		AgentProcessCount: 0, WindowBytes: 16 << 20, BytesPerSecond: 2048,
	}, PID: os.Getpid(), ProcessStartID: 99}
	summary.TopWriter = &writer.DiskWriterSummary
	if err := sessionpressure.NewTelemetryStore(dir).WriteLatest(sessionpressure.Snapshot{
		SchemaVersion: 1, Timestamp: now, Level: sessionpressure.LevelNormal,
		DiskWrite: &summary,
	}); err != nil {
		t.Fatal(err)
	}
	history := map[string]any{
		"schema_version":     1,
		"model_version":      sessionpressure.DiskWriteProfileQuietAdaptiveV1,
		"hour":               now.Truncate(time.Hour),
		"state":              sessionpressure.DiskWriteStateNormal,
		"bytes_written":      128 << 20,
		"sample_count":       240,
		"baseline_p99_bytes": 32 << 20,
		"contexts":           map[string]any{},
	}
	body, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "disk-writes-"+now.Local().Format("20060102")+".jsonl")
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, summary
}

func TestCmdSessionPressureIOReadContractsAndPrivacy(t *testing.T) {
	_, _ = seedPressureIOTest(t)
	commands := [][]string{
		{"pressure", "io", "status"},
		{"pressure", "io", "status", "--full"},
		{"pressure", "io", "top", "--limit", "1"},
		{"pressure", "io", "history", "--since", "24h", "--limit", "1"},
		{"pressure", "io", "policy", "show"},
		{"pressure", "io", "trace", "--pid", strconv.Itoa(os.Getpid()), "--duration", "5s"},
	}
	for _, args := range commands {
		t.Run(strings.Join(args[2:], "_"), func(t *testing.T) {
			rc, stdout, stderr := captureMainOutput(t, func() int {
				return cmdSessionPressure(&Flags{JSON: true}, args[1:])
			})
			if rc != 0 || stderr != "" {
				t.Fatalf("cmdSessionPressure(%v) rc=%d stderr=%q\n%s", args[1:], rc, stderr, stdout)
			}
			if err := operationcontract.ValidateOutputJSON(operationcontract.All(), "nicos.session.pressure-result.v1", []byte(stdout)); err != nil {
				t.Fatalf("cmdSessionPressure(%v) output contract: %v\n%s", args[1:], err, stdout)
			}
			if len(stdout) > 12*1024 {
				t.Fatalf("cmdSessionPressure(%v) payload=%d exceeds largest io budget", args[1:], len(stdout))
			}
			if args[2] == "top" && (strings.Contains(stdout, "\"pid\"") || strings.Contains(stdout, "process_start_id")) {
				t.Fatalf("default top leaked ephemeral process identity: %s", stdout)
			}
		})
	}
	rc, stdout, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressureIOTop(&Flags{JSON: true}, []string{"--limit", "5"})
	})
	if rc != 0 || stderr != "" {
		t.Fatalf("persisted top rc=%d stderr=%q\n%s", rc, stderr, stdout)
	}
	var top struct {
		AvailableCount int    `json:"available_count"`
		ReturnedCount  int    `json:"returned_count"`
		Truncated      bool   `json:"truncated"`
		OutputScope    string `json:"output_scope"`
	}
	if err := json.Unmarshal([]byte(stdout), &top); err != nil ||
		top.AvailableCount != 7 || top.ReturnedCount != 1 || !top.Truncated || top.OutputScope != "persisted_lead" {
		t.Fatalf("persisted writer availability=%+v err=%v\n%s", top, err, stdout)
	}
}

func TestCmdSessionPressureIOPolicyMutationsAreExplicit(t *testing.T) {
	_, _ = seedPressureIOTest(t)
	fake := &fakePressureLaunchdController{status: sessionpressure.LaunchdStatus{OK: true, Loaded: true, PID: 42}}
	installFakePressureLaunchd(t, fake)

	rc, stdout, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressureIOPolicy(&Flags{JSON: true}, []string{"enable-alerts"})
	})
	if rc != 0 || stderr != "" || fake.ensureCalls != 1 {
		t.Fatalf("enable alerts rc=%d ensure=%d stderr=%q\n%s", rc, fake.ensureCalls, stderr, stdout)
	}
	var enabled struct {
		Policy sessionpressure.DiskWritePolicy `json:"disk_write_policy"`
	}
	if err := json.Unmarshal([]byte(stdout), &enabled); err != nil || !enabled.Policy.Enabled || !enabled.Policy.NotificationsEnabled {
		t.Fatalf("enable payload=%+v err=%v\n%s", enabled, err, stdout)
	}

	rc, stdout, stderr = captureMainOutput(t, func() int {
		return cmdSessionPressureIOPolicy(&Flags{JSON: true}, []string{"disable"})
	})
	if rc != 0 || stderr != "" || fake.restartCalls != 1 {
		t.Fatalf("disable rc=%d restart=%d stderr=%q\n%s", rc, fake.restartCalls, stderr, stdout)
	}
	var disabled struct {
		Policy sessionpressure.DiskWritePolicy `json:"disk_write_policy"`
	}
	if err := json.Unmarshal([]byte(stdout), &disabled); err != nil || disabled.Policy.Enabled || disabled.Policy.NotificationsEnabled {
		t.Fatalf("disable payload=%+v err=%v\n%s", disabled, err, stdout)
	}
}

func TestCmdSessionPressureIOTopUsesStableEmptyArray(t *testing.T) {
	dir, summary := seedPressureIOTest(t)
	summary.TopWriter = nil
	summary.WriterAvailableCount = 0
	if err := sessionpressure.NewTelemetryStore(dir).WriteLatest(sessionpressure.Snapshot{
		SchemaVersion: 1, Timestamp: summary.CapturedAt, Level: sessionpressure.LevelNormal, DiskWrite: &summary,
	}); err != nil {
		t.Fatal(err)
	}
	rc, stdout, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressureIOTop(&Flags{JSON: true}, nil)
	})
	if rc != 0 || stderr != "" {
		t.Fatalf("top rc=%d stderr=%q\n%s", rc, stderr, stdout)
	}
	var payload struct {
		Writers []json.RawMessage `json:"writers"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil || payload.Writers == nil || len(payload.Writers) != 0 {
		t.Fatalf("empty writer contract=%+v err=%v\n%s", payload, err, stdout)
	}
}

func TestCmdSessionPressureIORejectsUnboundedRequests(t *testing.T) {
	seedPressureIOTest(t)
	for _, args := range [][]string{
		{"top", "--limit", "21"},
		{"history", "--limit", "201"},
		{"history", "--since", "721h"},
		{"trace", "--pid", strconv.Itoa(os.Getpid()), "--duration", "31s"},
	} {
		rc, _, _ := captureMainOutput(t, func() int {
			return cmdSessionPressureIO(&Flags{JSON: true}, args)
		})
		if rc != 2 {
			t.Fatalf("args=%v rc=%d want=2", args, rc)
		}
	}
}

func TestCompactPressureStatusKeepsDiskModelWithoutWriterDetail(t *testing.T) {
	top := &sessionpressure.DiskWriterSummary{
		Executable: "sqlite3", ProcessCount: 2, WindowBytes: 1 << 20,
	}
	summary := &sessionpressure.DiskWriteSummary{
		SchemaVersion:            sessionpressure.DiskWriteSummarySchemaVersion,
		ModelVersion:             sessionpressure.DiskWriteProfileQuietAdaptiveV1,
		State:                    sessionpressure.DiskWriteStateHigh,
		Confidence:               sessionpressure.DiskWriteConfidenceConfident,
		MeasurementWindowSeconds: 15,
		CurrentBytesPerSecond:    2048,
		Window15mBytes:           1 << 20,
		Bytes24h:                 1 << 30,
		BaselineAgeSeconds:       86_400,
		DeviceCount:              1,
		TotalPIDCount:            500,
		AccessiblePIDCount:       400,
		AttributionAvailable:     true,
		WriterAvailableCount:     7,
		TopWriter:                top,
		Reasons:                  []string{"process_attribution_partial"},
	}
	compact := compactPressureDiskWriteSummary(summary)
	if compact == summary || compact.State != summary.State || compact.Confidence != summary.Confidence ||
		compact.CurrentBytesPerSecond != summary.CurrentBytesPerSecond ||
		compact.Window15mBytes != summary.Window15mBytes || compact.Bytes24h != summary.Bytes24h ||
		len(compact.Reasons) != 1 {
		t.Fatalf("compact disk model lost agent-facing state: %+v", compact)
	}
	if compact.MeasurementWindowSeconds != 0 || compact.BaselineAgeSeconds != 0 ||
		compact.DeviceCount != 0 || compact.TotalPIDCount != 0 || compact.AccessiblePIDCount != 0 ||
		compact.WriterAvailableCount != 0 || compact.TopWriter != nil {
		t.Fatalf("compact disk model retained detail-only fields: %+v", compact)
	}
	if summary.TopWriter == nil || summary.TotalPIDCount != 500 {
		t.Fatalf("compaction mutated the resident summary: %+v", summary)
	}
}

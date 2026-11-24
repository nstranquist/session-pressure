package sessionpressurecontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/pkg/sessionpressurecontrol"
	"github.com/nstranquist/session-pressure/sessionpressure"
)

type exceedingRunner struct{}

func (exceedingRunner) Run(_ context.Context, _ []string) RunResult {
	return RunResult{Stdout: []byte(`{"truncated":true`), StdoutExceeded: true}
}

func TestControlAPIHealthAndFactoryReadContracts(t *testing.T) {
	runner := &fakeRunner{stdout: []byte(`{"ok":false,"action":"check","admission":{"allowed":false,"level":"red","dimension":"memory"}}`), exit: 11}
	server := newTestServer(t, runner)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	// Health advertises check and browser capabilities for factory consumers.
	resp, err := http.Get(httpServer.URL + "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	env := decodeEnvelope(t, resp)
	var health sessionpressurecontrol.Health
	if err := json.Unmarshal(env.Data, &health); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	for _, capName := range []string{"check", "browser", "work", "board", "actions"} {
		if !slices.Contains(health.Capabilities, capName) {
			t.Fatalf("health capabilities %v missing %q", health.Capabilities, capName)
		}
	}

	// Non-zero structured admission check (exit 11) stays HTTP 200 with valid JSON payload.
	resp, err = http.Get(httpServer.URL + "/v1/pressure/check")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("check status = %d, want 200", resp.StatusCode)
	}
	env = decodeEnvelope(t, resp)
	if env.Error != nil || !strings.Contains(string(env.Data), `"allowed":false`) {
		t.Fatalf("check envelope = %#v, want structured admission refusal", env)
	}

	// Invalid operation_id rejected cleanly with 400.
	resp, err = http.Get(httpServer.URL + "/v1/pressure/work?view=history&operation_id=../escape")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid operation_id status = %d, want 400", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestControlAPITruncatedStdoutReportsOutputLimit(t *testing.T) {
	server := newTestServer(t, exceedingRunner{})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	resp, err := http.Get(httpServer.URL + "/v1/pressure/board")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	env := decodeEnvelope(t, resp)
	if env.Error == nil || env.Error.Code != "output_limit" {
		t.Fatalf("error = %#v, want output_limit", env.Error)
	}
}

func TestControlAPIFactoryTypedActionsPreviewApproveAndReplayReject(t *testing.T) {
	runner := &fakeRunner{stdout: []byte(`{"ok":true}`)}
	server := newTestServer(t, runner)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	cases := []struct {
		action string
		want   []string
	}{
		{"recovery.clear", []string{"--json", "session", "pressure", "recovery", "clear"}},
		{"browser.pause", []string{"--json", "session", "pressure", "browser", "pause"}},
		{"browser.auto", []string{"--json", "session", "pressure", "browser", "auto"}},
	}

	for _, tc := range cases {
		body, _ := json.Marshal(sessionpressurecontrol.ActionRequest{Action: tc.action})
		resp, err := http.Post(httpServer.URL+"/v1/actions/preview", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s preview status = %d", tc.action, resp.StatusCode)
		}
		env := decodeEnvelope(t, resp)
		var preview sessionpressurecontrol.ActionPreview
		if err := json.Unmarshal(env.Data, &preview); err != nil {
			t.Fatalf("decode preview: %v", err)
		}
		if !slices.Equal(preview.Command, tc.want) {
			t.Fatalf("%s command = %v, want %v", tc.action, preview.Command, tc.want)
		}

		approveBody, _ := json.Marshal(sessionpressurecontrol.ApprovalRequest{PreviewID: preview.PreviewID, Note: "factory contract test"})
		resp, err = http.Post(httpServer.URL+"/v1/actions/approve", "application/json", bytes.NewReader(approveBody))
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s approve status = %d", tc.action, resp.StatusCode)
		}
		_ = resp.Body.Close()

		// Replay of the same preview_id must be rejected with 409 Conflict.
		resp, err = http.Post(httpServer.URL+"/v1/actions/approve", "application/json", bytes.NewReader(approveBody))
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("%s replay status = %d, want 409", tc.action, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
}

func TestFactoryDeliveryLaneWorkContracts(t *testing.T) {
	// 1. WorkExitReport distinguishes session_pressure exit 2 from child exit 2
	// and TakeWorkExitReportPath strips the variable so wrapped commands never inherit it.
	reportPath := filepath.Join(t.TempDir(), "exit-report.json")
	if err := os.WriteFile(reportPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(sessionpressure.WorkExitReportEnv, reportPath)
	code := sessionpressure.ReportOwnWorkExit(2)
	if code != 2 {
		t.Fatalf("ReportOwnWorkExit(2) = %d", code)
	}
	got, ok, err := sessionpressure.ReadWorkExitReport(reportPath)
	if err != nil || !ok || got.Source != sessionpressure.WorkExitSourceSessionPressure || !got.AppliesTo(2) || got.AppliesTo(1) {
		t.Fatalf("ReadWorkExitReport = (%+v, ok=%t, err=%v)", got, ok, err)
	}
	if taken := sessionpressure.TakeWorkExitReportPath(); taken != reportPath {
		t.Fatalf("TakeWorkExitReportPath = %q, want %q", taken, reportPath)
	}
	if _, stillSet := os.LookupEnv(sessionpressure.WorkExitReportEnv); stillSet {
		t.Fatal("WorkExitReportEnv must be unset after TakeWorkExitReportPath")
	}

	// 2. Concurrent delivery-lane fair-share & interactive exemption contract:
	// solo runs always make progress; concurrent non-interactive lane builds
	// respect disk_write_high and fair_share while interactive agent work is spared.
	now := time.Date(2026, 11, 24, 19, 0, 0, 0, time.UTC)
	headroom := sessionpressure.ResourceHeadroom{
		Rule: sessionpressure.ResourceHeadroomRuleVersion,
		Memory: sessionpressure.MemoryHeadroom{
			SampleFresh: true, SampledAt: now, Level: sessionpressure.LevelWarning,
			PhysicalMB: 49152, AvailableMB: 24576, ReserveMB: 6144, HeadroomMB: 18432,
		},
		CPU: sessionpressure.CPUHeadroom{
			Available: true, LogicalCPUs: 14, HostCPUPercent: 30, FreeCores: 9.8, ReserveCores: 1.8, HeadroomCores: 8.0,
		},
		DiskWrite: sessionpressure.DiskWriteStateHigh,
	}

	concurrentLane := sessionpressure.DecideResourceAdmission(headroom, sessionpressure.ResourceRequest{
		PredictedPeakMB: 2000, PredictedCores: 4, RunningLeases: 2, SessionLeases: 2, SessionShare: 0.75, Interactive: false,
	})
	if concurrentLane.Step != sessionpressure.ResourceStepThrottle || !concurrentLane.BackgroundIO {
		t.Fatalf("concurrent non-interactive lane = %+v, want BackgroundIO throttle", concurrentLane)
	}

	interactive := sessionpressure.DecideResourceAdmission(headroom, sessionpressure.ResourceRequest{
		PredictedPeakMB: 2000, PredictedCores: 4, RunningLeases: 2, SessionLeases: 2, SessionShare: 0.75, Interactive: true,
	})
	if interactive.Step != sessionpressure.ResourceStepAdmit || interactive.BackgroundIO {
		t.Fatalf("interactive command = %+v, want admit without BackgroundIO", interactive)
	}

	soloLane := sessionpressure.DecideResourceAdmission(headroom, sessionpressure.ResourceRequest{
		PredictedPeakMB: 2000, PredictedCores: 4, RunningLeases: 0, Interactive: false,
	})
	if soloLane.Step != sessionpressure.ResourceStepAdmit || soloLane.BackgroundIO {
		t.Fatalf("solo lane command = %+v, want admit without BackgroundIO", soloLane)
	}
}

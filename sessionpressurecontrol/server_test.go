package sessionpressurecontrol

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/pkg/sessionpressurecontrol"
)

type fakeRunner struct {
	mu     sync.Mutex
	calls  [][]string
	stdout []byte
	exit   int
	err    error
	stderr []byte
}

func (r *fakeRunner) Run(_ context.Context, args []string) RunResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string(nil), args...))
	return RunResult{Stdout: append([]byte(nil), r.stdout...), Stderr: append([]byte(nil), r.stderr...), ExitCode: r.exit, Err: r.err}
}

func newTestServer(t *testing.T, runner Runner) *Server {
	t.Helper()
	if runner == nil {
		runner = &fakeRunner{stdout: []byte(`{"ok":true,"action":"board"}`)}
	}
	server, err := New(Config{StateDir: t.TempDir(), Runner: runner, AllowTestTransport: true, PreviewTTL: time.Hour, CommandTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func decodeEnvelope(t *testing.T, response *http.Response) sessionpressurecontrol.Envelope {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var envelope sessionpressurecontrol.Envelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode envelope: %v (%s)", err, body)
	}
	return envelope
}

func TestHealthAndProjectionEnvelope(t *testing.T) {
	server := newTestServer(t, &fakeRunner{stdout: []byte(`{"ok":true,"action":"board","schema_version":1}`)})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	for _, route := range []string{"/v1/health", "/v1/pressure/board"} {
		response, err := http.Get(httpServer.URL + route)
		if err != nil {
			t.Fatal(err)
		}
		envelope := decodeEnvelope(t, response)
		if envelope.APIVersion != sessionpressurecontrol.APIVersion || envelope.RequestID == "" || envelope.Error != nil {
			t.Fatalf("bad envelope for %s: %#v", route, envelope)
		}
		if len(envelope.Data) == 0 {
			t.Fatalf("missing data for %s", route)
		}
	}
}

func TestProjectionPreservesStructuredNonZeroStatus(t *testing.T) {
	runner := &fakeRunner{stdout: []byte(`{"ok":false,"action":"status"}`), exit: 1}
	server := newTestServer(t, runner)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	response, err := http.Get(httpServer.URL + "/v1/pressure/board")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want %d", response.StatusCode, http.StatusOK)
	}
	envelope := decodeEnvelope(t, response)
	if envelope.Error != nil || string(envelope.Data) != string(runner.stdout) {
		t.Fatalf("envelope=%#v, want structured status payload", envelope)
	}
}

func TestProjectionQueryArgumentsPreserveCanonicalShape(t *testing.T) {
	runner := &fakeRunner{stdout: []byte(`{"ok":true}`)}
	server := newTestServer(t, runner)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	tests := []struct {
		name  string
		route string
		want  []string
	}{
		{
			name:  "board flags and include",
			route: "/v1/pressure/board?full=true&include=doctor&live=true",
			want:  []string{"--json", "session", "pressure", "board", "--full", "--include", "doctor", "--live"},
		},
		{
			name:  "status flags",
			route: "/v1/pressure/status?full=true&live=true",
			want:  []string{"--json", "session", "pressure", "status", "--full", "--live"},
		},
		{
			name:  "work history filters",
			route: "/v1/pressure/work?view=history&full=true&limit=2&since=24h",
			want:  []string{"--json", "session", "pressure", "work", "history", "--full", "--limit", "2", "--since", "24h"},
		},
		{
			name:  "telemetry filters",
			route: "/v1/pressure/telemetry?limit=2&since=24h",
			want:  []string{"--json", "session", "pressure", "telemetry", "--limit", "2", "--since", "24h"},
		},
		{
			name:  "storage history",
			route: "/v1/pressure/storage?view=history&limit=3",
			want:  []string{"--json", "session", "pressure", "storage", "history", "--limit", "3"},
		},
		{
			name:  "cleanup claims",
			route: "/v1/pressure/cleanup?view=claim",
			want:  []string{"--json", "session", "pressure", "cleanup", "claim", "list"},
		},
		{
			name:  "cleanup policy",
			route: "/v1/pressure/cleanup?view=policy",
			want:  []string{"--json", "session", "pressure", "cleanup", "policy", "show"},
		},
		{
			name:  "io top filters",
			route: "/v1/pressure/io?view=top&limit=5&live=true",
			want:  []string{"--json", "session", "pressure", "io", "top", "--limit", "5", "--live"},
		},
		{
			name:  "io policy",
			route: "/v1/pressure/io?view=policy",
			want:  []string{"--json", "session", "pressure", "io", "policy", "show"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, err := http.Get(httpServer.URL + test.route)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusOK {
				envelope := decodeEnvelope(t, response)
				t.Fatalf("status=%d error=%#v", response.StatusCode, envelope.Error)
			}
			_ = decodeEnvelope(t, response)

			runner.mu.Lock()
			got := append([][]string(nil), runner.calls...)
			runner.mu.Unlock()
			if len(got) == 0 || !reflect.DeepEqual(got[len(got)-1], test.want) {
				t.Fatalf("last authority call=%#v, want %#v", got, test.want)
			}
		})
	}

	duplicateView, err := http.Get(httpServer.URL + "/v1/pressure/work?view=history&view=stats")
	if err != nil {
		t.Fatal(err)
	}
	if duplicateView.StatusCode != http.StatusBadRequest {
		t.Fatalf("duplicate view status=%d, want %d", duplicateView.StatusCode, http.StatusBadRequest)
	}
	_ = decodeEnvelope(t, duplicateView)

	for _, route := range []string{
		"/v1/pressure/storage?view=policy",
		"/v1/pressure/storage?view=status&limit=2",
		"/v1/pressure/work?view=status&limit=2",
		"/v1/pressure/cleanup?view=policy&limit=2",
		"/v1/pressure/io?view=policy&live=true",
	} {
		invalid, err := http.Get(httpServer.URL + route)
		if err != nil {
			t.Fatal(err)
		}
		if invalid.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid route %s status=%d, want %d", route, invalid.StatusCode, http.StatusBadRequest)
		}
		_ = decodeEnvelope(t, invalid)
	}
}

func TestCommandEnvironmentPinsCanonicalStateDirectory(t *testing.T) {
	original := []string{"PATH=/bin", "NDEV_SESSION_PRESSURE_HOME=/old"}
	updated := withStateDirectory(original, "/tmp/session-pressure-test")
	if !reflect.DeepEqual(original, []string{"PATH=/bin", "NDEV_SESSION_PRESSURE_HOME=/old"}) {
		t.Fatalf("withStateDirectory mutated input: %#v", original)
	}
	if !reflect.DeepEqual(updated, []string{"PATH=/bin", "NDEV_SESSION_PRESSURE_HOME=/tmp/session-pressure-test"}) {
		t.Fatalf("updated environment=%#v", updated)
	}
	appended := withStateDirectory([]string{"PATH=/bin"}, "/tmp/session-pressure-test")
	if !reflect.DeepEqual(appended, []string{"PATH=/bin", "NDEV_SESSION_PRESSURE_HOME=/tmp/session-pressure-test"}) {
		t.Fatalf("appended environment=%#v", appended)
	}
}

func TestEventsSendInitialSnapshotAndSequence(t *testing.T) {
	server := newTestServer(t, &fakeRunner{stdout: []byte(`{"ok":true,"action":"board"}`)})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/v1/events?interval_ms=1000", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("events status=%d", response.StatusCode)
	}
	reader := bufio.NewReader(response.Body)
	eventLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	dataLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if eventLine != "event: board\n" || !strings.HasPrefix(dataLine, "data: ") {
		t.Fatalf("unexpected SSE frame: %q %q", eventLine, dataLine)
	}
	var event sessionpressurecontrol.Event
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(dataLine, "data: "))), &event); err != nil {
		t.Fatal(err)
	}
	if event.Sequence != 1 || event.Type != "board" {
		t.Fatalf("unexpected event: %#v", event)
	}
	cancel()
}

func TestActionPreviewApprovalIsSingleUse(t *testing.T) {
	runner := &fakeRunner{stdout: []byte(`{"ok":true,"action":"policy.observe"}`)}
	server := newTestServer(t, runner)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	previewResponse := postJSON(t, httpServer.URL+"/v1/actions/preview", map[string]any{"action": "policy.observe"})
	if previewResponse.StatusCode != http.StatusOK {
		t.Fatalf("preview status=%d", previewResponse.StatusCode)
	}
	previewEnvelope := decodeEnvelope(t, previewResponse)
	var preview sessionpressurecontrol.ActionPreview
	if err := json.Unmarshal(previewEnvelope.Data, &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Status != "pending" || !preview.RequiresApproval {
		t.Fatalf("unexpected preview: %#v", preview)
	}
	runner.mu.Lock()
	if len(runner.calls) != 0 {
		t.Fatalf("preview executed authority: %#v", runner.calls)
	}
	runner.mu.Unlock()
	auditResponse, err := http.Get(httpServer.URL + "/v1/actions/audit?limit=1")
	if err != nil {
		t.Fatal(err)
	}
	auditEnvelope := decodeEnvelope(t, auditResponse)
	if !strings.Contains(string(auditEnvelope.Data), `"outcome":"previewed"`) {
		t.Fatalf("preview was not audited: %s", auditEnvelope.Data)
	}

	approveResponse := postJSON(t, httpServer.URL+"/v1/actions/approve", map[string]any{"preview_id": preview.PreviewID, "note": "safe observe-only change"})
	if approveResponse.StatusCode != http.StatusOK {
		t.Fatalf("approve status=%d", approveResponse.StatusCode)
	}
	_ = decodeEnvelope(t, approveResponse)
	auditResponse, err = http.Get(httpServer.URL + "/v1/actions/audit?limit=1")
	if err != nil {
		t.Fatal(err)
	}
	auditEnvelope = decodeEnvelope(t, auditResponse)
	if !strings.Contains(string(auditEnvelope.Data), `"note":"safe observe-only change"`) {
		t.Fatalf("approval note was not audited: %s", auditEnvelope.Data)
	}
	replayResponse := postJSON(t, httpServer.URL+"/v1/actions/approve", map[string]any{"preview_id": preview.PreviewID})
	if replayResponse.StatusCode != http.StatusConflict {
		t.Fatalf("replay status=%d, want conflict", replayResponse.StatusCode)
	}
	_ = decodeEnvelope(t, replayResponse)
	runner.mu.Lock()
	defer runner.mu.Unlock()
	want := []string{"--json", "session", "pressure", "policy", "observe"}
	if !reflect.DeepEqual(runner.calls, [][]string{want}) {
		t.Fatalf("authority calls=%#v, want %#v", runner.calls, [][]string{want})
	}
}

func TestPreviewStoreIsBoundedAndPrunesTerminalEntries(t *testing.T) {
	store, err := newPreviewStore(filepath.Join(t.TempDir(), "api"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour)
	for index := 0; index < maxPreviewFiles; index++ {
		if err := store.put(sessionpressurecontrol.ActionPreview{PreviewID: fmt.Sprintf("preview-%03d", index), Action: "policy.observe", ExpiresAt: expires, Status: "pending"}); err != nil {
			t.Fatalf("put pending preview %d: %v", index, err)
		}
	}
	if err := store.put(sessionpressurecontrol.ActionPreview{PreviewID: "preview-over-capacity", Action: "policy.observe", ExpiresAt: expires, Status: "pending"}); err == nil {
		t.Fatal("accepted a preview above the bounded store capacity")
	}
	if err := store.put(sessionpressurecontrol.ActionPreview{PreviewID: "preview-000", Action: "policy.observe", ExpiresAt: expires, Status: "approved"}); err != nil {
		t.Fatal(err)
	}
	if err := store.put(sessionpressurecontrol.ActionPreview{PreviewID: "preview-after-prune", Action: "policy.observe", ExpiresAt: expires, Status: "pending"}); err != nil {
		t.Fatalf("terminal preview was not pruned: %v", err)
	}
}

func TestActionApprovalRejectsStateChange(t *testing.T) {
	runner := &fakeRunner{stdout: []byte(`{"ok":true}`)}
	server := newTestServer(t, runner)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	previewResponse := postJSON(t, httpServer.URL+"/v1/actions/preview", map[string]any{"action": "policy.observe"})
	previewEnvelope := decodeEnvelope(t, previewResponse)
	var preview sessionpressurecontrol.ActionPreview
	if err := json.Unmarshal(previewEnvelope.Data, &preview); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(server.cfg.StateDir, "policy.json"), []byte(`{"changed":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	response := postJSON(t, httpServer.URL+"/v1/actions/approve", map[string]any{"preview_id": preview.PreviewID})
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d, want conflict", response.StatusCode)
	}
	_ = decodeEnvelope(t, response)
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 0 {
		t.Fatalf("stale approval executed authority: %#v", runner.calls)
	}
}

func TestTraceRequestIsBoundedAndPathFree(t *testing.T) {
	server := newTestServer(t, &fakeRunner{stdout: []byte(`{"ok":true}`)})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	bad := postJSON(t, httpServer.URL+"/v1/pressure/io/trace-requests", map[string]any{"pid": os.Getpid(), "duration_seconds": 4})
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad trace status=%d", bad.StatusCode)
	}
	_ = decodeEnvelope(t, bad)
	good := postJSON(t, httpServer.URL+"/v1/pressure/io/trace-requests", map[string]any{"pid": os.Getpid(), "duration_seconds": 5})
	if good.StatusCode != http.StatusOK {
		t.Fatalf("good trace status=%d", good.StatusCode)
	}
	envelope := decodeEnvelope(t, good)
	var preview sessionpressurecontrol.ActionPreview
	if err := json.Unmarshal(envelope.Data, &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Action != "io.trace.request" || preview.ProcessStartIdentity == "" || strings.Contains(string(envelope.Data), `"path"`) {
		t.Fatalf("trace preview leaked unexpected data: %s", envelope.Data)
	}
	status, err := http.Get(httpServer.URL + "/v1/pressure/io/trace-requests")
	if err != nil {
		t.Fatal(err)
	}
	statusEnvelope := decodeEnvelope(t, status)
	if strings.Contains(string(statusEnvelope.Data), `"path"`) {
		t.Fatalf("trace status leaked path data: %s", statusEnvelope.Data)
	}
}

func TestTraceApprovalRejectsChangedProcessIdentity(t *testing.T) {
	runner := &fakeRunner{stdout: []byte(`{"ok":true}`)}
	server := newTestServer(t, runner)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	response := postJSON(t, httpServer.URL+"/v1/pressure/io/trace-requests", map[string]any{"pid": os.Getpid(), "duration_seconds": 5})
	envelope := decodeEnvelope(t, response)
	var preview sessionpressurecontrol.ActionPreview
	if err := json.Unmarshal(envelope.Data, &preview); err != nil {
		t.Fatal(err)
	}
	preview.ProcessStartIdentity = "changed-before-approval"
	body, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(server.previews.previewPath(preview.PreviewID), append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	approval := postJSON(t, httpServer.URL+"/v1/actions/approve", map[string]any{"preview_id": preview.PreviewID})
	if approval.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d, want conflict", approval.StatusCode)
	}
	_ = decodeEnvelope(t, approval)
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 0 {
		t.Fatalf("trace identity mismatch executed authority: %#v", runner.calls)
	}
}

func TestLoopbackValidationAndNonSocketProtection(t *testing.T) {
	if err := validateLoopbackAddr("0.0.0.0:1234"); err == nil {
		t.Fatal("accepted public bind")
	}
	if err := validateLoopbackAddr("127.0.0.1:1234"); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	socket := filepath.Join(root, "api.sock")
	if err := os.WriteFile(socket, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{StateDir: root, SocketPath: socket, Runner: &fakeRunner{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.listenUnix(); err == nil {
		t.Fatal("replaced regular file at socket path")
	}
}

func TestLiveSocketIsNotReplaced(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(root, "api.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server, err := New(Config{StateDir: root, SocketPath: socket, Runner: &fakeRunner{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.listenUnix(); err == nil {
		t.Fatal("replaced a live API socket")
	}
}

func TestRequestShapeAndTokenPathAreStrict(t *testing.T) {
	server := newTestServer(t, &fakeRunner{stdout: []byte(`{"ok":true}`)})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	extra := postJSON(t, httpServer.URL+"/v1/actions/preview", map[string]any{"action": "policy.observe", "extra": true})
	if extra.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d", extra.StatusCode)
	}
	_ = decodeEnvelope(t, extra)
	longNote := postJSON(t, httpServer.URL+"/v1/actions/approve", map[string]any{"preview_id": "missing", "note": strings.Repeat("x", 201)})
	if longNote.StatusCode != http.StatusBadRequest {
		t.Fatalf("long note status=%d", longNote.StatusCode)
	}
	_ = decodeEnvelope(t, longNote)

	tokenTarget := filepath.Join(server.cfg.StateDir, "token-target")
	if err := os.WriteFile(tokenTarget, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server.cfg.TokenPath = filepath.Join(server.cfg.StateDir, "token-link")
	if err := os.Symlink(tokenTarget, server.cfg.TokenPath); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ensureToken(); err == nil {
		t.Fatal("accepted symlink API token path")
	}
}

func postJSON(t *testing.T, endpoint string, value any) *http.Response {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

package sessionpressurecontrol

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nstranquist/session-pressure/internal/sessionpressure"
	"github.com/nstranquist/session-pressure/pkg/sessionpressurecontrol"
)

const (
	defaultCommandTimeout = 15 * time.Second
	defaultPreviewTTL     = 2 * time.Minute
	maxJSONBytes          = 2 << 20
	maxAuditBytes         = 64 << 10
	maxAuditRows          = 200
	maxPreviewFiles       = 256
	maxPreviewBytes       = 64 << 10
	maxEventSubscribers   = 4
)

type transportContextKey struct{}

type Runner interface {
	Run(context.Context, []string) RunResult
}

type RunResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Err      error
}

type Config struct {
	StateDir           string
	SocketPath         string
	HTTPAddr           string
	TokenPath          string
	NDevBin            string
	CommandTimeout     time.Duration
	PreviewTTL         time.Duration
	EventInterval      time.Duration
	Now                func() time.Time
	Runner             Runner
	AllowTestTransport bool
}

type Server struct {
	cfg       Config
	startedAt time.Time
	previews  *previewStore
	actions   *actionRegistry
	events    chan struct{}
	mu        sync.Mutex
}

func New(cfg Config) (*Server, error) {
	if cfg.StateDir == "" {
		return nil, errors.New("session pressure control: state directory is required")
	}
	stateDir, err := filepath.Abs(cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("resolve session pressure state directory: %w", err)
	}
	cfg.StateDir = stateDir
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.CommandTimeout <= 0 {
		cfg.CommandTimeout = defaultCommandTimeout
	}
	if cfg.PreviewTTL <= 0 {
		cfg.PreviewTTL = defaultPreviewTTL
	}
	if cfg.EventInterval <= 0 {
		cfg.EventInterval = 5 * time.Second
	}
	if cfg.Runner == nil {
		cfg.Runner = commandRunner{bin: cfg.NDevBin, stateDir: cfg.StateDir}
	}
	if cfg.SocketPath == "" {
		cfg.SocketPath = filepath.Join(cfg.StateDir, sessionpressurecontrol.DefaultSocketRelativePath)
	}
	if cfg.TokenPath == "" {
		cfg.TokenPath = filepath.Join(cfg.StateDir, sessionpressurecontrol.DefaultTokenRelativePath)
	}
	store, err := newPreviewStore(filepath.Join(cfg.StateDir, "api"), cfg.Now)
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:       cfg,
		startedAt: cfg.Now().UTC(),
		previews:  store,
		actions:   newActionRegistry(),
		events:    make(chan struct{}, maxEventSubscribers),
	}
	return s, nil
}

func (s *Server) Config() Config { return s.cfg }

// Handler returns the HTTP handler used by both Unix-socket and loopback
// transports. Production listeners attach their transport identity to the
// request context; tests may use AllowTestTransport with httptest.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", s.handleHealth)
	mux.HandleFunc("/v1/pressure/board", s.handleBoard)
	mux.HandleFunc("/v1/pressure/status", s.handleStatus)
	mux.HandleFunc("/v1/pressure/doctor", s.handleDoctor)
	mux.HandleFunc("/v1/pressure/work", s.handleWork)
	mux.HandleFunc("/v1/pressure/telemetry", s.handleTelemetry)
	mux.HandleFunc("/v1/pressure/policy", s.handlePolicy)
	mux.HandleFunc("/v1/pressure/storage", s.handleStorage)
	mux.HandleFunc("/v1/pressure/cleanup", s.handleCleanup)
	mux.HandleFunc("/v1/pressure/io", s.handleIO)
	mux.HandleFunc("/v1/events", s.handleEvents)
	mux.HandleFunc("/v1/actions/preview", s.handleActionPreview)
	mux.HandleFunc("/v1/actions/approve", s.handleActionApprove)
	mux.HandleFunc("/v1/actions/audit", s.handleActionAudit)
	mux.HandleFunc("/v1/pressure/io/trace-requests", s.handleTraceRequest)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "loopback HTTP requires a bearer token", false)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	transport, _ := r.Context().Value(transportContextKey{}).(string)
	if transport == "unix" || (s.cfg.AllowTestTransport && (transport == "" || transport == "test")) {
		return true
	}
	if transport != "http" {
		return false
	}
	token, err := os.ReadFile(s.cfg.TokenPath)
	if err != nil {
		return false
	}
	token = bytes.TrimSpace(token)
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if got == "" || len(token) != len(got) {
		return false
	}
	return subtle.ConstantTimeCompare(token, []byte(got)) == 1
}

func (s *Server) Serve(ctx context.Context) error {
	if s.cfg.HTTPAddr == "" {
		listener, err := s.listenUnix()
		if err != nil {
			return err
		}
		return s.serveListener(ctx, listener, "unix")
	}
	if err := validateLoopbackAddr(s.cfg.HTTPAddr); err != nil {
		return err
	}
	if _, err := s.ensureToken(); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", s.cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen loopback HTTP: %w", err)
	}
	return s.serveListener(ctx, listener, "http")
}

func (s *Server) listenUnix() (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(s.cfg.SocketPath), 0o700); err != nil {
		return nil, fmt.Errorf("create API socket directory: %w", err)
	}
	if info, err := os.Lstat(s.cfg.SocketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("refusing to replace non-socket at %s", s.cfg.SocketPath)
		}
		probe, dialErr := net.DialTimeout("unix", s.cfg.SocketPath, 100*time.Millisecond)
		if dialErr == nil {
			_ = probe.Close()
			return nil, fmt.Errorf("API socket is already in use at %s", s.cfg.SocketPath)
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) && !errors.Is(dialErr, syscall.ENOENT) {
			return nil, fmt.Errorf("probe existing API socket: %w", dialErr)
		}
		if err := os.Remove(s.cfg.SocketPath); err != nil {
			return nil, fmt.Errorf("remove stale API socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect API socket: %w", err)
	}
	listener, err := net.Listen("unix", s.cfg.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("listen Unix socket: %w", err)
	}
	if err := os.Chmod(s.cfg.SocketPath, 0o600); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("protect API socket: %w", err)
	}
	return listener, nil
}

func (s *Server) serveListener(ctx context.Context, listener net.Listener, transport string) error {
	defer func() {
		_ = listener.Close()
		if transport == "unix" {
			if info, err := os.Lstat(s.cfg.SocketPath); err == nil && info.Mode()&os.ModeSocket != 0 {
				_ = os.Remove(s.cfg.SocketPath)
			}
		}
	}()
	server := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       s.cfg.CommandTimeout + 3*time.Second,
		// SSE is intentionally long-lived. Each projection command still has
		// its own command timeout, while the request context closes the stream
		// when the client disconnects or the server shuts down.
		WriteTimeout: 0,
		IdleTimeout:  30 * time.Second,
		ConnContext: func(ctx context.Context, _ net.Conn) context.Context {
			return context.WithValue(ctx, transportContextKey{}, transport)
		},
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	err := server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) ensureToken() (string, error) {
	if err := os.MkdirAll(filepath.Dir(s.cfg.TokenPath), 0o700); err != nil {
		return "", fmt.Errorf("create API token directory: %w", err)
	}
	if info, err := os.Lstat(s.cfg.TokenPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", fmt.Errorf("refusing non-regular API token path %s", s.cfg.TokenPath)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect API token: %w", err)
	}
	if body, err := os.ReadFile(s.cfg.TokenPath); err == nil {
		if token := strings.TrimSpace(string(body)); token != "" {
			if err := os.Chmod(s.cfg.TokenPath, 0o600); err != nil {
				return "", fmt.Errorf("protect API token: %w", err)
			}
			return token, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read API token: %w", err)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate API token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	file, err := os.OpenFile(s.cfg.TokenPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			body, readErr := os.ReadFile(s.cfg.TokenPath)
			if readErr != nil || strings.TrimSpace(string(body)) == "" {
				return "", fmt.Errorf("read concurrently-created API token: %w", readErr)
			}
			return strings.TrimSpace(string(body)), nil
		}
		return "", fmt.Errorf("create API token: %w", err)
	}
	if _, err := file.WriteString(token + "\n"); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("write API token: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close API token: %w", err)
	}
	return token, nil
}

func validateLoopbackAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid HTTP address %q: %w", addr, err)
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return nil
	}
	return fmt.Errorf("refusing non-loopback HTTP address %q", addr)
}

func (s *Server) writeProjection(w http.ResponseWriter, r *http.Request, args []string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "projection routes require GET", false)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.CommandTimeout)
	defer cancel()
	result := s.cfg.Runner.Run(ctx, args)
	if len(result.Stdout) == 0 {
		message := strings.TrimSpace(string(result.Stderr))
		if message == "" && result.Err != nil {
			message = result.Err.Error()
		}
		if message == "" {
			message = "canonical ndev projection returned no JSON"
		}
		writeError(w, http.StatusBadGateway, "authority_unavailable", message, true)
		return
	}
	if !json.Valid(result.Stdout) {
		writeError(w, http.StatusBadGateway, "invalid_authority_json", "canonical ndev projection was not valid JSON", true)
		return
	}
	writeData(w, http.StatusOK, "cli", result.Stdout)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "health requires GET", false)
		return
	}
	transport, _ := r.Context().Value(transportContextKey{}).(string)
	health := sessionpressurecontrol.Health{
		Status: "ready", API: sessionpressurecontrol.APIVersion, Authority: "ndev.session.pressure",
		Transport: transport, PID: os.Getpid(), StartedAt: s.startedAt.Format(time.RFC3339Nano),
		Capabilities: []string{"board", "status", "doctor", "work", "telemetry", "policy", "storage", "cleanup", "io", "events", "actions", "trace-request"},
	}
	body, _ := json.Marshal(health)
	writeData(w, http.StatusOK, "api", body)
}

func (s *Server) handleBoard(w http.ResponseWriter, r *http.Request) {
	args, err := projectionArgs(r, "board", allowedQuery{"full": queryBool, "live": queryBool, "include": queryToken})
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", err.Error(), false)
		return
	}
	s.writeProjection(w, r, args)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	args, err := projectionArgs(r, "status", allowedQuery{"full": queryBool, "live": queryBool})
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", err.Error(), false)
		return
	}
	s.writeProjection(w, r, args)
}

func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	s.writeProjection(w, r, pressureArgs("doctor"))
}

func (s *Server) handleWork(w http.ResponseWriter, r *http.Request) {
	view, err := projectionView(r, "status", map[string]bool{"status": true, "history": true, "stats": true, "report": true, "evaluate": true}, "work view must be status, history, stats, report, or evaluate")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_view", err.Error(), false)
		return
	}
	allowed := allowedQuery{}
	switch view {
	case "history":
		allowed = allowedQuery{"full": queryBool, "limit": queryLimit, "since": queryDuration}
	case "stats", "report":
		allowed = allowedQuery{"full": queryBool, "since": queryDuration}
	}
	args, err := projectionArgs(r, "work", allowed, view)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", err.Error(), false)
		return
	}
	s.writeProjection(w, r, args)
}

func (s *Server) handleTelemetry(w http.ResponseWriter, r *http.Request) {
	args, err := projectionArgs(r, "telemetry", allowedQuery{"limit": queryLimit, "since": queryDuration})
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", err.Error(), false)
		return
	}
	s.writeProjection(w, r, args)
}

func (s *Server) handlePolicy(w http.ResponseWriter, r *http.Request) {
	s.writeProjection(w, r, pressureArgs("policy", "show"))
}

func (s *Server) handleStorage(w http.ResponseWriter, r *http.Request) {
	view, err := projectionView(r, "status", map[string]bool{"status": true, "providers": true, "plan": true, "history": true}, "unsupported storage view")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_view", err.Error(), false)
		return
	}
	allowed := allowedQuery{}
	if view == "history" {
		allowed = allowedQuery{"limit": queryLimit, "since": queryDuration}
	}
	args, err := projectionArgs(r, "storage", allowed, view)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", err.Error(), false)
		return
	}
	s.writeProjection(w, r, args)
}

func (s *Server) handleCleanup(w http.ResponseWriter, r *http.Request) {
	view, err := projectionView(r, "status", map[string]bool{"status": true, "plan": true, "history": true, "policy": true, "claim": true}, "unsupported cleanup view")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_view", err.Error(), false)
		return
	}
	parts := []string{view}
	if view == "policy" {
		parts = append(parts, "show")
	}
	if view == "claim" {
		parts = append(parts, "list")
	}
	allowed := allowedQuery{}
	if view == "history" {
		allowed = allowedQuery{"limit": queryLimit}
	}
	args, err := projectionArgs(r, "cleanup", allowed, parts...)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", err.Error(), false)
		return
	}
	s.writeProjection(w, r, args)
}

func (s *Server) handleIO(w http.ResponseWriter, r *http.Request) {
	view, err := projectionView(r, "status", map[string]bool{"status": true, "top": true, "history": true, "policy": true}, "unsupported io view")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_view", err.Error(), false)
		return
	}
	parts := []string{view}
	if view == "policy" {
		parts = append(parts, "show")
	}
	allowed := allowedQuery{}
	switch view {
	case "status":
		allowed = allowedQuery{"live": queryBool, "full": queryBool}
	case "top":
		allowed = allowedQuery{"live": queryBool, "limit": queryLimit}
	case "history":
		allowed = allowedQuery{"limit": queryLimit, "since": queryDuration}
	}
	args, err := projectionArgs(r, "io", allowed, parts...)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", err.Error(), false)
		return
	}
	s.writeProjection(w, r, args)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "events requires GET", false)
		return
	}
	if !s.tryEventSubscriber() {
		writeError(w, http.StatusTooManyRequests, "event_capacity", "event subscriber capacity is full", true)
		return
	}
	defer s.releaseEventSubscriber()
	interval := s.cfg.EventInterval
	if raw := r.URL.Query().Get("interval_ms"); raw != "" {
		milliseconds, err := strconv.Atoi(raw)
		if err != nil || milliseconds < 1000 || milliseconds > 30000 {
			writeError(w, http.StatusBadRequest, "invalid_interval", "interval_ms must be between 1000 and 30000", false)
			return
		}
		interval = time.Duration(milliseconds) * time.Millisecond
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "stream_unsupported", "response writer does not support SSE", false)
		return
	}
	var lastHash string
	sequence := uint64(0)
	write := func(data []byte) error {
		sequence++
		event := sessionpressurecontrol.Event{Sequence: sequence, Type: "board", At: s.cfg.Now().UTC(), Data: json.RawMessage(data)}
		body, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "event: board\ndata: %s\n\n", body); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	for {
		ctx, cancel := context.WithTimeout(r.Context(), s.cfg.CommandTimeout)
		result := s.cfg.Runner.Run(ctx, pressureArgs("board"))
		cancel()
		if len(result.Stdout) > 0 && json.Valid(result.Stdout) {
			hash := sha256.Sum256(result.Stdout)
			digest := hex.EncodeToString(hash[:])
			if digest != lastHash {
				if err := write(result.Stdout); err != nil {
					return
				}
				lastHash = digest
			}
		}
		timer := time.NewTimer(interval)
		select {
		case <-r.Context().Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func (s *Server) tryEventSubscriber() bool {
	select {
	case s.events <- struct{}{}:
		return true
	default:
		return false
	}
}
func (s *Server) releaseEventSubscriber() {
	select {
	case <-s.events:
	default:
	}
}

func pressureArgs(parts ...string) []string {
	args := []string{"--json", "session", "pressure"}
	return append(args, parts...)
}

type queryValidator func(string) (string, error)
type allowedQuery map[string]queryValidator

func projectionView(r *http.Request, defaultView string, valid map[string]bool, invalidMessage string) (string, error) {
	values, present := r.URL.Query()["view"]
	if !present {
		return defaultView, nil
	}
	if len(values) != 1 {
		return "", errors.New("view query parameter must occur once")
	}
	view := values[0]
	if view == "" {
		view = defaultView
	}
	if !valid[view] {
		return "", errors.New(invalidMessage)
	}
	return view, nil
}

func projectionArgs(r *http.Request, sub string, allowed allowedQuery, commandParts ...string) ([]string, error) {
	args := pressureArgs(sub)
	for _, part := range commandParts {
		if strings.TrimSpace(part) == "" {
			return nil, errors.New("projection command part must not be empty")
		}
		args = append(args, part)
	}
	query := r.URL.Query()
	keys := make([]string, 0, len(query))
	for key := range query {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		values := query[key]
		if key == "view" {
			if len(commandParts) == 0 {
				return nil, fmt.Errorf("unsupported query parameter %q", key)
			}
			if len(values) != 1 {
				return nil, fmt.Errorf("query parameter %q must occur once", key)
			}
			continue
		}
		validator, ok := allowed[key]
		if !ok {
			return nil, fmt.Errorf("unsupported query parameter %q", key)
		}
		if len(values) != 1 {
			return nil, fmt.Errorf("query parameter %q must occur once", key)
		}
		value, err := validator(values[0])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		if value == "" {
			continue
		}
		args = append(args, "--"+strings.ReplaceAll(key, "_", "-"))
		if key != "full" && key != "live" {
			args = append(args, value)
		}
	}
	return args, nil
}

func queryBool(value string) (string, error) {
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return "", errors.New("must be true or false")
	}
	if parsed {
		return "true", nil
	}
	return "", nil
}

func queryLimit(value string) (string, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 || parsed > 200 {
		return "", errors.New("must be between 1 and 200")
	}
	return strconv.Itoa(parsed), nil
}

func queryDuration(value string) (string, error) {
	if len(value) > 32 || strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("must be a bounded duration")
	}
	if _, err := time.ParseDuration(value); err != nil {
		return "", errors.New("must be a duration such as 24h")
	}
	return value, nil
}

func queryToken(value string) (string, error) {
	if len(value) == 0 || len(value) > 64 || strings.ContainsAny(value, "\r\n/\\") {
		return "", errors.New("must be a bounded token")
	}
	return value, nil
}

func writeData(w http.ResponseWriter, status int, source string, data []byte) {
	if len(data) > maxJSONBytes {
		writeError(w, http.StatusBadGateway, "output_limit", "projection exceeded the API output budget", false)
		return
	}
	if len(data) == 0 {
		data = []byte("null")
	}
	envelope := sessionpressurecontrol.Envelope{APIVersion: sessionpressurecontrol.APIVersion, RequestID: requestID(), GeneratedAt: time.Now().UTC(), Source: source, Data: json.RawMessage(data)}
	writeJSON(w, status, envelope)
}

func writeValue(w http.ResponseWriter, status int, source string, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encode_response", err.Error(), false)
		return
	}
	writeData(w, status, source, body)
}

func writeError(w http.ResponseWriter, status int, code, message string, retryable bool) {
	envelope := sessionpressurecontrol.Envelope{APIVersion: sessionpressurecontrol.APIVersion, RequestID: requestID(), GeneratedAt: time.Now().UTC(), Source: "api", Error: &sessionpressurecontrol.Error{Code: code, Message: truncate(message, 512), Retryable: retryable}}
	writeJSON(w, status, envelope)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func requestID() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(raw[:])
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}

type commandRunner struct {
	bin      string
	stateDir string
}

func (r commandRunner) Run(ctx context.Context, args []string) RunResult {
	bin := r.bin
	if bin == "" {
		bin, _ = exec.LookPath("ndev")
		if bin == "" {
			bin, _ = exec.LookPath("ndev-go")
		}
	}
	if bin == "" {
		return RunResult{ExitCode: 127, Err: errors.New("ndev authority not found; set NDEV_BIN")}
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = commandEnvironment(r.stateDir)
	stdout := newLimitedBuffer(maxJSONBytes)
	stderr := newLimitedBuffer(64 << 10)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	result := RunResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: 0, Err: err}
	if err != nil {
		result.ExitCode = 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				result.ExitCode = status.ExitStatus()
			}
		}
	}
	return result
}

func commandEnvironment(stateDir string) []string {
	return withStateDirectory(os.Environ(), stateDir)
}

func withStateDirectory(environment []string, stateDir string) []string {
	if stateDir == "" {
		return append([]string(nil), environment...)
	}
	key := sessionpressure.DataDirEnv + "="
	result := append([]string(nil), environment...)
	for index, value := range result {
		if strings.HasPrefix(value, key) {
			result[index] = key + stateDir
			return result
		}
	}
	return append(result, key+stateDir)
}

type limitedBuffer struct {
	buf      bytes.Buffer
	limit    int
	exceeded bool
}

func newLimitedBuffer(limit int) *limitedBuffer { return &limitedBuffer{limit: limit} }
func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len() < b.limit {
		remaining := b.limit - b.buf.Len()
		if len(p) > remaining {
			_, _ = b.buf.Write(p[:remaining])
			b.exceeded = true
			return len(p), nil
		}
		_, _ = b.buf.Write(p)
	} else {
		b.exceeded = true
	}
	return len(p), nil
}
func (b *limitedBuffer) Bytes() []byte { return b.buf.Bytes() }

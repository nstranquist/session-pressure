package sessionpressurecontrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/nstranquist/session-pressure/internal/sessionpressure"
	"github.com/nstranquist/session-pressure/pkg/sessionpressurecontrol"
)

var (
	safeID       = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,160}$`)
	safeToken    = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,80}$`)
	safeResource = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,160}$`)
	safeSize     = regexp.MustCompile(`^[0-9]+(?:B|KiB|KB|MiB|MB|GiB|GB)?$`)
	safeDuration = regexp.MustCompile(`^[0-9]+(?:ms|s|m|h)$`)
)

type actionSpec struct {
	allowed map[string]bool
	build   func(map[string]string) ([]string, error)
}

type actionRegistry struct{ specs map[string]actionSpec }

func newActionRegistry() *actionRegistry {
	registry := &actionRegistry{specs: map[string]actionSpec{}}
	registry.add("policy.enable", []string{"no_auto_shed"}, func(params map[string]string) ([]string, error) {
		args := pressureArgs("policy", "enable")
		if enabled, err := optionalBool(params, "no_auto_shed"); err != nil {
			return nil, err
		} else if enabled {
			args = append(args, "--no-auto-shed")
		}
		return args, nil
	})
	registry.addFixed("policy.observe", pressureArgs("policy", "observe"))
	registry.add("policy.profile.apply", []string{"profile"}, func(params map[string]string) ([]string, error) {
		profile := params["profile"]
		switch profile {
		case "balanced", "throughput", "interactive", "observe":
		default:
			return nil, errors.New("profile must be balanced, throughput, interactive, or observe")
		}
		return pressureArgs("policy", "profile", "apply", profile), nil
	})
	registry.add("monitor.install", []string{"enforce"}, func(params map[string]string) ([]string, error) {
		args := pressureArgs("monitor", "install")
		if enabled, err := optionalBool(params, "enforce"); err != nil {
			return nil, err
		} else if enabled {
			args = append(args, "--enforce")
		}
		return args, nil
	})
	registry.addFixed("monitor.uninstall", pressureArgs("monitor", "uninstall"))
	registry.addFixed("monitor.sample", pressureArgs("monitor", "once"))
	registry.add("work.override", []string{"operation_id", "all", "clear"}, func(params map[string]string) ([]string, error) {
		clear, err := optionalBool(params, "clear")
		if err != nil {
			return nil, err
		}
		all, err := optionalBool(params, "all")
		if err != nil {
			return nil, err
		}
		if clear && (all || params["operation_id"] != "") {
			return nil, errors.New("clear cannot be combined with all or operation_id")
		}
		if !clear && !all && params["operation_id"] == "" {
			return nil, errors.New("operation_id, all, or clear is required")
		}
		if clear {
			return append(pressureArgs("work", "override"), "--clear", "--confirm"), nil
		}
		args := pressureArgs("work", "override")
		if all {
			args = append(args, "--all")
		}
		if ids := splitIDs(params["operation_id"]); len(ids) > 0 {
			for _, id := range ids {
				if !safeID.MatchString(id) {
					return nil, fmt.Errorf("invalid operation_id %q", id)
				}
				args = append(args, "--operation-id", id)
			}
		}
		return append(args, "--confirm"), nil
	})

	for _, verb := range []string{"init", "schedule", "enable", "observe", "disable"} {
		registry.addFixed("cleanup.policy."+verb, pressureArgs("cleanup", "policy", verb))
	}
	registry.add("cleanup.claim.acquire", []string{"kind", "resource", "owner", "ttl", "note", "pid", "cleanup_on_stale"}, func(params map[string]string) ([]string, error) {
		for _, key := range []string{"kind", "resource", "owner", "ttl"} {
			if strings.TrimSpace(params[key]) == "" {
				return nil, fmt.Errorf("%s is required", key)
			}
		}
		if !safeToken.MatchString(params["kind"]) || !safeResource.MatchString(params["resource"]) || !safeID.MatchString(params["owner"]) {
			return nil, errors.New("kind, resource, and owner must be bounded identifiers")
		}
		if !safeDuration.MatchString(params["ttl"]) {
			return nil, errors.New("ttl must be a bounded duration")
		}
		args := pressureArgs("cleanup", "claim", "acquire", "--kind", params["kind"], "--resource", params["resource"], "--owner", params["owner"], "--ttl", params["ttl"])
		if note := params["note"]; note != "" {
			if len(note) > 200 || strings.ContainsAny(note, "\r\n") {
				return nil, errors.New("note must be at most 200 characters without line breaks")
			}
			args = append(args, "--note", note)
		}
		if pid := params["pid"]; pid != "" {
			value, err := positiveInt(pid)
			if err != nil {
				return nil, fmt.Errorf("pid: %w", err)
			}
			args = append(args, "--pid", strconv.Itoa(value))
		}
		if enabled, err := optionalBool(params, "cleanup_on_stale"); err != nil {
			return nil, err
		} else if enabled {
			args = append(args, "--cleanup-on-stale")
		}
		return args, nil
	})
	registry.add("cleanup.claim.heartbeat", []string{"claim_id"}, func(params map[string]string) ([]string, error) { return claimCommand("heartbeat", params) })
	registry.add("cleanup.claim.release", []string{"claim_id"}, func(params map[string]string) ([]string, error) { return claimCommand("release", params) })
	registry.add("storage.provider.apply", []string{"provider", "target_free"}, func(params map[string]string) ([]string, error) {
		provider := params["provider"]
		if !safeToken.MatchString(provider) {
			return nil, errors.New("provider must be a bounded identifier")
		}
		args := pressureArgs("storage", "apply", "--provider", provider, "--apply")
		if target := params["target_free"]; target != "" {
			if !safeSize.MatchString(target) {
				return nil, errors.New("target_free must be a bounded size")
			}
			args = append(args, "--target-free", target)
		}
		return args, nil
	})
	registry.add("storage.policy.enable", nil, func(params map[string]string) ([]string, error) {
		return pressureArgs("storage", "policy", "enable"), nil
	})
	registry.add("storage.policy.observe", nil, func(params map[string]string) ([]string, error) {
		return pressureArgs("storage", "policy", "observe"), nil
	})
	registry.addFixed("io.policy.enable-alerts", pressureArgs("io", "policy", "enable-alerts"))
	registry.addFixed("io.policy.disable", pressureArgs("io", "policy", "disable"))
	registry.add("io.trace.request", []string{"pid", "duration_seconds", "open", "process_start_identity"}, func(params map[string]string) ([]string, error) {
		pid, err := positiveInt(params["pid"])
		if err != nil {
			return nil, fmt.Errorf("pid: %w", err)
		}
		duration := 5
		if raw := params["duration_seconds"]; raw != "" {
			duration, err = strconv.Atoi(raw)
			if err != nil || duration < 5 || duration > 30 {
				return nil, errors.New("duration_seconds must be between 5 and 30")
			}
		}
		args := pressureArgs("io", "trace", "--pid", strconv.Itoa(pid), "--duration", strconv.Itoa(duration)+"s")
		if enabled, err := optionalBool(params, "open"); err != nil {
			return nil, err
		} else if enabled {
			args = append(args, "--open")
		}
		if identity := params["process_start_identity"]; identity != "" && (len(identity) > 128 || strings.ContainsAny(identity, "\r\n")) {
			return nil, errors.New("process_start_identity must be bounded and line-free")
		}
		return args, nil
	})
	return registry
}

func (r *actionRegistry) add(name string, keys []string, build func(map[string]string) ([]string, error)) {
	allowed := make(map[string]bool, len(keys))
	for _, key := range keys {
		allowed[key] = true
	}
	r.specs[name] = actionSpec{allowed: allowed, build: build}
}

func (r *actionRegistry) addFixed(name string, args []string) {
	r.add(name, nil, func(map[string]string) ([]string, error) { return append([]string(nil), args...), nil })
}

func (r *actionRegistry) build(name string, params map[string]string) ([]string, error) {
	spec, ok := r.specs[name]
	if !ok {
		return nil, fmt.Errorf("unsupported action %q", name)
	}
	for key := range params {
		if !spec.allowed[key] {
			return nil, fmt.Errorf("parameter %q is not allowed for %s", key, name)
		}
	}
	return spec.build(params)
}

func claimCommand(verb string, params map[string]string) ([]string, error) {
	id := params["claim_id"]
	if !safeID.MatchString(id) {
		return nil, errors.New("claim_id must be a bounded identifier")
	}
	return pressureArgs("cleanup", "claim", verb, "--claim-id", id), nil
}

func splitIDs(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			out = append(out, strings.TrimSpace(part))
		}
	}
	return out
}

func optionalBool(params map[string]string, key string) (bool, error) {
	value := params[key]
	if value == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return parsed, nil
}

func positiveInt(value string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, errors.New("must be a positive integer")
	}
	return parsed, nil
}

func (s *Server) handleActionPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "action preview requires POST", false)
		return
	}
	var request sessionpressurecontrol.ActionRequest
	if err := decodeBody(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", err.Error(), false)
		return
	}
	data, status, err := s.previewAction(request)
	if err != nil {
		writeError(w, status, actionErrorCode(err), err.Error(), false)
		return
	}
	writeValue(w, http.StatusOK, "api", data)
}

func (s *Server) previewAction(request sessionpressurecontrol.ActionRequest) (sessionpressurecontrol.ActionPreview, int, error) {
	if strings.TrimSpace(request.Action) == "" {
		return sessionpressurecontrol.ActionPreview{}, http.StatusBadRequest, errors.New("action is required")
	}
	if len(request.Action) > 64 {
		return sessionpressurecontrol.ActionPreview{}, http.StatusBadRequest, errors.New("action exceeds the 64-character limit")
	}
	if request.Params == nil {
		request.Params = map[string]string{}
	}
	if len(request.Params) > 16 {
		return sessionpressurecontrol.ActionPreview{}, http.StatusBadRequest, errors.New("params exceeds the 16-field limit")
	}
	command, err := s.actions.build(request.Action, request.Params)
	if err != nil {
		return sessionpressurecontrol.ActionPreview{}, http.StatusBadRequest, err
	}
	stateHash, err := stateHash(s.cfg.StateDir)
	if err != nil {
		return sessionpressurecontrol.ActionPreview{}, http.StatusInternalServerError, err
	}
	now := s.cfg.Now().UTC()
	preview := sessionpressurecontrol.ActionPreview{PreviewID: requestID(), Action: request.Action, Params: cloneParams(request.Params), Command: command, StateHash: stateHash, ExpiresAt: now.Add(s.cfg.PreviewTTL), Status: "pending", RequiresApproval: true}
	if request.Action == "io.trace.request" {
		pid, pidErr := positiveInt(request.Params["pid"])
		if pidErr != nil {
			return sessionpressurecontrol.ActionPreview{}, http.StatusBadRequest, fmt.Errorf("trace pid: %w", pidErr)
		}
		identity, identityErr := sessionpressure.ProcessStartIdentity(pid)
		if identityErr != nil {
			return sessionpressurecontrol.ActionPreview{}, http.StatusBadRequest, fmt.Errorf("trace target identity: %w", identityErr)
		}
		if requested := request.Params["process_start_identity"]; requested != "" && requested != identity {
			return sessionpressurecontrol.ActionPreview{}, http.StatusConflict, errors.New("trace target identity changed before preview")
		}
		preview.ProcessStartIdentity = identity
		preview.Params["process_start_identity"] = identity
	}
	if err := s.previews.put(preview); err != nil {
		return sessionpressurecontrol.ActionPreview{}, http.StatusInternalServerError, err
	}
	if err := s.recordAudit(preview, "previewed", 0, "", ""); err != nil {
		return sessionpressurecontrol.ActionPreview{}, http.StatusInternalServerError, err
	}
	return preview, http.StatusOK, nil
}

func (s *Server) handleTraceRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		rows, err := s.previews.audit(maxAuditRows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "audit_read_failed", err.Error(), false)
			return
		}
		traces := make([]sessionpressurecontrol.AuditRecord, 0, len(rows))
		for _, row := range rows {
			if row.Action == "io.trace.request" {
				traces = append(traces, row)
			}
		}
		writeValue(w, http.StatusOK, "api", map[string]any{"trace_requests": traces, "count": len(traces), "paths_persisted": false})
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "trace request requires POST", false)
		return
	}
	var request sessionpressurecontrol.TraceRequest
	if err := decodeBody(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", err.Error(), false)
		return
	}
	duration := request.DurationSeconds
	if duration == 0 {
		duration = 5
	}
	params := map[string]string{"pid": strconv.Itoa(request.PID), "duration_seconds": strconv.Itoa(duration)}
	if request.Open {
		params["open"] = "true"
	}
	data, status, err := s.previewAction(sessionpressurecontrol.ActionRequest{Action: "io.trace.request", Params: params})
	if err != nil {
		writeError(w, status, actionErrorCode(err), err.Error(), false)
		return
	}
	writeValue(w, http.StatusOK, "api", data)
}

func (s *Server) handleActionApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "action approval requires POST", false)
		return
	}
	var request sessionpressurecontrol.ApprovalRequest
	if err := decodeBody(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", err.Error(), false)
		return
	}
	if len(request.Note) > 200 || strings.ContainsAny(request.Note, "\r\n") {
		writeError(w, http.StatusBadRequest, "invalid_note", "note must be at most 200 characters without line breaks", false)
		return
	}
	if !safeID.MatchString(request.PreviewID) {
		writeError(w, http.StatusBadRequest, "invalid_preview", "preview_id is invalid", false)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	preview, err := s.previews.get(request.PreviewID)
	if err != nil {
		writeError(w, http.StatusNotFound, "preview_not_found", err.Error(), false)
		return
	}
	now := s.cfg.Now().UTC()
	if preview.Status != "pending" {
		_ = s.recordAudit(preview, "rejected", 0, "preview_replayed", request.Note)
		writeError(w, http.StatusConflict, "preview_replayed", "preview is no longer pending", false)
		return
	}
	if !now.Before(preview.ExpiresAt) {
		preview.Status = "expired"
		_ = s.previews.put(preview)
		_ = s.recordAudit(preview, "expired", 0, "preview_expired", request.Note)
		writeError(w, http.StatusConflict, "preview_expired", "preview has expired", false)
		return
	}
	currentHash, err := stateHash(s.cfg.StateDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "state_hash_failed", err.Error(), false)
		return
	}
	if currentHash != preview.StateHash {
		preview.Status = "stale"
		_ = s.previews.put(preview)
		_ = s.recordAudit(preview, "stale", 0, "state_changed", request.Note)
		writeError(w, http.StatusConflict, "preview_stale", "state changed after preview; request a new preview", false)
		return
	}
	if preview.Action == "io.trace.request" {
		pid, pidErr := positiveInt(preview.Params["pid"])
		if pidErr != nil {
			writeError(w, http.StatusConflict, "trace_identity_failed", pidErr.Error(), false)
			return
		}
		identity, identityErr := sessionpressure.ProcessStartIdentity(pid)
		if identityErr != nil || identity != preview.ProcessStartIdentity {
			preview.Status = "stale"
			_ = s.previews.put(preview)
			_ = s.recordAudit(preview, "stale", 0, "trace_identity_changed", request.Note)
			writeError(w, http.StatusConflict, "trace_identity_changed", "trace target identity changed after preview; request a new preview", false)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.CommandTimeout)
	result := s.cfg.Runner.Run(ctx, preview.Command)
	cancel()
	if result.ExitCode != 0 || result.Err != nil {
		preview.Status = "failed"
		_ = s.previews.put(preview)
		_ = s.recordAudit(preview, "failure", len(result.Stdout), "authority_failed", request.Note)
		payload := map[string]any{"preview": preview, "exit_code": result.ExitCode, "output": boundedOutput(result.Stdout), "error": truncate(string(result.Stderr), 512)}
		writeValue(w, http.StatusUnprocessableEntity, "api", payload)
		return
	}
	preview.Status = "approved"
	_ = s.previews.put(preview)
	if err := s.recordAudit(preview, "success", len(result.Stdout), "", request.Note); err != nil {
		writeError(w, http.StatusInternalServerError, "audit_failed", err.Error(), false)
		return
	}
	payload := map[string]any{"preview": preview, "exit_code": result.ExitCode, "output": boundedOutput(result.Stdout)}
	writeValue(w, http.StatusOK, "api", payload)
}

func (s *Server) handleActionAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "audit requires GET", false)
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxAuditRows {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 200", false)
			return
		}
		limit = parsed
	}
	rows, err := s.previews.audit(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "audit_read_failed", err.Error(), false)
		return
	}
	writeValue(w, http.StatusOK, "api", map[string]any{"audit": rows, "count": len(rows)})
}

func decodeBody(r *http.Request, target any) error {
	if r.ContentLength > maxJSONBytes {
		return errors.New("request exceeds the API body budget")
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxJSONBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("request must contain one JSON value")
	}
	return nil
}

func boundedOutput(body []byte) any {
	if len(body) > maxJSONBytes {
		body = body[:maxJSONBytes]
	}
	if json.Valid(body) {
		return json.RawMessage(body)
	}
	return truncate(string(body), maxJSONBytes)
}
func cloneParams(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
func actionErrorCode(err error) string {
	if strings.Contains(err.Error(), "unsupported action") {
		return "unsupported_action"
	}
	return "invalid_action"
}

func (s *Server) recordAudit(preview sessionpressurecontrol.ActionPreview, outcome string, outputBytes int, errorCode, note string) error {
	return s.previews.appendAudit(sessionpressurecontrol.AuditRecord{AuditID: requestID(), PreviewID: preview.PreviewID, Action: preview.Action, Outcome: outcome, RecordedAt: s.cfg.Now().UTC(), Note: note, OutputBytes: outputBytes, ErrorCode: errorCode})
}

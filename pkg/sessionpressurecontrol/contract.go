// Package sessionpressurecontrol defines the public wire contract for the
// local SessionPressure control plane. The server deliberately keeps the
// canonical CLI projection inside Data until a cross-client schema has been
// proven by parity fixtures.
package sessionpressurecontrol

import (
	"encoding/json"
	"time"
)

const (
	// APIVersion is the stable contract name, not the implementation release.
	APIVersion = "nicos.session.pressure.control.v1"
	// DefaultSocketRelativePath is resolved below the SessionPressure state
	// directory by the server and clients.
	DefaultSocketRelativePath = "api/session-pressure.sock"
	// DefaultTokenRelativePath is used only when explicit loopback HTTP is on.
	DefaultTokenRelativePath = "api/http.token"
)

// Envelope is returned for every JSON request, including errors.
type Envelope struct {
	APIVersion  string          `json:"api_version"`
	RequestID   string          `json:"request_id"`
	GeneratedAt time.Time       `json:"generated_at"`
	Source      string          `json:"source,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
	Error       *Error          `json:"error,omitempty"`
}

type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

type Health struct {
	Status       string   `json:"status"`
	API          string   `json:"api"`
	Authority    string   `json:"authority"`
	Transport    string   `json:"transport,omitempty"`
	PID          int      `json:"pid"`
	StartedAt    string   `json:"started_at"`
	Capabilities []string `json:"capabilities"`
}

type ActionRequest struct {
	Action string            `json:"action"`
	Params map[string]string `json:"params,omitempty"`
}

type ApprovalRequest struct {
	PreviewID string `json:"preview_id"`
	Note      string `json:"note,omitempty"`
}

type TraceRequest struct {
	PID             int  `json:"pid"`
	DurationSeconds int  `json:"duration_seconds,omitempty"`
	Open            bool `json:"open,omitempty"`
}

type ActionPreview struct {
	PreviewID            string            `json:"preview_id"`
	Action               string            `json:"action"`
	Params               map[string]string `json:"params,omitempty"`
	Command              []string          `json:"command,omitempty"`
	ProcessStartIdentity string            `json:"process_start_identity,omitempty"`
	StateHash            string            `json:"state_hash"`
	ExpiresAt            time.Time         `json:"expires_at"`
	Status               string            `json:"status"`
	RequiresApproval     bool              `json:"requires_approval"`
}

type AuditRecord struct {
	AuditID     string    `json:"audit_id"`
	PreviewID   string    `json:"preview_id,omitempty"`
	Action      string    `json:"action"`
	Outcome     string    `json:"outcome"`
	RecordedAt  time.Time `json:"recorded_at"`
	Note        string    `json:"note,omitempty"`
	OutputBytes int       `json:"output_bytes,omitempty"`
	ErrorCode   string    `json:"error_code,omitempty"`
}

type Event struct {
	Sequence uint64          `json:"sequence"`
	Type     string          `json:"type"`
	At       time.Time       `json:"at"`
	Data     json.RawMessage `json:"data,omitempty"`
	Dropped  uint64          `json:"dropped,omitempty"`
}

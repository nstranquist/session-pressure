package sessionpressure

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCompactWorkEventDropsForensicBulkKeepsDecisionFields(t *testing.T) {
	exit := 1
	event := WorkEvent{
		SchemaVersion:               WorkEventSchemaVersion,
		EventID:                     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Timestamp:                   time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC),
		Event:                       WorkEventCompleted,
		OperationID:                 "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Class:                       WorkClassTest,
		Weight:                      2,
		CommandDigest:               "sha256:deadbeef",
		SessionDigest:               "sha256:session",
		Blocker:                     WorkBlockerPressure,
		WaitMilliseconds:            1500,
		RuntimeMillis:               200,
		Outcome:                     "failed",
		ExitCode:                    &exit,
		PressureLevel:               LevelWarning,
		PressureDimension:           "cpu",
		AdmissionDecision:           WarningCapacityDeferredDecision,
		Capacity:                    8,
		Used:                        4,
		QueueDepth:                  2,
		ShadowDecisionReason:        "shadow_only",
		CoordinatedWorkCPUPercent:   12.5,
		CoordinatedWorkProcessCount: 9,
		CandidateSchedulingPolicy:   "fifo",
		ShadowSelectedOperationID:   "cccccccccccccccccccccccccccccccc",
	}
	compact := CompactWorkEventFrom(event)
	if compact.OperationID != event.OperationID || compact.Event != WorkEventCompleted || compact.Class != WorkClassTest {
		t.Fatalf("identity lost: %+v", compact)
	}
	if compact.WaitMilliseconds != 1500 || compact.RuntimeMillis != 200 || compact.Outcome != "failed" {
		t.Fatalf("timing/outcome lost: %+v", compact)
	}
	if compact.AdmissionDecision != WarningCapacityDeferredDecision || compact.PressureLevel != LevelWarning {
		t.Fatalf("admission context lost: %+v", compact)
	}
	raw, err := json.Marshal(compact)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, banned := range []string{"command_digest", "session_digest", "shadow_", "coordinated_work", "deadbeef"} {
		if strings.Contains(text, banned) {
			t.Fatalf("compact retained forensic bulk %q in %s", banned, text)
		}
	}
	if events := CompactWorkEvents([]WorkEvent{event}); len(events) != 1 {
		t.Fatalf("CompactWorkEvents len=%d", len(events))
	}
}

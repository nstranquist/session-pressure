package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"time"
)

// acceptance combines independent gates without restarting the stability run.
// It reports pending gates; it never enables a policy or promotes a release.
func acceptance(args []string) error {
	f := flag.NewFlagSet("acceptance", flag.ContinueOnError)
	input := f.String("observation", "", "existing stability observation JSON")
	out := f.String("out", "", "combined acceptance JSON")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || !filepath.IsAbs(*input) || !filepath.IsAbs(*out) || filepath.Clean(*input) == filepath.Clean(*out) {
		return errors.New("distinct absolute --observation and --out required")
	}
	// Clear a previous pass before any collection can fail. An unavailable
	// observer or child must never leave an old accepted=true as current state.
	if err := writeJSON(*out, acceptanceReport{SchemaVersion: 1, CapturedAt: time.Now().UTC(), Observation: *input, Pending: []string{"acceptance collection has not completed"}}); err != nil {
		return err
	}
	file, err := os.Open(*input)
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(file, 2*1024*1024+1))
	closeErr := file.Close()
	if err != nil || closeErr != nil || len(raw) > 2*1024*1024 {
		return errors.New("cannot read bounded stability observation")
	}
	var observed observation
	if err = json.Unmarshal(raw, &observed); err != nil {
		return err
	}
	if !filepath.IsAbs(observed.Binary) || !filepath.IsAbs(observed.AppPath) {
		return errors.New("observation has no absolute installed artifact paths")
	}
	now := time.Now().UTC()
	r := acceptanceReport{SchemaVersion: 1, CapturedAt: now, Observation: *input, BinaryDigest: observed.BinaryDigest, Pending: []string{}}
	if err = observed.verifyArtifacts(); err != nil {
		r.Pending = append(r.Pending, err.Error())
	} else {
		r.ArtifactIdentityPassed = true
	}
	audit, auditErr := readCLIReport(observed.Binary, true, "audit", "--since", "2h", "--full")
	evaluation, evaluationErr := readCLIReport(observed.Binary, true, "work", "evaluate")
	if auditErr != nil {
		r.Pending = append(r.Pending, "audit collection failed: "+auditErr.Error())
	}
	if evaluationErr != nil {
		r.Pending = append(r.Pending, "replay collection failed: "+evaluationErr.Error())
	}
	r.Audit, r.Evaluation = audit, evaluation
	r.evaluate(observed, now)
	return writeJSON(*out, r)
}

type acceptanceReport struct {
	SchemaVersion          int             `json:"schema_version"`
	CapturedAt             time.Time       `json:"captured_at"`
	Observation            string          `json:"observation"`
	BinaryDigest           string          `json:"binary_sha256"`
	ArtifactIdentityPassed bool            `json:"artifact_identity_passed"`
	StabilityPassed        bool            `json:"stability_72h_passed"`
	WriterPassed           bool            `json:"sustained_writer_passed"`
	ReplayPassed           bool            `json:"replay_promotion_passed"`
	Accepted               bool            `json:"accepted"`
	Pending                []string        `json:"pending"`
	Audit                  json.RawMessage `json:"audit,omitempty"`
	Evaluation             json.RawMessage `json:"evaluation,omitempty"`
}

type acceptanceWriter struct {
	Known     bool  `json:"known"`
	Attempted int64 `json:"attempted"`
	Written   int64 `json:"written"`
	Failed    int64 `json:"failed"`
	Gaps      int64 `json:"gaps"`
	Samples   int64 `json:"latency_samples"`
	P95       int64 `json:"latency_p95_us"`
}

func (w acceptanceWriter) passed() bool {
	return w.Known && w.Samples >= 20 && w.P95 > 0 && w.P95 <= 25000 && w.Attempted >= w.Samples && w.Attempted == w.Written && w.Failed == 0 && w.Gaps == 0
}

func (r *acceptanceReport) evaluate(s observation, now time.Time) {
	r.StabilityPassed = s.Accepted72H && !s.Started.IsZero() && s.Deadline.Sub(s.Started) >= 72*time.Hour && !now.Before(s.Deadline) && !s.Updated.Before(s.Deadline) && !s.Updated.After(now.Add(time.Minute)) && s.HealthPassed && s.ContinuityPassed && s.Failures == 0 && s.ResidentRestarts == 0 && s.AppRestarts == 0 && s.AppBudgetPassed
	if !r.StabilityPassed {
		r.Pending = append(r.Pending, "continuous 72-hour stability gate has not passed")
	}
	var audit struct {
		Audit struct {
			GeneratedAt time.Time `json:"generated_at"`
			Categories  []struct {
				ID      string `json:"id"`
				Metrics struct {
					CLI     acceptanceWriter `json:"cli_writer_recent_2h"`
					Product acceptanceWriter `json:"product_writer"`
				} `json:"metrics"`
			} `json:"categories"`
		} `json:"audit"`
	}
	if json.Unmarshal(r.Audit, &audit) == nil && freshAcceptance(audit.Audit.GeneratedAt, now) {
		for _, c := range audit.Audit.Categories {
			if c.ID == "telemetry" {
				r.WriterPassed = c.Metrics.CLI.passed() && c.Metrics.Product.passed()
			}
		}
	}
	if !r.WriterPassed {
		r.Pending = append(r.Pending, "both natural two-hour writers need >=20 samples, zero loss and p95 <=25ms")
	}
	var evaluation struct {
		Work struct {
			OK            bool `json:"ok"`
			ScenarioCount int  `json:"scenario_count"`
			Passed        int  `json:"passed"`
			Failed        int  `json:"failed"`
			Replay        struct {
				End         time.Time `json:"trace_end"`
				Ready       bool      `json:"promotion_ready"`
				Safety      bool      `json:"safety_gates_pass"`
				Performance bool      `json:"performance_gates_pass"`
				Calibration bool      `json:"calibration_gates_pass"`
				Selector    bool      `json:"selector_gate_pass"`
			} `json:"replay"`
		} `json:"work_evaluation"`
	}
	if json.Unmarshal(r.Evaluation, &evaluation) == nil {
		w := evaluation.Work
		r.ReplayPassed = w.OK && w.ScenarioCount >= 22 && w.Passed == w.ScenarioCount && w.Failed == 0 && w.Replay.Ready && w.Replay.Safety && w.Replay.Performance && w.Replay.Calibration && w.Replay.Selector && !w.Replay.End.IsZero() && !w.Replay.End.After(now.Add(time.Minute)) && now.Sub(w.Replay.End) <= 24*time.Hour
	}
	if !r.ReplayPassed {
		r.Pending = append(r.Pending, "fresh live replay has not cleared every promotion gate")
	}
	r.Accepted = r.ArtifactIdentityPassed && r.StabilityPassed && r.WriterPassed && r.ReplayPassed && len(r.Pending) == 0
}

func freshAcceptance(at, now time.Time) bool {
	return !at.IsZero() && !at.After(now.Add(time.Minute)) && now.Sub(at) <= 5*time.Minute
}

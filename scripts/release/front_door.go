package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/nstranquist/session-pressure/frontdoor"
)

// A front door is a host wrapper that execs the session-pressure CLI and also
// links this module in-process (on the primary Mac, ndev). Promotion installs
// the CLI, helper and app; the front door has its own build and keeps the old
// library code until it is rebuilt. The wrapper records how to rebuild and
// call it (package frontdoor), doctor reports that record, and promotion
// rebuilds the front door and checks the doctor through it.

const (
	frontDoorRepublished   = "republished"
	frontDoorRefNotHead    = "not-republished-ref-not-head"
	frontDoorBinaryMissing = "binary-missing"
	frontDoorUnreadable    = "registration-unreadable"
)

type frontDoorReceipt struct {
	Name     string `json:"name,omitempty"`
	Status   string `json:"status"`
	Revision string `json:"revision,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// commandRunner runs argv with env. stream, when set, also receives the
// output as it arrives, for long builds an operator watches.
type commandRunner func(argv, env []string, stream io.Writer) ([]byte, error)

func execRunner(argv, env []string, stream io.Writer) ([]byte, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	var out bytes.Buffer
	var sink io.Writer = &out
	if stream != nil {
		sink = io.MultiWriter(&out, stream)
	}
	cmd.Stdout, cmd.Stderr = sink, sink
	err := cmd.Run()
	return out.Bytes(), err
}

type doctorReport struct {
	OK     bool `json:"ok"`
	Doctor struct {
		SourceRevisions *struct {
			CLI             string `json:"cli"`
			FrontDoor       string `json:"front_door"`
			InstalledHelper string `json:"installed_helper"`
			Mismatch        bool   `json:"mismatch"`
		} `json:"source_revisions"`
		SourceLag *doctorSourceLag  `json:"source_lag"`
		FrontDoor *frontdoor.Status `json:"front_door"`
	} `json:"doctor"`
}

func runDoctor(run commandRunner, argv, env []string) (doctorReport, error) {
	raw, err := run(argv, env, nil)
	if err != nil {
		return doctorReport{}, fmt.Errorf("doctor verification failed (%s): %w: %s", strings.Join(argv, " "), err, tail(raw))
	}
	var report doctorReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return doctorReport{}, fmt.Errorf("decode doctor verification (%s): %w", strings.Join(argv, " "), err)
	}
	return report, nil
}

type doctorSourceLag struct {
	Checked           bool   `json:"checked"`
	Status            string `json:"status"`
	SourceRevision    string `json:"source_revision"`
	InstalledRevision string `json:"installed_revision"`
	CommitsBehind     int    `json:"commits_behind"`
}

// promoteFrontDoor rebuilds the recorded front door and checks, through it,
// that it now links targetRev and that every executing binary agrees. The
// rebuild gets targetRev in frontdoor.PinEnv, so a commit that lands in the
// session-pressure checkout during the build does not change what the front
// door links. A front door whose executable is gone, or a promotion of a ref
// other than HEAD, is reported and not treated as a failure: the front door
// builds its own HEAD, which need not compile against an older
// session-pressure, and a Republish command that predates PinEnv links the
// checkout's HEAD.
func promoteFrontDoor(run commandRunner, status frontdoor.Status, targetRev, headRev string, env []string) (frontDoorReceipt, error) {
	receipt := frontDoorReceipt{Name: status.Name}
	switch {
	case status.Name == "":
		receipt.Status, receipt.Detail = frontDoorUnreadable, status.Detail
		return receipt, nil
	case !status.BinaryPresent:
		receipt.Status, receipt.Detail = frontDoorBinaryMissing, status.Detail
		return receipt, nil
	case targetRev != headRev:
		receipt.Status = frontDoorRefNotHead
		receipt.Detail = fmt.Sprintf("%s builds session-pressure HEAD (%s), not the promoted %s; run `%s` once HEAD is promoted",
			status.Name, shortSHA(headRev), shortSHA(targetRev), strings.Join(status.Republish, " "))
		return receipt, nil
	}
	republishEnv := append(append([]string(nil), env...), frontdoor.PinEnv+"="+targetRev)
	if raw, err := run(status.Republish, republishEnv, os.Stderr); err != nil {
		receipt.Status = "republish-failed"
		return receipt, fmt.Errorf("republish front door %s (`%s`): %w: %s", status.Name, strings.Join(status.Republish, " "), err, tail(raw))
	}
	argv := append(append([]string(nil), status.Invoke...), "--json", "doctor")
	report, err := runDoctor(run, argv, env)
	if err != nil {
		receipt.Status = "verify-failed"
		return receipt, err
	}
	revisions := report.Doctor.SourceRevisions
	if revisions == nil || revisions.FrontDoor == "" {
		receipt.Status = "verify-failed"
		return receipt, fmt.Errorf("front door %s did not report the revision it links; it may not pass %s to the CLI", status.Name, frontdoor.RevisionEnv)
	}
	receipt.Revision = revisions.FrontDoor
	if revisions.FrontDoor != targetRev {
		receipt.Status = "verify-failed"
		return receipt, fmt.Errorf("post-install verification failed: front door %s links %s after republish, want %s; its republish command must link the commit in %s",
			status.Name, shortSHA(revisions.FrontDoor), shortSHA(targetRev), frontdoor.PinEnv)
	}
	if revisions.Mismatch {
		receipt.Status = "verify-failed"
		return receipt, fmt.Errorf("post-install verification failed: binaries disagree through front door %s (cli=%s front_door=%s helper=%s)",
			status.Name, shortSHA(revisions.CLI), shortSHA(revisions.FrontDoor), shortSHA(revisions.InstalledHelper))
	}
	receipt.Status = frontDoorRepublished
	return receipt, nil
}

// tail keeps the end of a command's output for an error message.
func tail(raw []byte) string {
	const limit = 2000
	text := strings.TrimSpace(string(raw))
	if len(text) > limit {
		text = "…" + text[len(text)-limit:]
	}
	return text
}

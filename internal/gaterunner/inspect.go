// Package gaterunner exposes bounded, read-only gate-runner evidence. It never
// changes delegate policy, launches work, or stores another telemetry database.
package gaterunner

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nstranquist/session-pressure/packages/processtree"
)

const maxRows = 1000

// The delegate run ledger is the gate-delegate.runs side stream in the
// delivery lane's side-streams.sqlite (it replaced gate-delegate/runs.jsonl
// on 2026-11-16). Rows are append-only JSON bodies keyed by seq.
const (
	sideStreamsName    = "side-streams.sqlite"
	delegateRunsStream = "gate-delegate.runs"
)

// Receipt lines carry the full escaped command, up to about 21 KB each, so
// 1,000 raw lines are about 6.5 MB. The runner cuts each line to
// receiptLineBytes and marks a cut line with a trailing \x01 byte, which keeps
// the minute-cadence read near 500 KB and lets the latest 1,000 receipts fit
// the wire budget. The full command comes with the job log (JobLog).
const (
	receiptLineBytes  = 512
	receiptCutMarker  = "\x01"
	receiptsReadLimit = 768 << 10
	rowsWireBudget    = 1 << 20
	receiptsScript    = `tail -n 1000 /srv/build/logs/jobs.log | LC_ALL=C awk '{ if (length($0) > 512) printf "%s\001\n", substr($0, 1, 512); else print }' && printf '\n__HZ_HEALTH__\n' && cat /proc/loadavg && free -m && df -P / && uptime -p`
)

type Run struct {
	ID                string     `json:"id"`
	At                *time.Time `json:"at"`
	Mode              string     `json:"mode"`
	Decision          string     `json:"decision"`
	DeliveryID        string     `json:"delivery_id"`
	RepoName          string     `json:"repo_name"`
	RepoPath          string     `json:"repo_path"`
	Command           string     `json:"command"`
	LocalPassed       *bool      `json:"local_passed"`
	LocalMS           *int64     `json:"local_ms"`
	LocalDetail       *string    `json:"local_detail"`
	LocalEnvironment  *string    `json:"local_environment"`
	Agreement         *bool      `json:"agreement"`
	RemoteRan         bool       `json:"remote_ran"`
	RemotePassed      *bool      `json:"remote_passed"`
	RemoteExit        *int       `json:"remote_exit"`
	RemoteRunMS       *int64     `json:"remote_run_ms"`
	RemoteWallMS      *int64     `json:"remote_wall_ms"`
	RemoteStage       *string    `json:"remote_stage"`
	TreeVerified      *bool      `json:"tree_verified"`
	TreeHash          *string    `json:"tree_hash"`
	PolicyDigest      *string    `json:"policy_digest"`
	RemotePassAllowed *bool      `json:"remote_pass_allowed"`
}
type Repo struct {
	Path string `json:"path"`
	Box  string `json:"box"`
}
type Config struct {
	Mode       string `json:"mode"`
	Repos      []Repo `json:"repos"`
	AllowCount int    `json:"allow_count"`
}
type Health struct {
	Load1           *float64 `json:"load1"`
	Load5           *float64 `json:"load5"`
	Load15          *float64 `json:"load15"`
	MemTotalMB      *int64   `json:"mem_total_mb"`
	MemAvailableMB  *int64   `json:"mem_available_mb"`
	DiskUsedPercent *int     `json:"disk_used_percent"`
	Uptime          *string  `json:"uptime"`
}
type Job struct {
	ID              string     `json:"id"`
	At              *time.Time `json:"at"`
	Caller          string     `json:"caller"`
	Repo            string     `json:"repo"`
	SHA             *string    `json:"sha"`
	Cand            *string    `json:"cand"`
	JobClass        *string    `json:"job_class"`
	ExitCode        string     `json:"exit_code"`
	DurationSeconds *int       `json:"duration_seconds"`
	MacEst          *string    `json:"mac_est"`
	Command         string     `json:"command"`
	// CommandTruncated marks a receipt the runner cut to receiptLineBytes;
	// JobLog returns the full command.
	CommandTruncated bool `json:"command_truncated,omitempty"`
}
type Inspection struct {
	Host             string        `json:"host"`
	SchemaVersion    int           `json:"schema_version"`
	CapturedAt       time.Time     `json:"captured_at"`
	Runs             []Run         `json:"runs"`
	Config           *Config       `json:"config"`
	LedgerSignature  string        `json:"ledger_signature"`
	LedgerUnchanged  bool          `json:"ledger_unchanged"`
	LedgerPresent    bool          `json:"ledger_present"`
	HistoryTruncated bool          `json:"history_truncated"`
	MalformedRows    int           `json:"malformed_rows"`
	LocalError       string        `json:"local_error,omitempty"`
	BoxRequested     bool          `json:"box_requested"`
	BoxReachable     bool          `json:"box_reachable"`
	BoxError         string        `json:"box_error,omitempty"`
	BoxFailure       string        `json:"box_failure,omitempty"`
	Jobs             []Job         `json:"jobs"`
	JobsSignature    string        `json:"jobs_signature,omitempty"`
	JobsUnchanged    bool          `json:"jobs_unchanged,omitempty"`
	Health           *Health       `json:"health"`
	Live             *LiveSection  `json:"live,omitempty"`
	Fleet            *FleetSection `json:"fleet,omitempty"`
}

// Inspect retains errors in its contract rather than inventing an empty,
// successful history. Clients can keep the prior successful value on error.
func Inspect(ctx context.Context, home string, includeBox bool) Inspection {
	return InspectSince(ctx, home, includeBox, "")
}

// A content signature avoids repeatedly decoding an unchanged history in the
// desktop. Configuration and remote health still refresh independently.
func InspectSince(ctx context.Context, home string, includeBox bool, known string) Inspection {
	return InspectWithCursors(ctx, home, includeBox, known, "")
}

// InspectWithCursors also accepts the jobs signature from the client's last
// box read. A matching signature returns jobs_unchanged=true and no rows, so
// the minute-cadence box read stops re-sending up to 512 KiB of receipts.
func InspectWithCursors(ctx context.Context, home string, includeBox bool, known, knownJobs string) Inspection {
	s := Inspection{SchemaVersion: 1, CapturedAt: time.Now().UTC(), Runs: []Run{}, Jobs: []Job{}, BoxRequested: includeBox}
	s.Host = runnerTarget(ctx)
	// Fleet probes run alongside the selected host read, not on the 15-second
	// local-only refresh. Every probe remains bound to its named endpoint.
	fleetReady := make(chan *FleetSection, 1)
	go func() { fleetReady <- readFleet(ctx, home, includeBox) }()
	dir := filepath.Join(home, ".nicos-dev", "commit-delivery", "v1")
	runs, present, truncated, malformed, signature, unchanged, err := readDelegateRuns(ctx, filepath.Join(dir, sideStreamsName), known)
	s.LedgerPresent, s.HistoryTruncated, s.MalformedRows = present, truncated, malformed
	s.LedgerSignature, s.LedgerUnchanged = signature, unchanged
	if err != nil {
		s.LocalError = "delegate_ledger_read_failed"
	} else {
		s.Runs = runs
	}
	// The runner-live store is read on every inspection (local, bounded,
	// query-only); an absent store leaves Live nil so clients can report
	// unavailable job samples independently from scheduler reachability.
	s.Live = readLiveSection(ctx, filepath.Join(dir, liveStoreName), s.CapturedAt)
	config, err := readConfig(filepath.Join(dir, "gate-delegate.json"))
	if err != nil {
		s.LocalError = strings.TrimSpace(s.LocalError + " delegate_config_read_failed")
	} else {
		s.Config = config
	}
	if includeBox {
		output, e := runSSH(ctx, receiptsScript, receiptsReadLimit)
		if e != nil {
			s.BoxError = "gate_runner_read_failed"
			s.BoxFailure = sshFailureKind(e)
		} else {
			halves := strings.SplitN(string(output), "\n__HZ_HEALTH__\n", 2)
			if len(halves) != 2 {
				s.BoxError = "gate_runner_payload_invalid"
			} else {
				sum := sha256.Sum256([]byte(halves[0]))
				s.JobsSignature = hex.EncodeToString(sum[:])
				s.JobsUnchanged = knownJobs != "" && knownJobs == s.JobsSignature
				scanner := bufio.NewScanner(strings.NewReader(halves[0]))
				scanner.Buffer(make([]byte, 4096), 256<<10)
				for scanner.Scan() {
					if j, ok := ParseJob(scanner.Text()); ok {
						s.Jobs = append(s.Jobs, j)
					}
				}
				if scanner.Err() != nil {
					s.BoxError = "gate_runner_payload_invalid"
					s.Jobs = []Job{}
				} else {
					reverse(s.Jobs)
					if s.JobsUnchanged {
						s.Jobs = []Job{}
					}
					h := ParseHealth(halves[1])
					if h.Load1 == nil || h.MemTotalMB == nil || h.DiskUsedPercent == nil {
						s.BoxError = "gate_runner_health_invalid"
					} else {
						s.Health = &h
						s.BoxReachable = true
					}
				}
			}
		}
	}
	s.Runs, truncated = boundRows(s.Runs, rowsWireBudget)
	s.HistoryTruncated = s.HistoryTruncated || truncated
	s.Jobs, _ = boundRows(s.Jobs, rowsWireBudget)
	s.Fleet = <-fleetReady
	s.CapturedAt = time.Now().UTC()
	return s
}

func reverse[T any](rows []T) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}
func parseDate(s string) *time.Time {
	t, e := time.Parse(time.RFC3339Nano, s)
	if e != nil {
		return nil
	}
	return &t
}

// readDelegateRuns reads the latest maxRows gate-delegate runs, newest
// first. A missing store is absent history, not an error. The signature
// names the stream's committed extent (row count and top seq), so an
// unchanged ledger is not decoded or re-sent.
func readDelegateRuns(ctx context.Context, path, known string) ([]Run, bool, bool, int, string, bool, error) {
	st, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return []Run{}, false, false, 0, "", false, nil
	}
	if err != nil || !st.Mode().IsRegular() {
		return nil, false, false, 0, "", false, errors.New("side-stream store must be a regular file")
	}
	extent, err := querySQLite(ctx, path, "SELECT count(*) AS n, coalesce(max(seq), 0) AS top FROM stream_rows WHERE stream = '"+delegateRunsStream+"'", 4096, 1)
	if err != nil || len(extent) != 1 {
		return nil, true, false, 0, "", false, errors.New("delegate ledger extent unreadable")
	}
	count, _ := extent[0]["n"].(float64)
	top, _ := extent[0]["top"].(float64)
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%.0f:%.0f", delegateRunsStream, count, top)))
	signature := hex.EncodeToString(digest[:])
	truncated := count > maxRows
	if known != "" && known == signature {
		return []Run{}, true, truncated, 0, signature, true, nil
	}
	rows, err := querySQLite(ctx, path, fmt.Sprintf("SELECT body FROM stream_rows WHERE stream = '%s' AND seq <= %.0f ORDER BY seq DESC LIMIT %d", delegateRunsStream, top, maxRows), 4<<20, maxRows)
	if err != nil {
		return nil, true, truncated, 0, "", false, err
	}
	runs := []Run{}
	malformed := 0
	occurrences := map[string]int{}
	for _, r := range rows {
		raw, _ := r["body"].(string)
		run, ok := parseDelegateRun([]byte(raw), occurrences)
		if !ok {
			malformed++
			continue
		}
		runs = append(runs, run)
	}
	return runs, true, truncated, malformed, signature, false, nil
}

// parseDelegateRun projects one ledger body. occurrences keeps repeated
// delivery@at rows distinct, newest first.
func parseDelegateRun(line []byte, occurrences map[string]int) (Run, bool) {
	var row struct {
		At            string `json:"at"`
		Mode          string `json:"mode"`
		Decision      string `json:"decision"`
		DeliveryID    string `json:"delivery_id"`
		Repository    string `json:"repository"`
		BoxRepository string `json:"box_repository"`
		Command       string `json:"command"`
		Verify        struct {
			Argv []string `json:"argv"`
		} `json:"verify"`
		Local struct {
			Passed      *bool   `json:"passed"`
			MS          *int64  `json:"ms"`
			Detail      *string `json:"detail"`
			Environment *string `json:"environment"`
		} `json:"local"`
		Remote struct {
			Ran          bool    `json:"ran"`
			Passed       *bool   `json:"passed"`
			Exit         *int    `json:"exit"`
			RunMS        *int64  `json:"run_ms"`
			WallMS       *int64  `json:"wall_ms"`
			Stage        *string `json:"stage"`
			TreeVerified *bool   `json:"tree_verified"`
		} `json:"remote"`
		Tree              *string `json:"tree"`
		PolicyDigest      *string `json:"policy_digest"`
		RemotePassAllowed *bool   `json:"remote_pass_allowed"`
		Agreement         *bool   `json:"agreement"`
	}
	if json.Unmarshal(line, &row) != nil || row.DeliveryID == "" || row.At == "" || parseDate(row.At) == nil {
		return Run{}, false
	}
	// command is a short label ("go run"); verify.argv is what ran.
	if len(row.Verify.Argv) > 0 {
		row.Command = strings.Join(row.Verify.Argv, " ")
	}
	name := row.BoxRepository
	if name == "" {
		name = filepath.Base(row.Repository)
	}
	baseID := row.DeliveryID + "@" + row.At
	occurrence := occurrences[baseID]
	occurrences[baseID]++
	return Run{ID: fmt.Sprintf("%s@%d", baseID, occurrence), At: parseDate(row.At), Mode: row.Mode, Decision: row.Decision, DeliveryID: row.DeliveryID, RepoName: name, RepoPath: row.Repository, Command: row.Command, LocalPassed: row.Local.Passed, LocalMS: row.Local.MS, LocalDetail: row.Local.Detail, LocalEnvironment: row.Local.Environment, Agreement: row.Agreement, RemoteRan: row.Remote.Ran, RemotePassed: row.Remote.Passed, RemoteExit: row.Remote.Exit, RemoteRunMS: row.Remote.RunMS, RemoteWallMS: row.Remote.WallMS, RemoteStage: row.Remote.Stage, TreeVerified: row.Remote.TreeVerified, TreeHash: row.Tree, PolicyDigest: row.PolicyDigest, RemotePassAllowed: row.RemotePassAllowed}, true
}
func readConfig(path string) (*Config, error) {
	f, e := openRegular(path)
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Size() > 1<<20 {
		return nil, errors.New("config exceeds bound or is not regular")
	}
	raw, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if e != nil || len(raw) > 1<<20 {
		return nil, errors.New("config read failed")
	}
	var row struct {
		Mode         string            `json:"mode"`
		Repositories map[string]string `json:"repositories"`
		Allow        []json.RawMessage `json:"allow"`
	}
	if e = json.Unmarshal(raw, &row); e != nil {
		return nil, e
	}
	c := &Config{Mode: row.Mode, Repos: []Repo{}, AllowCount: len(row.Allow)}
	for p, b := range row.Repositories {
		c.Repos = append(c.Repos, Repo{Path: p, Box: b})
	}
	sort.Slice(c.Repos, func(i, j int) bool { return c.Repos[i].Path < c.Repos[j].Path })
	encoded, e := json.Marshal(c)
	if e != nil || len(encoded) > 256<<10 {
		return nil, errors.New("config response exceeds bound")
	}
	return c, nil
}

var jobIDPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9]+$`)

// schedulerJobPattern matches a scheduler job (a live or recent job in the
// runner-live feed). Its output is /srv/build/scheduler/<id>.log and grows
// while the job runs.
var schedulerJobPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// A log up to jobLogWhole bytes comes back whole. A longer log comes back as
// its first jobLogHead and last jobLogTail bytes: setup context plus the end,
// where a failure or the latest output of a running job is.
const (
	jobLogWhole = 200_000
	jobLogHead  = 40_000
	jobLogTail  = 160_000
	// jobLogGap separates head and tail; printf prints it verbatim.
	jobLogGap = "\n\x1eHZ_GAP\x1e\n"
)

// JobLogResult is one bounded job log read.
type JobLogResult struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"` // receipt or scheduler
	Log          string `json:"log"`
	Bytes        int64  `json:"bytes"`
	OmittedBytes int64  `json:"omitted_bytes"`
	// Command is the full receipt command (receipt logs only); the inspect
	// list may carry a cut copy.
	Command *string `json:"command,omitempty"`
}

func jobLogTarget(id string) (path, kind string, ok bool) {
	switch {
	case len(id) <= 64 && jobIDPattern.MatchString(id):
		return "/srv/build/logs/jobs/" + id + ".log", "receipt", true
	case schedulerJobPattern.MatchString(id):
		return "/srv/build/scheduler/" + id + ".log", "scheduler", true
	}
	return "", "", false
}

// jobLogScript prints the size line, the receipt line (receipts only), a
// marker, then the whole log or its head and tail around a gap marker. The id
// is validated before it reaches the shell.
func jobLogScript(id, path, kind string) string {
	receipt := ""
	if kind == "receipt" {
		receipt = fmt.Sprintf(`grep -F -- ' job=%s ' /srv/build/logs/jobs.log | tail -n 1; `, id)
	}
	return fmt.Sprintf(`f=%s; if [ ! -f "$f" ]; then echo __HZ_MISSING__; exit 0; fi; s=$(($(wc -c < "$f"))); echo "__HZ_SIZE__ $s"; %secho __HZ_LOG__; if [ "$s" -le %d ]; then head -c %d "$f"; else head -c %d "$f"; printf '%s'; tail -c %d "$f"; fi`,
		path, receipt, jobLogWhole, jobLogWhole, jobLogHead, jobLogGap, jobLogTail)
}

func JobLog(ctx context.Context, id string) (JobLogResult, error) {
	path, kind, ok := jobLogTarget(id)
	if !ok {
		return JobLogResult{}, errors.New("invalid gate-runner job id")
	}
	raw, e := runSSH(ctx, jobLogScript(id, path, kind), jobLogWhole+64<<10)
	if e != nil {
		return JobLogResult{}, errors.New("gate-runner job log unavailable")
	}
	return parseJobLog(id, kind, string(raw))
}

func parseJobLog(id, kind, raw string) (JobLogResult, error) {
	if strings.HasPrefix(raw, "__HZ_MISSING__") {
		return JobLogResult{}, errors.New("gate-runner job log not found (SETUP receipts and queued jobs have none)")
	}
	header, body, ok := strings.Cut(raw, "__HZ_LOG__\n")
	sizeLine, rest, _ := strings.Cut(header, "\n")
	size, err := strconv.ParseInt(strings.TrimPrefix(sizeLine, "__HZ_SIZE__ "), 10, 64)
	if !ok || !strings.HasPrefix(sizeLine, "__HZ_SIZE__ ") || err != nil || size < 0 {
		return JobLogResult{}, errors.New("gate-runner job log payload invalid")
	}
	result := JobLogResult{ID: id, Kind: kind, Bytes: size}
	if kind == "receipt" {
		if job, ok := ParseJob(strings.TrimSpace(rest)); ok && job.ID == id {
			result.Command = &job.Command
		}
	}
	if head, tail, split := strings.Cut(body, jobLogGap); split {
		// A running log grows between stat and tail, so the omitted count is
		// a lower bound from the size at stat time.
		result.OmittedBytes = max(0, size-int64(jobLogHead+jobLogTail))
		body = head + fmt.Sprintf("\n\n… %d bytes omitted …\n\n", result.OmittedBytes) + tail
	}
	result.Log = strings.ToValidUTF8(body, "\uFFFD")
	return result, nil
}

// boundedCapture fails a write past limit. It does not embed bytes.Buffer:
// io.Copy would use Buffer.ReadFrom and skip the limit entirely.
type boundedCapture struct {
	buf   bytes.Buffer
	limit int
}

func (b *boundedCapture) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buf.Len() {
		return 0, errors.New("gate-runner output exceeds bound")
	}
	return b.buf.Write(p)
}
func (b *boundedCapture) Bytes() []byte { return b.buf.Bytes() }
func (b *boundedCapture) Len() int      { return b.buf.Len() }

func runSSH(parent context.Context, remote string, limit int) ([]byte, error) {
	target := runnerTarget(parent)
	if target == "" {
		return nil, &SSHFailure{Kind: "no_target", Err: errors.New("no runner endpoint bound; configure one")}
	}
	ctx, cancel := context.WithTimeout(parent, 12*time.Second)
	defer cancel()
	cmd := processtree.CommandContext(ctx, "/usr/bin/ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "-o", "ServerAliveInterval=3", "-o", "ServerAliveCountMax=2", target, remote)
	out := &boundedCapture{limit: limit}
	stderr := &boundedCapture{limit: 4096}
	cmd.Stdout = out
	cmd.Stderr = stderr
	if e := cmd.Run(); e != nil {
		return nil, classifySSH(ctx, e)
	}
	return out.Bytes(), nil
}

// SSHFailure names why a runner read failed without exposing remote text.
// Kind is timeout, connect_failed (ssh exit 255: DNS, refused, auth, host
// key), remote_failed (the remote command exited non-zero), or
// output_too_large.
type SSHFailure struct {
	Kind string
	Err  error
}

func (e *SSHFailure) Error() string { return "ssh " + e.Kind + ": " + e.Err.Error() }
func (e *SSHFailure) Unwrap() error { return e.Err }

func classifySSH(ctx context.Context, err error) error {
	kind := "remote_failed"
	var exit *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		kind = "timeout"
	case strings.Contains(err.Error(), "exceeds bound"):
		kind = "output_too_large"
	case errors.As(err, &exit) && exit.ExitCode() == 255:
		kind = "connect_failed"
	case errors.As(err, &exit):
		kind = "remote_failed"
	default:
		kind = "connect_failed"
	}
	return &SSHFailure{Kind: kind, Err: err}
}

func sshFailureKind(err error) string {
	var f *SSHFailure
	if errors.As(err, &f) {
		return f.Kind
	}
	return "connect_failed"
}

type targetKey struct{}

// WithTarget binds every read in one inspection to its validated endpoint.
func WithTarget(ctx context.Context, host, user string) (context.Context, error) {
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*$`).MatchString(host) || !regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]*$`).MatchString(user) {
		return nil, errors.New("invalid runner inspection host or user")
	}
	return context.WithValue(ctx, targetKey{}, user+"@"+host), nil
}

func runnerTarget(ctx context.Context) string {
	if target, ok := ctx.Value(targetKey{}).(string); ok {
		return target
	}
	// No built-in endpoint: callers bind one with WithTarget from operator
	// configuration (remote-work.json or the fleet inventory).
	return ""
}
func ParseJob(line string) (Job, bool) {
	cut := strings.HasSuffix(line, receiptCutMarker)
	line = strings.TrimSuffix(line, receiptCutMarker)
	head, command, hasCommand := strings.Cut(line, " cmd=")
	if cut && !hasCommand {
		// The cut fell before cmd=; the fields after it are unknown.
		return Job{}, false
	}
	tokens := strings.Fields(head)
	if len(tokens) < 2 {
		return Job{}, false
	}
	fields := map[string]string{}
	for _, tok := range tokens[1:] {
		k, v, ok := strings.Cut(tok, "=")
		if ok {
			fields[k] = v
		}
	}
	id := fields["job"]
	if !jobIDPattern.MatchString(id) {
		return Job{}, false
	}
	value := func(k string) *string {
		v := fields[k]
		if v == "" || v == "-" {
			return nil
		}
		return &v
	}
	var dur *int
	if n, e := strconv.Atoi(strings.TrimSuffix(fields["dur"], "s")); e == nil && n >= 0 {
		dur = &n
	}
	exit := fields["exit"]
	if exit == "" {
		exit = "?"
	}
	return Job{ID: id, At: parseDate(tokens[0]), Caller: fields["caller"], Repo: fields["repo"], SHA: value("sha"), Cand: value("cand"), JobClass: value("class"), ExitCode: exit, DurationSeconds: dur, MacEst: value("mac_est"), Command: strings.ToValidUTF8(unquoteShell(command), ""), CommandTruncated: cut}, true
}

// unquoteShell decodes a receipt cmd= value, which the runner writes with
// bash printf %q: backslash escapes for plain words, '...' for literals and
// $'...' (ANSI-C quoting) once the command holds a newline or control byte.
// A receipt the runner cut can end inside a quote; the decoder then keeps
// what it has instead of failing.
func unquoteShell(raw string) string {
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); {
		switch {
		case strings.HasPrefix(raw[i:], "$'"):
			i += 2
			for i < len(raw) && raw[i] != '\'' {
				if raw[i] != '\\' || i+1 >= len(raw) {
					out = append(out, raw[i])
					i++
					continue
				}
				var decoded []byte
				decoded, i = ansiCEscape(raw, i+1)
				out = append(out, decoded...)
			}
			i++
		case raw[i] == '\'':
			j := strings.IndexByte(raw[i+1:], '\'')
			if j < 0 {
				return string(append(out, raw[i+1:]...))
			}
			out = append(out, raw[i+1:i+1+j]...)
			i += j + 2
		case raw[i] == '\\' && i+1 < len(raw):
			out = append(out, raw[i+1])
			i += 2
		default:
			out = append(out, raw[i])
			i++
		}
	}
	return string(out)
}

// ansiCEscape decodes one bash $'...' escape whose letter starts at raw[i]
// and returns the bytes and the index after the escape.
func ansiCEscape(raw string, i int) ([]byte, int) {
	simple := map[byte]byte{'a': 7, 'b': 8, 'e': 27, 'E': 27, 'f': 12, 'n': '\n', 'r': '\r', 't': '\t', 'v': 11, '\\': '\\', '\'': '\'', '"': '"', '?': '?'}
	c := raw[i]
	if b, ok := simple[c]; ok {
		return []byte{b}, i + 1
	}
	digits := func(start, max, base int) (int, int) {
		n, j := 0, start
		for j < len(raw) && j-start < max {
			d, err := strconv.ParseUint(raw[j:j+1], base, 8)
			if err != nil {
				break
			}
			n = n*base + int(d)
			j++
		}
		return n, j
	}
	switch {
	case c >= '0' && c <= '7':
		n, j := digits(i, 3, 8)
		return []byte{byte(n)}, j
	case c == 'x':
		if n, j := digits(i+1, 2, 16); j > i+1 {
			return []byte{byte(n)}, j
		}
	case c == 'u' || c == 'U':
		max := 4
		if c == 'U' {
			max = 8
		}
		if n, j := digits(i+1, max, 16); j > i+1 {
			return []byte(string(rune(n))), j
		}
	case c == 'c' && i+1 < len(raw):
		return []byte{raw[i+1] & 0x1f}, i + 2
	}
	return []byte{'\\', c}, i + 1
}
func ParseHealth(raw string) Health {
	h := Health{}
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if h.Load1 == nil && len(fields) >= 5 && strings.Contains(fields[3], "/") {
			a, ea := strconv.ParseFloat(fields[0], 64)
			b, eb := strconv.ParseFloat(fields[1], 64)
			c, ec := strconv.ParseFloat(fields[2], 64)
			if ea == nil && eb == nil && ec == nil && a >= 0 && b >= 0 && c >= 0 && !math.IsInf(a, 0) && !math.IsInf(b, 0) && !math.IsInf(c, 0) {
				h.Load1, h.Load5, h.Load15 = &a, &b, &c
			}
		} else if fields[0] == "Mem:" && len(fields) >= 7 {
			a, ea := strconv.ParseInt(fields[1], 10, 64)
			b, eb := strconv.ParseInt(fields[6], 10, 64)
			if ea == nil && eb == nil && a > 0 && b >= 0 && b <= a {
				h.MemTotalMB, h.MemAvailableMB = &a, &b
			}
		}
		if strings.HasPrefix(fields[0], "/dev/") && len(fields) >= 5 {
			if n, e := strconv.Atoi(strings.TrimSuffix(fields[4], "%")); e == nil && n >= 0 && n <= 100 {
				h.DiskUsedPercent = &n
			}
		}
		if strings.HasPrefix(line, "up ") {
			v := strings.TrimPrefix(line, "up ")
			h.Uptime = &v
		}
	}
	return h
}

// Nonblocking open prevents a FIFO replacement from wedging the CLI. Reject
// links and special files and verify identity after opening to close the race.
func openRegular(path string) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		f.Close()
		return nil, errors.New("file changed while opening")
	}
	return f, nil
}

// Rows arrive newest first. Keep that prefix within a wire-size budget so an
// unusual receipt cannot overflow the desktop client's bounded JSON reader.
func boundRows[T any](rows []T, budget int) ([]T, bool) {
	used := 2
	for i, row := range rows {
		raw, err := json.Marshal(row)
		if err != nil || used+len(raw)+1 > budget {
			return rows[:i], true
		}
		used += len(raw) + 1
	}
	return rows, false
}

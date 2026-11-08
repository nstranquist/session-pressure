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
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nstranquist/session-pressure/packages/processtree"
)

const maxFileBytes = 4 << 20
const maxRows = 1000

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
}
type Inspection struct {
	SchemaVersion    int       `json:"schema_version"`
	CapturedAt       time.Time `json:"captured_at"`
	Runs             []Run     `json:"runs"`
	Config           *Config   `json:"config"`
	LedgerSignature  string    `json:"ledger_signature"`
	LedgerUnchanged  bool      `json:"ledger_unchanged"`
	LedgerPresent    bool      `json:"ledger_present"`
	HistoryTruncated bool      `json:"history_truncated"`
	MalformedRows    int       `json:"malformed_rows"`
	LocalError       string    `json:"local_error,omitempty"`
	BoxRequested     bool      `json:"box_requested"`
	BoxReachable     bool      `json:"box_reachable"`
	BoxError         string    `json:"box_error,omitempty"`
	Jobs             []Job     `json:"jobs"`
	Health           *Health   `json:"health"`
}

// Inspect retains errors in its contract rather than inventing an empty,
// successful history. Clients can keep the prior successful value on error.
func Inspect(ctx context.Context, home string, includeBox bool) Inspection {
	return InspectSince(ctx, home, includeBox, "")
}

// A content signature avoids repeatedly decoding an unchanged history in the
// desktop. Configuration and remote health still refresh independently.
func InspectSince(ctx context.Context, home string, includeBox bool, known string) Inspection {
	s := Inspection{SchemaVersion: 1, CapturedAt: time.Now().UTC(), Runs: []Run{}, Jobs: []Job{}, BoxRequested: includeBox}
	dir := filepath.Join(home, ".nicos-dev", "commit-delivery", "v1")
	runs, present, truncated, malformed, signature, unchanged, err := readLedgerSince(filepath.Join(dir, "gate-delegate", "runs.jsonl"), known)
	s.LedgerPresent, s.HistoryTruncated, s.MalformedRows = present, truncated, malformed
	s.LedgerSignature, s.LedgerUnchanged = signature, unchanged
	if err != nil {
		s.LocalError = "delegate_ledger_read_failed"
	} else {
		s.Runs = runs
	}
	config, err := readConfig(filepath.Join(dir, "gate-delegate.json"))
	if err != nil {
		s.LocalError = strings.TrimSpace(s.LocalError + " delegate_config_read_failed")
	} else {
		s.Config = config
	}
	if includeBox {
		output, e := runSSH(ctx, "tail -n 1000 /srv/build/logs/jobs.log && printf '\n__HZ_HEALTH__\n' && cat /proc/loadavg && free -m && df -P / && uptime -p", 1<<20)
		if e != nil {
			s.BoxError = "gate_runner_read_failed"
		} else {
			halves := strings.SplitN(string(output), "\n__HZ_HEALTH__\n", 2)
			if len(halves) != 2 {
				s.BoxError = "gate_runner_payload_invalid"
			} else {
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
	s.Runs, truncated = boundRows(s.Runs, 512<<10)
	s.HistoryTruncated = s.HistoryTruncated || truncated
	s.Jobs, _ = boundRows(s.Jobs, 512<<10)
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

// Read a bounded suffix, discard only an incomplete first line, and keep the
// latest 1000 complete rows. Files and configurations must be regular files.
func readLedger(path string) ([]Run, bool, bool, int, error) {
	rows, present, truncated, malformed, _, _, err := readLedgerSince(path, "")
	return rows, present, truncated, malformed, err
}
func readLedgerSince(path, known string) ([]Run, bool, bool, int, string, bool, error) {
	f, e := openRegular(path)
	if os.IsNotExist(e) {
		return []Run{}, false, false, 0, "", false, nil
	}
	if e != nil {
		return nil, false, false, 0, "", false, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() {
		return nil, false, false, 0, "", false, errors.New("ledger must be regular")
	}
	truncated := st.Size() > maxFileBytes
	if truncated {
		if _, e = f.Seek(-maxFileBytes, io.SeekEnd); e != nil {
			return nil, true, true, 0, "", false, e
		}
	}
	raw, e := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if e != nil || len(raw) > maxFileBytes {
		return nil, true, truncated, 0, "", false, errors.New("ledger exceeds read bound")
	}
	if truncated {
		_, raw, _ = bytes.Cut(raw, []byte("\n"))
	}
	// A writer may still be appending its last line. Do not count that prefix as
	// a malformed committed receipt or cache it as a complete record.
	if end := bytes.LastIndexByte(raw, '\n'); end >= 0 {
		raw = raw[:end]
	} else {
		raw = nil
	}
	digest := sha256.Sum256(raw)
	signature := hex.EncodeToString(digest[:])
	if known != "" && known == signature {
		return []Run{}, true, truncated, 0, signature, true, nil
	}
	lines := bytes.Split(raw, []byte("\n"))
	if len(lines) > maxRows {
		lines = lines[len(lines)-maxRows:]
		truncated = true
	}
	runs := []Run{}
	malformed := 0
	occurrences := map[string]int{}
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
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
			malformed++
			continue
		}
		if row.Command == "" {
			row.Command = strings.Join(row.Verify.Argv, " ")
		}
		name := row.BoxRepository
		if name == "" {
			name = filepath.Base(row.Repository)
		}
		baseID := row.DeliveryID + "@" + row.At
		occurrence := occurrences[baseID]
		occurrences[baseID]++
		runs = append(runs, Run{ID: fmt.Sprintf("%s@%d", baseID, occurrence), At: parseDate(row.At), Mode: row.Mode, Decision: row.Decision, DeliveryID: row.DeliveryID, RepoName: name, RepoPath: row.Repository, Command: row.Command, LocalPassed: row.Local.Passed, LocalMS: row.Local.MS, LocalDetail: row.Local.Detail, LocalEnvironment: row.Local.Environment, Agreement: row.Agreement, RemoteRan: row.Remote.Ran, RemotePassed: row.Remote.Passed, RemoteExit: row.Remote.Exit, RemoteRunMS: row.Remote.RunMS, RemoteWallMS: row.Remote.WallMS, RemoteStage: row.Remote.Stage, TreeVerified: row.Remote.TreeVerified, TreeHash: row.Tree, PolicyDigest: row.PolicyDigest, RemotePassAllowed: row.RemotePassAllowed})
	}
	return runs, true, truncated, malformed, signature, false, nil
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

func JobLog(ctx context.Context, id string) (string, error) {
	if !jobIDPattern.MatchString(id) || len(id) > 64 {
		return "", errors.New("invalid gate-runner job id")
	}
	raw, e := runSSH(ctx, "head -c 100000 /srv/build/logs/jobs/"+id+".log", 100000)
	if e != nil {
		return "", errors.New("gate-runner job log unavailable")
	}
	return string(raw), nil
}

type boundedCapture struct {
	bytes.Buffer
	limit int
}

func (b *boundedCapture) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("gate-runner output exceeds bound")
	}
	return b.Buffer.Write(p)
}
func runSSH(parent context.Context, remote string, limit int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, 12*time.Second)
	defer cancel()
	cmd := processtree.CommandContext(ctx, "/usr/bin/ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "-o", "ServerAliveInterval=3", "-o", "ServerAliveCountMax=2", "builder@100.106.202.89", remote)
	out := &boundedCapture{limit: limit}
	stderr := &boundedCapture{limit: 4096}
	cmd.Stdout = out
	cmd.Stderr = stderr
	if e := cmd.Run(); e != nil {
		return nil, e
	}
	return out.Bytes(), nil
}
func ParseJob(line string) (Job, bool) {
	head, command, _ := strings.Cut(line, " cmd=")
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
	return Job{ID: id, At: parseDate(tokens[0]), Caller: fields["caller"], Repo: fields["repo"], SHA: value("sha"), Cand: value("cand"), JobClass: value("class"), ExitCode: exit, DurationSeconds: dur, MacEst: value("mac_est"), Command: unescape(command)}, true
}
func unescape(raw string) string {
	var out strings.Builder
	escaped := false
	for _, r := range raw {
		if escaped {
			out.WriteRune(r)
			escaped = false
		} else if r == '\\' {
			escaped = true
		} else {
			out.WriteRune(r)
		}
	}
	if escaped {
		out.WriteRune('\\')
	}
	return out.String()
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

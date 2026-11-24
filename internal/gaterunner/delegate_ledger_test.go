package gaterunner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const receipt = `{"at":"2026-11-08T20:00:00.123Z","mode":"on","decision":"remote_pass","delivery_id":"delivery-1","repository":"/work/sample","box_repository":"sample","verify":{"argv":["go","test","./..."]},"local":{},"remote":{"ran":true,"passed":true,"exit":0,"run_ms":81,"wall_ms":120,"tree_verified":true},"remote_pass_allowed":true}`

// sideStreams writes a store with the delivery lane's stream_rows shape. The
// writer is the sqlite3 CLI, so the store is a real WAL-mode database.
func sideStreams(t *testing.T, path string, bodies ...string) {
	t.Helper()
	var sql strings.Builder
	sql.WriteString("PRAGMA journal_mode=WAL; CREATE TABLE IF NOT EXISTS stream_rows(seq INTEGER PRIMARY KEY, stream TEXT NOT NULL, at TEXT NOT NULL, delivery_id TEXT NOT NULL DEFAULT '', body TEXT NOT NULL, origin TEXT NOT NULL);")
	sql.WriteString("BEGIN;")
	for _, body := range bodies {
		fmt.Fprintf(&sql, "INSERT INTO stream_rows(stream, at, body, origin) VALUES('%s', 'x', '%s', 'test');", delegateRunsStream, strings.ReplaceAll(body, "'", "''"))
	}
	// Another stream in the same store must not leak into the runs.
	sql.WriteString("INSERT INTO stream_rows(stream, at, body, origin) VALUES('gate-delegate.probes', 'x', '{}', 'test'); COMMIT;")
	// The script goes on stdin: Linux caps one argv string at 128 KiB
	// (MAX_ARG_STRLEN), and 1,000 rows exceed it.
	cmd := exec.Command("/usr/bin/sqlite3", path)
	cmd.Stdin = strings.NewReader(sql.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sqlite3: %v %s", err, out)
	}
}

func TestDelegateRunsLatestWindowNewestFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), sideStreamsName)
	bodies := make([]string, 0, maxRows+4)
	for range maxRows + 3 {
		bodies = append(bodies, receipt)
	}
	newest := strings.Replace(receipt, `"delivery-1"`, `"delivery-newest"`, 1)
	bodies = append(bodies, "bad committed row", newest)
	sideStreams(t, path, bodies...)
	rows, present, truncated, malformed, signature, unchanged, err := readDelegateRuns(context.Background(), path, "")
	if err != nil || !present || !truncated || unchanged || malformed != 1 || len(rows) != maxRows-1 || len(signature) != 64 {
		t.Fatalf("rows=%d present=%v truncated=%v malformed=%d unchanged=%v err=%v", len(rows), present, truncated, malformed, unchanged, err)
	}
	if rows[0].DeliveryID != "delivery-newest" {
		t.Fatalf("newest row not first: %+v", rows[0])
	}
	if rows[1].Command != "go test ./..." || rows[1].RemoteRunMS == nil || *rows[1].RemoteRunMS != 81 || rows[1].LocalPassed != nil {
		t.Fatalf("lost recorded fields: %+v", rows[1])
	}
	if rows[1].ID == rows[2].ID {
		t.Fatal("duplicate receipt IDs must remain distinct")
	}
}

func TestDelegateRunsSignatureSkipsUnchangedLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), sideStreamsName)
	sideStreams(t, path, receipt)
	ctx := context.Background()
	rows, _, _, _, signature, unchanged, err := readDelegateRuns(ctx, path, "")
	if err != nil || unchanged || len(rows) != 1 {
		t.Fatalf("first read rows=%d unchanged=%v err=%v", len(rows), unchanged, err)
	}
	rows, _, _, _, _, unchanged, err = readDelegateRuns(ctx, path, signature)
	if err != nil || !unchanged || len(rows) != 0 {
		t.Fatal("unchanged ledger decoded again")
	}
	sideStreams(t, path, receipt)
	rows, _, _, _, _, unchanged, err = readDelegateRuns(ctx, path, signature)
	if err != nil || unchanged || len(rows) != 2 {
		t.Fatalf("appended run skipped: rows=%d unchanged=%v err=%v", len(rows), unchanged, err)
	}
}

// A WAL store whose writer closed and removed -wal and -shm cannot be opened
// read-only (SQLITE_CANTOPEN). This is the live state of side-streams.sqlite
// between delivery-lane writes, and it left the delegate cards empty.
func TestDelegateRunsReadClosedWALStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), sideStreamsName)
	sideStreams(t, path, receipt)
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	if err := exec.Command("/usr/bin/sqlite3", "-readonly", path, "SELECT 1").Run(); err == nil {
		t.Skip("this sqlite3 opens a closed WAL store read-only; the fallback is not exercised")
	}
	rows, present, _, _, _, _, err := readDelegateRuns(context.Background(), path, "")
	if err != nil || !present || len(rows) != 1 {
		t.Fatalf("closed WAL store unreadable: rows=%d err=%v", len(rows), err)
	}
}

func TestDelegateRunsMissingSpecialAndFailed(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	s := Inspect(ctx, home, false)
	if s.LocalError != "" || s.LedgerPresent || s.BoxRequested || s.BoxReachable || len(s.Runs) != 0 {
		t.Fatalf("unexpected missing state: %+v", s)
	}
	dir := filepath.Join(home, ".nicos-dev", "commit-delivery", "v1")
	if err := os.MkdirAll(filepath.Join(dir, sideStreamsName), 0700); err != nil {
		t.Fatal(err)
	}
	if s = Inspect(ctx, home, false); s.LocalError == "" {
		t.Fatal("read failure represented as empty success")
	}
	other := t.TempDir()
	target := filepath.Join(other, "real.sqlite")
	sideStreams(t, target, receipt)
	link := filepath.Join(other, "link.sqlite")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, _, _, err := readDelegateRuns(ctx, link, ""); err == nil {
		t.Fatal("symlink accepted")
	}
	fifo := filepath.Join(other, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, _, _, _, _, _, e := readDelegateRuns(ctx, fifo, ""); done <- e }()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("FIFO accepted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked reading FIFO")
	}
}

func TestDelegateRunCommandPrefersArgv(t *testing.T) {
	raw := strings.Replace(receipt, `"verify"`, `"command":"go run","verify"`, 1)
	run, ok := parseDelegateRun([]byte(raw), map[string]int{})
	if !ok || run.Command != "go test ./..." {
		t.Fatalf("argv lost to the label: %q", run.Command)
	}
	label := strings.Replace(receipt, `"verify":{"argv":["go","test","./..."]}`, `"command":"npm run"`, 1)
	if run, ok = parseDelegateRun([]byte(label), map[string]int{}); !ok || run.Command != "npm run" {
		t.Fatalf("label without argv lost: %q", run.Command)
	}
}

func TestAgreementUsesWorkerAuthority(t *testing.T) {
	raw := strings.Replace(receipt, `"local":{}`, `"local":{"passed":false,"environment":"killed"}`, 1)
	run, ok := parseDelegateRun([]byte(raw), map[string]int{})
	if !ok || run.Agreement != nil || run.LocalEnvironment == nil {
		t.Fatalf("incomparable outcome lost: %+v", run)
	}
}

// The limit must hold for io.Copy, which is how os/exec fills Stdout. An
// embedded bytes.Buffer let io.Copy call ReadFrom and skip Write entirely.
func TestBoundedCaptureHoldsThroughCopy(t *testing.T) {
	b := &boundedCapture{limit: 3}
	if _, err := b.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write([]byte("d")); err == nil || b.Len() != 3 {
		t.Fatal("output limit ignored on Write")
	}
	c := &boundedCapture{limit: 1024}
	if _, err := io.Copy(c, bytes.NewReader(make([]byte, 4096))); err == nil {
		t.Fatal("output limit ignored through io.Copy")
	}
	if c.Len() > 1024 {
		t.Fatalf("overflow retained: %d", c.Len())
	}
}

// Run the runner's receipt cut with this host's awk on a synthetic jobs.log:
// long lines are cut to receiptLineBytes and marked, short ones pass as is.
func TestReceiptsScriptCutsLongLines(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "jobs.log")
	short := `2026-11-08T21:19:55Z job=20261108T211634Z-1 caller=c repo=r sha=- cand=- class=test exit=0 dur=2s mac_est=4 cmd=go\ test\ ./...`
	long := `2026-11-08T21:19:56Z job=20261108T211634Z-2 caller=c repo=r sha=- cand=- class=test exit=1 dur=9s mac_est=4 cmd=` + strings.Repeat(`x\ `, 4000)
	if err := os.WriteFile(log, []byte(short+"\n"+long+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	pipeline, _, ok := strings.Cut(receiptsScript, " && printf")
	if !ok {
		t.Fatal("receipt script shape changed")
	}
	out, err := exec.Command("/bin/sh", "-c", strings.Replace(pipeline, "/srv/build/logs/jobs.log", log, 1)).Output()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != 2 || lines[0] != short {
		t.Fatalf("short line changed: %q", lines)
	}
	if len(lines[1]) != receiptLineBytes+len(receiptCutMarker) || !strings.HasSuffix(lines[1], receiptCutMarker) {
		t.Fatalf("long line not cut: %d bytes", len(lines[1]))
	}
	job, ok := ParseJob(lines[1])
	if !ok || !job.CommandTruncated || job.ExitCode != "1" || !strings.HasPrefix(job.Command, "x x x") {
		t.Fatalf("cut receipt not parsed: %+v", job)
	}
	if job, ok = ParseJob(lines[0]); !ok || job.CommandTruncated {
		t.Fatalf("whole receipt marked cut: %+v", job)
	}
}

func TestJobLogTargetsAndPayloads(t *testing.T) {
	if path, kind, ok := jobLogTarget("c0a40dd5-8fdb-4564-b7cf-cc8961031d1b"); !ok || kind != "scheduler" || path != "/srv/build/scheduler/c0a40dd5-8fdb-4564-b7cf-cc8961031d1b.log" {
		t.Fatalf("scheduler id: %q %q %v", path, kind, ok)
	}
	if path, kind, ok := jobLogTarget("20261124T183658Z-3632943"); !ok || kind != "receipt" || path != "/srv/build/logs/jobs/20261124T183658Z-3632943.log" {
		t.Fatalf("receipt id: %q %q %v", path, kind, ok)
	}

	if _, err := parseJobLog("x", "scheduler", "__HZ_MISSING__\n"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing log: %v", err)
	}
	if _, err := parseJobLog("x", "scheduler", "garbage"); err == nil {
		t.Fatal("invalid payload accepted")
	}

	whole, err := parseJobLog("x", "scheduler", "__HZ_SIZE__ 6\n__HZ_LOG__\nok\nok\n")
	if err != nil || whole.Log != "ok\nok\n" || whole.Bytes != 6 || whole.OmittedBytes != 0 || whole.Command != nil {
		t.Fatalf("whole log: %+v err=%v", whole, err)
	}

	id := "20261108T211634Z-2"
	line := `2026-11-08T21:19:56Z job=` + id + ` caller=c repo=r sha=- cand=- class=test exit=1 dur=9s mac_est=4 cmd=go\ test\ ./a\ ./b`
	split, err := parseJobLog(id, "receipt", "__HZ_SIZE__ 300000\n"+line+"\n__HZ_LOG__\nHEAD"+jobLogGap+"TAIL")
	if err != nil || split.Command == nil || *split.Command != "go test ./a ./b" {
		t.Fatalf("receipt command lost: %+v err=%v", split, err)
	}
	if split.OmittedBytes != 300000-jobLogHead-jobLogTail || !strings.HasPrefix(split.Log, "HEAD\n\n… ") || !strings.HasSuffix(split.Log, "…\n\nTAIL") {
		t.Fatalf("head and tail not joined: %+v", split)
	}
}

// jobLogScript runs as a real shell script against local files: whole,
// head-and-tail and missing.
func TestJobLogScriptShapes(t *testing.T) {
	dir := t.TempDir()
	small := filepath.Join(dir, "small.log")
	if err := os.WriteFile(small, []byte("line\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("/bin/sh", "-c", jobLogScript("x", small, "scheduler")).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := parseJobLog("x", "scheduler", string(out)); err != nil || got.Log != "line\n" {
		t.Fatalf("small log: %+v err=%v", got, err)
	}
	big := filepath.Join(dir, "big.log")
	if err := os.WriteFile(big, bytes.Repeat([]byte("y"), jobLogWhole+1000), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = exec.Command("/bin/sh", "-c", jobLogScript("x", big, "scheduler")).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := parseJobLog("x", "scheduler", string(out)); err != nil || got.OmittedBytes != int64(jobLogWhole+1000-jobLogHead-jobLogTail) {
		t.Fatalf("big log: omitted=%d err=%v", got.OmittedBytes, err)
	}
}

// The runner writes cmd= with bash printf %q, so the decoder is checked
// against real bash output, not hand-written fixtures.
func TestUnquoteShellRoundTripsBashPrintfQ(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not found")
	}
	cases := []string{
		"go test ./...",
		"printf '%s\\n' X >&2\nset -eu\nexport JOB=\"$PWD\"\ncd \"$JOB\"",
		"python3 - <<'PY'\nimport os\tprint(\"é ✓\")\nPY",
		"a\\b 'c' $HOME `x` \x1b[0m \x01",
		"",
	}
	for _, want := range cases {
		out, err := exec.Command(bash, "-c", `printf %q "$1"`, "_", want).Output()
		if err != nil {
			t.Fatal(err)
		}
		if got := unquoteShell(string(out)); got != want {
			t.Fatalf("quoted %q\n got %q\nwant %q", out, got, want)
		}
	}
	// A receipt cut inside $'...' keeps the decoded prefix.
	if got := unquoteShell(`$'set -eu\nexport JOB=\"$PW`); got != "set -eu\nexport JOB=\"$PW" {
		t.Fatalf("cut decode = %q", got)
	}
}

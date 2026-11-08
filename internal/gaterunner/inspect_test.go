package gaterunner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const receipt = `{"at":"2026-11-08T20:00:00.123Z","mode":"on","decision":"remote_pass","delivery_id":"delivery-1","repository":"/work/sample","box_repository":"sample","verify":{"argv":["go","test","./..."]},"local":{},"remote":{"ran":true,"passed":true,"exit":0,"run_ms":81,"wall_ms":120,"tree_verified":true},"remote_pass_allowed":true}`

func TestLedgerCommitBoundaryAndLatestWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	raw := strings.Repeat(receipt+"\n", maxRows+3) + "bad committed row\n" + receipt
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	rows, present, truncated, malformed, err := readLedger(path)
	if err != nil || !present || !truncated || malformed != 1 || len(rows) != maxRows-1 {
		t.Fatalf("rows=%d present=%v truncated=%v malformed=%d err=%v", len(rows), present, truncated, malformed, err)
	}
	if rows[0].Command != "go test ./..." || rows[0].RemoteRunMS == nil || *rows[0].RemoteRunMS != 81 || rows[0].LocalPassed != nil {
		t.Fatalf("lost recorded fields: %+v", rows[0])
	}
	if rows[0].ID == rows[1].ID {
		t.Fatal("duplicate receipt IDs must remain distinct")
	}
}

func TestLedgerSuffixBoundAndSpecialFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runs.jsonl")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxFileBytes)+"\n"+receipt+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rows, _, truncated, _, err := readLedger(path)
	if err != nil || !truncated || len(rows) != 1 {
		t.Fatalf("rows=%d truncated=%v err=%v", len(rows), truncated, err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err = readLedger(link); err == nil {
		t.Fatal("symlink accepted")
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, _, _, _, e := readLedger(fifo); done <- e }()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("FIFO accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked reading FIFO")
	}
}

func TestInspectionDistinguishesMissingFromFailed(t *testing.T) {
	home := t.TempDir()
	s := Inspect(context.Background(), home, false)
	if s.LocalError != "" || s.LedgerPresent || s.BoxRequested || s.BoxReachable || len(s.Runs) != 0 {
		t.Fatalf("unexpected missing state: %+v", s)
	}
	dir := filepath.Join(home, ".nicos-dev", "commit-delivery", "v1", "gate-delegate")
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(filepath.Join(dir, "runs.jsonl"), 0700); e != nil {
		t.Fatal(e)
	}
	s = Inspect(context.Background(), home, false)
	if s.LocalError == "" {
		t.Fatal("read failure represented as empty success")
	}
}

func TestResponseBudgetAndConfigOrder(t *testing.T) {
	rows, truncated := boundRows([]string{strings.Repeat("a", 100), strings.Repeat("b", 100)}, 150)
	if !truncated || len(rows) != 1 {
		t.Fatal("wire budget ignored")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if e := os.WriteFile(path, []byte(`{"mode":"shadow","repositories":{"/z":"z","/a":"a"},"allow":[{}]}`), 0600); e != nil {
		t.Fatal(e)
	}
	c, e := readConfig(path)
	if e != nil || c.AllowCount != 1 || len(c.Repos) != 2 || c.Repos[0].Path != "/a" {
		t.Fatalf("config=%+v err=%v", c, e)
	}
}

func TestJobReceiptFormatsAndLogValidation(t *testing.T) {
	row, ok := ParseJob(`2026-11-08T21:19:55Z job=20261108T211634Z-1533753 caller=pressure-env-go-review-v3 repo=nicos-tools sha=- cand=- class=test exit=0 dur=21s mac_est=4 cmd=go\ test\ ./...`)
	if !ok || row.Command != "go test ./..." || row.SHA != nil || row.DurationSeconds == nil || *row.DurationSeconds != 21 {
		t.Fatalf("bad v2 receipt: %+v", row)
	}
	if _, ok = ParseJob("nonsense"); ok {
		t.Fatal("malformed job accepted")
	}
	if _, err := JobLog(context.Background(), "20261108T211634Z-1;touch /tmp/x"); err == nil {
		t.Fatal("unsafe job ID accepted")
	}
}

func TestHealthRejectsInvalidNumbers(t *testing.T) {
	valid := "0.01 0.02 0.03 1/100 200\nMem: 32000 1000 1000 1 30000 31000\n/dev/sda1 100 30 70 30% /\nup 3 days\n"
	h := ParseHealth(valid)
	if h.Load1 == nil || h.MemAvailableMB == nil || *h.MemAvailableMB != 31000 || h.DiskUsedPercent == nil || *h.DiskUsedPercent != 30 {
		t.Fatalf("health=%+v", h)
	}
	for _, bad := range []string{"Inf 0 0 1/1 1", "NaN 0 0 1/1 1", "-1 0 0 1/1 1"} {
		h = ParseHealth(bad)
		if h.Load1 != nil {
			t.Fatalf("invalid load accepted: %q", bad)
		}
		if _, e := json.Marshal(h); e != nil {
			t.Fatal(e)
		}
	}
}

func TestBoundedCapture(t *testing.T) {
	b := boundedCapture{limit: 3}
	if _, e := b.Write([]byte("abc")); e != nil {
		t.Fatal(e)
	}
	if _, e := b.Write([]byte("d")); e == nil {
		t.Fatal("output limit ignored")
	}
	if b.Len() != 3 {
		t.Fatal("overflow retained")
	}
}

func TestLedgerSignatureSkipsOnlyUnchangedCommittedHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	if e := os.WriteFile(path, []byte(receipt+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	rows, _, _, _, signature, unchanged, e := readLedgerSince(path, "")
	if e != nil || unchanged || len(rows) != 1 || len(signature) != 64 {
		t.Fatalf("first read rows=%d signature=%q unchanged=%v err=%v", len(rows), signature, unchanged, e)
	}
	rows, _, _, _, _, unchanged, e = readLedgerSince(path, signature)
	if e != nil || !unchanged || len(rows) != 0 {
		t.Fatal("unchanged history decoded again")
	}
	if e = os.WriteFile(path, []byte(receipt+"\npartial"), 0600); e != nil {
		t.Fatal(e)
	}
	_, _, _, _, _, unchanged, e = readLedgerSince(path, signature)
	if e != nil || !unchanged {
		t.Fatal("uncommitted append invalidated the history")
	}
	if e = os.WriteFile(path, []byte(receipt+"\n"+receipt+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	rows, _, _, _, _, unchanged, e = readLedgerSince(path, signature)
	if e != nil || unchanged || len(rows) != 2 {
		t.Fatal("committed append was skipped")
	}
}

func TestAgreementUsesWorkerAuthority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	raw := strings.Replace(receipt, `"local":{}`, `"local":{"passed":false,"environment":"killed"}`, 1)
	if e := os.WriteFile(path, []byte(raw+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	rows, _, _, _, e := readLedger(path)
	if e != nil || len(rows) != 1 || rows[0].Agreement != nil || rows[0].LocalEnvironment == nil {
		t.Fatalf("incomparable outcome lost: %+v err=%v", rows, e)
	}
}

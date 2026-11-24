package gaterunner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	for _, bad := range []string{"20261108T211634Z-1;touch /tmp/x", "../x", "C0A40DD5-8FDB-4564-B7CF-CC8961031D1B", "c0a40dd5-8fdb-4564-b7cf-cc8961031d1b;id"} {
		if _, err := JobLog(context.Background(), bad); err == nil {
			t.Fatalf("unsafe job ID accepted: %q", bad)
		}
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

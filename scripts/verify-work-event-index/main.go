// Verify the installed CLI against a frozen private ledger, including its scan
// fallback. No live ledger, policy, lease or monitor is modified.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

type check struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	IndexMS int64  `json:"index_ms"`
	ScanMS  int64  `json:"scan_ms"`
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func normalize(v any) {
	switch x := v.(type) {
	case map[string]any:
		delete(x, "since")
		delete(x, "generated_at")
		for _, y := range x {
			normalize(y)
		}
	case []any:
		for _, y := range x {
			normalize(y)
		}
	}
}

// Each CLI invocation has its own wall clock. These three derived rates use
// that observation time, rather than just the frozen ledger timestamps.
// Counts, event rows and every other field must still compare exactly.
func equivalent(a, b any, path string) bool {
	if x, ok := a.(map[string]any); ok {
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			if !equivalent(v, y[k], path+"/"+k) {
				return false
			}
		}
		return true
	}
	if x, ok := a.([]any); ok {
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i, v := range x {
			if !equivalent(v, y[i], path+fmt.Sprintf("/%d", i)) {
				return false
			}
		}
		return true
	}
	switch path {
	case "/calibration/interrupt_projection/window_hours", "/calibration/interrupt_projection/wrapper_interrupt_rate_per_hour", "/work_stats/swap_derate/wait_minutes_per_day":
		x, xok := a.(float64)
		y, yok := b.(float64)
		return xok && yok && math.Abs(x-y) <= 1e-5*math.Max(1, math.Max(math.Abs(x), math.Abs(y)))
	}
	return reflect.DeepEqual(a, b)
}

func invoke(bin, home string, args ...string) (any, int64, error) {
	c := exec.Command(bin, append([]string{"--json", "work"}, args...)...)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "NDEV_SESSION_PRESSURE_HOME=") && !strings.HasPrefix(e, "SESSION_PRESSURE_HOME=") {
			c.Env = append(c.Env, e)
		}
	}
	c.Env = append(c.Env, "NDEV_SESSION_PRESSURE_HOME="+home)
	start := time.Now()
	var stderr bytes.Buffer
	c.Stderr = &stderr
	b, err := c.Output()
	ms := time.Since(start).Milliseconds()
	if err != nil {
		return nil, ms, fmt.Errorf("installed command failed: %w: %s %s", err, stderr.String(), b)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, ms, err
	}
	normalize(v)
	return v, ms, nil
}

func main() {
	bin := flag.String("binary", "", "absolute installed CLI")
	ledger := flag.String("ledger", "", "live ledger directory (read only)")
	out := flag.String("out", "", "new private evidence directory")
	flag.Parse()
	if !filepath.IsAbs(*bin) || !filepath.IsAbs(*ledger) || !filepath.IsAbs(*out) {
		panic("absolute binary, ledger and out paths required")
	}
	must(os.Mkdir(*out, 0700))
	indexed, scanned := filepath.Join(*out, "indexed"), filepath.Join(*out, "scan")
	must(os.Mkdir(indexed, 0700))
	must(os.Mkdir(scanned, 0700))
	// A directory at the database path deterministically makes SQLite fail to
	// open; the same installed executable then uses its existing scan fallback.
	must(os.Mkdir(filepath.Join(scanned, "work-events.sqlite"), 0700))
	paths, err := filepath.Glob(filepath.Join(*ledger, "work-events-*.jsonl"))
	must(err)
	sort.Strings(paths)
	if len(paths) < 2 {
		panic("need at least two ledger shards")
	}
	hash := sha256.New()
	bodies := map[string][]byte{}
	var operation string
	var lines int
	for _, p := range paths {
		b, err := os.ReadFile(p)
		must(err)
		// Freeze complete records only, so a concurrent append cannot introduce
		// an unfinished final line into this isolated snapshot.
		if n := bytes.LastIndexByte(b, '\n'); n >= 0 {
			b = b[:n+1]
		} else {
			b = nil
		}
		name := filepath.Base(p)
		bodies[name] = b
		hash.Write([]byte(name))
		hash.Write([]byte{0})
		hash.Write(b)
		lines += bytes.Count(b, []byte{'\n'})
		for _, home := range []string{indexed, scanned} {
			must(os.WriteFile(filepath.Join(home, name), b, 0600))
		}
		if operation == "" {
			for _, line := range bytes.Split(b, []byte{'\n'}) {
				var row map[string]any
				if json.Unmarshal(line, &row) == nil {
					if id, ok := row["operation_id"].(string); ok && id != "" {
						operation = id
						break
					}
				}
			}
		}
	}
	var checks []check
	parity := func(name string, args ...string) {
		a, ams, err := invoke(*bin, indexed, args...)
		must(err)
		b, bms, err := invoke(*bin, scanned, args...)
		must(err)
		if !equivalent(a, b, "") {
			panic("index/scan mismatch: " + name)
		}
		checks = append(checks, check{name, true, ams, bms})
	}
	parity("cold full stats", "stats", "--since", "336h", "--full")
	parity("history", "history", "--since", "336h", "--limit", "100", "--full")
	parity("event filter", "history", "--since", "336h", "--event", "completed", "--full")
	parity("class filter", "history", "--since", "336h", "--class", "test", "--full")
	parity("operation filter", "history", "--since", "336h", "--operation-id", operation, "--full")
	latest := filepath.Base(paths[len(paths)-1])
	for _, home := range []string{indexed, scanned} {
		must(os.WriteFile(filepath.Join(home, latest), nil, 0600))
	}
	parity("shrunk shard", "stats", "--since", "336h", "--full")
	for _, home := range []string{indexed, scanned} {
		must(os.WriteFile(filepath.Join(home, latest), bodies[latest], 0600))
	}
	parity("old writer appended records", "stats", "--since", "336h", "--full")
	oldest := filepath.Base(paths[0])
	for _, home := range []string{indexed, scanned} {
		must(os.Remove(filepath.Join(home, oldest)))
	}
	parity("pruned shard", "stats", "--since", "336h", "--full")
	for _, home := range []string{indexed, scanned} {
		must(os.WriteFile(filepath.Join(home, oldest), bodies[oldest], 0600))
	}
	for i := 0; i < 3; i++ {
		parity(fmt.Sprintf("warm stats %d", i+1), "stats", "--since", "24h", "--full")
		parity(fmt.Sprintf("warm operation %d", i+1), "history", "--since", "336h", "--operation-id", operation, "--full")
	}
	// The database is disposable; rebuilding from the same log preserves output.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		err := os.Remove(filepath.Join(indexed, "work-events.sqlite"+suffix))
		if !os.IsNotExist(err) {
			must(err)
		}
	}
	parity("rebuilt index", "stats", "--since", "336h", "--full")
	// A malformed ledger must fail closed through both paths, rather than
	// silently becoming an empty workload. Only the private snapshot is changed.
	for _, home := range []string{indexed, scanned} {
		f, err := os.OpenFile(filepath.Join(home, latest), os.O_APPEND|os.O_WRONLY, 0600)
		must(err)
		_, err = f.WriteString("{invalid-ledger-record}\n")
		must(err)
		must(f.Close())
		_, _, err = invoke(*bin, home, "stats", "--since", "336h", "--full")
		if err == nil {
			panic("malformed ledger did not fail closed")
		}
	}
	checks = append(checks, check{Name: "malformed ledger fails closed through index and scan", Passed: true})
	stat, err := os.Stat(filepath.Join(indexed, "work-events.sqlite"))
	must(err)
	if stat.Mode().Perm() != 0600 {
		panic("index permissions are not owner only")
	}
	report := map[string]any{"schema": "session-pressure.installed-work-index-acceptance.v1", "captured_at": time.Now().UTC(), "binary": *bin, "frozen_shards": len(paths), "frozen_records": lines, "ledger_digest": "sha256:" + hex.EncodeToString(hash.Sum(nil)), "checks": checks, "ok": true, "live_ledger_modified": false, "normalization": []string{"since", "generated_at"}, "clock_tolerance": "1e-5 relative/absolute for only interrupt_projection.window_hours, interrupt_projection.wrapper_interrupt_rate_per_hour and swap_derate.wait_minutes_per_day; all other fields exact", "timing_scope": "same installed binary, frozen ledger; scan forced by unavailable index; not an old-version comparison"}
	b, err := json.MarshalIndent(report, "", "  ")
	must(err)
	must(os.WriteFile(filepath.Join(*out, "acceptance.json"), append(b, '\n'), 0600))
	fmt.Printf("PASS %d installed checks; %d frozen records across %d shards\n", len(checks), lines, len(paths))
}

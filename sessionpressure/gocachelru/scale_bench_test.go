package gocachelru

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

// Scale benchmarks for the shared runner cache (runner-3, 2026-11-15):
// a 27 GB cache whose journal held 1,123,094 rows for 861,885 live keys, with
// 8-10 Go processes starting a helper each. They measure what a Go process
// waits for: time until the helper can answer, and get latency while several
// helpers serve at once. GOCACHE_LRU_SCALE_ROWS overrides the journal size.

func scaleRows(b *testing.B) int {
	rows := 860_000
	if raw := os.Getenv("GOCACHE_LRU_SCALE_ROWS"); raw != "" {
		if _, err := fmt.Sscan(raw, &rows); err != nil || rows < 1 {
			b.Fatalf("GOCACHE_LRU_SCALE_ROWS=%q", raw)
		}
	}
	return rows
}

// scaleCache writes a journal of rows live entries and hits real action and
// object files for the first hits keys, so gets succeed.
func scaleCache(b *testing.B, rows, hits int) (dir string, actions [][]byte) {
	b.Helper()
	dir = b.TempDir()
	for _, part := range []string{"objects", "actions", "clients"} {
		if err := os.MkdirAll(filepath.Join(dir, part), 0o700); err != nil {
			b.Fatal(err)
		}
	}
	file, err := os.Create(filepath.Join(dir, "index.jsonl"))
	if err != nil {
		b.Fatal(err)
	}
	w := bufio.NewWriterSize(file, 1<<20)
	enc := json.NewEncoder(w)
	now := time.Now().UnixNano()
	for i := 0; i < rows; i++ {
		sum := sha256.Sum256([]byte(fmt.Sprint("object-", i)))
		key := hex.EncodeToString(sum[:])
		if err := enc.Encode(indexEntry{Key: key, Size: 4096, Access: now - int64(rows-i)}); err != nil {
			b.Fatal(err)
		}
		if i >= hits {
			continue
		}
		c := &Cache{dir: dir}
		if err := os.MkdirAll(filepath.Dir(c.objectPath(key)), 0o700); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(c.objectPath(key), make([]byte, 4096), 0o600); err != nil {
			b.Fatal(err)
		}
		action := sum[:16]
		meta, _ := json.Marshal(objectMeta{OutputID: action, PutAt: time.Now().UTC(), Size: 4096, Object: key})
		if err := os.MkdirAll(filepath.Dir(c.actionPath(action)), 0o700); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(c.actionPath(action), meta, 0o600); err != nil {
			b.Fatal(err)
		}
		actions = append(actions, action)
	}
	if err := w.Flush(); err != nil {
		b.Fatal(err)
	}
	if err := file.Close(); err != nil {
		b.Fatal(err)
	}
	return dir, actions
}

// BenchmarkStartupLargeJournal is the time one Go process waits before its
// helper is ready, with no other client running.
func BenchmarkStartupLargeJournal(b *testing.B) {
	dir, _ := scaleCache(b, scaleRows(b), 0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c, err := New(dir, 64<<30)
		if err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		c.closed = true // skip Close's compaction pass: it is not startup cost
		c.clientLock.Close()
		c.lock.Close()
		os.RemoveAll(c.pins)
		b.StartTimer()
	}
}

// BenchmarkStartupEightConcurrent starts eight helpers at once, as eight Go
// processes on one runner do, and reports the slowest and median wait.
func BenchmarkStartupEightConcurrent(b *testing.B) {
	dir, _ := scaleCache(b, scaleRows(b), 0)
	var waits []time.Duration
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var mu sync.Mutex
		var wg sync.WaitGroup
		var caches []*Cache
		start := time.Now()
		for j := 0; j < 8; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c, err := New(dir, 64<<30)
				if err != nil {
					b.Error(err)
					return
				}
				mu.Lock()
				waits = append(waits, time.Since(start))
				caches = append(caches, c)
				mu.Unlock()
			}()
		}
		wg.Wait()
		b.StopTimer()
		for _, c := range caches {
			c.closed = true
			c.clientLock.Close()
			c.lock.Close()
			os.RemoveAll(c.pins)
		}
		b.StartTimer()
	}
	b.StopTimer()
	sort.Slice(waits, func(i, j int) bool { return waits[i] < waits[j] })
	if len(waits) > 0 {
		b.ReportMetric(float64(waits[len(waits)/2].Milliseconds()), "p50-ready-ms")
		b.ReportMetric(float64(waits[len(waits)-1].Milliseconds()), "max-ready-ms")
	}
}

// BenchmarkGetEightClients is get latency with eight helpers serving hits at
// once on the same cache.
func BenchmarkGetEightClients(b *testing.B) {
	dir, actions := scaleCache(b, scaleRows(b), 2000)
	caches := make([]*Cache, 8)
	for i := range caches {
		c, err := New(dir, 64<<30)
		if err != nil {
			b.Fatal(err)
		}
		caches[i] = c
	}
	defer func() {
		for _, c := range caches {
			c.closed = true
			c.clientLock.Close()
			c.lock.Close()
			os.RemoveAll(c.pins)
		}
	}()
	var mu sync.Mutex
	var latencies []time.Duration
	b.ResetTimer()
	var wg sync.WaitGroup
	per := b.N/len(caches) + 1
	for index, c := range caches {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := make([]time.Duration, 0, per)
			for i := 0; i < per; i++ {
				action := actions[(index*per+i)%len(actions)]
				started := time.Now()
				resp := c.get(Request{ID: int64(i), Command: "get", ActionID: action})
				local = append(local, time.Since(started))
				if resp.Miss {
					b.Error("unexpected miss")
					return
				}
			}
			mu.Lock()
			latencies = append(latencies, local...)
			mu.Unlock()
		}()
	}
	wg.Wait()
	b.StopTimer()
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	if n := len(latencies); n > 0 {
		b.ReportMetric(float64(latencies[n/2].Microseconds()), "p50-get-us")
		b.ReportMetric(float64(latencies[n*99/100].Microseconds()), "p99-get-us")
	}
}

// BenchmarkPutClientLargeIndex is one Go process that puts one object into
// the large cache: start, put, close. On runner-3 (2026-11-21) this cost
// about 3.8 s with the JSONL journal, because the first put replayed it.
// The first client, which pays any one-time index setup, is reported apart
// as first-client-ms.
func BenchmarkPutClientLargeIndex(b *testing.B) {
	dir, _ := scaleCache(b, scaleRows(b), 0)
	putClient := func(i int) {
		c, err := New(dir, 64<<30)
		if err != nil {
			b.Fatal(err)
		}
		action := []byte(fmt.Sprintf("bench-put-%d", i))
		if r := c.put(Request{ID: 1, ActionID: action, OutputID: action, BodySize: 8}, []byte("abcdefgh")); r.Err != "" {
			b.Fatal(r.Err)
		}
		if err := c.Close(); err != nil {
			b.Fatal(err)
		}
	}
	started := time.Now()
	putClient(-1)
	first := time.Since(started)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		putClient(i)
	}
	b.StopTimer()
	b.ReportMetric(float64(first.Milliseconds()), "first-client-ms")
	b.ReportMetric(float64(b.Elapsed().Microseconds())/float64(b.N)/1000, "put-client-ms")
}

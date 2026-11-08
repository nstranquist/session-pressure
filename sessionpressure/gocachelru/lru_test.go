package gocachelru

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestPinnedCandidatesDoNotStarveLaterEviction(t *testing.T) {
	c, err := New(t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// More than one maintenance budget of old pinned objects precedes a
	// reclaimable object. The next pass must advance past the pinned prefix.
	for i := 0; i < 300; i++ {
		key := fmt.Sprintf("%064x", i)
		path := c.objectPath(key)
		if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := c.pin(key); err != nil {
			t.Fatal(err)
		}
		c.applyRow(indexEntry{Key: key, Size: 1, Access: int64(i + 1)})
	}
	key := fmt.Sprintf("%064x", 1000)
	if err := os.WriteFile(c.objectPath(key), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	c.applyRow(indexEntry{Key: key, Size: 1, Access: 1000})
	for i := 0; i < 2; i++ {
		if err := c.evictLocked(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(c.objectPath(key)); !os.IsNotExist(err) {
		t.Fatalf("reclaimable object starved: %v", err)
	}
}

func TestCorruptIndexCannotEscapeCache(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.jsonl"), []byte("{\"key\":\"../../outside\",\"size\":1,\"access\":1}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if c, err := New(dir, 1); err == nil {
		c.Close()
		t.Fatal("unsafe index accepted")
	}
}

func TestCachePinsReturnedPathsUntilClose(t *testing.T) {
	dir := t.TempDir()
	cache, err := New(dir, 50)
	if err != nil {
		t.Fatal(err)
	}
	first := cache.put(Request{ID: 1, Command: "put", ActionID: []byte("aaaa"), OutputID: []byte("out-a"), BodySize: 40}, bytes.Repeat([]byte("a"), 40))
	if first.Err != "" {
		t.Fatal(first.Err)
	}
	time.Sleep(20 * time.Millisecond)
	second := cache.put(Request{ID: 2, Command: "put", ActionID: []byte("bbbb"), OutputID: []byte("out-b"), BodySize: 40}, bytes.Repeat([]byte("b"), 40))
	if second.Err != "" {
		t.Fatal(second.Err)
	}
	if raw, err := os.ReadFile(first.DiskPath); err != nil || len(raw) != 40 {
		t.Fatalf("first live pin lost: %v", err)
	}
	if raw, err := os.ReadFile(second.DiskPath); err != nil || len(raw) != 40 {
		t.Fatalf("second live pin lost: %v", err)
	}
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := New(dir, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	got := next.get(Request{ID: 3, Command: "get", ActionID: []byte("aaaa")})
	if !got.Miss {
		t.Fatalf("oldest unpinned object was not evicted: %+v", got)
	}
	kept := next.get(Request{ID: 4, Command: "get", ActionID: []byte("bbbb")})
	if kept.Miss || kept.Size != 40 {
		t.Fatalf("newest object lost: %+v", kept)
	}

}

func TestServeProtocolGetPutClose(t *testing.T) {
	dir := t.TempDir()
	var stdin bytes.Buffer
	putReq, err := json.Marshal(Request{ID: 1, Command: "put", ActionID: []byte("key"), OutputID: []byte("out"), BodySize: 4})
	if err != nil {
		t.Fatal(err)
	}
	stdin.Write(putReq)
	stdin.WriteByte('\n')
	body, err := json.Marshal(base64.StdEncoding.EncodeToString([]byte("data")))
	if err != nil {
		t.Fatal(err)
	}
	stdin.Write(body)
	stdin.WriteByte('\n')
	getReq, err := json.Marshal(Request{ID: 2, Command: "get", ActionID: []byte("key")})
	if err != nil {
		t.Fatal(err)
	}
	stdin.Write(getReq)
	stdin.WriteByte('\n')
	closeReq, err := json.Marshal(Request{ID: 3, Command: "close"})
	if err != nil {
		t.Fatal(err)
	}
	stdin.Write(closeReq)
	stdin.WriteByte('\n')

	var stdout bytes.Buffer
	if err := Serve(&stdin, &stdout, filepath.Join(dir, "cache"), 1<<20); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(&stdout)
	var hello Response
	if err := dec.Decode(&hello); err != nil {
		t.Fatal(err)
	}
	if hello.ID != 0 || len(hello.KnownCommands) != 3 {
		t.Fatalf("hello = %+v", hello)
	}
	var put Response
	if err := dec.Decode(&put); err != nil {
		t.Fatal(err)
	}
	if put.Err != "" || put.DiskPath == "" {
		t.Fatalf("put = %+v", put)
	}
	var get Response
	if err := dec.Decode(&get); err != nil {
		t.Fatal(err)
	}
	if get.Miss || get.Size != 4 {
		t.Fatalf("get = %+v", get)
	}
	var closed Response
	if err := dec.Decode(&closed); err != nil && err != io.EOF {
		t.Fatal(err)
	}
}

func TestConcurrentClientsAndImmutableActionReplacement(t *testing.T) {
	dir := t.TempDir()
	a, err := New(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := New(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	first := a.put(Request{ID: 1, ActionID: []byte("same"), OutputID: []byte("one"), BodySize: 3}, []byte("one"))
	if first.Err != "" {
		t.Fatal(first.Err)
	}
	second := b.put(Request{ID: 2, ActionID: []byte("same"), OutputID: []byte("two"), BodySize: 3}, []byte("two"))
	if second.Err != "" {
		t.Fatal(second.Err)
	}
	if raw, err := os.ReadFile(first.DiskPath); err != nil || string(raw) != "one" {
		t.Fatalf("immutable first path: %s %v", raw, err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(first.DiskPath); err != nil || string(raw) != "one" {
		t.Fatalf("another client removed live pin: %s %v", raw, err)
	}
}
func TestRejectBodySizeMismatch(t *testing.T) {
	c, err := New(t.TempDir(), 100)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, size := range []int64{-1, 1, 4, MaxObjectBytes + 1} {
		got := c.put(Request{ID: 1, ActionID: []byte("key"), OutputID: []byte("out"), BodySize: size}, []byte("abc"))
		if got.Err == "" {
			t.Fatalf("accepted size %d", size)
		}
	}
}
func TestCacheConcurrentRequests(t *testing.T) {
	c, err := New(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := c.put(Request{ID: int64(i), ActionID: []byte{byte(i)}, OutputID: []byte{byte(i)}, BodySize: 3}, []byte("abc"))
			if r.Err != "" {
				t.Errorf("put: %s", r.Err)
			}
		}(i)
	}
	wg.Wait()
}
func TestRecoverAbandonedPins(t *testing.T) {
	dir := t.TempDir()
	a, err := New(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	first := a.put(Request{ID: 1, ActionID: []byte("a"), OutputID: []byte("a"), BodySize: 3}, []byte("abc"))
	if first.Err != "" {
		t.Fatal(first.Err)
	}
	// Model an exited process: the OS lock closes without protocol close.
	a.clientLock.Close()
	a.lock.Close()
	b, err := New(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err = os.Stat(first.DiskPath); !os.IsNotExist(err) {
		t.Fatalf("abandoned pin remains: %v", err)
	}
}
func BenchmarkCachePut(b *testing.B) {
	c, err := New(b.TempDir(), 16<<20)
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()
	body := bytes.Repeat([]byte("a"), 32<<10)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := c.put(Request{ID: int64(i), ActionID: []byte("key"), OutputID: []byte("out"), BodySize: int64(len(body))}, body)
		if r.Err != "" {
			b.Fatal(r.Err)
		}
	}
}

func TestEvictionHeapTouchesStayBounded(t *testing.T) {
	c := &Cache{index: map[string]indexEntry{}}
	for i := 0; i < 10000; i++ {
		c.applyRow(indexEntry{Key: fmt.Sprintf("%064x", i), Size: 1, Access: int64(i)})
	}
	for i := 0; i < 10000; i++ {
		c.applyRow(indexEntry{Key: fmt.Sprintf("%064x", i%100), Size: 1, Access: int64(i + 10000)})
	}
	if c.eviction.Len() != 10000 || len(c.eviction.positions) != 10000 || c.total != 10000 {
		t.Fatalf("heap grew on touches: %d/%d total %d", c.eviction.Len(), len(c.eviction.positions), c.total)
	}
	if c.eviction.rows[0].Access != 100 {
		t.Fatalf("oldest touch not updated: %+v", c.eviction.rows[0])
	}
}
func BenchmarkEvictionTouch(b *testing.B) {
	for _, n := range []int{1000, 100000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			c := &Cache{index: map[string]indexEntry{}}
			for i := 0; i < n; i++ {
				c.applyRow(indexEntry{Key: fmt.Sprintf("%064x", i), Size: 1, Access: int64(i)})
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				c.applyRow(indexEntry{Key: fmt.Sprintf("%064x", i%n), Size: 1, Access: int64(n + i)})
			}
		})
	}
}

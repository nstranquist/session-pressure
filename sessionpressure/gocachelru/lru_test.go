package gocachelru

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"
	"time"
)

func TestCacheEvictsOldestWhenOverMax(t *testing.T) {
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
	got := cache.get(Request{ID: 3, Command: "get", ActionID: []byte("aaaa")})
	if !got.Miss {
		t.Fatalf("expected oldest object to be evicted, got %+v", got)
	}
	kept := cache.get(Request{ID: 4, Command: "get", ActionID: []byte("bbbb")})
	if kept.Miss || kept.Size != 40 {
		t.Fatalf("expected newest object kept: %+v", kept)
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

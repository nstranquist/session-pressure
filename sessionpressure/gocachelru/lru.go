// Package gocachelru implements a size-capped GOCACHEPROG.
//
// Go 1.27 has no GOCACHEMAX. The go command already drops unused entries
// after 5 days, but a busy host keeps the whole cache hot. This helper
// stores objects on disk and evicts oldest-mtime files when the directory
// exceeds MaxBytes. It is a growth governor, not a reclaim wipe.
package gocachelru

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	DefaultMaxBytes = 16 << 30
)

type Request struct {
	ID       int64  `json:"ID"`
	Command  string `json:"Command"`
	ActionID []byte `json:"ActionID,omitempty"`
	OutputID []byte `json:"OutputID,omitempty"`
	BodySize int64  `json:"BodySize,omitempty"`
}

type Response struct {
	ID            int64      `json:"ID"`
	Err           string     `json:"Err,omitempty"`
	KnownCommands []string   `json:"KnownCommands,omitempty"`
	Miss          bool       `json:"Miss,omitempty"`
	OutputID      []byte     `json:"OutputID,omitempty"`
	Size          int64      `json:"Size,omitempty"`
	Time          *time.Time `json:"Time,omitempty"`
	DiskPath      string     `json:"DiskPath,omitempty"`
}

type Cache struct {
	dir      string
	maxBytes int64
	mu       sync.Mutex
}

func New(dir string, maxBytes int64) (*Cache, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Cache{dir: dir, maxBytes: maxBytes}, nil
}

func Serve(in io.Reader, out io.Writer, dir string, maxBytes int64) error {
	cache, err := New(dir, maxBytes)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(out)
	if err := enc.Encode(Response{ID: 0, KnownCommands: []string{"get", "put", "close"}}); err != nil {
		return err
	}
	dec := json.NewDecoder(in)
	for {
		var req Request
		if err := dec.Decode(&req); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		var body []byte
		if req.Command == "put" && req.BodySize > 0 {
			var encoded string
			if err := dec.Decode(&encoded); err != nil {
				return fmt.Errorf("read put body: %w", err)
			}
			body, err = base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return fmt.Errorf("decode put body: %w", err)
			}
		}
		resp := cache.handle(req, body)
		if err := enc.Encode(resp); err != nil {
			return err
		}
		if req.Command == "close" {
			return nil
		}
	}
}

func (c *Cache) handle(req Request, body []byte) Response {
	switch req.Command {
	case "get":
		return c.get(req)
	case "put":
		return c.put(req, body)
	case "close":
		return Response{ID: req.ID}
	default:
		return Response{ID: req.ID, Err: "unsupported command " + req.Command}
	}
}

func (c *Cache) objectPath(actionID []byte) string {
	hexID := hex.EncodeToString(actionID)
	if hexID == "" {
		hexID = "empty"
	}
	prefix := hexID
	if len(prefix) > 2 {
		prefix = prefix[:2]
	}
	return filepath.Join(c.dir, prefix, hexID)
}

func (c *Cache) get(req Request) Response {
	path := c.objectPath(req.ActionID)
	info, err := os.Stat(path)
	if err != nil {
		return Response{ID: req.ID, Miss: true}
	}
	meta, err := readMeta(path + ".meta")
	if err != nil {
		return Response{ID: req.ID, Miss: true}
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now)
	return Response{ID: req.ID, OutputID: meta.OutputID, Size: info.Size(), DiskPath: path, Time: &meta.PutAt}
}

func (c *Cache) put(req Request, body []byte) Response {
	c.mu.Lock()
	defer c.mu.Unlock()
	path := c.objectPath(req.ActionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Response{ID: req.ID, Err: err.Error()}
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return Response{ID: req.ID, Err: err.Error()}
	}
	meta := objectMeta{OutputID: req.OutputID, PutAt: time.Now().UTC(), Size: int64(len(body))}
	if err := writeMeta(path+".meta", meta); err != nil {
		return Response{ID: req.ID, Err: err.Error()}
	}
	if err := c.evictLocked(); err != nil {
		return Response{ID: req.ID, Err: err.Error()}
	}
	return Response{ID: req.ID, DiskPath: path, Size: meta.Size, OutputID: req.OutputID, Time: &meta.PutAt}
}

type objectMeta struct {
	OutputID []byte    `json:"output_id"`
	PutAt    time.Time `json:"put_at"`
	Size     int64     `json:"size"`
}

func readMeta(path string) (objectMeta, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return objectMeta{}, err
	}
	var meta objectMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return objectMeta{}, err
	}
	return meta, nil
}

func writeMeta(path string, meta objectMeta) error {
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

type objectStat struct {
	path    string
	modTime time.Time
	size    int64
}

func (c *Cache) evictLocked() error {
	var objects []objectStat
	total := int64(0)
	err := filepath.WalkDir(c.dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) == ".meta" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		objects = append(objects, objectStat{path: path, modTime: info.ModTime(), size: info.Size()})
		total += info.Size()
		return nil
	})
	if err != nil {
		return err
	}
	if total <= c.maxBytes {
		return nil
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].modTime.Before(objects[j].modTime) })
	for _, object := range objects {
		if total <= c.maxBytes {
			break
		}
		if err := os.Remove(object.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		_ = os.Remove(object.path + ".meta")
		total -= object.size
	}
	return nil
}

package sessionpressure

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const goProfileMaximumLine = 64 << 10
const goProfileMaximumRecords = 2000
const goProfileRetention = 7 * 24 * time.Hour
const goProfileMaximumBytes = 128 << 20

// GoTestTiming deliberately has no Output field. Profiling must be requested;
// only structured timing and test identity is saved, never arbitrary test logs.
type GoTestTiming struct {
	Package string  `json:"package"`
	Test    string  `json:"test,omitempty"`
	Action  string  `json:"action"`
	Elapsed float64 `json:"elapsed_seconds"`
}

type goTestProfile struct {
	SchemaVersion int            `json:"schema_version"`
	OperationID   string         `json:"operation_id"`
	CreatedAt     time.Time      `json:"created_at"`
	Records       []GoTestTiming `json:"records"`
	Dropped       int            `json:"dropped"`
	line          []byte
	discarding    bool
}

func newGoTestProfile(id string) *goTestProfile {
	return &goTestProfile{SchemaVersion: 1, OperationID: id, CreatedAt: time.Now().UTC(), Records: []GoTestTiming{}}
}

func isGoTestJSON(argv []string) bool {
	if len(argv) < 3 || filepath.Base(argv[0]) != "go" || argv[1] != "test" {
		return false
	}
	for _, arg := range argv[2:] {
		if arg == "-json" || arg == "-json=true" {
			return true
		}
		if arg == "-args" {
			break
		}
	}
	return false
}

// Write always accepts bytes. Malformed, oversized, and arbitrary lines cannot
// fail or backpressure the child's output, and parsing memory is bounded.
func (p *goTestProfile) Write(data []byte) (int, error) {
	n := len(data)
	for len(data) > 0 {
		at := bytes.IndexByte(data, '\n')
		end := at
		if at < 0 {
			end = len(data)
		}
		part := data[:end]
		if !p.discarding {
			if len(p.line)+len(part) > goProfileMaximumLine {
				p.Dropped++
				p.line = p.line[:0]
				p.discarding = true
			} else {
				p.line = append(p.line, part...)
			}
		}
		if at < 0 {
			break
		}
		if !p.discarding {
			p.consume(p.line)
		}
		p.line = p.line[:0]
		p.discarding = false
		data = data[at+1:]
	}
	return n, nil
}
func (p *goTestProfile) consume(line []byte) {
	var row struct {
		Package, Test, Action string
		Elapsed               float64
	}
	if json.Unmarshal(line, &row) != nil {
		return
	}
	switch row.Action {
	case "pass", "fail", "skip":
	default:
		return
	}
	if row.Package == "" || len(row.Package) > 1024 || len(row.Test) > 1024 || row.Elapsed < 0 {
		return
	}
	if len(p.Records) >= goProfileMaximumRecords {
		p.Dropped++
		return
	}
	p.Records = append(p.Records, GoTestTiming{Package: row.Package, Test: row.Test, Action: row.Action, Elapsed: row.Elapsed})
}
func (p *goTestProfile) save(dir string) error {
	if len(p.line) > 0 && !p.discarding {
		p.consume(p.line)
		p.line = nil
	}
	sort.SliceStable(p.Records, func(i, j int) bool { return p.Records[i].Elapsed > p.Records[j].Elapsed })
	root := filepath.Join(dir, "work-profiles")
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(root, p.OperationID+".json"), append(body, '\n'), 0600); err != nil {
		return err
	}
	return pruneGoTestProfiles(root, time.Now())
}
func pruneGoTestProfiles(root string, now time.Time) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	type file struct {
		path string
		mod  time.Time
		size int64
	}
	var files []file
	var total int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		f := file{filepath.Join(root, e.Name()), info.ModTime(), info.Size()}
		if now.Sub(f.mod) > goProfileRetention {
			if err := os.Remove(f.path); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		files = append(files, f)
		total += f.size
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, f := range files {
		if total <= goProfileMaximumBytes {
			break
		}
		if err := os.Remove(f.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		total -= f.size
	}
	return nil
}

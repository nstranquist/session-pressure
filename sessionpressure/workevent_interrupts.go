package sessionpressure

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// wrapperInterruptCounter counts wrapper-interrupted operations over the day
// shards of the work-event log. It keeps its parse state between counts and
// reads only complete lines appended since the previous count. A full re-parse
// of the day's shards (about 24 MB and 110 ms by late afternoon) on every
// heartbeat was one of the resident's largest idle costs.
//
// The result equals a fresh full pass: the state resets when the window's
// first day changes, when the shard list is not the old list plus new names
// at the end, when a consumed shard shrank or was replaced, and after any
// error. Event IDs are kept as 64-bit hashes to bound the heap.
type wrapperInterruptCounter struct {
	dir             string
	startDay        string
	shards          []wrapperInterruptShard
	seenEventIDs    map[uint64]struct{}
	failed          map[string]struct{}
	latestInterrupt map[string]time.Time
	lifecycle       *workEventLifecycleValidator
}

type wrapperInterruptShard struct {
	path   string
	info   os.FileInfo
	offset int64
}

func newWrapperInterruptCounter(dir string) *wrapperInterruptCounter {
	counter := &wrapperInterruptCounter{dir: dir}
	counter.reset("")
	return counter
}

func (counter *wrapperInterruptCounter) reset(startDay string) {
	counter.startDay = startDay
	counter.shards = nil
	counter.seenEventIDs = make(map[uint64]struct{})
	counter.failed = make(map[string]struct{})
	counter.latestInterrupt = make(map[string]time.Time)
	counter.lifecycle = newWorkEventLifecycleValidator()
}

// residentWrapperInterrupts holds one counter per data dir for the heartbeat.
var residentWrapperInterrupts = struct {
	sync.Mutex
	byDir map[string]*wrapperInterruptCounter
}{byDir: make(map[string]*wrapperInterruptCounter)}

func countResidentWrapperInterrupts(dir string, since, generatedAt time.Time) (int, error) {
	residentWrapperInterrupts.Lock()
	defer residentWrapperInterrupts.Unlock()
	counter := residentWrapperInterrupts.byDir[dir]
	if counter == nil {
		counter = newWrapperInterruptCounter(dir)
		residentWrapperInterrupts.byDir[dir] = counter
	}
	return counter.count(since, generatedAt)
}

func (counter *wrapperInterruptCounter) count(since, generatedAt time.Time) (int, error) {
	generatedAt = generatedAt.UTC()
	if since.IsZero() || since.After(generatedAt) {
		return 0, errors.New("wrapper interrupt count requires a non-zero window start at or before generated_at")
	}
	since = since.UTC()
	maximumTimestamp := generatedAt.Add(maximumWorkClockSkew)
	startDay := since.In(time.Local).Format("20060102")
	endDay := maximumTimestamp.In(time.Local).Format("20060102")
	paths, err := wrapperInterruptShardPaths(counter.dir, startDay, endDay)
	if err != nil {
		counter.reset("")
		return 0, err
	}
	if counter.startDay != startDay || !counter.extends(paths) {
		counter.reset(startDay)
	}
	for index, path := range paths {
		if err := counter.consume(index, path, maximumTimestamp); err != nil {
			counter.reset("")
			return 0, err
		}
	}
	interrupted := 0
	for _, at := range counter.latestInterrupt {
		if !at.Before(since) {
			interrupted++
		}
	}
	return interrupted, nil
}

func wrapperInterruptShardPaths(dir, startDay, endDay string) ([]string, error) {
	all, err := filepath.Glob(filepath.Join(dir, "work-events-*.jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(all)
	paths := all[:0]
	for _, path := range all {
		day := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "work-events-"), ".jsonl")
		validDay := len(day) == len("20060102")
		for _, r := range day {
			if r < '0' || r > '9' {
				validDay = false
				break
			}
		}
		if validDay && (day < startDay || day > endDay) {
			continue
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// extends reports whether paths is the consumed shard list with zero or more
// names added at the end. Lifecycle validation depends on file order.
func (counter *wrapperInterruptCounter) extends(paths []string) bool {
	if len(paths) < len(counter.shards) {
		return false
	}
	for index, shard := range counter.shards {
		if paths[index] != shard.path {
			return false
		}
	}
	return true
}

func (counter *wrapperInterruptCounter) consume(index int, path string, maximumTimestamp time.Time) (resultErr error) {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if index == len(counter.shards) {
		counter.shards = append(counter.shards, wrapperInterruptShard{path: path, info: info})
	}
	shard := &counter.shards[index]
	if !os.SameFile(shard.info, info) || info.Size() < shard.offset {
		return fmt.Errorf("work event shard %s was replaced or truncated", filepath.Base(path))
	}
	shard.info = info
	if info.Size() == shard.offset {
		return nil
	}
	if _, err := file.Seek(shard.offset, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(file, 64<<10)
	base := filepath.Base(path)
	for {
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			// Long line: gather it whole, bounded like the old scanner.
			whole := append([]byte(nil), line...)
			for errors.Is(err, bufio.ErrBufferFull) && len(whole) <= workEventIndexMaxLineBytes {
				line, err = reader.ReadSlice('\n')
				whole = append(whole, line...)
			}
			if len(whole) > workEventIndexMaxLineBytes {
				return fmt.Errorf("decode %s at byte %d: line exceeds %d bytes", base, shard.offset, workEventIndexMaxLineBytes)
			}
			line = whole
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if len(line) == 0 || line[len(line)-1] != '\n' {
			return nil // A partial last line is read once its writer ends it.
		}
		if lineErr := counter.add(bytes.TrimSuffix(line, []byte{'\n'}), maximumTimestamp); lineErr != nil {
			return fmt.Errorf("%s at byte %d: %w", base, shard.offset, lineErr)
		}
		shard.offset += int64(len(line))
	}
}

func (counter *wrapperInterruptCounter) add(line []byte, maximumTimestamp time.Time) error {
	var event WorkEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if err := event.Validate(); err != nil {
		return fmt.Errorf("validate: %w", err)
	}
	if event.Timestamp.After(maximumTimestamp) {
		return fmt.Errorf("work event %s timestamp %s is beyond allowed clock skew", event.EventID, event.Timestamp.Format(time.RFC3339Nano))
	}
	id := fnv64a(event.EventID)
	if _, duplicate := counter.seenEventIDs[id]; duplicate {
		return fmt.Errorf("duplicate work event_id %s", event.EventID)
	}
	counter.seenEventIDs[id] = struct{}{}
	if _, bad := counter.failed[event.OperationID]; bad {
		return nil
	}
	if err := counter.lifecycle.add(event); err != nil {
		// Tolerate a single unmodelled operation the same way the corpus
		// reader does; the resident heartbeat must not lose its whole
		// interrupt count to one bad lifecycle.
		counter.failed[event.OperationID] = struct{}{}
		delete(counter.latestInterrupt, event.OperationID)
		return nil
	}
	if IsWrapperInterruptEvent(event) && event.Timestamp.After(counter.latestInterrupt[event.OperationID]) {
		counter.latestInterrupt[event.OperationID] = event.Timestamp
	}
	return nil
}

func fnv64a(value string) uint64 {
	hash := uint64(14695981039346656037)
	for index := 0; index < len(value); index++ {
		hash ^= uint64(value[index])
		hash *= 1099511628211
	}
	return hash
}

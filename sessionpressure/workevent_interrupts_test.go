package sessionpressure

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

func wrapperInterruptLines(t *testing.T, at time.Time, first, count int) []byte {
	t.Helper()
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	for index := first; index < first+count; index++ {
		outcome := "user_abort"
		if index%10 == 0 {
			outcome = "wrapper_interrupt"
		}
		event := WorkEvent{
			SchemaVersion: WorkEventSchemaVersion, EventID: fmt.Sprintf("%032x", index+1),
			Timestamp: at.Add(time.Duration(index) * time.Minute), Event: WorkEventCancelled,
			OperationID: fmt.Sprintf("%032x", index+100_000), Class: WorkClassTest, Weight: 3, Outcome: outcome,
		}
		if err := encoder.Encode(event); err != nil {
			t.Fatal(err)
		}
	}
	return body.Bytes()
}

// The resident counter keeps state between heartbeats and must always agree
// with a fresh full pass: after appends, across a partial last line, after the
// window slides, and after a shard is replaced.
func TestWrapperInterruptCounterMatchesFreshPass(t *testing.T) {
	dir := t.TempDir()
	store := NewWorkEventStore(dir)
	day := time.Date(2026, 11, 17, 1, 0, 0, 0, time.Local)
	path := store.dayPath(day)
	counter := newWrapperInterruptCounter(dir)
	check := func(label string, since, now time.Time) int {
		t.Helper()
		got, err := counter.count(since, now)
		if err != nil {
			t.Fatalf("%s: incremental: %v", label, err)
		}
		want, err := newWrapperInterruptCounter(dir).count(since, now)
		if err != nil {
			t.Fatalf("%s: fresh: %v", label, err)
		}
		if got != want {
			t.Fatalf("%s: incremental=%d fresh=%d", label, got, want)
		}
		return got
	}
	appendBytes := func(body []byte) {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(body); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	now := day.Add(12 * time.Hour)
	since := now.Add(-24 * time.Hour)
	appendBytes(wrapperInterruptLines(t, day, 0, 100))
	if got := check("first", since, now); got != 10 {
		t.Fatalf("first count = %d, want 10", got)
	}
	more := wrapperInterruptLines(t, day, 100, 101) // the last line, index 200, is an interrupt
	cut := bytes.LastIndexByte(more[:len(more)-1], '\n') + 20
	appendBytes(more[:cut]) // ends in a partial line
	if got := check("partial", since, now); got != 20 {
		t.Fatalf("partial count = %d, want 20 (partial line not read)", got)
	}
	appendBytes(more[cut:])
	if got := check("completed", since, now); got != 21 {
		t.Fatalf("completed count = %d, want 21", got)
	}
	// Slide the window past the first 50 minutes of events.
	if got := check("slid", day.Add(50*time.Minute), now); got != 16 {
		t.Fatalf("slid count = %d, want 16", got)
	}
	// Replace the shard with a shorter one.
	if err := os.WriteFile(path+".new", wrapperInterruptLines(t, day, 0, 30), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}
	if got := check("replaced", since, now); got != 3 {
		t.Fatalf("replaced count = %d, want 3", got)
	}
	// A duplicate event fails closed, and the next count starts again clean.
	appendBytes(wrapperInterruptLines(t, day, 0, 1))
	if _, err := counter.count(since, now); err == nil {
		t.Fatal("duplicate event_id was accepted")
	}
	if _, err := counter.count(since, now); err == nil {
		t.Fatal("duplicate event_id was accepted after reset")
	}
}

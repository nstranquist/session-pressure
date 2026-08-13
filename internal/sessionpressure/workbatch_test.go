package sessionpressure

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDecodeWorkBatchManifestIsStrictAndArgvOnly(t *testing.T) {
	valid := []byte(`{
  "schema_version": 1,
  "class": "test",
  "steps": [{"id":"unit", "cwd":"nicos-dev", "argv":["go","test","./internal/sessionpressure"]}],
  "reuse": {"mode":"off"}
}`)
	manifest, err := DecodeWorkBatchManifest(valid)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Steps[0].CWD != "nicos-dev" || manifest.Reuse.Mode != WorkBatchReuseOff {
		t.Fatalf("normalized manifest=%+v", manifest)
	}

	for name, body := range map[string]string{
		"unknown field": `{"schema_version":1,"class":"test","steps":[{"id":"x","cwd":".","argv":["true"]}],"reuse":{"mode":"off"},"surprise":true}`,
		"duplicate id":  `{"schema_version":1,"class":"test","steps":[{"id":"x","cwd":".","argv":["true"]},{"id":"x","cwd":".","argv":["true"]}],"reuse":{"mode":"off"}}`,
		"cwd escape":    `{"schema_version":1,"class":"test","steps":[{"id":"x","cwd":"../outside","argv":["true"]}],"reuse":{"mode":"off"}}`,
		"inline shell":  `{"schema_version":1,"class":"test","steps":[{"id":"x","cwd":".","argv":["sh","-c","go test ./..."]}],"reuse":{"mode":"off"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeWorkBatchManifest([]byte(body)); err == nil {
				t.Fatal("invalid manifest unexpectedly accepted")
			}
		})
	}
}

func TestWorkBatchSuccessfulReuseRequiresDeclaredBoundary(t *testing.T) {
	body := []byte(`{
  "schema_version": 1,
  "class": "test",
  "steps": [{"id":"unit", "cwd":".", "argv":["true"]}],
  "reuse": {"mode":"successful"}
}`)
	if _, err := DecodeWorkBatchManifest(body); err == nil || !strings.Contains(err.Error(), "coverage_paths") {
		t.Fatalf("successful reuse without coverage err=%v", err)
	}
}

func TestExecuteWorkBatchStopsAtFirstFailureAndRecordsProgress(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first")
	third := filepath.Join(dir, "third")
	resultPath := filepath.Join(dir, "result.json")
	manifest := WorkBatchManifest{
		SchemaVersion: WorkBatchSchemaVersion,
		Class:         WorkClassTest,
		Steps: []WorkBatchStep{
			{ID: "first", CWD: ".", Argv: []string{"/usr/bin/touch", first}},
			{ID: "fail", CWD: ".", Argv: []string{"/usr/bin/false"}},
			{ID: "never", CWD: ".", Argv: []string{"/usr/bin/touch", third}},
		},
		Reuse: WorkBatchReusePolicy{Mode: WorkBatchReuseOff},
	}
	var stderr bytes.Buffer
	if code := executeWorkBatch(manifest, dir, resultPath, WorkProgressHuman, &bytes.Buffer{}, &stderr); code == 0 {
		t.Fatal("failing batch returned success")
	}
	if _, err := os.Stat(first); err != nil {
		t.Fatalf("first step did not run: %v", err)
	}
	if _, err := os.Stat(third); !os.IsNotExist(err) {
		t.Fatalf("third step ran after failure: %v", err)
	}
	result, err := readWorkBatchResult(resultPath)
	if err != nil || result.CompletedSteps != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if !strings.Contains(stderr.String(), "batch step 2/3 id=fail") {
		t.Fatalf("progress=%q", stderr.String())
	}
}

func TestWorkBatchFingerprintChangesWithDeclaredSource(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.txt")
	if err := os.WriteFile(input, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := WorkBatchManifest{
		SchemaVersion: WorkBatchSchemaVersion,
		Class:         WorkClassTest,
		Steps:         []WorkBatchStep{{ID: "unit", CWD: ".", Argv: []string{"/usr/bin/true"}}},
		Reuse: WorkBatchReusePolicy{
			Mode: WorkBatchReuseSuccessful, ResultContract: WorkBatchExitStatusOnly,
			MaxAgeSeconds: 60, CoveragePaths: []string{"."}, ExternalState: WorkBatchExternalNone,
		},
	}
	key := bytes.Repeat([]byte{0x42}, workReuseKeyBytes)
	before, err := fingerprintWorkBatch(dir, manifest, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := fingerprintWorkBatch(dir, manifest, key)
	if err != nil {
		t.Fatal(err)
	}
	if before.KeyDigest == after.KeyDigest || before.SourceDigest == after.SourceDigest {
		t.Fatalf("declared source change did not invalidate fingerprint: before=%+v after=%+v", before, after)
	}
}

func TestRunWorkBatchReusesSuccessfulReceiptWithoutSecondLease(t *testing.T) {
	manifest := WorkBatchManifest{
		SchemaVersion: WorkBatchSchemaVersion,
		Class:         WorkClassTest,
		Steps:         []WorkBatchStep{{ID: "unit", CWD: "nicos-dev", Argv: []string{"/usr/bin/true"}}},
		Reuse: WorkBatchReusePolicy{
			Mode: WorkBatchReuseSuccessful, ResultContract: WorkBatchExitStatusOnly,
			MaxAgeSeconds: 60, CoveragePaths: []string{"nicos-dev/go.mod"}, ExternalState: WorkBatchExternalNone,
		},
	}
	dir := t.TempDir()
	coordinator := NewWorkCoordinator(dir, DefaultPolicy(16*1024).WorkLimits)
	var stderr bytes.Buffer
	streams := WorkRunStreams{
		Stdout: &bytes.Buffer{}, Stderr: &stderr,
		CommandFactory: func(string, []string) (*exec.Cmd, io.WriteCloser, error) {
			return exec.Command("/bin/sleep", "0.05"), nil, nil
		},
	}
	admissionCalls := 0
	admission := func() Admission {
		admissionCalls++
		return Admission{Allowed: true, Level: LevelNormal}
	}
	options := WorkBatchOptions{Wait: 2 * time.Second, Progress: WorkProgressQuiet, RetentionDays: 14}
	if code, err := RunWorkBatch(coordinator, options, manifest, admission, streams); err != nil || code != 0 {
		t.Fatalf("first batch code=%d err=%v stderr=%q", code, err, stderr.String())
	}
	firstAdmissionCalls := admissionCalls
	if firstAdmissionCalls == 0 {
		t.Fatal("first batch did not traverse work admission")
	}
	if code, err := RunWorkBatch(coordinator, options, manifest, admission, streams); err != nil || code != 0 {
		t.Fatalf("reused batch code=%d err=%v stderr=%q", code, err, stderr.String())
	}
	if admissionCalls != firstAdmissionCalls {
		t.Fatalf("cache hit acquired capacity: admission calls %d -> %d", firstAdmissionCalls, admissionCalls)
	}
	events, err := NewWorkEventStore(dir).Read(WorkEventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	stats := SummarizeWorkEvents(events, time.Now().Add(-time.Hour), time.Now())
	if stats.ReviewSignals.CacheHits != 1 || stats.ReviewSignals.CacheMisses != 1 {
		t.Fatalf("reuse signals=%+v events=%+v", stats.ReviewSignals, events)
	}
	if len(events) != 5 || events[len(events)-1].Event != WorkEventReused || events[len(events)-1].BatchCompletedSteps != 1 {
		t.Fatalf("unexpected batch lifecycle events=%+v", events)
	}
}

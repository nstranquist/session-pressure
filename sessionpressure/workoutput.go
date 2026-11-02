package sessionpressure

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	workOutputDirName = "work-output"
	workOutputTeeMax  = 32 * 1024
)

const (
	WorkOutputNone     = "none"
	WorkOutputSchedule = "schedule"
	WorkOutputWorkRun  = "work_run"
)

// WorkInspectOutput is an operator-initiated preview. It is never written to
// work-events. The work-run tee is a bounded local ring, deleted when the
// lease drops.
type WorkInspectOutput struct {
	Source    string `json:"source"`
	Text      string `json:"text,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	ByteCount int    `json:"byte_count,omitempty"`
}

type workOutputTee struct {
	mu   sync.Mutex
	path string
	buf  []byte
}

func workOutputPath(dir, operationID string) string {
	return filepath.Join(dir, workOutputDirName, operationID+".log")
}

func openWorkOutputTee(dir, operationID string) (*workOutputTee, error) {
	operationID = strings.TrimSpace(operationID)
	if !validPrivateID(operationID) {
		return nil, errWorkInspectNotFound
	}
	outDir := filepath.Join(dir, workOutputDirName)
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return nil, err
	}
	path := workOutputPath(dir, operationID)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	_ = file.Close()
	return &workOutputTee{path: path}, nil
}

func (tee *workOutputTee) Write(p []byte) (int, error) {
	if tee == nil {
		return len(p), nil
	}
	tee.mu.Lock()
	defer tee.mu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	tee.buf = append(tee.buf, p...)
	if len(tee.buf) > workOutputTeeMax {
		tee.buf = append([]byte(nil), tee.buf[len(tee.buf)-workOutputTeeMax:]...)
	}
	tmp := tee.path + ".tmp"
	if err := os.WriteFile(tmp, tee.buf, 0o600); err != nil {
		return len(p), err
	}
	if err := os.Rename(tmp, tee.path); err != nil {
		_ = os.Remove(tmp)
		return len(p), err
	}
	return len(p), nil
}

func readWorkOutputTail(dir, operationID string, max int) (string, bool, int) {
	operationID = strings.TrimSpace(operationID)
	if !validPrivateID(operationID) || max < 1 {
		return "", false, 0
	}
	raw, err := os.ReadFile(workOutputPath(dir, operationID))
	if err != nil || len(raw) == 0 {
		return "", false, 0
	}
	truncated := len(raw) > max
	if truncated {
		raw = raw[len(raw)-max:]
	}
	return string(raw), truncated, len(raw)
}

func removeWorkOutput(dir, operationID string) {
	operationID = strings.TrimSpace(operationID)
	if dir == "" || !validPrivateID(operationID) {
		return
	}
	path := workOutputPath(dir, operationID)
	_ = os.Remove(path)
	_ = os.Remove(path + ".tmp")
}

func pruneWorkOutput(dir string, maxAge time.Duration) error {
	if dir == "" || maxAge <= 0 {
		return nil
	}
	outDir := filepath.Join(dir, workOutputDirName)
	entries, err := os.ReadDir(outDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	cutoff := time.Now().Add(-maxAge)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".log") && !strings.HasSuffix(name, ".tmp") {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(outDir, name))
		}
	}
	return nil
}

func collectWorkInspectOutput(dir, operationID string, origin WorkInspectOrigin) WorkInspectOutput {
	text, truncated, n := readWorkOutputTail(dir, operationID, workInspectLogMax)
	if strings.TrimSpace(text) != "" {
		return WorkInspectOutput{
			Source:    WorkOutputWorkRun,
			Text:      strings.TrimRight(text, "\n"),
			Truncated: truncated,
			ByteCount: n,
		}
	}
	snippet := strings.TrimSpace(origin.ScheduleSnippet)
	if snippet != "" {
		out := WorkInspectOutput{Source: WorkOutputSchedule, Text: snippet, ByteCount: len(snippet)}
		if len(snippet) >= workInspectLogMax {
			out.Truncated = true
		}
		return out
	}
	return WorkInspectOutput{Source: WorkOutputNone}
}

package sessionpressurecmd

import (
	"bytes"
	"io"
	"os"
	"sync"
	"testing"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	_, stdout, _ := captureMainOutput(t, func() int {
		fn()
		return 0
	})
	return stdout
}

func captureMainOutput(t *testing.T, fn func() int) (int, string, string) {
	t.Helper()

	oldStdout := os.Stdout
	oldStderr := os.Stderr

	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}

	os.Stdout = stdoutWriter
	os.Stderr = stderrWriter

	var stdoutBuf, stderrBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&stdoutBuf, stdoutReader)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&stderrBuf, stderrReader)
	}()

	rc := fn()
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()
	wg.Wait()
	os.Stdout = oldStdout
	os.Stderr = oldStderr
	_ = stdoutReader.Close()
	_ = stderrReader.Close()
	return rc, stdoutBuf.String(), stderrBuf.String()
}

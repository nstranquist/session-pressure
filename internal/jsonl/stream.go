// Package jsonl provides a streaming reader for newline-delimited JSON.
//
// Inspired by veritas-kanban's services/metrics::createLineReader / streamEvents
// (MIT) and extracted as a generic Go primitive — see the dissection at
// ~/code/refs/_cases/veritas-kanban/.
//
// We read JSONL files growing into the GBs (telemetry, autocommit observer
// logs, ndev ask query traces) without loading them into memory.
package jsonl

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// DefaultMaxLineBytes is the per-line buffer cap. Telemetry events typically
// fit in a few KB; lift the ceiling for callers that ship base64 blobs.
const DefaultMaxLineBytes = 1024 * 1024

// Options configures a Stream call. Zero value is fine.
type Options struct {
	// MaxLineBytes overrides DefaultMaxLineBytes. Lines exceeding this cap
	// surface as a *LineTooLongError.
	MaxLineBytes int
	// SkipBlank silently skips empty lines (after trimming whitespace).
	// Default true. Useful when files have trailing newlines or hand-edited
	// blanks between records.
	SkipBlank *bool
	// SkipMalformed makes JSON-decode errors non-fatal: the offending line is
	// passed to OnError (if set) and processing continues. Default false —
	// malformed lines stop the stream and return the error.
	SkipMalformed bool
	// OnError, if non-nil, is invoked for every malformed line when
	// SkipMalformed is true. Callers can log or count without halting.
	OnError func(lineNo int, raw []byte, err error)
}

// LineTooLongError is returned when a single line exceeds the buffer cap.
type LineTooLongError struct {
	LineNo int
	Cap    int
}

func (e *LineTooLongError) Error() string {
	return fmt.Sprintf("jsonl: line %d exceeds %d-byte cap", e.LineNo, e.Cap)
}

// Stream reads path line-by-line, decodes each non-blank line as JSON into a
// fresh T, and invokes handler for each successful decode. Returning a
// non-nil error from handler stops the stream and returns that error.
//
// The handler may keep references to T because Stream allocates a fresh value
// per line.
//
// Stream honors ctx — cancellation between lines stops the read and returns
// ctx.Err().
func Stream[T any](
	ctx context.Context,
	path string,
	opts Options,
	handler func(ctx context.Context, lineNo int, value T) error,
) error {
	if handler == nil {
		return errors.New("jsonl: handler is nil")
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("jsonl: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	return StreamReader[T](ctx, f, opts, handler)
}

// StreamReader is the io.Reader-backed variant of Stream. Useful for tests
// and for callers that have already opened the underlying source (gzip
// stream, network response, etc.).
func StreamReader[T any](
	ctx context.Context,
	r io.Reader,
	opts Options,
	handler func(ctx context.Context, lineNo int, value T) error,
) error {
	if handler == nil {
		return errors.New("jsonl: handler is nil")
	}
	max := opts.MaxLineBytes
	if max <= 0 {
		max = DefaultMaxLineBytes
	}
	skipBlank := true
	if opts.SkipBlank != nil {
		skipBlank = *opts.SkipBlank
	}

	sc := bufio.NewScanner(r)
	// bufio.Scanner enforces cap = max(cap(initialBuf), maxArg). To honor a
	// caller-provided MaxLineBytes that is smaller than the default initial
	// buffer, size the initial buffer to min(64KB, max).
	initial := 64 * 1024
	if max < initial {
		initial = max
	}
	sc.Buffer(make([]byte, 0, initial), max)

	lineNo := 0
	for sc.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		lineNo++
		raw := sc.Bytes()
		if skipBlank && len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var v T
		if err := json.Unmarshal(raw, &v); err != nil {
			if opts.SkipMalformed {
				if opts.OnError != nil {
					// Copy raw before passing — Scanner reuses its buffer.
					cp := append([]byte(nil), raw...)
					opts.OnError(lineNo, cp, err)
				}
				continue
			}
			return fmt.Errorf("jsonl: line %d: %w", lineNo, err)
		}
		if err := handler(ctx, lineNo, v); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		// bufio.ErrTooLong means we hit the per-line cap.
		if errors.Is(err, bufio.ErrTooLong) {
			return &LineTooLongError{LineNo: lineNo + 1, Cap: max}
		}
		return fmt.Errorf("jsonl: scan: %w", err)
	}
	return nil
}

// Collect is a small convenience that drains a JSONL file into a slice.
// Useful for fixtures and small files; do NOT use for files that exceed
// available memory — that's what Stream is for.
func Collect[T any](ctx context.Context, path string, opts Options) ([]T, error) {
	var out []T
	err := Stream[T](ctx, path, opts, func(_ context.Context, _ int, v T) error {
		out = append(out, v)
		return nil
	})
	return out, err
}

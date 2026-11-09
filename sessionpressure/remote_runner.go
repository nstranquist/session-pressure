package sessionpressure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// A subscriber reconnects only after the durable scheduler acknowledges its
// protocol. The stable request key belongs to the caller, across connections.
type runnerSubscriber struct {
	mu           sync.Mutex
	ready        bool
	job          string
	offset       int64
	terminalExit *int
	err          error
}

type runnerStream struct {
	s       *runnerSubscriber
	output  io.Writer
	pending []byte
}

func (w *runnerStream) Write(p []byte) (int, error) {
	w.pending = append(w.pending, p...)
	for {
		i := bytes.IndexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		w.line(w.pending[:i+1])
		w.pending = w.pending[i+1:]
	}
	// Guest output is framed in bounded lines. Refuse unbounded malformed
	// protocol input rather than buffering a compiler's entire transcript.
	if len(w.pending) > 128*1024 {
		w.line(w.pending)
		w.pending = nil
	}
	return len(p), nil
}

func (w *runnerStream) line(line []byte) {
	s := w.s
	s.mu.Lock()
	defer s.mu.Unlock()
	write := func(p []byte) {
		if s.err == nil {
			n, err := w.output.Write(p)
			if err == nil && n != len(p) {
				err = io.ErrShortWrite
			}
			s.err = err
		}
	}
	const status = "@@gaterunner "
	const frame = "@@gaterunner-output "
	if bytes.HasPrefix(line, []byte(status)) {
		var event struct {
			State    string `json:"state"`
			Protocol int    `json:"protocol"`
			Job      string `json:"job_id"`
			Exit     *int   `json:"exit"`
		}
		if json.Unmarshal(bytes.TrimSpace(line[len(status):]), &event) == nil {
			if event.State == "ready" && event.Protocol == 1 {
				s.ready = true
			}
			if event.Job != "" {
				if s.job != "" && s.job != event.Job {
					s.err = errors.New("runner changed durable job identity")
				} else {
					s.job = event.Job
				}
				if event.State == "passed" || event.State == "failed" || event.State == "cancelled" || event.State == "expired" || event.State == "interrupted" {
					s.terminalExit = event.Exit
				}
			}
		}
		write(line)
		return
	}
	if bytes.HasPrefix(line, []byte(frame)) {
		var f struct {
			Job    string `json:"job_id"`
			Offset int64  `json:"offset"`
			Data   []byte `json:"data"`
		}
		if json.Unmarshal(bytes.TrimSpace(line[len(frame):]), &f) != nil || f.Job == "" || f.Offset < 0 || len(f.Data) > 16*1024 {
			s.err = errors.New("invalid runner output frame")
			return
		}
		if s.job == "" {
			s.job = f.Job
		}
		if s.job != f.Job || f.Offset > s.offset {
			s.err = errors.New("runner output identity or offset mismatch")
			return
		}
		skip := s.offset - f.Offset
		if skip < int64(len(f.Data)) {
			data := f.Data[skip:]
			write(data)
			if s.err == nil {
				s.offset += int64(len(data))
			}
		}
		return
	}
	write(line)
}

func runRunnerSubscriber(ctx context.Context, command func() *exec.Cmd, stdout, stderr io.Writer) (bool, error) {
	s := &runnerSubscriber{}
	for retry := 0; ; retry++ {
		out, errout := &runnerStream{s: s, output: stdout}, &runnerStream{s: s, output: stderr}
		cmd := command()
		cmd.Stdout, cmd.Stderr = out, errout
		err := cmd.Run()
		for _, w := range []*runnerStream{out, errout} {
			// A disconnect can split a frame. Replay supplies it in full.
			if !bytes.HasPrefix(w.pending, []byte("@@gaterunner-output ")) {
				w.line(w.pending)
			}
		}
		if s.err != nil {
			return false, s.err
		}
		var exit *exec.ExitError
		transport := errors.As(err, &exit) && exit.ExitCode() == 255 && (s.terminalExit == nil || *s.terminalExit != 255)
		if !transport || !s.ready || retry >= 59 {
			safe := err == nil || s.terminalExit != nil || (!s.ready && exit != nil && exit.ExitCode() >= 0 && exit.ExitCode() != 255)
			return safe, err
		}
		fmt.Fprintln(stderr, "session-pressure: reconnecting to the same durable runner job")
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, ctx.Err()
		case <-timer.C:
		}
	}
}

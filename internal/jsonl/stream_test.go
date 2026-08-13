package jsonl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type evt struct {
	Type string `json:"type"`
	N    int    `json:"n"`
}

func writeJSONL(t *testing.T, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "data.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStreamBasic(t *testing.T) {
	p := writeJSONL(t,
		`{"type":"a","n":1}`,
		`{"type":"b","n":2}`,
		`{"type":"c","n":3}`,
	)
	got, err := Collect[evt](context.Background(), p, Options{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len=%d", len(got))
	}
	if got[1].Type != "b" || got[1].N != 2 {
		t.Fatalf("got[1]=%+v", got[1])
	}
}

func TestStreamSkipsBlankLinesByDefault(t *testing.T) {
	p := writeJSONL(t,
		`{"type":"a","n":1}`,
		``,
		`   `,
		`{"type":"b","n":2}`,
	)
	got, err := Collect[evt](context.Background(), p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len=%d", len(got))
	}
}

func TestStreamMalformedLineHaltsByDefault(t *testing.T) {
	p := writeJSONL(t,
		`{"type":"a","n":1}`,
		`{not json`,
		`{"type":"b","n":2}`,
	)
	_, err := Collect[evt](context.Background(), p, Options{})
	if err == nil {
		t.Fatal("expected error on malformed line")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("error should mention line 2: %v", err)
	}
}

func TestStreamMalformedLineSkipped(t *testing.T) {
	p := writeJSONL(t,
		`{"type":"a","n":1}`,
		`{not json`,
		`{"type":"b","n":2}`,
	)
	var bad []int
	got, err := Collect[evt](context.Background(), p, Options{
		SkipMalformed: true,
		OnError: func(lineNo int, _ []byte, _ error) {
			bad = append(bad, lineNo)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len=%d", len(got))
	}
	if len(bad) != 1 || bad[0] != 2 {
		t.Fatalf("bad=%v", bad)
	}
}

func TestStreamHandlerErrorAborts(t *testing.T) {
	p := writeJSONL(t,
		`{"type":"a","n":1}`,
		`{"type":"b","n":2}`,
		`{"type":"c","n":3}`,
	)
	want := errors.New("stop")
	count := 0
	err := Stream[evt](context.Background(), p, Options{}, func(_ context.Context, _ int, v evt) error {
		count++
		if v.Type == "b" {
			return want
		}
		return nil
	})
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
	if count != 2 {
		t.Fatalf("count=%d, want 2", count)
	}
}

func TestStreamHonorsContext(t *testing.T) {
	p := writeJSONL(t,
		`{"type":"a","n":1}`,
		`{"type":"b","n":2}`,
		`{"type":"c","n":3}`,
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Stream[evt](ctx, p, Options{}, func(_ context.Context, _ int, _ evt) error {
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestStreamLineTooLong(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "huge.jsonl")
	huge := strings.Repeat("x", 2_000)
	body := fmt.Sprintf(`{"type":"big","payload":"%s"}`+"\n", huge)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	type wide struct {
		Type    string `json:"type"`
		Payload string `json:"payload"`
	}
	_, err := Collect[wide](context.Background(), p, Options{MaxLineBytes: 256})
	var lte *LineTooLongError
	if !errors.As(err, &lte) {
		t.Fatalf("got %v, want *LineTooLongError", err)
	}
}

func TestStreamMissingFile(t *testing.T) {
	err := Stream[evt](context.Background(), "/no/such/file.jsonl", Options{},
		func(_ context.Context, _ int, _ evt) error { return nil })
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestStreamReaderFromString(t *testing.T) {
	r := strings.NewReader(strings.Join([]string{
		`{"type":"a","n":1}`,
		`{"type":"b","n":2}`,
	}, "\n"))
	var got []evt
	err := StreamReader[evt](context.Background(), r, Options{},
		func(_ context.Context, _ int, v evt) error {
			got = append(got, v)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len=%d", len(got))
	}
}

func TestStreamSkipBlankFalse(t *testing.T) {
	p := writeJSONL(t,
		`{"type":"a","n":1}`,
		``,
		`{"type":"b","n":2}`,
	)
	skipBlank := false
	_, err := Collect[evt](context.Background(), p, Options{SkipBlank: &skipBlank})
	if err == nil {
		t.Fatal("expected error when SkipBlank=false hits a blank line")
	}
}

func TestStreamNilHandler(t *testing.T) {
	p := writeJSONL(t, `{"type":"a","n":1}`)
	err := Stream[evt](context.Background(), p, Options{}, nil)
	if err == nil {
		t.Fatal("expected error for nil handler")
	}
}

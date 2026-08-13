---
{
  "generated_at": "2026-05-13T22:19:44.390096Z",
  "import_path": "nicos-dev/internal/jsonl",
  "package": "jsonl",
  "schema": 1,
  "stats_exported": 8,
  "stats_files": 2,
  "stats_loc": 199,
  "surface_hash": "dd4e9f307e8bd284"
}
---

# `nicos-dev/internal/jsonl` — package runbook

_8 exported symbols across 2 files (199 LOC). Surface hash `dd4e9f307e8bd284`._

## Public functions

### `AppendLine`

```go
func AppendLine(path string, body []byte, perm os.FileMode) error
```

> AppendLine appends body plus one trailing newline to path while holding the
> file's lockfile. body should be one already-marshaled JSON value.

_Source: `nicos-dev/internal/jsonl/append.go:15`_

### `Collect`

```go
func Collect(ctx context.Context, path string, opts Options) ([]T, error)
```

> Collect is a small convenience that drains a JSONL file into a slice.
> Useful for fixtures and small files; do NOT use for files that exceed
> available memory — that's what Stream is for.

_Source: `nicos-dev/internal/jsonl/stream.go:150`_

### `Error` (method on `*LineTooLongError`)

```go
func (*LineTooLongError) Error() string
```

_Source: `nicos-dev/internal/jsonl/stream.go:50`_

### `Stream`

```go
func Stream(ctx context.Context, path string, opts Options, handler func(ctx context.Context, lineNo int, value T) error) error
```

> Stream reads path line-by-line, decodes each non-blank line as JSON into a
> fresh T, and invokes handler for each successful decode. Returning a
> non-nil error from handler stops the stream and returns that error.
> 
> The handler may keep references to T because Stream allocates a fresh value
> per line.
> 
> Stream honors ctx — cancellation between lines stops the read and returns
> ctx.Err().

_Source: `nicos-dev/internal/jsonl/stream.go:63`_

### `StreamReader`

```go
func StreamReader(ctx context.Context, r io.Reader, opts Options, handler func(ctx context.Context, lineNo int, value T) error) error
```

> StreamReader is the io.Reader-backed variant of Stream. Useful for tests
> and for callers that have already opened the underlying source (gzip
> stream, network response, etc.).

_Source: `nicos-dev/internal/jsonl/stream.go:83`_

## Types

### `LineTooLongError`

```go
type LineTooLongError struct {
    LineNo int
    Cap    int
}
```

> LineTooLongError is returned when a single line exceeds the buffer cap.

_Source: `nicos-dev/internal/jsonl/stream.go:45`_

### `Options`

```go
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
```

> Options configures a Stream call. Zero value is fine.

_Source: `nicos-dev/internal/jsonl/stream.go:27`_

## Consts

### `DefaultMaxLineBytes`

```go
const DefaultMaxLineBytes = 1024 * 1024
```

> DefaultMaxLineBytes is the per-line buffer cap. Telemetry events typically
> fit in a few KB; lift the ceiling for callers that ship base64 blobs.

_Source: `nicos-dev/internal/jsonl/stream.go:24`_

## Example (`TestStreamBasic` from `nicos-dev/internal/jsonl/stream_test.go`)

```go
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
```

## Callers (top references)

- `nicos-dev/internal/catalog/driftgate.go` — 2 ref(s), first symbol `Options`
- `nicos-dev/internal/agentmonitor/runner.go` — 1 ref(s), first symbol `AppendLine`
- `nicos-dev/internal/ask/telemetry.go` — 1 ref(s), first symbol `AppendLine`
- `nicos-dev/internal/catalog/telemetry.go` — 1 ref(s), first symbol `AppendLine`
- `nicos-dev/internal/claudemonitor/store.go` — 1 ref(s), first symbol `AppendLine`
- `nicos-dev/internal/dogfoodharness/harness.go` — 1 ref(s), first symbol `AppendLine`
- `nicos-dev/internal/gateway/slack/store.go` — 1 ref(s), first symbol `AppendLine`
- `nicos-dev/internal/gateway/telegram/store.go` — 1 ref(s), first symbol `AppendLine`

## Tribal knowledge

<!-- HUMAN-SLAB:START -->
_Empty slab. Fill with: invariants the source can't carry, surprising behaviors, cross-package coupling that isn't captured by imports, incident postmortems._
<!-- HUMAN-SLAB:END -->

## Source files

- `nicos-dev/internal/jsonl/append.go`
- `nicos-dev/internal/jsonl/stream.go`

---
_Generated by `ndev kb runbook generate`. The HUMAN slab is preserved across regeneration; everything else is overwritten._

package gaterunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"time"

	"github.com/nstranquist/session-pressure/packages/processtree"
)

// querySQLite runs one query-only /usr/bin/sqlite3 read with bounded output:
// no database engine in-process and a short busy timeout. The caller
// validates the path as a regular file before any query runs.
//
// A WAL-mode database whose writer has closed has no -shm file, and a
// read-only connection cannot create one, so the open fails with
// SQLITE_CANTOPEN. Then, and only when no WAL content is pending, the read
// retries with immutable=1: every committed page is in the main file. A
// writer that starts during that read can at worst make it fail, and the next
// refresh reads normally.
func querySQLite(parent context.Context, path, query string, outputLimit, maxRows int) ([]map[string]any, error) {
	rows, err := runSQLiteQuery(parent, path, query, outputLimit, maxRows)
	if err == nil || !walIsEmpty(path) {
		return rows, err
	}
	immutable := url.URL{Scheme: "file", Path: path, RawQuery: "immutable=1"}
	return runSQLiteQuery(parent, immutable.String(), query, outputLimit, maxRows)
}

func walIsEmpty(path string) bool {
	st, err := os.Lstat(path + "-wal")
	return os.IsNotExist(err) || (err == nil && st.Mode().IsRegular() && st.Size() == 0)
}

func runSQLiteQuery(parent context.Context, target, query string, outputLimit, maxRows int) ([]map[string]any, error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	cmd := processtree.CommandContext(ctx, "/usr/bin/sqlite3", "-readonly", "-bail", "-cmd", ".timeout 100", "-cmd", "PRAGMA query_only=ON;", "-json", target, query)
	out := &boundedCapture{limit: outputLimit}
	stderr := &boundedCapture{limit: 4096}
	cmd.Stdout = out
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(out.Bytes())) == 0 {
		return []map[string]any{}, nil
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		return nil, err
	}
	if len(rows) > maxRows {
		return nil, errors.New("sqlite result exceeds row bound")
	}
	return rows, nil
}

package gaterunner

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// History rule (telemetry-retention-v1, R2/R3): net_samples and
// probe_samples rows leave the live store only after a verified copy. The
// copy goes to the approved backup retention-offload-local:
//
//	~/.nicos-dev/backups/retention-offload/session-pressure-telemetry/<YYYY-MM>.sqlite
//
// Each batch is copied, the archive is closed and reopened on a separate
// handle, PRAGMA integrity_check must return ok, and the batch row count is
// read back. Only then are exactly the copied keys deleted from the live
// table, and a receipt row (the pointer) is written to
// retention_offload_receipts in the live database. Any failure deletes
// nothing. Both tables are WITHOUT ROWID, so batches are keyed by the
// primary key (host, time); samples are immutable, so a retried batch
// rewrites the same values.

// offloadTable describes one live table that moves to the archive.
type offloadTable struct {
	name string
	at   string // time column; the key is (host, at)
	cols string // every column, in archive order
	defs string // archive column definitions for cols
	// cutoff converts the retention cutoff to the unit of the at column.
	cutoff func(time.Time) int64
}

var capacityOffloadTables = []offloadTable{
	{
		name:   "net_samples",
		at:     "at",
		cols:   "host, at, boot_id, iface, rx, tx",
		defs:   "host TEXT NOT NULL, at INTEGER NOT NULL, boot_id TEXT NOT NULL, iface TEXT NOT NULL, rx INTEGER NOT NULL, tx INTEGER NOT NULL",
		cutoff: func(t time.Time) int64 { return t.Unix() },
	},
	{
		name:   "probe_samples",
		at:     "at_ms",
		cols:   "host, at_ms, outcome, attempts, probe_ms",
		defs:   "host TEXT NOT NULL, at_ms INTEGER NOT NULL, outcome TEXT NOT NULL, attempts INTEGER NOT NULL, probe_ms INTEGER NOT NULL",
		cutoff: func(t time.Time) int64 { return t.UnixMilli() },
	},
}

const capacityOffloadStore = "session-pressure-telemetry"

const capacityReceiptDDL = `CREATE TABLE IF NOT EXISTS retention_offload_receipts(
  batch TEXT PRIMARY KEY,
  tbl TEXT NOT NULL,
  rows INTEGER NOT NULL,
  archive TEXT NOT NULL,
  integrity TEXT NOT NULL,
  reason TEXT NOT NULL,
  cutoff INTEGER NOT NULL,
  at TEXT NOT NULL
);`

// defaultCapacityArchiveDir maps ~/.nicos-dev/session-pressure to
// ~/.nicos-dev/backups/retention-offload/session-pressure-telemetry.
func defaultCapacityArchiveDir(dataDir string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(dataDir)), "backups", "retention-offload", capacityOffloadStore)
}

type capacityOffloadResult struct {
	Table   string
	Batch   string
	Rows    int64
	Archive string
}

// offload moves the rows of table older than cutoff to the archive.
func (s *capacityStore) offload(table offloadTable, now, cutoff time.Time) (capacityOffloadResult, error) {
	ctx := context.Background()
	limit := table.cutoff(cutoff)
	var due int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM main.`+table.name+` WHERE `+table.at+` < ?`, limit).Scan(&due); err != nil {
		return capacityOffloadResult{}, err
	}
	if due == 0 {
		return capacityOffloadResult{}, nil
	}
	if s.archiveDir == "" {
		return capacityOffloadResult{}, errors.New("capacity offload: archive directory missing; nothing deleted")
	}
	if err := os.MkdirAll(s.archiveDir, 0o700); err != nil {
		return capacityOffloadResult{}, err
	}
	archive := filepath.Join(s.archiveDir, now.UTC().Format("2006-01")+".sqlite")
	batch := table.name + "-" + now.UTC().Format("20060102T150405.000000000Z")
	stamp := now.UTC().Format(time.RFC3339Nano)
	res := capacityOffloadResult{Table: table.name, Batch: batch, Archive: archive}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return res, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, capacityReceiptDDL); err != nil {
		return res, err
	}
	// 1. Fix the batch: the keys due now. Later inserts cannot join it.
	if _, err := conn.ExecContext(ctx, `DROP TABLE IF EXISTS temp.capacity_offload_batch`); err != nil {
		return res, err
	}
	defer conn.ExecContext(ctx, `DROP TABLE IF EXISTS temp.capacity_offload_batch`)
	if _, err := conn.ExecContext(ctx, `CREATE TEMP TABLE capacity_offload_batch AS SELECT host, `+table.at+` AS at FROM main.`+table.name+` WHERE `+table.at+` < ?`, limit); err != nil {
		return res, err
	}
	var rows int64
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM temp.capacity_offload_batch`).Scan(&rows); err != nil {
		return res, err
	}
	if rows == 0 {
		return res, nil
	}
	res.Rows = rows

	// 2. Copy into the archive and commit it.
	if _, err := conn.ExecContext(ctx, `ATTACH DATABASE ? AS offload_archive`, archive); err != nil {
		return res, fmt.Errorf("capacity offload attach: %w", err)
	}
	attached := true
	detach := func() {
		if attached {
			_, _ = conn.ExecContext(ctx, `DETACH DATABASE offload_archive`)
			attached = false
		}
	}
	defer detach()
	if err := copyCapacityBatch(ctx, conn, table, batch, stamp, rows, limit); err != nil {
		return res, fmt.Errorf("capacity offload copy (nothing deleted): %w", err)
	}
	detach()
	_ = os.Chmod(archive, 0o600)

	// 3. Verify on a fresh handle: integrity_check and read-back.
	if err := verifyCapacityArchive(ctx, archive, table, batch, rows); err != nil {
		return res, fmt.Errorf("capacity offload verify (nothing deleted): %w", err)
	}

	// 4. Delete exactly the verified keys and leave the receipt.
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	deleted, err := tx.ExecContext(ctx, `DELETE FROM main.`+table.name+` WHERE (host, `+table.at+`) IN (SELECT host, at FROM temp.capacity_offload_batch)`)
	if err != nil {
		return res, err
	}
	if n, err := deleted.RowsAffected(); err != nil || n != rows {
		return res, fmt.Errorf("capacity offload: deleted %d of %d verified rows; rolled back", n, rows)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO retention_offload_receipts(batch, tbl, rows, archive, integrity, reason, cutoff, at) VALUES(?,?,?,?,?,?,?,?)`,
		batch, table.name, rows, archive, "ok", "age>"+capacityStoreRetention.String(), limit, stamp); err != nil {
		return res, err
	}
	return res, tx.Commit()
}

func copyCapacityBatch(ctx context.Context, conn *sql.Conn, table offloadTable, batch, stamp string, rows, limit int64) error {
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS offload_archive.`+table.name+`(
  `+table.defs+`, _offload_batch TEXT NOT NULL, _offloaded_at TEXT NOT NULL,
  PRIMARY KEY(host, `+table.at+`)) WITHOUT ROWID`); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS offload_archive.retention_offloads(
  batch TEXT PRIMARY KEY, store TEXT NOT NULL, tbl TEXT NOT NULL, rows INTEGER NOT NULL,
  source TEXT NOT NULL, cutoff INTEGER NOT NULL, at TEXT NOT NULL)`); err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// REPLACE: a batch retried after a crash rewrites identical samples and
	// tags them with this batch, so the read-back count is exact.
	copied, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO offload_archive.`+table.name+`(`+table.cols+`, _offload_batch, _offloaded_at)
SELECT `+prefixColumns("n.", table.cols)+`, ?, ? FROM main.`+table.name+` n
JOIN temp.capacity_offload_batch b ON b.host = n.host AND b.at = n.`+table.at, batch, stamp)
	if err != nil {
		return err
	}
	if n, err := copied.RowsAffected(); err != nil || n != rows {
		return fmt.Errorf("copied %d of %d rows", n, rows)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO offload_archive.retention_offloads(batch, store, tbl, rows, source, cutoff, at) VALUES(?,?,?,?,?,?,?)`,
		batch, capacityOffloadStore, table.name, rows, capacityStoreName, limit, stamp); err != nil {
		return err
	}
	return tx.Commit()
}

// prefixColumns turns "a, b" into "n.a, n.b".
func prefixColumns(prefix, cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = prefix + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}

func verifyCapacityArchive(ctx context.Context, archive string, table offloadTable, batch string, rows int64) error {
	q := url.Values{}
	q.Add("mode", "ro")
	q.Add("_pragma", "busy_timeout(2000)")
	db, err := sql.Open("sqlite", "file:"+archive+"?"+q.Encode())
	if err != nil {
		return err
	}
	defer db.Close()
	var check string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&check); err != nil {
		return err
	}
	if check != "ok" {
		return fmt.Errorf("integrity_check: %s", check)
	}
	var got, logged int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table.name+` WHERE _offload_batch = ?`, batch).Scan(&got); err != nil {
		return err
	}
	if err := db.QueryRowContext(ctx, `SELECT rows FROM retention_offloads WHERE batch = ?`, batch).Scan(&logged); err != nil {
		return err
	}
	if got != rows || logged != rows {
		return fmt.Errorf("read-back %d rows (log %d), want %s", got, logged, strconv.FormatInt(rows, 10))
	}
	return nil
}

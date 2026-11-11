package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func historyFixture(t *testing.T) (*appHistory, string, []string, string, []byte) {
	t.Helper()
	destination := filepath.Join(t.TempDir(), "NDev Pressure.app")
	h, err := openAppHistoryAt(destination, filepath.Join(t.TempDir(), "history"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.db.Close() })
	oldCopy, oldVerify := copyHistoryApp, verifyHistoryApp
	t.Cleanup(func() { copyHistoryApp, verifyHistoryApp = oldCopy, oldVerify })
	copyHistoryApp = func(source, target string) error {
		return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			to := filepath.Join(target, rel)
			if entry.IsDir() {
				return os.MkdirAll(to, info.Mode().Perm())
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(to, raw, info.Mode().Perm())
		})
	}
	verifyHistoryApp = func(string) error { return nil }
	var backups []string
	for _, stamp := range []string{"20261108T010000.000000000Z", "20261109T010000.000000000Z", "20261110T010000.000000000Z"} {
		path := destination + ".rollback-" + stamp
		if err = os.Rename(fixtureApp(t, "1.6.0", "23"), path); err != nil {
			t.Fatal(err)
		}
		// Distinct bytes with identical version labels must remain distinct.
		if err = os.WriteFile(filepath.Join(path, "Contents/Resources/icon"), []byte(stamp), 0755); err != nil {
			t.Fatal(err)
		}
		backups = append(backups, path)
	}
	identity, err := readAppIdentity(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(appReceipt{InstalledAt: time.Date(2026, 11, 10, 1, 0, 0, 0, time.UTC), App: identity, Previous: identity, Expected: identity.Digest, Backup: backups[0]}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	receipt := destination + ".install-20261110T010000.000000000Z.json"
	if err = os.WriteFile(receipt, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return h, t.TempDir(), backups, receipt, raw
}

func TestAppHistoryMigrationPreservesBytesAndOneLocalRollback(t *testing.T) {
	h, root, backups, receipt, raw := historyFixture(t)
	unlock, err := h.lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err = h.maintain(root); err != nil {
		t.Fatal(err)
	}
	for _, path := range append(backups, receipt) {
		if _, err = os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("source remains: %s: %v", path, err)
		}
	}
	locals, err := os.ReadDir(filepath.Join(h.dir, "rollbacks"))
	if err != nil || len(locals) != 1 || locals[0].Name() != filepath.Base(backups[2]) {
		t.Fatalf("wrong local retention: %v %v", locals, err)
	}
	for _, path := range backups[:2] {
		if _, err = os.Stat(filepath.Join(root, filepath.Base(h.dir), "rollbacks", filepath.Base(path))); err != nil {
			t.Fatal(err)
		}
	}
	var saved []byte
	if err = h.db.QueryRow(`SELECT original_json FROM receipts`).Scan(&saved); err != nil || !bytes.Equal(saved, raw) {
		t.Fatalf("receipt bytes lost: %v", err)
	}
	if err = h.maintain(""); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	var n int
	if err = h.db.QueryRow(`SELECT count(*) FROM receipts`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("receipt duplicated: %d %v", n, err)
	}
	if err = h.db.QueryRow(`SELECT count(*) FROM backups`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("backup index duplicated on retry: %d %v", n, err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, filepath.Base(h.dir), "history.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.QueryRow(`SELECT original_json FROM receipts`).Scan(&saved); err != nil || !bytes.Equal(saved, raw) {
		t.Fatalf("external history differs: %v", err)
	}
	for _, path := range []string{h.dir, filepath.Join(h.dir, "history.sqlite")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatalf("history permissions: %v %v", info, err)
		}
	}
}

func TestAppHistoryFailureNeverDeletesOriginals(t *testing.T) {
	for _, kind := range []string{"corrupt-copy", "invalid-receipt", "offline-archive", "signature-failure", "conflicting-archive", "no-archive-config"} {
		t.Run(kind, func(t *testing.T) {
			h, root, backups, receipt, _ := historyFixture(t)
			switch kind {
			case "corrupt-copy":
				original := copyHistoryApp
				copyHistoryApp = func(a, b string) error {
					if err := original(a, b); err != nil {
						return err
					}
					return os.WriteFile(filepath.Join(b, "Contents/Resources/icon"), []byte("corrupt"), 0755)
				}
			case "invalid-receipt":
				if err := os.WriteFile(receipt, []byte(`{"unknown":"text"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "offline-archive":
				root = filepath.Join(root, "missing")
			case "signature-failure":
				verifyHistoryApp = func(string) error { return errors.New("invalid signature") }
			case "conflicting-archive":
				target := filepath.Join(root, filepath.Base(h.dir), "rollbacks", filepath.Base(backups[1]))
				if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(fixtureApp(t, "1.1.0", "3"), target); err != nil {
					t.Fatal(err)
				}
			case "no-archive-config":
				root = ""
			}
			if err := h.maintain(root); err == nil {
				t.Fatal("unsafe maintenance accepted")
			}
			for _, path := range append(backups, receipt) {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("original lost after failure: %s %v", path, err)
				}
			}
		})
	}
}

func TestAppHistoryRearchivesFormerLocalRollbackAndUpdatesIndex(t *testing.T) {
	h, root, backups, _, _ := historyFixture(t)
	if err := h.maintain(root); err != nil {
		t.Fatal(err)
	}
	newest := filepath.Join(h.dir, "rollbacks", filepath.Base(h.destination)+".rollback-20261111T010000.000000000Z")
	if err := os.Rename(fixtureApp(t, "1.6.1", "24"), newest); err != nil {
		t.Fatal(err)
	}
	if err := h.maintain(""); err != nil {
		t.Fatal(err)
	}
	var path string
	if err := h.db.QueryRow(`SELECT stored_path FROM backups WHERE source_path=?`, backups[2]).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(root, filepath.Base(h.dir), "rollbacks", filepath.Base(backups[2])) {
		t.Fatal("former local rollback index is stale")
	}
	if _, err := os.Stat(newest); err != nil {
		t.Fatal("newest local rollback lost")
	}
	var n int
	if err := h.db.QueryRow(`SELECT count(*) FROM backups`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("backup provenance duplicated after archival: %d %v", n, err)
	}
}

func TestAppHistoryConsolidatesEarlyDuplicateIndexRows(t *testing.T) {
	h, root, backups, _, _ := historyFixture(t)
	if err := h.maintain(root); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`DROP INDEX backups_stored_path;
INSERT INTO backups SELECT stored_path,digest,version,build,stored_path,verified_at,0 FROM backups WHERE source_path=?`, backups[2]); err != nil {
		t.Fatal(err)
	}
	if err := h.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openAppHistoryAt(h.destination, h.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.db.Close()
	if err = reopened.maintain(""); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = reopened.db.QueryRow(`SELECT count(*) FROM backups`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("duplicate migration failed: %d %v", n, err)
	}
}

func TestAppHistoryLockExcludesLegacyAndCurrentInstallers(t *testing.T) {
	h, _, _, _, _ := historyFixture(t)
	if err := os.WriteFile(h.destination+".install.lock", nil, 0600); err != nil {
		t.Fatal(err)
	}
	unlock, err := h.lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if second, err := h.lock(); err == nil {
		second()
		t.Fatal("concurrent maintenance accepted")
	}
}

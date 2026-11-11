package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/mod/semver"
	_ "modernc.org/sqlite"
)

// App history is private, structured state. Original receipt bytes are preserved
// only after decoding the closed installer schema; no arbitrary log text enters it.
type appReceipt struct {
	InstalledAt   time.Time   `json:"installed_at"`
	App           appIdentity `json:"app"`
	Previous      appIdentity `json:"previous_app"`
	Expected      string      `json:"expected_current"`
	AllowRollback bool        `json:"allow_rollback"`
	Backup        string      `json:"backup"`
}

type appHistory struct {
	db          *sql.DB
	dir         string
	destination string
}

func openAppHistory(destination string) (*appHistory, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	key := sha256.Sum256([]byte(destination))
	dir := filepath.Join(home, ".nicos-dev", "session-pressure", "app-installs", hex.EncodeToString(key[:]))
	return openAppHistoryAt(destination, dir)
}

func openAppHistoryAt(destination, dir string) (*appHistory, error) {
	if !filepath.IsAbs(destination) || !strings.HasSuffix(destination, ".app") {
		return nil, errors.New("absolute app destination required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "history.sqlite")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA busy_timeout=5000; PRAGMA synchronous=FULL;
CREATE TABLE IF NOT EXISTS receipts (
 sha256 TEXT PRIMARY KEY, source_name TEXT NOT NULL, installed_at TEXT NOT NULL,
 app_digest TEXT NOT NULL, previous_digest TEXT NOT NULL, version TEXT NOT NULL,
 build TEXT NOT NULL, original_json BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS backups (
 source_path TEXT PRIMARY KEY, digest TEXT NOT NULL, version TEXT NOT NULL,
 build TEXT NOT NULL, stored_path TEXT NOT NULL, verified_at TEXT NOT NULL,
 source_removed INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	// Early history helpers could index a retained artifact again under its
	// new local path. Consolidate only identical locations and digests, keeping
	// the first provenance row, then fence future duplicate locations.
	_, err = db.Exec(`DELETE FROM backups WHERE rowid NOT IN (SELECT min(rowid) FROM backups GROUP BY stored_path,digest);
CREATE UNIQUE INDEX IF NOT EXISTS backups_stored_path ON backups(stored_path);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &appHistory{db: db, dir: dir, destination: destination}, nil
}

// Use the legacy lock as well, so an already running older installer cannot
// race the migration. Keep its empty inode: unlinking a lock can split ownership.
func (h *appHistory) lock() (func(), error) {
	var locks []*os.File
	unlock := func() {
		for i := len(locks) - 1; i >= 0; i-- {
			_ = syscall.Flock(int(locks[i].Fd()), syscall.LOCK_UN)
			_ = locks[i].Close()
		}
	}
	paths := []string{filepath.Join(h.dir, "install.lock")}
	if _, err := os.Lstat(h.destination + ".install.lock"); err == nil {
		paths = append(paths, h.destination+".install.lock")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	for _, path := range paths {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			unlock()
			return nil, err
		}
		locks = append(locks, f)
		if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			unlock()
			return nil, errors.New("app install or history maintenance already active")
		}
	}
	return unlock, nil
}

func (h *appHistory) recordReceipt(name string, raw []byte) error {
	if len(raw) > 64<<10 {
		return errors.New("receipt exceeds size bound")
	}
	var receipt appReceipt
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return errors.New("invalid installer receipt schema")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("receipt has trailing content")
	}
	if receipt.InstalledAt.IsZero() || !receipt.App.Exists || !validAppDigest(receipt.App.Digest) || (receipt.Previous.Exists && !validAppDigest(receipt.Previous.Digest)) {
		return errors.New("invalid installer receipt identity")
	}
	// Paths and all strings must be the existing closed installer vocabulary.
	if _, err := appBuildParts(receipt.App.Build); err != nil {
		return err
	}
	if !semver.IsValid("v" + receipt.App.Version) {
		return errors.New("invalid receipt version")
	}
	if receipt.Previous.Exists {
		if !semver.IsValid("v" + receipt.Previous.Version) {
			return errors.New("invalid previous receipt version")
		}
		if _, err := appBuildParts(receipt.Previous.Build); err != nil {
			return err
		}
	}
	if receipt.Expected != "none" && !validAppDigest(receipt.Expected) {
		return errors.New("invalid expected receipt identity")
	}
	if receipt.Backup != "" && !validBackupPath(receipt.Backup, h.destination, h.dir) {
		return errors.New("unexpected receipt backup path")
	}
	sum := sha256.Sum256(raw)
	key := hex.EncodeToString(sum[:])
	_, err := h.db.Exec(`INSERT OR IGNORE INTO receipts VALUES (?,?,?,?,?,?,?,?)`, key, filepath.Base(name), receipt.InstalledAt.UTC().Format(time.RFC3339Nano), receipt.App.Digest, receipt.Previous.Digest, receipt.App.Version, receipt.App.Build, raw)
	if err != nil {
		return err
	}
	var saved []byte
	if err = h.db.QueryRow(`SELECT original_json FROM receipts WHERE sha256=?`, key).Scan(&saved); err != nil {
		return err
	}
	if !bytes.Equal(saved, raw) {
		return errors.New("receipt readback mismatch")
	}
	return nil
}

func validAppDigest(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == sha256.Size && s == strings.ToLower(s)
}

func validBackupPath(path, destination, dir string) bool {
	base := filepath.Base(path)
	prefix := filepath.Base(destination) + ".rollback-"
	if !strings.HasPrefix(base, prefix) {
		return false
	}
	if _, err := time.Parse("20060102T150405.000000000Z", strings.TrimPrefix(base, prefix)); err != nil {
		return false
	}
	return path == filepath.Join(filepath.Dir(destination), base) || path == filepath.Join(dir, "rollbacks", base)
}

type backupMove struct {
	source, target string
	identity       appIdentity
	recordKey      string
}
type receiptImport struct {
	path string
	raw  []byte
}

var copyHistoryApp = func(source, target string) error {
	_, err := command("ditto", source, target)
	return err
}
var verifyHistoryApp = func(path string) error {
	_, err := command("codesign", "--verify", "--deep", "--strict", path)
	return err
}

func verifyAppCopy(path string, expected appIdentity) error {
	actual, err := readAppIdentity(path)
	if err != nil {
		return err
	}
	if !actual.Exists || actual != expected {
		return errors.New("rollback copy identity mismatch")
	}
	return verifyHistoryApp(path)
}

func stageAppCopy(source, target string, identity appIdentity) error {
	if _, err := os.Lstat(target); err == nil {
		if err = verifyAppCopy(target, identity); err != nil {
			return err
		}
		return syncAppCopy(target)
	} else if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".copy-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	next := filepath.Join(stage, "app")
	if err = copyHistoryApp(source, next); err != nil {
		return err
	}
	if err = verifyAppCopy(next, identity); err != nil {
		return err
	}
	if err = syncAppCopy(next); err != nil {
		return err
	}
	if err = os.Rename(next, target); err != nil {
		return err
	}
	return syncHistoryPath(parent)
}

func syncHistoryPath(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	err = f.Sync()
	return errors.Join(err, f.Close())
}

func syncAppCopy(path string) error {
	return filepath.WalkDir(path, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return syncHistoryPath(name)
	})
}

func (h *appHistory) integrity() error {
	var result string
	if err := h.db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return errors.New("app history integrity check failed")
	}
	return nil
}

func (h *appHistory) archiveRoot() (string, error) {
	var root string
	err := h.db.QueryRow(`SELECT value FROM settings WHERE key='archive_root'`).Scan(&root)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return root, err
}

func checkArchiveRoot(root, local string) error {
	if !filepath.IsAbs(root) {
		return errors.New("archive root must be absolute")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("archive root unavailable; mount the configured archive disk first")
	}
	// A disconnected /Volumes disk must never become a directory on the Mac.
	if strings.HasPrefix(root, "/Volumes/") {
		parts := strings.Split(strings.TrimPrefix(root, "/Volumes/"), string(filepath.Separator))
		volume, err := os.Stat(filepath.Join("/Volumes", parts[0]))
		if err != nil {
			return err
		}
		parent, err := os.Stat("/Volumes")
		if err != nil {
			return err
		}
		v, vok := volume.Sys().(*syscall.Stat_t)
		p, pok := parent.Sys().(*syscall.Stat_t)
		if !vok || !pok || v.Dev == p.Dev {
			return errors.New("archive volume is not mounted")
		}
	}
	a, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	b, err := filepath.EvalSymlinks(local)
	if err != nil {
		return err
	}
	if a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator)) {
		return errors.New("archive and local history must be separate")
	}
	return nil
}

// SQLite makes a consistent, standalone snapshot. Reopen and check it before
// atomically replacing the previous external copy; never copy a live DB file.
func (h *appHistory) snapshot(root string) error {
	if err := checkArchiveRoot(root, h.dir); err != nil {
		return err
	}
	dir := filepath.Join(root, filepath.Base(h.dir))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".history-*.sqlite")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err = tmp.Close(); err != nil {
		return err
	}
	defer os.Remove(name)
	if err = os.Remove(name); err != nil {
		return err
	}
	if _, err = h.db.Exec(`VACUUM INTO ?`, name); err != nil {
		return err
	}
	if err = os.Chmod(name, 0600); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", name)
	if err != nil {
		return err
	}
	var result string
	err = db.QueryRow(`PRAGMA integrity_check`).Scan(&result)
	closeErr := db.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if result != "ok" {
		return errors.New("archive history integrity check failed")
	}
	if err = syncHistoryPath(name); err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(dir, "history.sqlite")); err != nil {
		return err
	}
	return syncHistoryPath(dir)
}

// Maintain is two phase and restartable: copy every selected app and commit and
// read back every receipt before deleting any source. Failed/offline archives
// retain all sources. Each source and destination is checked again at deletion.
func (h *appHistory) maintain(configuredRoot string) error {
	root, err := h.archiveRoot()
	if err != nil {
		return err
	}
	if configuredRoot != "" {
		root = configuredRoot
	}
	if root != "" {
		if err = checkArchiveRoot(root, h.dir); err != nil {
			return err
		}
	}
	var backups []string
	for _, parent := range []string{filepath.Dir(h.destination), filepath.Join(h.dir, "rollbacks")} {
		entries, e := os.ReadDir(parent)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		for _, entry := range entries {
			p := filepath.Join(parent, entry.Name())
			if validBackupPath(p, h.destination, h.dir) {
				backups = append(backups, p)
			}
		}
	}
	sort.Slice(backups, func(i, j int) bool { return filepath.Base(backups[i]) > filepath.Base(backups[j]) })
	if len(backups) > 1 && root == "" {
		return errors.New("older rollbacks retained: configure app-maintain --archive-root before archival")
	}
	var moves []backupMove
	for i, source := range backups {
		identity, e := readAppIdentity(source)
		if e != nil {
			return e
		}
		if !identity.Exists {
			return errors.New("rollback disappeared")
		}
		target := filepath.Join(h.dir, "rollbacks", filepath.Base(source))
		if i > 0 {
			target = filepath.Join(root, filepath.Base(h.dir), "rollbacks", filepath.Base(source))
		}
		if source != target {
			if e = stageAppCopy(source, target, identity); e != nil {
				return e
			}
		} else if e = verifyAppCopy(target, identity); e != nil {
			return e
		}
		// Follow the first provenance row across moves, rather than mistaking
		// its new filesystem location for another backup on every retry.
		recordKey := source
		e = h.db.QueryRow(`SELECT source_path FROM backups WHERE source_path=? OR stored_path=? OR stored_path=? LIMIT 1`, source, source, target).Scan(&recordKey)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if _, e = h.db.Exec(`INSERT INTO backups (source_path,digest,version,build,stored_path,verified_at) VALUES (?,?,?,?,?,?) ON CONFLICT(source_path) DO UPDATE SET stored_path=excluded.stored_path, verified_at=excluded.verified_at WHERE backups.digest=excluded.digest`, recordKey, identity.Digest, identity.Version, identity.Build, target, time.Now().UTC().Format(time.RFC3339Nano)); e != nil {
			return e
		}
		var savedDigest, savedPath string
		if e = h.db.QueryRow(`SELECT digest,stored_path FROM backups WHERE source_path=?`, recordKey).Scan(&savedDigest, &savedPath); e != nil {
			return e
		}
		if savedDigest != identity.Digest || savedPath != target {
			return errors.New("backup history readback mismatch")
		}
		if source != target {
			moves = append(moves, backupMove{source, target, identity, recordKey})
		}
	}
	var imports []receiptImport
	entries, err := os.ReadDir(filepath.Dir(h.destination))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		prefix := filepath.Base(h.destination) + ".install-"
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".json") {
			continue
		}
		if _, err = time.Parse("20060102T150405.000000000Z", strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".json")); err != nil {
			return errors.New("unexpected legacy receipt name")
		}
		path := filepath.Join(filepath.Dir(h.destination), name)
		info, e := os.Lstat(path)
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() || info.Size() > 64<<10 {
			return errors.New("invalid legacy receipt file")
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		if e = h.recordReceipt(name, raw); e != nil {
			return e
		}
		imports = append(imports, receiptImport{path, raw})
	}
	if err = h.integrity(); err != nil {
		return err
	}
	if root != "" {
		if _, err = h.db.Exec(`INSERT INTO settings VALUES ('archive_root',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, root); err != nil {
			return err
		}
		if err = h.snapshot(root); err != nil {
			return err
		}
	}
	for _, move := range moves {
		if err = verifyAppCopy(move.source, move.identity); err != nil {
			return err
		}
		if err = verifyAppCopy(move.target, move.identity); err != nil {
			return err
		}
		if err = os.RemoveAll(move.source); err != nil {
			return err
		}
		if _, err = h.db.Exec(`UPDATE backups SET source_removed=1 WHERE source_path=? AND digest=?`, move.recordKey, move.identity.Digest); err != nil {
			return err
		}
	}
	for _, receipt := range imports {
		raw, e := os.ReadFile(receipt.path)
		if e != nil {
			return e
		}
		if !bytes.Equal(raw, receipt.raw) {
			return errors.New("receipt changed before removal")
		}
		if e = h.recordReceipt(filepath.Base(receipt.path), raw); e != nil {
			return e
		}
		if e = os.Remove(receipt.path); e != nil {
			return e
		}
	}
	if root != "" {
		return h.snapshot(root)
	}
	return nil
}

func maintainApp(destination, root string) error {
	h, err := openAppHistory(destination)
	if err != nil {
		return err
	}
	defer h.db.Close()
	unlock, err := h.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if err = h.maintain(root); err != nil {
		return err
	}
	var receipts, backups int
	if err = h.db.QueryRow(`SELECT count(*) FROM receipts`).Scan(&receipts); err != nil {
		return err
	}
	if err = h.db.QueryRow(`SELECT count(*) FROM backups`).Scan(&backups); err != nil {
		return err
	}
	local, err := os.ReadDir(filepath.Join(h.dir, "rollbacks"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Printf("App history verified: receipts=%d backup_records=%d local_rollbacks=%d\n", receipts, backups, len(local))
	return nil
}

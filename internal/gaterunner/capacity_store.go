package gaterunner

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// capacityStoreName holds NIC counter samples per runner. Runners keep no
// network history of their own, so bandwidth over time is built here from
// the counters each capacity read returns. Rates come from consecutive
// samples on the same boot and interface; a reboot or counter reset breaks
// the series instead of producing a negative or huge rate.
const capacityStoreName = "runner-capacity.sqlite"

const (
	capacityStoreRetention = 35 * 24 * time.Hour
	netSampleMinInterval   = 30 * time.Second
	netSeriesMaxGap        = 15 * time.Minute
	netSeriesMaxPoints     = 720
)

const capacityStoreDDL = `CREATE TABLE IF NOT EXISTS net_samples(
  host TEXT NOT NULL,
  at INTEGER NOT NULL,
  boot_id TEXT NOT NULL,
  iface TEXT NOT NULL,
  rx INTEGER NOT NULL,
  tx INTEGER NOT NULL,
  PRIMARY KEY(host, at)
) WITHOUT ROWID;`

type capacityStore struct{ db *sql.DB }

func openCapacityStore(dir string) (*capacityStore, error) {
	if dir == "" {
		return nil, errors.New("capacity store directory missing")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, capacityStoreName)
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(2000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(capacityStoreDDL); err != nil {
		db.Close()
		return nil, fmt.Errorf("capacity store schema: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
		db.Close()
		return nil, err
	}
	return &capacityStore{db: db}, nil
}

func (s *capacityStore) Close() error { return s.db.Close() }

func (s *capacityStore) record(host string, at time.Time, n *NetworkUse) error {
	if n == nil || n.Interface == "" {
		return nil
	}
	var last sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(at) FROM net_samples WHERE host = ?`, host).Scan(&last); err != nil {
		return err
	}
	if last.Valid && at.Unix()-last.Int64 < int64(netSampleMinInterval/time.Second) {
		return nil
	}
	_, err := s.db.Exec(`INSERT OR REPLACE INTO net_samples(host, at, boot_id, iface, rx, tx) VALUES(?,?,?,?,?,?)`,
		host, at.Unix(), n.bootID, n.Interface, int64(n.RxBytesSinceBoot), int64(n.TxBytesSinceBoot))
	return err
}

// series returns rates between consecutive samples since the window start,
// plus the seconds those rates cover. Gaps longer than netSeriesMaxGap (the
// app was closed) are skipped, so a rate always describes a short interval.
func (s *capacityStore) series(host, iface string, since time.Time) ([]NetPoint, int64, error) {
	rows, err := s.db.Query(`SELECT at, boot_id, iface, rx, tx FROM net_samples WHERE host = ? AND at >= ? ORDER BY at`, host, since.Add(-netSeriesMaxGap).Unix())
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	type sample struct {
		at     int64
		boot   string
		iface  string
		rx, tx int64
	}
	var prev *sample
	points := []NetPoint{}
	var coverage int64
	for rows.Next() {
		var cur sample
		if err := rows.Scan(&cur.at, &cur.boot, &cur.iface, &cur.rx, &cur.tx); err != nil {
			return nil, 0, err
		}
		if prev != nil && cur.iface == iface && prev.iface == iface && cur.boot == prev.boot &&
			cur.rx >= prev.rx && cur.tx >= prev.tx {
			dt := cur.at - prev.at
			if dt > 0 && dt <= int64(netSeriesMaxGap/time.Second) && cur.at >= since.Unix() {
				points = append(points, NetPoint{
					At:    time.Unix(cur.at, 0).UTC(),
					RxBps: round1(float64(cur.rx-prev.rx) / float64(dt)),
					TxBps: round1(float64(cur.tx-prev.tx) / float64(dt)),
				})
				coverage += dt
			}
		}
		c := cur
		prev = &c
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return downsample(points, netSeriesMaxPoints), coverage, nil
}

// downsample keeps the wire size bounded. Each kept point carries the
// maximum of the points it replaces, so short spikes stay visible.
func downsample(points []NetPoint, limit int) []NetPoint {
	if len(points) <= limit || limit <= 0 {
		return points
	}
	step := (len(points) + limit - 1) / limit
	out := make([]NetPoint, 0, limit)
	for i := 0; i < len(points); i += step {
		end := min(i+step, len(points))
		p := points[end-1]
		for _, q := range points[i:end] {
			p.RxBps = max(p.RxBps, q.RxBps)
			p.TxBps = max(p.TxBps, q.TxBps)
		}
		out = append(out, p)
	}
	return out
}

func (s *capacityStore) prune(now time.Time) error {
	_, err := s.db.Exec(`DELETE FROM net_samples WHERE at < ?`, now.Add(-capacityStoreRetention).Unix())
	return err
}

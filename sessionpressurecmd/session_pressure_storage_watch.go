package sessionpressurecmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nstranquist/session-pressure/internal/notifyinbox"
	"github.com/nstranquist/session-pressure/sessionpressure"
)

// storage watch (repo restructure program S2, ST7). One run evaluates four
// conditions, keeps per-condition episode state, and alerts once when a
// condition opens or escalates, then again every 24 h while it stays open.
//
// Disk thresholds follow the policy bands so admission and alerts agree:
// at or below warning_free_bytes (50 GiB) the alert is "warning"; at or
// below red_free_bytes (25 GiB) it is "critical".

const (
	storageWatchSchema          = 1
	storageWatchRealertInterval = 24 * time.Hour
	storageWatchBackupMaxAge    = 48 * time.Hour
	storageWatchDriveMaxAbsence = 24 * time.Hour
	storageWatchQuotaAlert      = 0.90
	storageWatchDriveDefault    = "/Volumes/X10 Pro"
	storageWatchTMDefault       = "/Volumes/NicosTM"
)

type storageWatchCondition struct {
	ID       string `json:"id"`
	Severity string `json:"severity"` // ok, info, warning, critical
	Summary  string `json:"summary"`
	Action   string `json:"action,omitempty"`
}

type storageWatchEpisode struct {
	Severity    string    `json:"severity"`
	OpenedAt    time.Time `json:"opened_at"`
	LastAlerted time.Time `json:"last_alerted"`
}

type storageWatchState struct {
	SchemaVersion     int                            `json:"schema_version"`
	DriveLastSeenAt   time.Time                      `json:"drive_last_seen_at,omitempty"`
	Episodes          map[string]storageWatchEpisode `json:"episodes"`
	LastRunAt         time.Time                      `json:"last_run_at"`
	LastAlertOutcomes []string                       `json:"last_alert_outcomes,omitempty"`
}

type storageWatchProbe struct {
	Storage      sessionpressure.StorageSnapshot
	Policy       sessionpressure.StoragePolicy
	DriveMounted bool
	TMConfigured bool
	TMLatest     time.Time // zero when no completed backup
	TMError      string
	TMMounted    bool
	TMUsedBytes  int64
	TMTotalBytes int64
}

// Test seams.
var (
	storageWatchMounted = func(path string) bool {
		var volume, parent syscall.Stat_t
		if syscall.Stat(path, &volume) != nil || syscall.Stat(filepath.Dir(path), &parent) != nil {
			return false
		}
		return volume.Dev != parent.Dev
	}
	storageWatchTMUtil = func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, "/usr/bin/tmutil", args...).CombinedOutput()
	}
	storageWatchStatfs = func(path string) (used, total int64, err error) {
		var fs syscall.Statfs_t
		if err := syscall.Statfs(path, &fs); err != nil {
			return 0, 0, err
		}
		total = int64(fs.Blocks) * int64(fs.Bsize)
		return total - int64(fs.Bavail)*int64(fs.Bsize), total, nil
	}
	storageWatchNotify = func(condition storageWatchCondition) error {
		severity := "warning"
		if condition.Severity == "critical" {
			severity = "error"
		}
		return notifyinbox.Append(notifyinbox.Path(os.Getenv("HOME"), ""), notifyinbox.Toast{
			Title: "Storage watch: " + condition.ID, Body: condition.Summary, Severity: severity,
			Source: "session-pressure-storage-watch", Seconds: "15",
		}, time.Now)
	}
	storageWatchScratchpad = func(text string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, "ndev", "--json", "scratchpad", "capture", "--target", "agents", "--index", "--source", "session-pressure-storage-watch", "--header", "Storage watch alert")
		command.Stdin = strings.NewReader(text)
		output, err := command.CombinedOutput()
		if err != nil {
			return fmt.Errorf("scratchpad capture: %w: %s", err, strings.TrimSpace(string(output)))
		}
		return nil
	}
)

func probeTimeMachine(probe *storageWatchProbe) {
	output, err := storageWatchTMUtil("destinationinfo")
	text := string(output)
	if err != nil && !strings.Contains(text, "No destinations configured") {
		probe.TMError = strings.TrimSpace(text)
		return
	}
	probe.TMConfigured = strings.Contains(text, "Name") && !strings.Contains(text, "No destinations configured")
	if !probe.TMConfigured {
		return
	}
	output, err = storageWatchTMUtil("latestbackup")
	if err != nil {
		probe.TMError = strings.TrimSpace(string(output))
		return
	}
	probe.TMLatest = parseTMBackupTime(strings.TrimSpace(string(output)))
}

// parseTMBackupTime reads the timestamp from a latestbackup path such as
// /Volumes/.timemachine/.../2026-11-16-101530.backup.
func parseTMBackupTime(path string) time.Time {
	base := filepath.Base(strings.TrimSpace(path))
	base = strings.TrimSuffix(strings.TrimSuffix(base, ".backup"), ".previous")
	if len(base) < len("2006-01-02-150405") {
		return time.Time{}
	}
	at, err := time.ParseInLocation("2006-01-02-150405", base[:len("2006-01-02-150405")], time.Local)
	if err != nil {
		return time.Time{}
	}
	return at
}

// evaluateStorageWatch is pure: probe + prior state + clock → conditions.
func evaluateStorageWatch(probe storageWatchProbe, state *storageWatchState, now time.Time) []storageWatchCondition {
	disk := storageWatchCondition{ID: "disk-free", Severity: "ok"}
	switch {
	case !probe.Storage.Available:
		disk.Severity, disk.Summary = "warning", "internal volume capacity is unavailable"
	case probe.Storage.AvailableBytes <= probe.Policy.RedFreeBytes:
		disk.Severity = "critical"
	case probe.Storage.AvailableBytes <= probe.Policy.WarningFreeBytes:
		disk.Severity = "warning"
	}
	if disk.Summary == "" {
		disk.Summary = fmt.Sprintf("internal free %.1f GiB (warning ≤ %.0f GiB, critical ≤ %.0f GiB)", bytesToGiB(probe.Storage.AvailableBytes), bytesToGiB(probe.Policy.WarningFreeBytes), bytesToGiB(probe.Policy.RedFreeBytes))
	}
	if disk.Severity != "ok" {
		disk.Action = "ndev session pressure storage plan; reclaim through typed providers"
	}

	backup := storageWatchCondition{ID: "time-machine", Severity: "ok"}
	switch {
	case probe.TMError != "":
		backup.Severity, backup.Summary = "warning", "tmutil failed: "+probe.TMError
	case !probe.TMConfigured:
		backup.Severity, backup.Summary = "info", "no Time Machine destination (ST2 pending)"
	case probe.TMLatest.IsZero():
		backup.Severity, backup.Summary = "warning", "Time Machine has no completed backup"
	case now.Sub(probe.TMLatest) > storageWatchBackupMaxAge:
		backup.Severity, backup.Summary = "warning", fmt.Sprintf("latest Time Machine backup is %s old", now.Sub(probe.TMLatest).Round(time.Hour))
	default:
		backup.Summary = fmt.Sprintf("latest Time Machine backup %s ago", now.Sub(probe.TMLatest).Round(time.Minute))
	}

	drive := storageWatchCondition{ID: "x10-mounted", Severity: "ok", Summary: "X10 Pro is mounted"}
	if probe.DriveMounted {
		state.DriveLastSeenAt = now
	} else if state.DriveLastSeenAt.IsZero() {
		drive.Severity, drive.Summary = "info", "X10 Pro not mounted and never seen by this watch"
	} else if absent := now.Sub(state.DriveLastSeenAt); absent > storageWatchDriveMaxAbsence {
		drive.Severity, drive.Summary = "warning", fmt.Sprintf("X10 Pro unmounted for %s; offload links and backups depend on it", absent.Round(time.Hour))
		drive.Action = "plug in the X10 Pro"
	} else {
		drive.Summary = fmt.Sprintf("X10 Pro unmounted for %s (alert after 24h)", absent.Round(time.Minute))
	}

	quota := storageWatchCondition{ID: "nicostm-quota", Severity: "ok"}
	switch {
	case !probe.TMMounted:
		quota.Severity, quota.Summary = "info", "NicosTM volume not mounted"
	case probe.TMTotalBytes <= 0:
		quota.Severity, quota.Summary = "warning", "NicosTM capacity unavailable"
	default:
		ratio := float64(probe.TMUsedBytes) / float64(probe.TMTotalBytes)
		quota.Summary = fmt.Sprintf("NicosTM %.0f%% of %.0f GiB quota", ratio*100, bytesToGiB(probe.TMTotalBytes))
		if ratio > storageWatchQuotaAlert {
			quota.Severity = "warning"
			quota.Action = "review exclusions (tm-exclude plan); Time Machine will delete oldest history"
		}
	}
	return []storageWatchCondition{disk, backup, drive, quota}
}

func storageWatchRank(severity string) int {
	switch severity {
	case "critical":
		return 3
	case "warning":
		return 2
	case "info":
		return 1
	}
	return 0
}

// storageWatchDue updates episodes and returns the conditions to alert.
func storageWatchDue(conditions []storageWatchCondition, state *storageWatchState, now time.Time) []storageWatchCondition {
	if state.Episodes == nil {
		state.Episodes = map[string]storageWatchEpisode{}
	}
	var due []storageWatchCondition
	for _, condition := range conditions {
		rank := storageWatchRank(condition.Severity)
		episode, open := state.Episodes[condition.ID]
		if rank < 2 {
			delete(state.Episodes, condition.ID)
			continue
		}
		switch {
		case !open:
			episode = storageWatchEpisode{Severity: condition.Severity, OpenedAt: now, LastAlerted: now}
			due = append(due, condition)
		case rank > storageWatchRank(episode.Severity) || now.Sub(episode.LastAlerted) >= storageWatchRealertInterval:
			episode.Severity, episode.LastAlerted = condition.Severity, now
			due = append(due, condition)
		default:
			episode.Severity = condition.Severity
		}
		state.Episodes[condition.ID] = episode
	}
	return due
}

func storageWatchStatePath(dir string) string { return filepath.Join(dir, "storage-watch-state.json") }
func storageWatchLogPath(dir string) string   { return filepath.Join(dir, "storage-watch-alerts.jsonl") }

func loadStorageWatchState(path string) storageWatchState {
	state := storageWatchState{SchemaVersion: storageWatchSchema, Episodes: map[string]storageWatchEpisode{}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &state)
	}
	if state.Episodes == nil {
		state.Episodes = map[string]storageWatchEpisode{}
	}
	return state
}

func writeStorageWatchFile(path string, data []byte) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func appendStorageWatchLog(path string, record any) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := bufio.NewWriter(file)
	if err := json.NewEncoder(writer).Encode(record); err != nil {
		return err
	}
	return writer.Flush()
}

// routeStorageWatchAlerts sends each due alert to the notify inbox and one
// combined Scratchpad note. Failures are reported, never fatal.
func routeStorageWatchAlerts(due []storageWatchCondition, logPath string, now time.Time) []string {
	if len(due) == 0 {
		return nil
	}
	var outcomes []string
	var note strings.Builder
	fmt.Fprintf(&note, "Storage watch %s\n\n", now.UTC().Format(time.RFC3339))
	for _, condition := range due {
		outcome := "toast:ok"
		if err := storageWatchNotify(condition); err != nil {
			outcome = "toast:" + err.Error()
		}
		outcomes = append(outcomes, condition.ID+" "+outcome)
		fmt.Fprintf(&note, "- %s [%s]: %s", condition.ID, condition.Severity, condition.Summary)
		if condition.Action != "" {
			fmt.Fprintf(&note, " Next: `%s`.", condition.Action)
		}
		note.WriteString("\n")
		_ = appendStorageWatchLog(logPath, map[string]any{"schema_version": storageWatchSchema, "at": now.UTC(), "condition": condition})
	}
	if err := storageWatchScratchpad(note.String()); err != nil {
		outcomes = append(outcomes, "scratchpad:"+err.Error())
	} else {
		outcomes = append(outcomes, "scratchpad:ok")
	}
	return outcomes
}

func cmdSessionPressureStorageWatch(g *Flags, args []string) int {
	drive, tmVolume := storageWatchDriveDefault, storageWatchTMDefault
	dryRun, testAlert := false, false
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--dry-run":
			dryRun = true
		case "--test-alert":
			testAlert = true
		case "--drive", "--tm-volume":
			if index+1 >= len(args) {
				return sessionPressureError(args[index]+" requires a path", 2)
			}
			if args[index] == "--drive" {
				drive = args[index+1]
			} else {
				tmVolume = args[index+1]
			}
			index++
		default:
			return sessionPressureError("unknown storage watch argument "+args[index], 2)
		}
	}
	runtime, sample, err := loadStorageState()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	now := time.Now()
	probe := storageWatchProbe{Storage: sample, Policy: runtime.policy.Storage, DriveMounted: storageWatchMounted(drive), TMMounted: storageWatchMounted(tmVolume)}
	probeTimeMachine(&probe)
	if probe.TMMounted {
		probe.TMUsedBytes, probe.TMTotalBytes, _ = storageWatchStatfs(tmVolume)
	}
	statePath := storageWatchStatePath(runtime.dir)
	state := loadStorageWatchState(statePath)
	conditions := evaluateStorageWatch(probe, &state, now)
	due := storageWatchDue(conditions, &state, now)
	if testAlert {
		due = []storageWatchCondition{{ID: "test-alert", Severity: "warning", Summary: "storage watch test alert; routing check only"}}
	}
	var outcomes []string
	if !dryRun {
		outcomes = routeStorageWatchAlerts(due, storageWatchLogPath(runtime.dir), now)
		state.SchemaVersion, state.LastRunAt = storageWatchSchema, now.UTC()
		if len(outcomes) > 0 {
			state.LastAlertOutcomes = outcomes
		}
		if data, marshalErr := json.MarshalIndent(state, "", "  "); marshalErr == nil {
			if writeErr := writeStorageWatchFile(statePath, data); writeErr != nil {
				err = errors.Join(err, writeErr)
			}
		}
	}
	payload := map[string]any{"ok": err == nil, "action": "storage.watch", "dry_run": dryRun, "conditions": conditions, "alerted": due, "routing": outcomes, "state_path": statePath}
	var text strings.Builder
	for _, condition := range conditions {
		fmt.Fprintf(&text, "%-14s %-8s %s\n", condition.ID, condition.Severity, condition.Summary)
	}
	fmt.Fprintf(&text, "alerted=%d dry_run=%v\n", len(due), dryRun)
	exit := 0
	if err != nil {
		exit = 1
	}
	return emitPressure(g, payload, text.String(), exit)
}

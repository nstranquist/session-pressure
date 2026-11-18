package filelock

import (
	"bytes"
	"os"
	"strconv"
	"time"
)

// userHZ is the clock-tick unit of /proc/<pid>/stat start times. The kernel
// fixes it at 100 for user space on every architecture this repository
// builds for.
const userHZ = 100

// platformProcessInfo computes pid's start time from the boot time in
// /proc/stat and the start tick in /proc/<pid>/stat, and reads its state
// (Z zombie, X dead). It reports false when the process is gone or either
// file cannot be parsed.
func platformProcessInfo(pid int) (processInfo, bool) {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return processInfo{}, false
	}
	// The command name (field 2) is in parentheses and may hold spaces, so
	// count fields from the last ')'. Field 3 (state) is the first after it,
	// so field 22 (starttime) is index 19.
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 {
		return processInfo{}, false
	}
	fields := bytes.Fields(stat[end+1:])
	if len(fields) <= 19 {
		return processInfo{}, false
	}
	ticks, err := strconv.ParseInt(string(fields[19]), 10, 64)
	if err != nil || ticks < 0 {
		return processInfo{}, false
	}
	boot, ok := bootTime()
	if !ok {
		return processInfo{}, false
	}
	state := string(fields[0])
	return processInfo{
		started: boot.Add(time.Duration(ticks) * time.Second / userHZ),
		zombie:  state == "Z" || state == "X",
	}, true
}

// bootTime reads btime (seconds since the epoch) from /proc/stat.
func bootTime() (time.Time, bool) {
	stat, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, false
	}
	for _, line := range bytes.Split(stat, []byte{'\n'}) {
		if value, found := bytes.CutPrefix(line, []byte("btime ")); found {
			seconds, err := strconv.ParseInt(string(bytes.TrimSpace(value)), 10, 64)
			if err != nil || seconds <= 0 {
				return time.Time{}, false
			}
			return time.Unix(seconds, 0), true
		}
	}
	return time.Time{}, false
}

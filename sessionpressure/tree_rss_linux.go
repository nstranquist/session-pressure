//go:build linux

package sessionpressure

import (
	"os"
	"strconv"
	"strings"
)

// listProcessRows reads pid, ppid and RSS from /proc.
func listProcessRows() ([]processRow, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	pageKB := int64(os.Getpagesize() / 1024)
	rows := make([]processRow, 0, len(entries))
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			continue
		}
		// Fields after the parenthesised command: state ppid ... rss is field 24.
		text := string(stat)
		closing := strings.LastIndexByte(text, ')')
		if closing < 0 {
			continue
		}
		fields := strings.Fields(text[closing+1:])
		if len(fields) < 22 {
			continue
		}
		ppid, _ := strconv.Atoi(fields[1])
		rssPages, _ := strconv.ParseInt(fields[21], 10, 64)
		rows = append(rows, processRow{pid: pid, ppid: ppid, rssKB: rssPages * pageKB})
	}
	return rows, nil
}

// processRSSKB is nil on Linux: /proc rows already carry RSS.
var processRSSKB func(pid int) (int64, bool)

//go:build linux

package sessionpressure

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

func processGuardIdentity(pid int) (string, int, error) {
	start, err := processStartIdentity(pid)
	if err != nil {
		return "", 0, err
	}
	path := fmt.Sprintf("/proc/%d", pid)
	info, err := os.Stat(path)
	if err != nil {
		return "", 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", 0, fmt.Errorf("process owner unavailable")
	}
	body, err := os.ReadFile(path + "/stat")
	if err != nil {
		return "", 0, err
	}
	end := strings.LastIndexByte(string(body), ')')
	if end < 0 {
		return "", 0, fmt.Errorf("invalid process state")
	}
	fields := strings.Fields(string(body[end+1:]))
	if len(fields) == 0 || fields[0] == "Z" {
		return "", 0, errProcessGuardExited
	}
	current, err := processStartIdentity(pid)
	if err != nil || current != start {
		return "", 0, fmt.Errorf("process identity changed")
	}
	return start, int(stat.Uid), nil
}

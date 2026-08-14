//go:build linux

package sessionpressure

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func processStartIdentity(pid int) (string, error) {
	body, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	// comm may contain spaces and parentheses; the final ')' terminates it.
	end := strings.LastIndexByte(string(body), ')')
	if end < 0 || end+2 >= len(body) {
		return "", fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	fields := strings.Fields(string(body[end+2:]))
	// fields starts at proc(5) field 3 (state); starttime is field 22.
	if len(fields) <= 19 {
		return "", fmt.Errorf("short /proc/%d/stat", pid)
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return "", fmt.Errorf("invalid /proc/%d/stat starttime: %w", pid, err)
	}
	return "linux:" + fields[19], nil
}

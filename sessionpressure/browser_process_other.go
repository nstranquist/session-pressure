//go:build !darwin

package sessionpressure

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func automatedBrowserProcesses(ctx context.Context) ([]browserProcess, error) {
	body, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,lstart=,command=").Output()
	if err != nil {
		return nil, err
	}
	var roots []browserProcess
	for _, row := range strings.Split(string(body), "\n") {
		fields := strings.Fields(row)
		if len(fields) < 7 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 1 || !automatedBrowserRoot(strings.Join(fields[6:], " ")) {
			continue
		}
		started, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(fields[1:6], " "), time.Local)
		if err != nil {
			continue
		}
		roots = append(roots, browserProcess{PID: pid, StartedAt: started})
	}
	return roots, nil
}

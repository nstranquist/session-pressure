package sessionpressure

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func snapshotProcessTree(root int) *WorkProcessSnapshot {
	if root <= 0 {
		return &WorkProcessSnapshot{PID: root, Alive: false}
	}
	byPID := listProcessTable()
	snap, ok := byPID[root]
	if !ok {
		return &WorkProcessSnapshot{PID: root, Alive: false}
	}
	snap.Alive = true
	if cwd := processCWD(root); cwd != "" {
		snap.CWD = cwd
	}
	if snap.PPID > 1 {
		if _, ok := byPID[snap.PPID]; !ok {
			snap.ParentDead = true
		}
	}
	var chain []WorkProcessRef
	cur := snap.PPID
	for i := 0; i < 8 && cur > 1; i++ {
		node, ok := byPID[cur]
		if !ok {
			chain = append(chain, WorkProcessRef{PID: cur})
			break
		}
		chain = append(chain, WorkProcessRef{PID: node.PID, Command: node.Command})
		cur = node.PPID
	}
	snap.ParentChain = chain
	for pid, child := range byPID {
		if child.PPID == root && pid != root {
			child.Alive = true
			snap.Children = append(snap.Children, child)
		}
	}
	return &snap
}

func listProcessTable() map[int]WorkProcessSnapshot {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,pgid=,stat=,rss=,pcpu=,command=").Output()
	if err != nil {
		return map[int]WorkProcessSnapshot{}
	}
	table := map[int]WorkProcessSnapshot{}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 6 {
			continue
		}
		pid := parsePSInt(fields[0])
		if pid <= 0 {
			continue
		}
		cpu, _ := strconv.ParseFloat(fields[5], 64)
		cmd, truncated := truncateCommand(strings.Join(fields[6:], " "))
		table[pid] = WorkProcessSnapshot{
			PID:        pid,
			PPID:       parsePSInt(fields[1]),
			PGID:       parsePSInt(fields[2]),
			State:      fields[3],
			RSSKB:      int64(parsePSInt(fields[4])),
			CPUPercent: cpu,
			Command:    cmd,
			Truncated:  truncated,
			Alive:      true,
		}
	}
	return table
}

func descendantPIDs(root int) []int {
	if root <= 1 {
		return nil
	}
	children := map[int][]int{}
	for pid, proc := range listProcessTable() {
		if pid > 1 && proc.PPID > 0 {
			children[proc.PPID] = append(children[proc.PPID], pid)
		}
	}
	seen := map[int]struct{}{root: {}}
	order := make([]int, 0, 8)
	queue := []int{root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, child := range children[cur] {
			if _, dup := seen[child]; dup {
				continue
			}
			seen[child] = struct{}{}
			order = append(order, child)
			queue = append(queue, child)
		}
	}
	return order
}

func processCWD(pid int) string {
	if pid <= 0 {
		return ""
	}
	if target, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "cwd")); err == nil {
		return target
	}
	out, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			return strings.TrimPrefix(line, "n")
		}
	}
	return ""
}

func processEnvKeys(pid int, keys []string) map[string]string {
	found := map[string]string{}
	if pid <= 0 || len(keys) == 0 {
		return found
	}
	if native := darwinEnvKeys(pid, keys); len(native) > 0 {
		return native
	}
	want := map[string]struct{}{}
	for _, key := range keys {
		want[key] = struct{}{}
	}
	if raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ")); err == nil {
		for _, pair := range bytes.Split(raw, []byte{0}) {
			key, value, ok := strings.Cut(string(pair), "=")
			if !ok {
				continue
			}
			if _, keep := want[key]; keep {
				found[key] = value
			}
		}
		if len(found) > 0 {
			return found
		}
	}
	out, err := exec.Command("ps", "eww", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return found
	}
	for _, token := range strings.Fields(string(out)) {
		key, value, ok := strings.Cut(token, "=")
		if !ok {
			continue
		}
		if _, keep := want[key]; keep {
			found[key] = value
		}
	}
	return found
}

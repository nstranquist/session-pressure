//go:build darwin

package sessionpressure

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

const (
	darwinRusageInfoVersion0 = 0
	darwinRusageInfoVersion2 = 2
	darwinCPUScale           = 1 << 11
)

type darwinRusageInfoV0 struct {
	UUID              [16]byte
	UserTime          uint64
	SystemTime        uint64
	PackageIdleWakeup uint64
	InterruptWakeup   uint64
	Pageins           uint64
	WiredSize         uint64
	ResidentSize      uint64
	PhysicalFootprint uint64
	ProcessStart      uint64
	ProcessExit       uint64
}

type darwinRusageInfoV2 struct {
	UUID                   [16]byte
	UserTime               uint64
	SystemTime             uint64
	PackageIdleWakeup      uint64
	InterruptWakeup        uint64
	Pageins                uint64
	WiredSize              uint64
	ResidentSize           uint64
	PhysicalFootprint      uint64
	ProcessStart           uint64
	ProcessExit            uint64
	ChildUserTime          uint64
	ChildSystemTime        uint64
	ChildPackageIdleWakeup uint64
	ChildInterruptWakeup   uint64
	ChildPageins           uint64
	ChildElapsed           uint64
	DiskIOBytesRead        uint64
	DiskIOBytesWritten     uint64
}

var darwinLibproc struct {
	once        sync.Once
	err         error
	pidRusage   func(int32, int32, *darwinRusageInfoV0) int32
	pidRusageV2 func(int32, int32, *darwinRusageInfoV2) int32
	pidPath     func(int32, *byte, uint32) int32
	sysctl      func(*int32, uint32, unsafe.Pointer, *uintptr, unsafe.Pointer, uintptr) int32
}

var darwinNativeProcessScan struct {
	sync.Mutex
	args darwinProcessArgsReader
}

func loadDarwinLibproc() error {
	darwinLibproc.once.Do(func() {
		handle, err := purego.Dlopen("/usr/lib/libproc.dylib", purego.RTLD_LAZY|purego.RTLD_LOCAL)
		if err != nil {
			darwinLibproc.err = err
			return
		}
		purego.RegisterLibFunc(&darwinLibproc.pidRusage, handle, "proc_pid_rusage")
		purego.RegisterLibFunc(&darwinLibproc.pidRusageV2, handle, "proc_pid_rusage")
		purego.RegisterLibFunc(&darwinLibproc.pidPath, handle, "proc_pidpath")
		systemHandle, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_LAZY|purego.RTLD_LOCAL)
		if err != nil {
			darwinLibproc.err = err
			return
		}
		purego.RegisterLibFunc(&darwinLibproc.sysctl, systemHandle, "sysctl")
	})
	return darwinLibproc.err
}

func nativeProcesses(ctx context.Context) ([]Process, string, error) {
	if err := loadDarwinLibproc(); err != nil {
		return nil, "libproc", err
	}
	// Keep the heavyweight native inventory single-flight. In particular, reuse
	// one bounded kern.procargs2 buffer instead of allocating a prompt-sized
	// buffer independently in the resident and an overlapping operator sample.
	darwinNativeProcessScan.Lock()
	defer darwinNativeProcessScan.Unlock()
	rows, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, "libproc", err
	}
	now := time.Now()
	pageKB := int64(os.Getpagesize() / 1024)
	processes := make([]Process, 0, len(rows))
	argsReader := &darwinNativeProcessScan.args
	identityCatalog := ActiveAgentIdentityCatalog()
	for index := range rows {
		if index%64 == 0 {
			select {
			case <-ctx.Done():
				return nil, "libproc", ctx.Err()
			default:
			}
		}
		row := rows[index]
		pid := int(row.Proc.P_pid)
		if pid <= 0 {
			continue
		}
		comm := darwinCString(row.Proc.P_comm[:])
		started := time.Unix(int64(row.Proc.P_starttime.Sec), int64(row.Proc.P_starttime.Usec)*1000)
		elapsed := int64(0)
		if !started.IsZero() && now.After(started) {
			elapsed = int64(now.Sub(started).Seconds())
		}
		rssKB := int64(0)
		cpuTotalNS := uint64(0)
		cpuTotalValid := false
		cpuStartID := uint64(0)
		var usage darwinRusageInfoV2
		diskWriteValid := false
		diskWriteBytes := uint64(0)
		if darwinLibproc.pidRusageV2(int32(pid), darwinRusageInfoVersion2, &usage) == 0 {
			rssKB = int64(usage.ResidentSize / 1024)
			if usage.UserTime <= ^uint64(0)-usage.SystemTime {
				cpuTotalNS = usage.UserTime + usage.SystemTime
				cpuTotalValid = true
				cpuStartID = usage.ProcessStart
			}
			diskWriteValid = true
			diskWriteBytes = usage.DiskIOBytesWritten
		} else if row.Eproc.Xrssize > 0 {
			rssKB = int64(row.Eproc.Xrssize) * pageKB
		}
		agent, executable, sessionID := darwinAgentIdentity(pid, comm, argsReader, identityCatalog)
		if executable == "" {
			executable = privacySafeExecutable(comm)
		}
		processes = append(processes, Process{
			PID: pid, PPID: int(row.Eproc.Ppid), RSSKB: rssKB,
			// kern.proc.all currently returns zero p_pctcpu values on the target
			// macOS host. Preserve it only as a diagnostic fallback; the sampler
			// marks CPU authoritative after a same-process rusage delta.
			CPUPercent:     float64(row.Proc.P_pctcpu) * 100 / darwinCPUScale,
			ElapsedSeconds: elapsed, Command: comm,
			Agent: agent, Executable: executable, SessionID: sessionID,
			CPUTotalNS: cpuTotalNS, CPUTotalValid: cpuTotalValid, CPUStartID: cpuStartID,
			StartedAtNS: started.UnixNano(), DiskWriteBytes: diskWriteBytes, DiskWriteValid: diskWriteValid,
		})
	}
	if len(processes) == 0 {
		return nil, "libproc", fmt.Errorf("native process inventory contained no rows")
	}
	return processes, "libproc", nil
}

// refreshNativeProcessCPUTotals takes the second half of the first-inventory
// CPU delta without repeating sysctl enumeration, argv classification, RSS,
// or executable-path work. The rusage start identity prevents a PID reused in
// the warm-up window from inheriting the previous process's CPU counter.
func refreshNativeProcessCPUTotals(ctx context.Context, processes []Process) []Process {
	refreshed := append([]Process(nil), processes...)
	for index := range refreshed {
		refreshed[index].CPUTotalValid = false
	}
	for index := range refreshed {
		if index%64 == 0 {
			select {
			case <-ctx.Done():
				return refreshed
			default:
			}
		}
		process := &refreshed[index]
		if process.PID <= 0 || process.CPUStartID == 0 {
			continue
		}
		var usage darwinRusageInfoV0
		if darwinLibproc.pidRusage(int32(process.PID), darwinRusageInfoVersion0, &usage) != 0 || usage.ProcessStart != process.CPUStartID || usage.UserTime > ^uint64(0)-usage.SystemTime {
			continue
		}
		process.CPUTotalNS = usage.UserTime + usage.SystemTime
		process.CPUTotalValid = true
	}
	return refreshed
}

func darwinCString(value []byte) string {
	if index := bytes.IndexByte(value, 0); index >= 0 {
		return string(value[:index])
	}
	return string(value)
}

func darwinAgentIdentity(pid int, comm string, argsReader *darwinProcessArgsReader, catalog *AgentIdentityCatalog) (agent, executable, sessionID string) {
	if catalog == nil {
		catalog = ActiveAgentIdentityCatalog()
	}
	lower := strings.ToLower(filepath.Base(comm))
	if agent, executable, ok := catalog.MatchExactBasename(lower); ok {
		args, err := argsReader.args(pid)
		if err != nil {
			args = nil
		}
		return agent, executable, sessionIDFromArgs(args)
	}
	if lower == "node" {
		args, err := argsReader.args(pid)
		if err != nil {
			return "", "", ""
		}
		agent, executable, sessionID = darwinAgentIdentityFromNodeArgs(catalog, args)
		return agent, executable, sessionID
	}
	// Claude SemVer p_comm / Grok versioned p_comm: resolve path only for those
	// shapes so we do not pay one path syscall per process.
	if catalog.NeedsPathProbe(lower) {
		home, _ := os.UserHomeDir()
		if agent, executable, ok := catalog.MatchPath(darwinProcessPath(pid), home); ok {
			return agent, executable, ""
		}
		return "", "", ""
	}
	return "", "", ""
}

func darwinProcessPath(pid int) string {
	if pid <= 0 || darwinLibproc.pidPath == nil {
		return ""
	}
	var buffer [4096]byte
	written := darwinLibproc.pidPath(int32(pid), &buffer[0], uint32(len(buffer)))
	if written <= 0 || int(written) > len(buffer) {
		return ""
	}
	return strings.TrimRight(string(buffer[:written]), "\x00")
}

// darwinAgentIdentityFromPath is retained for tests and path-only diagnostics.
func darwinAgentIdentityFromPath(path, home string) (agent, executable, sessionID string) {
	agent, executable, ok := ActiveAgentIdentityCatalog().MatchPath(path, home)
	if !ok {
		return "", "", ""
	}
	return agent, executable, ""
}

func darwinAgentIdentityFromArgs(lower string, args []string) (agent, executable, sessionID string) {
	return darwinAgentIdentityFromArgsWithCatalog(lower, args, ActiveAgentIdentityCatalog())
}

func darwinAgentIdentityFromArgsWithCatalog(lower string, args []string, catalog *AgentIdentityCatalog) (agent, executable, sessionID string) {
	if catalog == nil {
		catalog = ActiveAgentIdentityCatalog()
	}
	if agent, executable, ok := catalog.MatchExactBasename(lower); ok {
		return agent, executable, sessionIDFromArgs(args)
	}
	if lower == "node" {
		return darwinAgentIdentityFromNodeArgs(catalog, args)
	}
	return "", "", ""
}

func darwinAgentIdentityFromNodeArgs(catalog *AgentIdentityCatalog, args []string) (agent, executable, sessionID string) {
	// For node, only the first positional script may establish identity.
	// Scanning every argument could mistake prompt text ending in "codex" for
	// an agent root and eventually make it eligible for automatic relief.
	start := 0
	if len(args) > 0 && strings.EqualFold(filepath.Base(args[0]), "node") {
		start = 1
	}
	for index := start; index < len(args) && index < start+4; index++ {
		if strings.HasPrefix(args[index], "-") {
			continue
		}
		candidate := filepath.Base(args[index])
		if agent, executable, ok := catalog.MatchNodeScript(candidate); ok {
			return agent, executable, sessionIDFromArgs(args)
		}
		if agent, executable, ok := catalog.MatchExactBasename(candidate); ok {
			return agent, executable, sessionIDFromArgs(args)
		}
		break
	}
	return "", "", ""
}

func sessionIDFromArgs(args []string) string {
	for _, arg := range args {
		if sessionID := sessionIDPattern.FindString(arg); sessionID != "" {
			return sessionID
		}
	}
	return ""
}

const (
	darwinKernProcArgs2               = 49
	darwinProcessArgsMaxBytes         = 4 << 20
	darwinProcessArgsMaxRetainedCount = 64
	darwinProcessArgMaxRetainedBytes  = 4 << 10
	darwinProcessArgsMaxRetainedBytes = 32 << 10
)

// darwinProcessArgsReader reuses one sysctl buffer across a complete process
// inventory. unix.SysctlRaw allocates the kernel-reported ARG_MAX buffer for
// every PID; dozens of agent-like processes with long launch arguments can
// otherwise expand the resident guard heap beyond its 30 MiB authority budget.
type darwinProcessArgsReader struct {
	buffer []byte
}

func (reader *darwinProcessArgsReader) args(pid int) ([]string, error) {
	if reader == nil {
		return nil, fmt.Errorf("darwin process argument reader is nil")
	}
	if pid <= 0 || pid > 1<<31-1 {
		return nil, fmt.Errorf("invalid process pid %d", pid)
	}
	if err := loadDarwinLibproc(); err != nil {
		return nil, err
	}
	mib := [3]int32{int32(unix.CTL_KERN), darwinKernProcArgs2, int32(pid)}
	required := uintptr(0)
	if darwinLibproc.sysctl(&mib[0], uint32(len(mib)), nil, &required, nil, 0) != 0 {
		return nil, fmt.Errorf("size kern.procargs2 for pid %d", pid)
	}
	if required < 5 || required > darwinProcessArgsMaxBytes {
		return nil, fmt.Errorf("invalid kern.procargs2 size %d for pid %d", required, pid)
	}
	if uintptr(cap(reader.buffer)) < required {
		reader.buffer = make([]byte, required)
	} else {
		reader.buffer = reader.buffer[:required]
	}
	written := required
	if darwinLibproc.sysctl(&mib[0], uint32(len(mib)), unsafe.Pointer(&reader.buffer[0]), &written, nil, 0) != 0 {
		return nil, fmt.Errorf("read kern.procargs2 for pid %d", pid)
	}
	if written < 5 || written > uintptr(len(reader.buffer)) {
		return nil, fmt.Errorf("invalid kern.procargs2 response size %d for pid %d", written, pid)
	}
	return parseDarwinProcessArgs(reader.buffer[:written])
}

// parseDarwinProcessArgs scans the complete kernel response but retains only a
// small identity projection. Agent prompts can be multi-megabyte argv entries;
// copying every argument for every agent-like process made the resident's RSS
// scale with prompt volume even though classification needs only the launch
// prefix and an optional session UUID.
func parseDarwinProcessArgs(body []byte) ([]string, error) {
	if len(body) < 5 {
		return nil, fmt.Errorf("short kern.procargs2 response")
	}
	argc := int(binary.LittleEndian.Uint32(body[:4]))
	if argc < 1 || argc > 16384 {
		return nil, fmt.Errorf("invalid kern.procargs2 argc %d", argc)
	}
	index := 4
	for index < len(body) && body[index] != 0 {
		index++
	}
	for index < len(body) && body[index] == 0 {
		index++
	}
	args := make([]string, 0, min(argc+1, darwinProcessArgsMaxRetainedCount+1))
	retainedBytes := 0
	sessionID := ""
	for argIndex := 0; argIndex < argc && index < len(body); argIndex++ {
		end := index
		for end < len(body) && body[end] != 0 {
			end++
		}
		if end > index {
			raw := body[index:end]
			if sessionID == "" {
				if match := findDarwinSessionID(raw); len(match) > 0 {
					sessionID = string(match)
				}
			}
			if len(args) < darwinProcessArgsMaxRetainedCount && retainedBytes < darwinProcessArgsMaxRetainedBytes {
				keep := min(len(raw), darwinProcessArgMaxRetainedBytes, darwinProcessArgsMaxRetainedBytes-retainedBytes)
				projected := raw[:keep]
				// The executable and early node script are path-shaped; if either
				// is exceptionally long, retain its basename-bearing suffix.
				if argIndex < 4 && keep < len(raw) {
					projected = raw[len(raw)-keep:]
				}
				args = append(args, string(projected))
				retainedBytes += keep
			}
		}
		index = end + 1
	}
	if sessionID != "" {
		args = append(args, sessionID)
	}
	return args, nil
}

func findDarwinSessionID(body []byte) []byte {
	const uuidBytes = 36
	for searchAt := 8; searchAt < len(body); {
		offset := bytes.IndexByte(body[searchAt:], '-')
		if offset < 0 {
			return nil
		}
		firstDash := searchAt + offset
		start := firstDash - 8
		if start >= 0 && start+uuidBytes <= len(body) {
			candidate := body[start : start+uuidBytes]
			if validDarwinSessionID(candidate) &&
				(start == 0 || !darwinASCIIWord(body[start-1])) &&
				(start+uuidBytes == len(body) || !darwinASCIIWord(body[start+uuidBytes])) {
				return candidate
			}
		}
		searchAt = firstDash + 1
	}
	return nil
}

func validDarwinSessionID(candidate []byte) bool {
	if len(candidate) != 36 || candidate[8] != '-' || candidate[13] != '-' || candidate[18] != '-' || candidate[23] != '-' {
		return false
	}
	for index, value := range candidate {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !((value >= '0' && value <= '9') || (value >= 'a' && value <= 'f') || (value >= 'A' && value <= 'F')) {
			return false
		}
	}
	return true
}

func darwinASCIIWord(value byte) bool {
	return (value >= '0' && value <= '9') || (value >= 'A' && value <= 'Z') || (value >= 'a' && value <= 'z') || value == '_'
}

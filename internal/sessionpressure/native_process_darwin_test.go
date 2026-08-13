//go:build darwin

package sessionpressure

import (
	"context"
	"encoding/binary"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNativePhysicalAndSwapMemoryProbes(t *testing.T) {
	physicalMB, err := nativePhysicalMemoryMB()
	if err != nil || physicalMB < 1024 {
		t.Fatalf("native physical memory=%.1fMB err=%v", physicalMB, err)
	}
	body := make([]byte, 32)
	binary.LittleEndian.PutUint64(body[16:24], 1536*bytesPerMiB)
	if swapMB, err := darwinSwapUsedMB(body); err != nil || swapMB != 1536 {
		t.Fatalf("parsed swap=%.1fMB err=%v", swapMB, err)
	}
	if _, err := darwinSwapUsedMB(body[:16]); err == nil {
		t.Fatal("short native swap structure unexpectedly parsed")
	}
	if swapMB, err := nativeSwapUsedMB(); err != nil || swapMB < 0 {
		t.Fatalf("native live swap=%.1fMB err=%v", swapMB, err)
	}
}

func TestNativeHostCPUTicksAdvance(t *testing.T) {
	first, err := nativeHostCPUTicks()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		second, sampleErr := nativeHostCPUTicks()
		if sampleErr != nil {
			t.Fatal(sampleErr)
		}
		if percent, ok := hostCPUPercentBetween(first, second); ok {
			if percent < 0 || percent > 100 {
				t.Fatalf("native host CPU percent=%.1f", percent)
			}
			return
		}
	}
	t.Fatalf("native host CPU counters did not advance from %+v", first)
}

func TestDarwinNodeIdentityDoesNotScanPromptArguments(t *testing.T) {
	if agent, _, _ := darwinAgentIdentityFromArgs("node", []string{"node", "/tmp/server.js", "please", "inspect", "codex"}); agent != "" {
		t.Fatalf("prompt token was classified as an agent: %q", agent)
	}
	if agent, executable, _ := darwinAgentIdentityFromArgs("node", []string{"node", "--no-warnings", "/opt/bin/codex", "resume"}); agent != "codex" || executable != "codex" {
		t.Fatalf("node agent script was not recognized: agent=%q executable=%q", agent, executable)
	}
}

func TestDarwinClaudeVersionedExecutableIdentityUsesPathOnly(t *testing.T) {
	agent, executable, sessionID := darwinAgentIdentityFromPath("/Users/nico/.local/share/claude/versions/2.1.211", "/Users/nico")
	if agent != "claude" || executable != "claude" || sessionID != "" {
		t.Fatalf("versioned Claude path identity=%q %q %q", agent, executable, sessionID)
	}
	for _, path := range []string{
		"/tmp/claude/versions/2.1.211",
		"/Users/nico/.local/share/not-claude/versions/2.1.211",
		"/Users/nico/.local/share/claude/versions-helper/2.1.211",
	} {
		if agent, _, _ := darwinAgentIdentityFromPath(path, "/Users/nico"); agent != "" {
			t.Fatalf("untrusted path %q classified as %q", path, agent)
		}
	}
	for _, value := range []string{"2.1.211", "1.0", "2026.07.16"} {
		if !isVersionExecutable(value) {
			t.Fatalf("version executable %q rejected", value)
		}
	}
	for _, value := range []string{"claude", "2.1.211-beta", "2..1", ".2"} {
		if isVersionExecutable(value) {
			t.Fatalf("non-version executable %q accepted", value)
		}
	}
}

func TestDarwinGrokVersionedExecutableIdentityUsesPathOnly(t *testing.T) {
	for _, path := range []string{
		"/Users/nico/.grok/downloads/grok-0.2.118-macos-aarch64",
		"/Users/nico/.grok/bin/grok",
		"/Users/nico/.grok/bin/agent",
	} {
		agent, executable, sessionID := darwinAgentIdentityFromPath(path, "/Users/nico")
		if agent != "grok" || executable != "grok" || sessionID != "" {
			t.Fatalf("versioned Grok path %q identity=%q %q %q", path, agent, executable, sessionID)
		}
	}
	for _, path := range []string{
		"/tmp/grok/downloads/grok-0.2.118-macos-aarch64",
		"/Users/nico/.grok/downloads/not-grok-helper",
		"/Users/nico/.grok/cache/grok-0.2.118-macos-aarch64",
		"/Users/nico/.grok/bin/grok-helper",
		"/Users/nico/.other/bin/grok",
	} {
		if agent, _, _ := darwinAgentIdentityFromPath(path, "/Users/nico"); agent != "" {
			t.Fatalf("untrusted path %q classified as %q", path, agent)
		}
	}
	catalog := CompileAgentIdentityCatalog(DefaultAgentIdentityRules())
	for _, value := range []string{"grok-0.2.118-mac", "grok-0.2.118-macos-aarch64", "GROK-1.0.0-linux-x64"} {
		if !catalog.NeedsPathProbe(value) || catalog.agentForPathProbeBasename(value) != "grok" {
			t.Fatalf("Grok version executable %q rejected", value)
		}
	}
	for _, value := range []string{"grok", "grok-helper", "grok-", "agent", "codex"} {
		if catalog.agentForPathProbeBasename(value) == "grok" && value != "grok" {
			t.Fatalf("non-Grok-version executable %q accepted as path-probe grok", value)
		}
		if value != "grok" && catalog.NeedsPathProbe(value) && catalog.agentForPathProbeBasename(value) != "" {
			t.Fatalf("non-Grok-version executable %q unexpectedly path-probes", value)
		}
	}
}

func TestNativeProcessInventoryIsFastAndIncludesSelf(t *testing.T) {
	deadline := 2 * time.Second
	maxElapsed := time.Second
	if raceDetectorEnabled {
		// The race runtime instruments every Go access around the native scan;
		// preserve the production latency assertion in normal builds while
		// giving the race-only correctness run enough wall-clock headroom.
		deadline = 4 * time.Second
		maxElapsed = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	started := time.Now()
	processes, source, err := nativeProcesses(ctx)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	if source != "libproc" || len(processes) < 10 {
		t.Fatalf("native inventory source=%q rows=%d", source, len(processes))
	}
	foundSelf := false
	for _, process := range processes {
		if process.PID == os.Getpid() {
			foundSelf = process.RSSKB > 0 && process.Executable != ""
			break
		}
	}
	if !foundSelf {
		t.Fatal("native inventory omitted current process or its RSS")
	}
	if elapsed > maxElapsed {
		t.Fatalf("native inventory took %s; expected <= %s", elapsed, maxElapsed)
	}
	t.Logf("native inventory: rows=%d elapsed=%s", len(processes), elapsed)
}

func TestNativeDiskWriteCountersAreBoundedAndIncludeSelf(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	device, err := nativeDiskDeviceCounter(ctx)
	if err != nil {
		t.Fatal(err)
	}
	processes, err := nativeDiskProcessCounters(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if device.BytesWritten == 0 || device.DeviceCount < 1 || device.Identity == "" || device.Source != "iokit.IOBlockStorageDriver" {
		t.Fatalf("invalid native device counter: %+v", device)
	}
	if processes.AccessibleCount < 10 || processes.AccessibleCount > processes.TotalPIDCount {
		t.Fatalf("invalid native process coverage: %+v", processes)
	}
	foundSelf := false
	for _, process := range processes.Counters {
		if process.PID == os.Getpid() {
			foundSelf = process.StartID != 0 && process.Executable != ""
			break
		}
	}
	if !foundSelf {
		t.Fatal("native disk-write scan omitted current process")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("native disk-write scan took %s; expected <=500ms", elapsed)
	} else {
		t.Logf("native disk-write scan: devices=%d pids=%d/%d elapsed=%s", device.DeviceCount, processes.AccessibleCount, processes.TotalPIDCount, elapsed)
	}
}

func TestDarwinProcessArgsReaderReusesBoundedSysctlBuffer(t *testing.T) {
	reader := &darwinProcessArgsReader{}
	first, err := reader.args(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 || len(reader.buffer) < 5 || cap(reader.buffer) > darwinProcessArgsMaxBytes {
		t.Fatalf("args=%q buffer_len=%d buffer_cap=%d", first, len(reader.buffer), cap(reader.buffer))
	}
	address := &reader.buffer[0]
	second, err := reader.args(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if len(second) == 0 || &reader.buffer[0] != address {
		t.Fatalf("second args=%q buffer was reallocated", second)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range 64 {
		if _, err := reader.args(os.Getpid()); err != nil {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&after)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 8<<20 {
		t.Fatalf("64 reused argument reads allocated %d bytes", allocated)
	}
}

func TestParseDarwinProcessArgsBoundsPromptRetention(t *testing.T) {
	const sessionID = "019f5be0-7d38-7271-ba7d-8ade4a407bf0"
	argc := 96
	body := make([]byte, 4)
	binary.LittleEndian.PutUint32(body, uint32(argc))
	body = append(body, []byte("/opt/bin/codex")...)
	body = append(body, 0, 0)
	for index := 0; index < argc; index++ {
		arg := []byte("ordinary-argument")
		if index == 0 {
			arg = []byte("codex")
		} else if index == argc-1 {
			arg = []byte(strings.Repeat("x", 96<<10) + " " + sessionID)
		}
		body = append(body, arg...)
		body = append(body, 0)
	}

	args, err := parseDarwinProcessArgs(body)
	if err != nil {
		t.Fatal(err)
	}
	retained := 0
	for _, arg := range args {
		retained += len(arg)
	}
	if retained > darwinProcessArgsMaxRetainedBytes+len(sessionID) {
		t.Fatalf("retained %d argument bytes", retained)
	}
	if len(args) > darwinProcessArgsMaxRetainedCount+1 {
		t.Fatalf("retained %d arguments", len(args))
	}
	agent, executable, gotSessionID := darwinAgentIdentityFromArgs("codex", args)
	if agent != "codex" || executable != "codex" || gotSessionID != sessionID {
		t.Fatalf("identity=(%q,%q,%q)", agent, executable, gotSessionID)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range 64 {
		if _, err := parseDarwinProcessArgs(body); err != nil {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&after)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 8<<20 {
		t.Fatalf("64 bounded long-prompt parses allocated %d bytes", allocated)
	}
}

func TestFindDarwinSessionIDPreservesUUIDWordBoundaries(t *testing.T) {
	const sessionID = "019f5be0-7d38-7271-ba7d-8ade4a407bf0"
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "standalone", body: "resume " + sessionID + " now", want: sessionID},
		{name: "uppercase", body: strings.ToUpper(sessionID), want: strings.ToUpper(sessionID)},
		{name: "word prefix", body: "x" + sessionID},
		{name: "word suffix", body: sessionID + "_x"},
		{name: "invalid hex", body: "019f5be0-7d38-7271-ba7d-8ade4a407bfz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(findDarwinSessionID([]byte(tc.body))); got != tc.want {
				t.Fatalf("findDarwinSessionID(%q)=%q want %q", tc.body, got, tc.want)
			}
		})
	}
}

func TestNativeProcessCPUUsesLiveCumulativeDelta(t *testing.T) {
	timeout := 2 * time.Second
	if raceDetectorEnabled {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	first, _, err := nativeProcesses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	firstAt := time.Now()
	deadline := firstAt.Add(125 * time.Millisecond)
	var work uint64
	for time.Now().Before(deadline) {
		work = work*1664525 + 1013904223
	}
	second := refreshNativeProcessCPUTotals(ctx, first)
	secondAt := time.Now()
	annotateProcessCPUPercent(second, first, secondAt.Sub(firstAt))
	for _, process := range second {
		if process.PID != os.Getpid() {
			continue
		}
		if !process.CPUAvailable || process.CPUPercent <= 0 {
			t.Fatalf("live native CPU evidence unavailable or zero after busy interval: %+v work=%d", process, work)
		}
		return
	}
	t.Fatal("native CPU delta omitted current process")
}

func TestNativeSamplerBuildsAgentTreesWithinOperatorBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	snapshot, err := NewSampler().Sample(ctx, DefaultPolicy(16*1024))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ProcessInventorySource != "libproc" || !snapshot.ProcessInventoryFresh {
		t.Fatalf("unexpected native inventory projection: %+v", snapshot)
	}
	if !snapshot.HostCPUAvailable || (snapshot.HostCPUSource != "mach-host-statistics" && snapshot.HostCPUSource != "process-inventory-fallback") {
		t.Fatalf("unexpected native host CPU projection: %+v", snapshot)
	}
	// Host aggregate path always applies.
	if snapshot.ProcessCount < 10 {
		t.Fatalf("native sample omitted host process aggregate: %+v", snapshot)
	}
	// Agent trees are host-state dependent (no Claude/Codex tree running).
	// Keep the budget assertion when trees exist; skip with explicit reason
	// when the host currently has none (KEP follow-up slice 4).
	if snapshot.AgentTreeCount < 1 || snapshot.AgentRSSSumMB <= 0 {
		t.Skipf("no agent trees on host (AgentTreeCount=%d AgentRSSSumMB=%v); host inventory still healthy", snapshot.AgentTreeCount, snapshot.AgentRSSSumMB)
	}
	maxDurationMS := 1500.0
	if raceDetectorEnabled {
		maxDurationMS = 2000
	}
	if snapshot.SampleDurationMS > maxDurationMS {
		t.Fatalf("native sample took %.3fms; expected <= %.0fms", snapshot.SampleDurationMS, maxDurationMS)
	}
	t.Logf("native sample: processes=%d trees=%d duration=%.3fms cpu=%.3fms", snapshot.ProcessCount, snapshot.AgentTreeCount, snapshot.SampleDurationMS, snapshot.SampleCPUTimeMS)
}

func TestResidentSampler64SampleCPUAndLatencySoak(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	// This is the healthy-cadence soak. Keep live agent RSS on the developer's
	// machine from intentionally activating pressure-refresh behavior.
	policy.Thresholds.AgentTotalWarningMB = 1 << 50
	policy.Thresholds.TreeWarningMB = 1 << 50
	sampler := NewResidentSampler()
	var ticks uint64 = 1000
	sampler.hostCPUSource = func() (hostCPUTicks, error) {
		ticks += 100
		return hostCPUTicks{Busy: ticks / 10, Total: ticks}, nil
	}
	stats := monitorStats{}
	lastProcessCPU := 0.0
	latest := Snapshot{}
	freshInventories := 0
	for index := 0; index < monitorStatsWindow; index++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		snapshot, err := sampler.Sample(ctx, policy)
		cancel()
		if err != nil {
			t.Fatalf("sample %d: %v", index+1, err)
		}
		if snapshot.ProcessInventoryFresh {
			freshInventories++
		}
		snapshot.intervalCPUTimeMS = snapshot.SampleCPUTimeMS
		if lastProcessCPU > 0 && snapshot.processCPUTotalMS >= lastProcessCPU {
			snapshot.intervalCPUTimeMS = snapshot.processCPUTotalMS - lastProcessCPU
		}
		lastProcessCPU = snapshot.processCPUTotalMS
		stats.add(snapshot, float64(policy.SampleIntervalSeconds))
		latest = snapshot
	}
	stats.apply(&latest, policy)
	if freshInventories != 1 {
		t.Fatalf("healthy 64-sample soak refreshed process inventory %d times, want 1", freshInventories)
	}
	maxDurationMS := 300.0
	if raceDetectorEnabled {
		maxDurationMS = 750
	}
	if latest.SampleDurationP95MS > maxDurationMS || latest.SampleCPUTimeP95MS > 50 || latest.GuardIdleCPUDutyPercent > policy.ResourceBudgets.MaxIdleCPUPercent {
		t.Fatalf("64-sample overhead budget failed: duration_p95=%.3fms cpu_p95=%.3fms idle_duty=%.4f%% rss_peak=%.3fMB",
			latest.SampleDurationP95MS, latest.SampleCPUTimeP95MS, latest.GuardIdleCPUDutyPercent, latest.GuardRSSMaxMB)
	}
	t.Logf("64-sample soak: duration_p95=%.3fms cpu_p95=%.3fms idle_duty=%.4f%% rss_peak=%.3fMB",
		latest.SampleDurationP95MS, latest.SampleCPUTimeP95MS, latest.GuardIdleCPUDutyPercent, latest.GuardRSSMaxMB)
}

func TestResidentSamplerRepeatedFreshInventoriesStayWithinRSSBudget(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	policy.Thresholds.AgentTotalWarningMB = 1 << 50
	policy.Thresholds.TreeWarningMB = 1 << 50
	sampler := NewResidentSampler()
	// Exercise repeated synchronous inventory allocation directly. Healthy
	// resident heartbeats schedule this refresh asynchronously; the operator
	// role keeps this RSS regression focused on the native inventory itself.
	sampler.role = "operator"
	now := time.Now().UTC()
	sampler.now = func() time.Time { return now }
	var ticks uint64 = 1000
	sampler.hostCPUSource = func() (hostCPUTicks, error) {
		ticks += 100
		return hostCPUTicks{Busy: ticks / 10, Total: ticks}, nil
	}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	maxRSSMB := 0.0
	inventories := 24
	deadline := 2 * time.Second
	if raceDetectorEnabled {
		// RSS is intentionally not evaluated under the race runtime below. A
		// smaller repeated sample still exercises the native/sampler boundary
		// for races without multiplying instrumentation cost into timeouts.
		inventories = 6
		deadline = 5 * time.Second
	}
	for index := 0; index < inventories; index++ {
		now = now.Add(time.Duration(policy.ProcessInventoryIntervalSeconds+1) * time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), deadline)
		snapshot, err := sampler.Sample(ctx, policy)
		cancel()
		if err != nil {
			t.Fatalf("fresh inventory %d: %v", index+1, err)
		}
		if !snapshot.ProcessInventoryFresh {
			t.Fatalf("inventory %d was unexpectedly reused", index+1)
		}
		maxRSSMB = max(maxRSSMB, snapshot.GuardRSSMB)
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	t.Logf("%d fresh inventories: rss_max=%.3fMB heap_alloc=%.3fMB heap_inuse=%.3fMB heap_sys=%.3fMB total_alloc_delta=%.3fMB",
		inventories,
		maxRSSMB,
		float64(after.HeapAlloc)/(1<<20),
		float64(after.HeapInuse)/(1<<20),
		float64(after.HeapSys)/(1<<20),
		float64(after.TotalAlloc-before.TotalAlloc)/(1<<20))
	if !raceDetectorEnabled && maxRSSMB > policy.ResourceBudgets.MaxSelfRSSMB {
		t.Fatalf("repeated fresh inventory RSS %.3fMB exceeds %.3fMB resident budget", maxRSSMB, policy.ResourceBudgets.MaxSelfRSSMB)
	}
}

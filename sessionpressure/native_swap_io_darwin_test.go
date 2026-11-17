//go:build darwin

package sessionpressure

import (
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestDecodeDarwinSwapIOUsesVMStatistics64Offsets(t *testing.T) {
	body := make([]byte, darwinHostVMInfo64Count*4)
	binary.LittleEndian.PutUint64(body[darwinVMStatsSwapIns:], 3)
	binary.LittleEndian.PutUint64(body[darwinVMStatsSwapOuts:], 5)
	got, err := decodeDarwinSwapIO(body, 16384)
	if err != nil || got.SwapInBytes != 3*16384 || got.SwapOutBytes != 5*16384 {
		t.Fatalf("decode=%+v err=%v", got, err)
	}
	if _, err := decodeDarwinSwapIO(body[:100], 16384); err == nil {
		t.Fatal("short buffer decoded")
	}
	if _, err := decodeDarwinSwapIO(body, 0); err == nil {
		t.Fatal("zero page size decoded")
	}
}

// The live kernel counter is monotonic, so vm_stat's reading must fall between
// two native reads taken around it. This proves the struct offsets and page
// unit against Apple's own tool rather than against our own decoder.
func TestNativeSwapIOCounterBracketsVMStat(t *testing.T) {
	before, err := nativeSwapIOCounter(context.Background())
	if err != nil {
		t.Fatalf("native swap counter: %v", err)
	}
	output, err := exec.Command("/usr/bin/vm_stat").Output()
	if err != nil {
		t.Skipf("vm_stat unavailable: %v", err)
	}
	after, err := nativeSwapIOCounter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pageSize := regexp.MustCompile(`page size of (\d+) bytes`).FindSubmatch(output)
	swapOuts := regexp.MustCompile(`Swapouts:\s+(\d+)\.`).FindSubmatch(output)
	swapIns := regexp.MustCompile(`Swapins:\s+(\d+)\.`).FindSubmatch(output)
	if pageSize == nil || swapOuts == nil || swapIns == nil {
		t.Skipf("unrecognized vm_stat output")
	}
	page, _ := strconv.ParseUint(string(pageSize[1]), 10, 64)
	outPages, _ := strconv.ParseUint(string(swapOuts[1]), 10, 64)
	inPages, _ := strconv.ParseUint(string(swapIns[1]), 10, 64)
	if out := outPages * page; out < before.SwapOutBytes || out > after.SwapOutBytes {
		t.Fatalf("vm_stat swapouts %d outside native bracket [%d,%d]", out, before.SwapOutBytes, after.SwapOutBytes)
	}
	if in := inPages * page; in < before.SwapInBytes || in > after.SwapInBytes {
		t.Fatalf("vm_stat swapins %d outside native bracket [%d,%d]", in, before.SwapInBytes, after.SwapInBytes)
	}
}

// kern.memorystatus_level is the value memory_pressure -Q prints. Bracket the
// subprocess between two native reads so live movement cannot flake the test.
func TestNativeFreePercentMatchesMemoryPressure(t *testing.T) {
	before, err := nativeFreePercent()
	if err != nil {
		t.Fatalf("native free percent: %v", err)
	}
	output, err := exec.Command("/usr/bin/memory_pressure", "-Q").Output()
	if err != nil {
		t.Skipf("memory_pressure unavailable: %v", err)
	}
	after, err := nativeFreePercent()
	if err != nil {
		t.Fatal(err)
	}
	tool, err := parseFreePercent(string(output))
	if err != nil {
		t.Fatal(err)
	}
	if tool < min(before, after) || tool > max(before, after) {
		t.Fatalf("memory_pressure=%d outside native bracket [%d,%d]", tool, before, after)
	}
}

// rusage CPU totals are mach ticks. A process spinning on one core must read
// as roughly one core of CPU time per wall second, not 1/41.7 of it.
func TestNativeRusageCPUTimeIsNanoseconds(t *testing.T) {
	if err := loadDarwinLibproc(); err != nil {
		t.Fatal(err)
	}
	// Compare libproc's converted CPU time of this process with getrusage,
	// which reports the same quantity in microseconds. Both read this
	// process's own CPU, so host load changes how long the spin takes, not
	// the ratio. The unconverted Apple Silicon tick count reads about 0.024.
	pid := int32(os.Getpid())
	libproc := func() uint64 {
		var usage darwinRusageInfoV0
		if darwinLibproc.pidRusage(pid, darwinRusageInfoVersion0, &usage) != 0 {
			t.Fatal("proc_pid_rusage failed")
		}
		total, ok := darwinRusageCPUTimeNS(usage.UserTime, usage.SystemTime)
		if !ok {
			t.Fatal("cpu total overflow")
		}
		return total
	}
	getrusage := func() int64 {
		var usage syscall.Rusage
		if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
			t.Fatal(err)
		}
		return usage.Utime.Nano() + usage.Stime.Nano()
	}
	beforeLibproc, beforeRusage := libproc(), getrusage()
	var work uint64
	for getrusage()-beforeRusage < int64(200*time.Millisecond) {
		for range 1 << 16 {
			work = work*1664525 + 1013904223
		}
	}
	ratio := float64(libproc()-beforeLibproc) / float64(getrusage()-beforeRusage)
	if ratio < 0.5 || ratio > 2 {
		t.Fatalf("libproc CPU / getrusage CPU = %.3f (work=%d); rusage unit conversion is wrong", ratio, work)
	}
}

func TestDarwinMachTicksToNS(t *testing.T) {
	for _, test := range []struct {
		ticks        uint64
		numer, denom uint32
		want         uint64
	}{
		{24_000_000, 125, 3, 1_000_000_000}, // Apple Silicon: 24 MHz ticks
		{1_000, 1, 1, 1_000},                // Intel: ticks are ns
		{7, 0, 0, 7},                        // unknown timebase never inflates
		{^uint64(0), 125, 3, ^uint64(0)},    // saturates instead of wrapping
	} {
		if got := darwinMachTicksToNS(test.ticks, test.numer, test.denom); got != test.want {
			t.Fatalf("ticks=%d %d/%d got=%d want=%d", test.ticks, test.numer, test.denom, got, test.want)
		}
	}
}

// A sleeping group must read as idle and a spinning group as working, with
// reaped children counted, so parking never releases weight held by work.
// The busy case is condition-driven: it waits for an amount of CPU, not for a
// wall-clock window, so a loaded host only makes it slower.
func TestNativeProcessGroupCPUSeparatesIdleFromWork(t *testing.T) {
	start := func(script string, env ...string) *exec.Cmd {
		command := exec.Command("/bin/sh", "-c", script)
		command.Env = append(os.Environ(), env...)
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := command.Start(); err != nil {
			t.Skipf("shell unavailable: %v", err)
		}
		t.Cleanup(func() { _ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL); _ = command.Wait() })
		return command
	}
	activity := func(pgid int) uint64 {
		t.Helper()
		got, err := nativeProcessGroupActivity(pgid)
		if err != nil {
			t.Fatal(err)
		}
		return got.CPUNS
	}

	idle := start("sleep 600")
	before, began := activity(idle.Process.Pid), time.Now()
	time.Sleep(600 * time.Millisecond)
	if cores := float64(activity(idle.Process.Pid)-before) / float64(time.Since(began).Nanoseconds()); cores >= workIdleMaxCores {
		t.Fatalf("sleeping group used %.4f cores", cores)
	}

	// A child that spins and is reaped mid-window still counts as work.
	dir := t.TempDir()
	stop, reaped := dir+"/stop", dir+"/reaped"
	busy := start(`/usr/bin/yes >/dev/null & Y=$!
while [ ! -e "$SP_STOP" ]; do sleep 0.01; done
kill $Y; wait $Y; : > "$SP_REAPED"; sleep 600`, "SP_STOP="+stop, "SP_REAPED="+reaped)
	base := activity(busy.Process.Pid)
	spun := base
	deadline := time.Now().Add(testWatchdog)
	for spun-base < uint64(100*time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("spinning group read %s of CPU; units wrong or spinner never ran", time.Duration(spun-base))
		}
		time.Sleep(10 * time.Millisecond)
		spun = activity(busy.Process.Pid)
	}
	if err := os.WriteFile(stop, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitForTestFile(t, reaped)
	if after := activity(busy.Process.Pid); after < spun {
		t.Fatalf("group CPU fell from %s to %s after the spinner was reaped; reaped child time lost", time.Duration(spun-base), time.Duration(after-base))
	}
	if _, err := nativeProcessGroupActivity(1 << 30); err == nil {
		t.Fatal("nonexistent process group sampled")
	}
}

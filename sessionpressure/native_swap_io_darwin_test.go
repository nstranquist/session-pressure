//go:build darwin

package sessionpressure

import (
	"context"
	"encoding/binary"
	"os/exec"
	"regexp"
	"strconv"
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
	spinner := exec.Command("/usr/bin/yes")
	if err := spinner.Start(); err != nil {
		t.Skipf("spinner unavailable: %v", err)
	}
	defer func() { _ = spinner.Process.Kill(); _ = spinner.Wait() }()
	read := func() uint64 {
		var usage darwinRusageInfoV0
		if darwinLibproc.pidRusage(int32(spinner.Process.Pid), darwinRusageInfoVersion0, &usage) != 0 {
			t.Fatal("proc_pid_rusage failed")
		}
		total, ok := darwinRusageCPUTimeNS(usage.UserTime, usage.SystemTime)
		if !ok {
			t.Fatal("cpu total overflow")
		}
		return total
	}
	time.Sleep(100 * time.Millisecond)
	before, start := read(), time.Now()
	time.Sleep(500 * time.Millisecond)
	cores := float64(read()-before) / float64(time.Since(start).Nanoseconds())
	// A loaded host can starve the spinner, but never by 40x; the unconverted
	// tick count reads ~0.024 here, so 0.2 cleanly separates the two.
	if cores < 0.2 || cores > 1.3 {
		t.Fatalf("spinner used %.3f cores per wall second; rusage unit conversion is wrong", cores)
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

//go:build darwin

package sessionpressure

import (
	"context"
	"encoding/binary"
	"os/exec"
	"regexp"
	"strconv"
	"testing"
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

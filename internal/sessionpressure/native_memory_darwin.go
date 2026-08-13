//go:build darwin

package sessionpressure

import (
	"encoding/binary"
	"fmt"

	"golang.org/x/sys/unix"
)

const bytesPerMiB = 1024 * 1024

func nativePhysicalMemoryMB() (float64, error) {
	bytes, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0, err
	}
	if bytes == 0 {
		return 0, fmt.Errorf("native hw.memsize returned zero")
	}
	return float64(bytes) / bytesPerMiB, nil
}

func nativeSwapUsedMB() (float64, error) {
	body, err := unix.SysctlRaw("vm.swapusage")
	if err != nil {
		return 0, err
	}
	return darwinSwapUsedMB(body)
}

// xsw_usage begins with xsu_total, xsu_avail, and xsu_used uint64 fields.
// All supported Darwin targets are little-endian; parsing the stable kernel
// structure avoids spawning /usr/sbin/sysctl in every resident sample.
func darwinSwapUsedMB(body []byte) (float64, error) {
	if len(body) < 24 {
		return 0, fmt.Errorf("short vm.swapusage response: %d bytes", len(body))
	}
	usedBytes := binary.LittleEndian.Uint64(body[16:24])
	return float64(usedBytes) / bytesPerMiB, nil
}

//go:build darwin

package sessionpressure

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

// HOST_VM_INFO64 returns struct vm_statistics64: 152 bytes, 38 integer_t.
// swapins and swapouts are cumulative page counts at byte offsets 112/120.
// This is the same kernel counter `vm_stat` prints as Swapins/Swapouts.
const (
	darwinHostVMInfo64      = 4
	darwinHostVMInfo64Count = 38
	darwinVMStatsSwapIns    = 112
	darwinVMStatsSwapOuts   = 120
)

var darwinSwapIOAPI struct {
	once             sync.Once
	err              error
	host             uint32
	pageSize         uint64
	machHostSelf     func() uint32
	hostStatistics64 func(uint32, int32, unsafe.Pointer, *uint32) int32
}

func loadDarwinSwapIOAPI() error {
	darwinSwapIOAPI.once.Do(func() {
		handle, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_LAZY|purego.RTLD_LOCAL)
		if err != nil {
			darwinSwapIOAPI.err = err
			return
		}
		purego.RegisterLibFunc(&darwinSwapIOAPI.machHostSelf, handle, "mach_host_self")
		purego.RegisterLibFunc(&darwinSwapIOAPI.hostStatistics64, handle, "host_statistics64")
		darwinSwapIOAPI.host = darwinSwapIOAPI.machHostSelf()
		if darwinSwapIOAPI.host == 0 {
			darwinSwapIOAPI.err = fmt.Errorf("mach_host_self returned a null port")
			return
		}
		pageSize, err := unix.SysctlUint32("vm.pagesize")
		if err != nil || pageSize == 0 {
			darwinSwapIOAPI.err = fmt.Errorf("vm.pagesize unavailable: %v", err)
			return
		}
		darwinSwapIOAPI.pageSize = uint64(pageSize)
	})
	return darwinSwapIOAPI.err
}

func nativeSwapIOCounter(context.Context) (swapIOCounter, error) {
	if err := loadDarwinSwapIOAPI(); err != nil {
		return swapIOCounter{}, err
	}
	var buffer [darwinHostVMInfo64Count * 4]byte
	count := uint32(darwinHostVMInfo64Count)
	result := darwinSwapIOAPI.hostStatistics64(darwinSwapIOAPI.host, darwinHostVMInfo64, unsafe.Pointer(&buffer[0]), &count)
	if result != 0 || count < darwinHostVMInfo64Count {
		return swapIOCounter{}, fmt.Errorf("host_statistics64 vm_info result=%d count=%d", result, count)
	}
	return decodeDarwinSwapIO(buffer[:], darwinSwapIOAPI.pageSize)
}

func decodeDarwinSwapIO(body []byte, pageSize uint64) (swapIOCounter, error) {
	if len(body) < darwinVMStatsSwapOuts+8 {
		return swapIOCounter{}, fmt.Errorf("short vm_statistics64 response: %d bytes", len(body))
	}
	if pageSize == 0 {
		return swapIOCounter{}, fmt.Errorf("vm page size is zero")
	}
	swapIns := binary.LittleEndian.Uint64(body[darwinVMStatsSwapIns : darwinVMStatsSwapIns+8])
	swapOuts := binary.LittleEndian.Uint64(body[darwinVMStatsSwapOuts : darwinVMStatsSwapOuts+8])
	return swapIOCounter{
		SwapInBytes:  saturatingMulUint64(swapIns, pageSize),
		SwapOutBytes: saturatingMulUint64(swapOuts, pageSize),
	}, nil
}

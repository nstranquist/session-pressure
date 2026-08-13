//go:build darwin

package sessionpressure

import (
	"fmt"
	"sync"

	"github.com/ebitengine/purego"
)

const (
	darwinHostCPULoadInfo      = 3
	darwinHostCPULoadInfoCount = 4
	darwinCPUStateUser         = 0
	darwinCPUStateSystem       = 1
	darwinCPUStateIdle         = 2
	darwinCPUStateNice         = 3
)

var darwinHostCPUAPI struct {
	once           sync.Once
	err            error
	host           uint32
	machHostSelf   func() uint32
	hostStatistics func(uint32, int32, *[darwinHostCPULoadInfoCount]uint32, *uint32) int32
}

func loadDarwinHostCPUAPI() error {
	darwinHostCPUAPI.once.Do(func() {
		handle, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_LAZY|purego.RTLD_LOCAL)
		if err != nil {
			darwinHostCPUAPI.err = err
			return
		}
		purego.RegisterLibFunc(&darwinHostCPUAPI.machHostSelf, handle, "mach_host_self")
		purego.RegisterLibFunc(&darwinHostCPUAPI.hostStatistics, handle, "host_statistics")
		darwinHostCPUAPI.host = darwinHostCPUAPI.machHostSelf()
		if darwinHostCPUAPI.host == 0 {
			darwinHostCPUAPI.err = fmt.Errorf("mach_host_self returned a null port")
		}
	})
	return darwinHostCPUAPI.err
}

func nativeHostCPUTicks() (hostCPUTicks, error) {
	if err := loadDarwinHostCPUAPI(); err != nil {
		return hostCPUTicks{}, err
	}
	var values [darwinHostCPULoadInfoCount]uint32
	count := uint32(darwinHostCPULoadInfoCount)
	result := darwinHostCPUAPI.hostStatistics(
		darwinHostCPUAPI.host,
		darwinHostCPULoadInfo,
		&values,
		&count,
	)
	if result != 0 || count < darwinHostCPULoadInfoCount {
		return hostCPUTicks{}, fmt.Errorf("host_statistics cpu_load result=%d count=%d", result, count)
	}
	busy := uint64(values[darwinCPUStateUser]) + uint64(values[darwinCPUStateSystem]) + uint64(values[darwinCPUStateNice])
	total := busy + uint64(values[darwinCPUStateIdle])
	return hostCPUTicks{Busy: busy, Total: total}, nil
}

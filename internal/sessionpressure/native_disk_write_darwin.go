//go:build darwin

package sessionpressure

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

const (
	darwinCFStringEncodingUTF8 = 0x08000100
	darwinCFNumberSInt64Type   = 4
)

var darwinDiskIOKit struct {
	once sync.Once
	err  error

	serviceMatching            func(*byte) uintptr
	serviceGetMatchingServices func(uint32, uintptr, *uint32) int32
	iteratorNext               func(uint32) uint32
	objectRelease              func(uint32) int32
	registryCreateCFProperty   func(uint32, uintptr, uintptr, uint32) uintptr
	registryGetParentEntry     func(uint32, *byte, *uint32) int32
	registryGetRegistryEntryID func(uint32, *uint64) int32
	cfStringCreateWithCString  func(uintptr, *byte, uint32) uintptr
	cfStringGetCString         func(uintptr, *byte, int64, uint32) bool
	cfDictionaryGetValue       func(uintptr, uintptr) uintptr
	cfNumberGetValue           func(uintptr, int32, unsafe.Pointer) bool
	cfGetTypeID                func(uintptr) uintptr
	cfDictionaryGetTypeID      func() uintptr
	cfNumberGetTypeID          func() uintptr
	cfStringGetTypeID          func() uintptr
	cfRelease                  func(uintptr)
}

func loadDarwinDiskIOKit() error {
	darwinDiskIOKit.once.Do(func() {
		iokit, err := purego.Dlopen("/System/Library/Frameworks/IOKit.framework/IOKit", purego.RTLD_LAZY|purego.RTLD_LOCAL)
		if err != nil {
			darwinDiskIOKit.err = err
			return
		}
		coreFoundation, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_LAZY|purego.RTLD_LOCAL)
		if err != nil {
			darwinDiskIOKit.err = err
			return
		}
		purego.RegisterLibFunc(&darwinDiskIOKit.serviceMatching, iokit, "IOServiceMatching")
		purego.RegisterLibFunc(&darwinDiskIOKit.serviceGetMatchingServices, iokit, "IOServiceGetMatchingServices")
		purego.RegisterLibFunc(&darwinDiskIOKit.iteratorNext, iokit, "IOIteratorNext")
		purego.RegisterLibFunc(&darwinDiskIOKit.objectRelease, iokit, "IOObjectRelease")
		purego.RegisterLibFunc(&darwinDiskIOKit.registryCreateCFProperty, iokit, "IORegistryEntryCreateCFProperty")
		purego.RegisterLibFunc(&darwinDiskIOKit.registryGetParentEntry, iokit, "IORegistryEntryGetParentEntry")
		purego.RegisterLibFunc(&darwinDiskIOKit.registryGetRegistryEntryID, iokit, "IORegistryEntryGetRegistryEntryID")
		purego.RegisterLibFunc(&darwinDiskIOKit.cfStringCreateWithCString, coreFoundation, "CFStringCreateWithCString")
		purego.RegisterLibFunc(&darwinDiskIOKit.cfStringGetCString, coreFoundation, "CFStringGetCString")
		purego.RegisterLibFunc(&darwinDiskIOKit.cfDictionaryGetValue, coreFoundation, "CFDictionaryGetValue")
		purego.RegisterLibFunc(&darwinDiskIOKit.cfNumberGetValue, coreFoundation, "CFNumberGetValue")
		purego.RegisterLibFunc(&darwinDiskIOKit.cfGetTypeID, coreFoundation, "CFGetTypeID")
		purego.RegisterLibFunc(&darwinDiskIOKit.cfDictionaryGetTypeID, coreFoundation, "CFDictionaryGetTypeID")
		purego.RegisterLibFunc(&darwinDiskIOKit.cfNumberGetTypeID, coreFoundation, "CFNumberGetTypeID")
		purego.RegisterLibFunc(&darwinDiskIOKit.cfStringGetTypeID, coreFoundation, "CFStringGetTypeID")
		purego.RegisterLibFunc(&darwinDiskIOKit.cfRelease, coreFoundation, "CFRelease")
	})
	return darwinDiskIOKit.err
}

func nativeDiskDeviceCounter(ctx context.Context) (diskDeviceCounter, error) {
	if err := loadDarwinDiskIOKit(); err != nil {
		return diskDeviceCounter{}, err
	}
	className := append([]byte("IOBlockStorageDriver"), 0)
	matching := darwinDiskIOKit.serviceMatching(&className[0])
	if matching == 0 {
		return diskDeviceCounter{}, fmt.Errorf("IOServiceMatching returned no dictionary")
	}
	var iterator uint32
	if result := darwinDiskIOKit.serviceGetMatchingServices(0, matching, &iterator); result != 0 {
		return diskDeviceCounter{}, fmt.Errorf("IOServiceGetMatchingServices returned %#x", uint32(result))
	}
	if iterator == 0 {
		return diskDeviceCounter{}, fmt.Errorf("no IOBlockStorageDriver iterator")
	}
	defer darwinDiskIOKit.objectRelease(iterator)

	var total uint64
	var identities []uint64
	for {
		select {
		case <-ctx.Done():
			return diskDeviceCounter{}, ctx.Err()
		default:
		}
		service := darwinDiskIOKit.iteratorNext(iterator)
		if service == 0 {
			break
		}
		bytes, identity, included := darwinInternalSSDDriver(service)
		darwinDiskIOKit.objectRelease(service)
		if !included {
			continue
		}
		if total > ^uint64(0)-bytes {
			return diskDeviceCounter{}, fmt.Errorf("internal SSD byte counter overflow")
		}
		total += bytes
		identities = append(identities, identity)
	}
	if len(identities) == 0 {
		return diskDeviceCounter{}, fmt.Errorf("no internal solid-state IOBlockStorageDriver found")
	}
	sort.Slice(identities, func(i, j int) bool { return identities[i] < identities[j] })
	parts := make([]string, 0, len(identities))
	for _, identity := range identities {
		parts = append(parts, fmt.Sprintf("%x", identity))
	}
	return diskDeviceCounter{BytesWritten: total, DeviceCount: len(identities), Identity: strings.Join(parts, "+"), Source: "iokit.IOBlockStorageDriver"}, nil
}

func darwinInternalSSDDriver(service uint32) (uint64, uint64, bool) {
	plane := append([]byte("IOService"), 0)
	var parent uint32
	if darwinDiskIOKit.registryGetParentEntry(service, &plane[0], &parent) != 0 || parent == 0 {
		return 0, 0, false
	}
	defer darwinDiskIOKit.objectRelease(parent)
	deviceCharacteristics := darwinRegistryDictionary(parent, "Device Characteristics")
	if deviceCharacteristics == 0 {
		return 0, 0, false
	}
	defer darwinDiskIOKit.cfRelease(deviceCharacteristics)
	protocolCharacteristics := darwinRegistryDictionary(parent, "Protocol Characteristics")
	if protocolCharacteristics == 0 {
		return 0, 0, false
	}
	defer darwinDiskIOKit.cfRelease(protocolCharacteristics)
	if !strings.EqualFold(darwinDictionaryString(deviceCharacteristics, "Medium Type"), "Solid State") ||
		!strings.EqualFold(darwinDictionaryString(protocolCharacteristics, "Physical Interconnect Location"), "Internal") {
		return 0, 0, false
	}
	statistics := darwinRegistryDictionary(service, "Statistics")
	if statistics == 0 {
		return 0, 0, false
	}
	defer darwinDiskIOKit.cfRelease(statistics)
	bytes, ok := darwinDictionaryUint64(statistics, "Bytes (Write)")
	if !ok {
		return 0, 0, false
	}
	var identity uint64
	if darwinDiskIOKit.registryGetRegistryEntryID(service, &identity) != 0 || identity == 0 {
		return 0, 0, false
	}
	return bytes, identity, true
}

func darwinRegistryDictionary(entry uint32, property string) uintptr {
	key := darwinCreateCFString(property)
	if key == 0 {
		return 0
	}
	defer darwinDiskIOKit.cfRelease(key)
	value := darwinDiskIOKit.registryCreateCFProperty(entry, key, 0, 0)
	if value == 0 || darwinDiskIOKit.cfGetTypeID(value) != darwinDiskIOKit.cfDictionaryGetTypeID() {
		if value != 0 {
			darwinDiskIOKit.cfRelease(value)
		}
		return 0
	}
	return value
}

func darwinCreateCFString(value string) uintptr {
	body := append([]byte(value), 0)
	return darwinDiskIOKit.cfStringCreateWithCString(0, &body[0], darwinCFStringEncodingUTF8)
}

func darwinDictionaryValue(dictionary uintptr, keyText string) uintptr {
	key := darwinCreateCFString(keyText)
	if key == 0 {
		return 0
	}
	defer darwinDiskIOKit.cfRelease(key)
	return darwinDiskIOKit.cfDictionaryGetValue(dictionary, key)
}

func darwinDictionaryString(dictionary uintptr, keyText string) string {
	value := darwinDictionaryValue(dictionary, keyText)
	if value == 0 || darwinDiskIOKit.cfGetTypeID(value) != darwinDiskIOKit.cfStringGetTypeID() {
		return ""
	}
	var buffer [256]byte
	if !darwinDiskIOKit.cfStringGetCString(value, &buffer[0], int64(len(buffer)), darwinCFStringEncodingUTF8) {
		return ""
	}
	length := 0
	for length < len(buffer) && buffer[length] != 0 {
		length++
	}
	return string(buffer[:length])
}

func darwinDictionaryUint64(dictionary uintptr, keyText string) (uint64, bool) {
	value := darwinDictionaryValue(dictionary, keyText)
	if value == 0 || darwinDiskIOKit.cfGetTypeID(value) != darwinDiskIOKit.cfNumberGetTypeID() {
		return 0, false
	}
	var number int64
	if !darwinDiskIOKit.cfNumberGetValue(value, darwinCFNumberSInt64Type, unsafe.Pointer(&number)) || number < 0 {
		return 0, false
	}
	return uint64(number), true
}

func nativeDiskProcessCounters(ctx context.Context) (diskProcessSnapshot, error) {
	if err := loadDarwinLibproc(); err != nil {
		return diskProcessSnapshot{}, err
	}
	rows, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return diskProcessSnapshot{}, err
	}
	result := diskProcessSnapshot{TotalPIDCount: len(rows), Counters: make([]diskProcessCounter, 0, len(rows))}
	for index := range rows {
		if index%64 == 0 {
			select {
			case <-ctx.Done():
				return result, ctx.Err()
			default:
			}
		}
		pid := int(rows[index].Proc.P_pid)
		if pid <= 0 {
			continue
		}
		var usage darwinRusageInfoV2
		if darwinLibproc.pidRusageV2(int32(pid), darwinRusageInfoVersion2, &usage) != 0 || usage.ProcessStart == 0 {
			continue
		}
		executable := privacySafeExecutable(darwinCString(rows[index].Proc.P_comm[:]))
		result.Counters = append(result.Counters, diskProcessCounter{
			PID: pid, StartID: usage.ProcessStart, Executable: executable,
			BytesWritten: usage.DiskIOBytesWritten, AgentOwned: hostConsumerCategory(executable) == "agent",
		})
	}
	result.AccessibleCount = len(result.Counters)
	if result.AccessibleCount == 0 {
		return result, fmt.Errorf("libproc disk-write scan returned no accessible processes")
	}
	return result, nil
}

//go:build darwin && cgo

package sessionpressure

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>
#include <IOKit/IOKitLib.h>
#include <IOKit/IOCFPlugIn.h>
#include <IOKit/storage/nvme/NVMeSMARTLibExternal.h>
#include <string.h>
#include <stdint.h>

typedef struct {
    int available;
    int return_code;
    uint8_t critical_warning;
    uint16_t temperature_k;
    uint8_t available_spare;
    uint8_t available_spare_threshold;
    uint8_t percentage_used;
    uint64_t data_units_read;
    uint64_t data_units_written;
    uint64_t host_read_commands;
    uint64_t host_write_commands;
    uint64_t controller_busy_time;
    uint64_t power_cycles;
    uint64_t power_on_hours;
    uint64_t unsafe_shutdowns;
    uint64_t media_errors;
    uint64_t error_info_log_entries;
    char model[48];
    char serial[32];
    char firmware[16];
} nvme_smart_c_result;

static void nvme_trim_copy(char *dst, size_t dstsz, const uint8_t *src, size_t n) {
    size_t start = 0;
    size_t end = n;
    while (start < n && src[start] == ' ') {
        start++;
    }
    while (end > start && src[end - 1] == ' ') {
        end--;
    }
    size_t len = end - start;
    if (len >= dstsz) {
        len = dstsz - 1;
    }
    memcpy(dst, src + start, len);
    dst[len] = 0;
}

static int nvme_smart_from_service(io_object_t svc, nvme_smart_c_result *out) {
    IOCFPlugInInterface **plugin = NULL;
    SInt32 score = 0;
    kern_return_t kr = IOCreatePlugInInterfaceForService(
        svc, kIONVMeSMARTUserClientTypeID, kIOCFPlugInInterfaceID, &plugin, &score);
    if (kr != KERN_SUCCESS || plugin == NULL) {
        return kr != KERN_SUCCESS ? (int)kr : (int)kIOReturnNoResources;
    }
    IONVMeSMARTInterface **smart = NULL;
    HRESULT hr = (*plugin)->QueryInterface(plugin, CFUUIDGetUUIDBytes(kIONVMeSMARTInterfaceID), (LPVOID *)&smart);
    if (hr != S_OK || smart == NULL) {
        IODestroyPlugInInterface(plugin);
        return (int)kIOReturnError;
    }
    NVMeIdentifyControllerStruct id;
    memset(&id, 0, sizeof(id));
    IOReturn rc = (*smart)->GetIdentifyData(smart, &id, 0);
    if (rc == kIOReturnSuccess) {
        nvme_trim_copy(out->model, sizeof(out->model), id.MODEL_NUMBER, sizeof(id.MODEL_NUMBER));
        nvme_trim_copy(out->serial, sizeof(out->serial), id.SERIAL_NUMBER, sizeof(id.SERIAL_NUMBER));
        nvme_trim_copy(out->firmware, sizeof(out->firmware), id.FW_REVISION, sizeof(id.FW_REVISION));
    }
    NVMeSMARTData data;
    memset(&data, 0, sizeof(data));
    rc = (*smart)->SMARTReadData(smart, &data);
    (*smart)->Release(smart);
    IODestroyPlugInInterface(plugin);
    if (rc != kIOReturnSuccess) {
        return (int)rc;
    }
    out->available = 1;
    out->critical_warning = data.CRITICAL_WARNING;
    out->temperature_k = data.TEMPERATURE;
    out->available_spare = data.AVAILABLE_SPARE;
    out->available_spare_threshold = data.AVAILABLE_SPARE_THRESHOLD;
    out->percentage_used = data.PERCENTAGE_USED;
    out->data_units_read = data.DATA_UNITS_READ[0];
    out->data_units_written = data.DATA_UNITS_WRITTEN[0];
    out->host_read_commands = data.HOST_READ_COMMANDS[0];
    out->host_write_commands = data.HOST_WRITE_COMMANDS[0];
    out->controller_busy_time = data.CONTROLLER_BUSY_TIME[0];
    out->power_cycles = data.POWER_CYCLES[0];
    out->power_on_hours = data.POWER_ON_HOURS[0];
    out->unsafe_shutdowns = data.UNSAFE_SHUTDOWNS[0];
    out->media_errors = data.MEDIA_ERRORS[0];
    out->error_info_log_entries = data.NUM_ERROR_INFO_LOG_ENTRIES[0];
    return 0;
}

static int nvme_smart_scan_class(const char *class_name, nvme_smart_c_result *out) {
    CFMutableDictionaryRef matching = IOServiceMatching(class_name);
    if (matching == NULL) {
        return (int)kIOReturnNoResources;
    }
    io_iterator_t iter = 0;
    kern_return_t kr = IOServiceGetMatchingServices(kIOMainPortDefault, matching, &iter);
    if (kr != KERN_SUCCESS) {
        return (int)kr;
    }
    int rc = (int)kIOReturnNotFound;
    io_object_t svc;
    while ((svc = IOIteratorNext(iter)) != 0) {
        CFTypeRef capable = IORegistryEntryCreateCFProperty(svc, CFSTR(kIOPropertyNVMeSMARTCapableKey), kCFAllocatorDefault, 0);
        Boolean yes = capable != NULL;
        if (capable != NULL && CFGetTypeID(capable) == CFBooleanGetTypeID()) {
            yes = CFBooleanGetValue((CFBooleanRef)capable);
        }
        if (capable != NULL) {
            CFRelease(capable);
        }
        if (!yes) {
            IOObjectRelease(svc);
            continue;
        }
        rc = nvme_smart_from_service(svc, out);
        IOObjectRelease(svc);
        if (out->available) {
            break;
        }
    }
    IOObjectRelease(iter);
    return rc;
}

int nvme_smart_read(nvme_smart_c_result *out) {
    memset(out, 0, sizeof(*out));
    int rc = nvme_smart_scan_class("IOEmbeddedNVMeBlockDevice", out);
    if (out->available) {
        return 0;
    }
    rc = nvme_smart_scan_class("IONVMeBlockStorageDevice", out);
    if (out->available) {
        return 0;
    }
    return rc;
}
*/
import "C"

import (
	"context"
	"fmt"
	"math"
	"time"
)

func nativeReadNVMeSMART(ctx context.Context) (NVMeSMARTSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return NVMeSMARTSnapshot{}, err
	}
	var raw C.nvme_smart_c_result
	rc := C.nvme_smart_read(&raw)
	if raw.available == 0 {
		if rc == 0 {
			return NVMeSMARTSnapshot{
				SchemaVersion: nvmeSMARTSnapshotSchemaVersion,
				CapturedAt:    time.Now().UTC(),
				Source:        "iokit.NVMeSMARTLib",
				Reason:        "nvme_smart_not_found",
			}, nil
		}
		return NVMeSMARTSnapshot{}, fmt.Errorf("NVMe SMART read failed: IOReturn 0x%x", uint32(rc))
	}
	snapshot := NVMeSMARTSnapshot{
		SchemaVersion:                  nvmeSMARTSnapshotSchemaVersion,
		CapturedAt:                     time.Now().UTC(),
		Source:                         "iokit.NVMeSMARTLib",
		Available:                      true,
		Model:                          C.GoString(&raw.model[0]),
		Serial:                         C.GoString(&raw.serial[0]),
		Firmware:                       C.GoString(&raw.firmware[0]),
		CriticalWarning:                uint8(raw.critical_warning),
		AvailableSparePercent:          uint8(raw.available_spare),
		AvailableSpareThresholdPercent: uint8(raw.available_spare_threshold),
		PercentageUsed:                 uint8(raw.percentage_used),
		DataUnitsRead:                  uint64(raw.data_units_read),
		DataUnitsWritten:               uint64(raw.data_units_written),
		HostReadCommands:               uint64(raw.host_read_commands),
		HostWriteCommands:              uint64(raw.host_write_commands),
		ControllerBusyMinutes:          uint64(raw.controller_busy_time),
		PowerCycles:                    uint64(raw.power_cycles),
		PowerOnHours:                   uint64(raw.power_on_hours),
		UnsafeShutdowns:                uint64(raw.unsafe_shutdowns),
		MediaErrors:                    uint64(raw.media_errors),
		ErrorInfoLogEntries:            uint64(raw.error_info_log_entries),
	}
	if raw.temperature_k > 0 {
		snapshot.TemperatureCelsius = math.Round((float64(raw.temperature_k)-273.15)*10) / 10
	}
	return snapshot, nil
}

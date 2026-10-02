//go:build windows

package daemon

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	psapiDLL                 = syscall.NewLazyDLL("psapi.dll")
	procGetProcessMemoryInfo = psapiDLL.NewProc("GetProcessMemoryInfo")
)

const processQueryLimitedInformation = 0x1000

type processMemoryCounters struct {
	Cb                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

// residentKB 通过 psapi 读取进程工作集（单位 KB）。
func residentKB(pid int) (int64, error) {
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return 0, err
	}
	defer syscall.CloseHandle(handle)

	var counters processMemoryCounters
	counters.Cb = uint32(unsafe.Sizeof(counters))
	ret, _, callErr := procGetProcessMemoryInfo.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&counters)),
		uintptr(counters.Cb),
	)
	if ret == 0 {
		return 0, fmt.Errorf("GetProcessMemoryInfo 调用失败: %w", callErr)
	}
	return int64(counters.WorkingSetSize / 1024), nil
}

//go:build windows

package scheduler

import "syscall"

const (
	windowsCreateNewProcessGroup = 0x00000200
	windowsDetachedProcess       = 0x00000008
	windowsCreateNoWindow        = 0x08000000
	windowsSynchronize           = 0x00100000
	windowsWaitTimeout           = 0x00000102
)

// detachAttr 让调度器守护进程脱离当前控制台。
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windowsDetachedProcess | windowsCreateNewProcessGroup | windowsCreateNoWindow,
		HideWindow:    true,
	}
}

// processAlive 判断进程是否还活着。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(windowsSynchronize, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	r, err := syscall.WaitForSingleObject(h, 0)
	if err != nil {
		return false
	}
	return r == windowsWaitTimeout
}

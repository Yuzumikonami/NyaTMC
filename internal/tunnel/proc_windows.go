//go:build windows

package tunnel

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

const (
	windowsCreateNewProcessGroup = 0x00000200
	windowsDetachedProcess       = 0x00000008
	windowsCreateNoWindow        = 0x08000000
	windowsSynchronize           = 0x00100000
	windowsWaitTimeout           = 0x00000102
)

// detachAttr 让 cloudflared 脱离当前控制台：nyatmc 退出后隧道继续运行。
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windowsDetachedProcess | windowsCreateNewProcessGroup | windowsCreateNoWindow,
		HideWindow:    true,
	}
}

// terminate 在 Windows 上没有与 SIGTERM 对应的优雅信号，直接走 taskkill。
func terminate(pid int) error { return forceKill(pid) }

// forceKill 结束进程及其子进程。
func forceKill(pid int) error {
	if pid <= 0 {
		return nil
	}
	err := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid)).Run()
	if err == nil {
		return nil
	}
	// taskkill 不可用时退化为直接 Kill
	if p, ferr := os.FindProcess(pid); ferr == nil {
		if kerr := p.Kill(); kerr == nil {
			return nil
		}
	}
	return err
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

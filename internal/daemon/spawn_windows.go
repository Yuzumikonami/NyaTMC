//go:build windows

package daemon

import (
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

// prepareCommand 让服务端进程独立成组，便于整组结束。
func prepareCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windowsCreateNewProcessGroup | windowsCreateNoWindow,
		HideWindow:    true,
	}
}

// detachAttr 供派生 supervisor 使用：脱离当前控制台，父进程退出后继续运行。
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windowsDetachedProcess | windowsCreateNewProcessGroup | windowsCreateNoWindow,
		HideWindow:    true,
	}
}

// killTree 结束进程及其子进程（Windows 没有进程组信号，用 taskkill /T）。
func killTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	killer := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid))
	if err := killer.Run(); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}

// killPID 结束指定进程（用于清理失去 supervisor 的孤儿服务端进程）。
func killPID(pid int) error {
	if pid <= 0 {
		return nil
	}
	return exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid)).Run()
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

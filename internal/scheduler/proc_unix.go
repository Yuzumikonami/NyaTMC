//go:build !windows

package scheduler

import (
	"errors"
	"syscall"
)

// detachAttr 让调度器守护进程脱离当前终端（新会话）。
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// processAlive 判断进程是否还活着。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}

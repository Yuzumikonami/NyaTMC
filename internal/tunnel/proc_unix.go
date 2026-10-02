//go:build !windows

package tunnel

import (
	"errors"
	"syscall"
)

// detachAttr 让 cloudflared 脱离当前终端（新会话、新进程组）：
// nyatmc 退出后隧道继续运行，同时便于用 kill(-pid) 结束整组。
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// terminate 先发 SIGTERM，让 cloudflared 自行收尾。
func terminate(pid int) error {
	if pid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		if err2 := syscall.Kill(pid, syscall.SIGTERM); err2 != nil && !errors.Is(err2, syscall.ESRCH) {
			return err2
		}
	}
	return nil
}

// forceKill 强杀进程组。
func forceKill(pid int) error {
	if pid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		if err2 := syscall.Kill(pid, syscall.SIGKILL); err2 != nil && !errors.Is(err2, syscall.ESRCH) {
			return err2
		}
	}
	return nil
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

//go:build !windows && !plan9

package tui

import (
	"os"
	"os/signal"
	"syscall"
)

// notifyWinch 订阅窗口尺寸变化信号（SIGWINCH）。
func notifyWinch(ch chan os.Signal) {
	signal.Notify(ch, syscall.SIGWINCH)
}

// ignoreWinch 取消订阅。
func ignoreWinch(ch chan os.Signal) {
	signal.Stop(ch)
}

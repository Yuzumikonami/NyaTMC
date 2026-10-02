//go:build windows || plan9

package tui

import "os"

// notifyWinch 在 Windows 上没有 SIGWINCH：什么都不做，主循环会轮询终端尺寸。
func notifyWinch(ch chan os.Signal) {}

// ignoreWinch 同 notifyWinch。
func ignoreWinch(ch chan os.Signal) {}

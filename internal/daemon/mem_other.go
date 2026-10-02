//go:build !linux && !windows

package daemon

// residentKB 在该平台上不可用，返回 0 让调用方优雅降级。
func residentKB(pid int) (int64, error) { return 0, nil }

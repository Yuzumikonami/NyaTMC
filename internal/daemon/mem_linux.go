//go:build linux

package daemon

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// residentKB 从 /proc/<pid>/statm 读取常驻内存（单位 KB）。
func residentKB(pid int) (int64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", pid))
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0, fmt.Errorf("无法解析 /proc/%d/statm", pid)
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0, err
	}
	return pages * (int64(os.Getpagesize()) / 1024), nil
}

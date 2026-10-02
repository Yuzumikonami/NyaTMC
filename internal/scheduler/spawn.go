package scheduler

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/yuzumikonami/nyatmc/internal/config"
)

// Spawn 派生一个脱离终端的调度器守护进程（`nyatmc schedule daemon --detach` 使用）。
//
// 返回守护进程的 pid；真正的运行状态由守护进程自己写进 state/scheduler.json。
func Spawn(cfgPath string, instances []string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("无法定位 nyatmc 可执行文件: %w", err)
	}
	if strings.TrimSpace(cfgPath) == "" {
		cfgPath = config.ConfigPath()
	}
	args := []string{"__schedule", "--config", config.ExpandPath(cfgPath)}
	if list := cleanNames(instances); len(list) > 0 {
		args = append(args, "--instances", strings.Join(list, ","))
	}

	logPath := LogPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return 0, fmt.Errorf("创建状态目录 %s 失败: %w", filepath.Dir(logPath), err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, fmt.Errorf("打开调度器日志 %s 失败: %w", logPath, err)
	}
	defer logFile.Close()
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, fmt.Errorf("打开 %s 失败: %w", os.DevNull, err)
	}
	defer devnull.Close()

	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = detachAttr()
	cmd.Dir = filepath.Dir(logPath)
	cmd.Stdin = devnull
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("派生调度器守护进程失败: %w", err)
	}
	pid := cmd.Process.Pid
	go func() { _ = cmd.Wait() }()
	return pid, nil
}

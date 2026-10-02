package tunnel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yuzumikonami/nyatmc/internal/config"
)

// State 是持久化到 state/tunnel.json 的隧道状态。
//
// 这份文件是“跨进程”的唯一事实来源：CLI 启动隧道后退出，Web 进程或下一次
// CLI 调用都能据此判断隧道是否还在跑、公网地址是什么。
type State struct {
	Running   bool      `json:"running"`
	URL       string    `json:"url"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	Target    string    `json:"target"`
	// Mode 取值为 quick 或 named。
	Mode      string    `json:"mode"`
	Protocol  string    `json:"protocol,omitempty"`
	Hostname  string    `json:"hostname,omitempty"`
	BinPath   string    `json:"bin_path"`
	LogPath   string    `json:"log_path"`
	LastError string    `json:"last_error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// StatePath 返回隧道状态文件路径（~/.nyatmc/state/tunnel.json）。
func StatePath() string { return filepath.Join(config.StateDir(), "tunnel.json") }

// LogPath 返回 cloudflared 输出日志路径。
//
// 输出重定向到文件而不是管道，才能让隧道在 nyatmc 退出后继续可观测；
// 状态里的最近日志就是从这个文件尾部读取的（相当于跨进程的环形缓冲）。
func LogPath() string { return filepath.Join(config.StateDir(), "tunnel.log") }

// stateLogPath 返回状态记录里的日志路径。
func stateLogPath(s State) string {
	if p := strings.TrimSpace(s.LogPath); p != "" {
		return p
	}
	return LogPath()
}

// writeState 原子写入状态文件。
func writeState(s State) error {
	path := StatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建状态目录 %s 失败: %w", filepath.Dir(path), err)
	}
	s.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化隧道状态失败: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写入隧道状态 %s 失败: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换隧道状态 %s 失败: %w", path, err)
	}
	return nil
}

// writeStoppedState 记录一次停止（保留目标信息，便于 status 展示）。
func writeStoppedState(target, note string) error {
	return writeState(State{Running: false, Target: target, LastError: note})
}

// loadState 读取状态文件；文件不存在时返回 os.ErrNotExist。
func loadState() (State, error) {
	var s State
	data, err := os.ReadFile(StatePath())
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("隧道状态文件损坏: %w", err)
	}
	return s, nil
}

// ClearState 删除状态文件。
func ClearState() error {
	if err := os.Remove(StatePath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

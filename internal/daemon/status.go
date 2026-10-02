// Package daemon 是 nyatmc 的守护引擎：进程启动、看门狗、控制通道与优雅关闭。
//
// 运行模型：
//
//	nyatmc start          → 派生一个脱离终端的 supervisor 进程
//	supervisor            → 持有实例锁、启动 java、看门狗守护、对外提供本地控制通道
//	nyatmc stop/status/…  → 通过控制通道（pkg/socket）与 supervisor 通信
//	nyatmc start -f       → supervisor 直接跑在当前终端里，Ctrl+C 优雅关闭
//
// 这样 CLI、TUI 看板与 Web 仪表盘操作的是同一个 supervisor，状态不会分叉。
package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// State 是实例的运行状态。
type State string

// 运行状态取值。
const (
	StateStopped    State = "stopped"
	StateStarting   State = "starting"
	StateRunning    State = "running"
	StateStopping   State = "stopping"
	StateRestarting State = "restarting"
	StateCrashed    State = "crashed"
	StateUnknown    State = "unknown"
)

// Label 返回中文状态名，供 CLI / TUI / Web 展示。
func (s State) Label() string {
	switch s {
	case StateStopped:
		return "已停止"
	case StateStarting:
		return "启动中"
	case StateRunning:
		return "运行中"
	case StateStopping:
		return "停止中"
	case StateRestarting:
		return "重启中"
	case StateCrashed:
		return "已崩溃"
	default:
		return "未知"
	}
}

// Active 表示进程应该还活着（用于判断是否需要停）。
func (s State) Active() bool {
	switch s {
	case StateStarting, StateRunning, StateStopping, StateRestarting:
		return true
	default:
		return false
	}
}

// Status 是实例的完整运行状态，会同时写入 state.json 并通过控制通道返回。
type Status struct {
	Instance      string    `json:"instance"`
	State         State     `json:"state"`
	Ready         bool      `json:"ready"`
	PID           int       `json:"pid"`
	SupervisorPID int       `json:"supervisor_pid"`
	StartedAt     time.Time `json:"started_at"`
	StoppedAt     time.Time `json:"stopped_at"`
	UptimeSeconds int64     `json:"uptime_seconds"`
	Restarts      int       `json:"restarts"`
	LastExitCode  int       `json:"last_exit_code"`
	LastError     string    `json:"last_error,omitempty"`
	Note          string    `json:"note,omitempty"`

	Players      []string `json:"players"`
	MaxPlayers   int      `json:"max_players"`
	TPS          float64  `json:"tps"`
	TPSKnown     bool     `json:"tps_known"`
	TickBehindMS int64    `json:"tick_behind_ms"`
	Version      string   `json:"version"`
	Warnings     int      `json:"warnings"`
	Errors       int      `json:"errors"`

	ServerType   string `json:"server_type"`
	MemoryLimit  string `json:"memory_limit"`
	MemoryUsedMB int64  `json:"memory_used_mb"`
	ServerDir    string `json:"server_dir"`
	ConfigPath   string `json:"config_path"`
	Watchdog     bool   `json:"watchdog"`
	ConsoleLines int    `json:"console_lines"`

	UpdatedAt time.Time `json:"updated_at"`
	// Stale 表示这份状态来自落盘文件而不是活着的 supervisor。
	Stale bool `json:"stale,omitempty"`
}

// Uptime 返回运行时长。
func (s Status) Uptime() time.Duration { return time.Duration(s.UptimeSeconds) * time.Second }

// PlayerCount 返回在线人数。
func (s Status) PlayerCount() int { return len(s.Players) }

// WriteStatus 原子写入状态文件。
func WriteStatus(path string, st Status) error {
	st.UpdatedAt = time.Now()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建状态目录失败: %w", err)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写入状态文件失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换状态文件失败: %w", err)
	}
	return nil
}

// ReadStatus 读取状态文件。
func ReadStatus(path string) (Status, error) {
	var st Status
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Status{State: StateStopped}, nil
		}
		return st, fmt.Errorf("读取状态文件失败: %w", err)
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return Status{State: StateUnknown}, fmt.Errorf("状态文件损坏: %w", err)
	}
	if st.State == "" {
		st.State = StateUnknown
	}
	return st, nil
}

// RemoveStatus 删除状态文件。
func RemoveStatus(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ---------------------------------------------------------------- 控制通道报文

// 控制命令名。
const (
	CmdPing      = "ping"
	CmdStatus    = "status"
	CmdStart     = "start"
	CmdStop      = "stop"
	CmdRestart   = "restart"
	CmdCommand   = "command"
	CmdLogs      = "logs"
	CmdSubscribe = "subscribe"
	CmdInfo      = "info"
)

// CommandArgs 是往服务端控制台发指令的参数。
type CommandArgs struct {
	Line string `json:"line"`
}

// StopArgs 是停止参数。
type StopArgs struct {
	// Force 为 true 时直接强杀，不走优雅关闭。
	Force bool `json:"force"`
	// Timeout 是等待优雅退出的最长时间，0 表示用配置值。
	Timeout time.Duration `json:"timeout"`
	// KeepSupervisor 为 true 时只停服务端进程，保留 supervisor。
	KeepSupervisor bool `json:"keep_supervisor"`
}

// RestartArgs 是重启参数。
type RestartArgs struct {
	Force bool          `json:"force"`
	Delay time.Duration `json:"delay"`
}

// LogsArgs 是取日志参数。
type LogsArgs struct {
	Lines int `json:"lines"`
}

// LogsResult 是日志返回。
type LogsResult struct {
	Lines []string `json:"lines"`
}

// LogEvent 是订阅流里的一条事件。
type LogEvent struct {
	Kind   string    `json:"kind"` // log | status | exit
	Text   string    `json:"text,omitempty"`
	Time   time.Time `json:"time"`
	Status *Status   `json:"status,omitempty"`
}

// InfoResult 是实例详情。
type InfoResult struct {
	Status Status `json:"status"`
}

// ExitEvent 描述一次进程退出。
type ExitEvent struct {
	Code     int           `json:"code"`
	Err      string        `json:"err,omitempty"`
	At       time.Time     `json:"at"`
	Expected bool          `json:"expected"`
	Uptime   time.Duration `json:"uptime"`
}

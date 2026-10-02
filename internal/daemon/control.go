package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/yuzumikonami/nyatmc/internal/config"
	"github.com/yuzumikonami/nyatmc/internal/instance"
	"github.com/yuzumikonami/nyatmc/pkg/logger"
	"github.com/yuzumikonami/nyatmc/pkg/socket"
)

// ErrNotRunning 表示实例当前没有 supervisor 在运行。
var ErrNotRunning = errors.New("实例未在运行")

// ErrAlreadyRunning 表示实例的服务端进程已经在运行。
var ErrAlreadyRunning = errors.New("实例已在运行")

// StartOptions 控制启动行为。
type StartOptions struct {
	// Foreground 在当前终端里跑 supervisor（Ctrl+C 优雅关闭）。
	Foreground bool
	// Wait 等到服务端就绪（或失败）再返回。
	Wait bool
	// WaitTimeout 是 Wait 的最长时间。
	WaitTimeout time.Duration
	// NoStart 只启动守护，不拉起服务端。
	NoStart bool
	// Watchdog 覆盖配置里的看门狗开关。
	Watchdog *bool
	// ConfigPath 用于派生 supervisor 时显式传递配置文件。
	ConfigPath string
	// Logger 前台模式下的日志输出。
	Logger *logger.Logger
}

// StopOptions 控制停止行为。
type StopOptions struct {
	Force          bool
	Timeout        time.Duration
	KeepSupervisor bool
}

// RestartOptions 控制重启行为。
type RestartOptions struct {
	Force bool
	Delay time.Duration
	// Wait 等到重启后再次就绪。
	Wait        bool
	WaitTimeout time.Duration
}

// Start 启动实例。
func Start(ctx context.Context, cfg *config.Config, inst *instance.Instance, opts StartOptions) (Status, error) {
	cfgPath := configPathOf(cfg, opts.ConfigPath)

	if opts.Foreground {
		err := RunSupervisor(ctx, cfgPath, inst.Name, SupervisorOptions{
			Attach:   true,
			NoStart:  opts.NoStart,
			Watchdog: opts.Watchdog,
			Logger:   opts.Logger,
		})
		st, _ := GetStatus(ctx, cfg, inst)
		return st, err
	}

	if Running(inst) {
		st, _ := GetStatus(ctx, cfg, inst)
		if st.State.Active() {
			return st, fmt.Errorf("%w：实例 %s 已经在运行（pid %d，状态 %s）", ErrAlreadyRunning, inst.Name, st.PID, st.State.Label())
		}
		// 守护进程还活着但服务端没跑（例如用 --keep-supervisor 停过），
		// 这时不需要再派生一个守护进程，直接让它把服务端拉起来。
		return startViaSupervisor(ctx, inst, opts)
	}

	if err := spawnSupervisor(cfgPath, inst, opts); err != nil {
		return Status{Instance: inst.Name, State: StateCrashed}, err
	}
	if !opts.Wait {
		return GetStatus(ctx, cfg, inst)
	}

	timeout := opts.WaitTimeout
	if timeout <= 0 {
		timeout = time.Duration(inst.Res.Daemon.StartupTimeout.Std())
	}
	return waitFor(ctx, cfg, inst, timeout, func(st Status) bool {
		return st.Ready || st.State == StateCrashed
	})
}

// startViaSupervisor 让已经存在的 supervisor 拉起服务端进程。
func startViaSupervisor(ctx context.Context, inst *instance.Instance, opts StartOptions) (Status, error) {
	var st Status
	if opts.NoStart {
		return GetStatus(ctx, nil, inst)
	}
	if err := socket.CallPath(ctx, inst.Res.Endpoint, CmdStart, nil, &st); err != nil {
		return st, fmt.Errorf("请求守护进程启动服务端失败: %w", err)
	}
	if !opts.Wait {
		return st, nil
	}
	timeout := opts.WaitTimeout
	if timeout <= 0 {
		timeout = time.Duration(inst.Res.Daemon.StartupTimeout.Std())
	}
	return waitFor(ctx, nil, inst, timeout, func(s Status) bool {
		return s.Ready || s.State == StateCrashed
	})
}

// StartServer 让运行中的 supervisor 拉起服务端进程（实例未在守护时返回 ErrNotRunning）。
func StartServer(ctx context.Context, inst *instance.Instance) (Status, error) {
	if !Running(inst) {
		return Status{Instance: inst.Name, State: StateStopped}, ErrNotRunning
	}
	return startViaSupervisor(ctx, inst, StartOptions{})
}

// spawnSupervisor 派生一个脱离终端的 supervisor 进程。
func spawnSupervisor(cfgPath string, inst *instance.Instance, opts StartOptions) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("无法定位 nyatmc 可执行文件: %w", err)
	}
	if err := inst.EnsureDirs(); err != nil {
		return err
	}

	args := []string{"__supervise", "--config", cfgPath, "--instance", inst.Name}
	if opts.NoStart {
		args = append(args, "--no-start")
	}
	if opts.Watchdog != nil {
		if *opts.Watchdog {
			args = append(args, "--watchdog")
		} else {
			args = append(args, "--no-watchdog")
		}
	}

	cmd := exec.Command(exe, args...)
	cmd.Dir = inst.Res.Root
	cmd.SysProcAttr = detachAttr()

	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("打开 %s 失败: %w", os.DevNull, err)
	}
	defer devnull.Close()
	cmd.Stdin = devnull
	cmd.Stdout = devnull
	cmd.Stderr = devnull

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("派生 supervisor 失败: %w", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()

	// 等控制端点出现，确认 supervisor 真的起来了
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if socket.PingPath(inst.Res.Endpoint, 700*time.Millisecond) {
			return nil
		}
		if !processAlive(pid) {
			return fmt.Errorf("supervisor 启动后立即退出，请查看日志：%s", inst.Res.SupLog)
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("supervisor 启动超时（20s），请查看日志：%s", inst.Res.SupLog)
}

// Stop 停止实例（默认连 supervisor 一起停）。
func Stop(ctx context.Context, cfg *config.Config, inst *instance.Instance, opts StopOptions) error {
	if !Running(inst) {
		// supervisor 不在，但可能有失去守护的孤儿进程
		if st, err := ReadStatus(inst.Res.StateFile); err == nil && st.PID > 0 && processAlive(st.PID) {
			if err := killPID(st.PID); err != nil {
				return fmt.Errorf("清理孤儿服务端进程（pid %d）失败: %w", st.PID, err)
			}
			_ = WriteStatus(inst.Res.StateFile, Status{Instance: inst.Name, State: StateStopped, Note: "已清理失去守护的服务端进程"})
			return nil
		}
		return ErrNotRunning
	}
	args := StopArgs{Force: opts.Force, Timeout: opts.Timeout, KeepSupervisor: opts.KeepSupervisor}
	var st Status
	if err := socket.CallPath(ctx, inst.Res.Endpoint, CmdStop, args, &st); err != nil {
		return fmt.Errorf("发送停止请求失败: %w", err)
	}
	return nil
}

// Restart 重启实例。
func Restart(ctx context.Context, cfg *config.Config, inst *instance.Instance, opts RestartOptions) (Status, error) {
	if !Running(inst) {
		return Start(ctx, cfg, inst, StartOptions{Wait: opts.Wait, WaitTimeout: opts.WaitTimeout})
	}
	args := RestartArgs{Force: opts.Force, Delay: opts.Delay}
	var st Status
	if err := socket.CallPath(ctx, inst.Res.Endpoint, CmdRestart, args, &st); err != nil {
		return st, fmt.Errorf("发送重启请求失败: %w", err)
	}
	if !opts.Wait {
		return GetStatus(ctx, cfg, inst)
	}
	timeout := opts.WaitTimeout
	if timeout <= 0 {
		timeout = time.Duration(inst.Res.Daemon.StartupTimeout.Std())
	}
	// 先等服务端退出，再等再次就绪
	time.Sleep(time.Second)
	return waitFor(ctx, cfg, inst, timeout, func(st Status) bool {
		return (st.Ready || st.State == StateCrashed) && st.UptimeSeconds < int64(timeout.Seconds())
	})
}

// SendCommand 往服务端控制台发送一行指令。
func SendCommand(ctx context.Context, inst *instance.Instance, line string) error {
	if !Running(inst) {
		return ErrNotRunning
	}
	var out map[string]any
	if err := socket.CallPath(ctx, inst.Res.Endpoint, CmdCommand, CommandArgs{Line: line}, &out); err != nil {
		return err
	}
	return nil
}

// GetStatus 获取实例状态：优先问 supervisor，失败时回退到状态文件。
func GetStatus(ctx context.Context, cfg *config.Config, inst *instance.Instance) (Status, error) {
	_ = cfg
	var st Status
	if err := socket.CallPath(ctx, inst.Res.Endpoint, CmdStatus, nil, &st); err == nil {
		st.Instance = inst.Name
		return st, nil
	}

	fileStatus, ferr := ReadStatus(inst.Res.StateFile)
	if ferr != nil {
		return Status{Instance: inst.Name, State: StateUnknown}, ferr
	}
	fileStatus.Instance = inst.Name
	fileStatus.Stale = true
	switch {
	case fileStatus.State.Active() && fileStatus.PID > 0 && processAlive(fileStatus.PID):
		if fileStatus.Note == "" {
			fileStatus.Note = "supervisor 未响应，状态可能滞后"
		}
	case fileStatus.State.Active():
		fileStatus.State = StateStopped
		fileStatus.PID = 0
		fileStatus.Ready = false
		fileStatus.UptimeSeconds = 0
		fileStatus.Note = "supervisor 已退出"
	}
	// 控制通道都连不上了，端点文件已经没用了，顺手清掉，避免下次误判“已在运行”
	if ep, eerr := socket.ReadEndpoint(inst.Res.Endpoint); eerr == nil {
		if ep.PID <= 0 || !processAlive(ep.PID) {
			_ = socket.RemoveEndpoint(inst.Res.Endpoint)
		}
	}
	return fileStatus, nil
}

// Logs 返回最近 n 行控制台输出。
func Logs(ctx context.Context, inst *instance.Instance, n int) ([]string, error) {
	if n <= 0 {
		n = 200
	}
	if Running(inst) {
		var res LogsResult
		if err := socket.CallPath(ctx, inst.Res.Endpoint, CmdLogs, LogsArgs{Lines: n}, &res); err == nil {
			return res.Lines, nil
		}
	}
	lines, err := readTailLines(inst.ConsoleLogPath(), n, 512*1024)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return lines, nil
}

// Subscribe 订阅实例的实时事件流（日志 + 状态）。
func Subscribe(ctx context.Context, inst *instance.Instance, fn func(LogEvent) error) error {
	if !Running(inst) {
		return ErrNotRunning
	}
	return socket.StreamPath(ctx, inst.Res.Endpoint, CmdSubscribe, nil, func(resp socket.Response) error {
		var ev LogEvent
		if len(resp.Data) > 0 {
			if err := json.Unmarshal(resp.Data, &ev); err != nil {
				return fmt.Errorf("解析事件失败: %w", err)
			}
		}
		if fn == nil {
			return nil
		}
		return fn(ev)
	})
}

// Running 判断实例的 supervisor 是否活着。
//
// 除了探测控制通道，还会确认端点里的 supervisor 进程确实存在：
// 机器重启、进程被强杀之后端点文件会变成“陈旧端点”，
// 只靠端口探测可能碰上端口被复用而误判。
func Running(inst *instance.Instance) bool {
	ep, err := socket.ReadEndpoint(inst.Res.Endpoint)
	if err != nil {
		return false
	}
	if ep.PID > 0 && !processAlive(ep.PID) {
		return false
	}
	return socket.Ping(ep, 700*time.Millisecond) == nil
}

// SupervisorPID 返回 supervisor 的进程号（0 表示未运行）。
func SupervisorPID(inst *instance.Instance) int {
	ep, err := socket.ReadEndpoint(inst.Res.Endpoint)
	if err != nil {
		return 0
	}
	return ep.PID
}

// InstanceStatus 汇总一批实例的状态。
func InstanceStatus(ctx context.Context, cfg *config.Config, instances []*instance.Instance) []Status {
	out := make([]Status, 0, len(instances))
	for _, inst := range instances {
		st, err := GetStatus(ctx, cfg, inst)
		if err != nil {
			out = append(out, Status{Instance: inst.Name, State: StateUnknown, LastError: err.Error()})
			continue
		}
		out = append(out, st)
	}
	return out
}

// waitFor 轮询状态直到满足条件或超时。
func waitFor(ctx context.Context, cfg *config.Config, inst *instance.Instance, timeout time.Duration, done func(Status) bool) (Status, error) {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var last Status
	for {
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-ticker.C:
		}
		st, err := GetStatus(ctx, cfg, inst)
		if err == nil {
			last = st
			if done(st) {
				if st.State == StateCrashed {
					msg := st.LastError
					if msg == "" {
						msg = "服务端启动失败"
					}
					return st, errors.New(msg)
				}
				return st, nil
			}
		}
		if time.Now().After(deadline) {
			if last.Instance == "" {
				last = Status{Instance: inst.Name, State: StateUnknown}
			}
			return last, fmt.Errorf("等待超时（%s）：实例 %s 当前状态 %s，请查看日志 %s",
				timeout, inst.Name, last.State.Label(), inst.Res.SupLog)
		}
	}
}

func configPathOf(cfg *config.Config, override string) string {
	if strings.TrimSpace(override) != "" {
		return config.ExpandPath(override)
	}
	if cfg != nil && strings.TrimSpace(cfg.Path) != "" {
		return cfg.Path
	}
	return config.ConfigPath()
}

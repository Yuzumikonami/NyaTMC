package daemon

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/yuzumikonami/nyatmc/internal/instance"
	"github.com/yuzumikonami/nyatmc/internal/logwatch"
	"github.com/yuzumikonami/nyatmc/pkg/logger"
)

// Process 封装一个 Minecraft 服务端进程：启动、控制台读写、输出解析与退出等待。
type Process struct {
	inst *instance.Instance
	log  *logger.Logger

	mu        sync.RWMutex
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	startedAt time.Time
	running   bool
	stopping  bool
	exitCode  int
	exitErr   error
	exited    chan struct{}

	parser  *logwatch.Parser
	ring    *ringBuffer
	console *logger.RotatingWriter

	subMu   sync.Mutex
	subs    map[int]func(string)
	nextSub int
	onExit  func(ExitEvent)
}

// NewProcess 创建进程包装器。
func NewProcess(inst *instance.Instance, log *logger.Logger) *Process {
	if log == nil {
		log = logger.Discard()
	}
	lines := inst.Res.Log.ConsoleLines
	return &Process{
		inst:   inst,
		log:    log,
		exited: make(chan struct{}),
		parser: logwatch.New(),
		ring:   newRingBuffer(lines),
		subs:   map[int]func(string){},
	}
}

// SetExitHandler 注册退出回调（看门狗依赖它）。
func (p *Process) SetExitHandler(fn func(ExitEvent)) {
	p.mu.Lock()
	p.onExit = fn
	p.mu.Unlock()
}

// Start 启动服务端进程。
func (p *Process) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return fmt.Errorf("服务端进程已在运行（pid %d）", p.cmd.Process.Pid)
	}
	java, args := p.inst.LaunchCommand()
	cmd := exec.CommandContext(context.WithoutCancel(ctx), java, args...)
	cmd.Dir = p.inst.Res.Path
	prepareCommand(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		p.mu.Unlock()
		return fmt.Errorf("创建标准输入管道失败: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		p.mu.Unlock()
		return fmt.Errorf("创建标准输出管道失败: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		p.mu.Unlock()
		return fmt.Errorf("创建标准错误管道失败: %w", err)
	}

	// 控制台日志（由 nyatmc 自己轮转，随时可控）
	if p.inst.Res.Daemon.CaptureConsoleEnabled() {
		if p.console == nil {
			w, err := logger.NewRotatingWriter(logger.RotateOptions{
				Path:       p.inst.ConsoleLogPath(),
				MaxSize:    p.inst.Res.Log.RotateSize.Bytes(),
				MaxBackups: p.inst.Res.Log.KeepFiles,
				MaxAge:     time.Duration(p.inst.Res.Log.MaxAgeDays) * 24 * time.Hour,
				Compress:   p.inst.Res.Log.CompressEnabled(),
			})
			if err != nil {
				p.log.Warnf("无法写入控制台日志（继续运行）：%v", err)
			} else {
				p.console = w
			}
		}
	}

	if err := cmd.Start(); err != nil {
		p.mu.Unlock()
		return fmt.Errorf("启动服务端失败（命令：%s %s）：%w", java, strings.Join(args, " "), err)
	}

	// 每次启动都从干净状态开始解析
	p.parser.Reset()
	p.parser.Feed("[nyatmc] Starting minecraft server process")
	p.ring.Add(fmt.Sprintf("[nyatmc] %s 已启动：%s %s (pid %d)",
		time.Now().Format("2006-01-02 15:04:05"), java, strings.Join(args, " "), cmd.Process.Pid))

	p.cmd = cmd
	p.stdin = stdin
	p.startedAt = time.Now()
	p.running = true
	p.stopping = false
	p.exitCode = 0
	p.exitErr = nil
	p.exited = make(chan struct{})

	pid := cmd.Process.Pid
	started := p.startedAt
	exited := p.exited
	p.mu.Unlock()

	go p.consume(stdout)
	go p.consume(stderr)

	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		p.running = false
		p.exitErr = err
		code := 0
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		p.exitCode = code
		expected := p.stopping
		handler := p.onExit
		p.mu.Unlock()

		select {
		case <-exited:
		default:
			close(exited)
		}
		p.ring.Add(fmt.Sprintf("[nyatmc] 服务端进程已退出，退出码 %d，运行时长 %s", code, time.Since(started).Round(time.Second)))
		_ = p.CloseConsole()
		if handler != nil {
			ev := ExitEvent{Code: code, At: time.Now(), Expected: expected, Uptime: time.Since(started)}
			if err != nil && !expected {
				ev.Err = err.Error()
			}
			handler(ev)
		}
	}()
	_ = pid
	return nil
}

func (p *Process) consume(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		p.handleLine(sc.Text())
	}
}

func (p *Process) handleLine(line string) {
	p.mu.RLock()
	console := p.console
	p.mu.RUnlock()

	p.ring.Add(line)
	p.parser.Feed(line)
	if console != nil {
		if _, err := console.Write([]byte(line + "\n")); err != nil {
			p.log.Debugf("写入控制台日志失败: %v", err)
		}
	}

	p.subMu.Lock()
	subs := make([]func(string), 0, len(p.subs))
	for _, fn := range p.subs {
		subs = append(subs, fn)
	}
	p.subMu.Unlock()
	for _, fn := range subs {
		func() {
			defer func() { recover() }()
			fn(line)
		}()
	}
}

// Send 往服务端控制台写一行指令。
func (p *Process) Send(line string) error {
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return fmt.Errorf("指令不能为空")
	}
	p.mu.RLock()
	stdin := p.stdin
	running := p.running
	p.mu.RUnlock()
	if !running || stdin == nil {
		return fmt.Errorf("服务端进程未在运行")
	}
	if _, err := io.WriteString(stdin, line+"\n"); err != nil {
		return fmt.Errorf("发送指令失败: %w", err)
	}
	p.ring.Add("[nyatmc] > " + line)
	return nil
}

// Stop 优雅关闭：先发停止指令，超时后强制结束进程树。
func (p *Process) Stop(ctx context.Context, stopCommand string, timeout, killTimeout time.Duration) error {
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return nil
	}
	p.stopping = true
	exited := p.exited
	p.mu.Unlock()

	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	if killTimeout <= 0 {
		killTimeout = 15 * time.Second
	}

	if strings.TrimSpace(stopCommand) != "" {
		if err := p.Send(stopCommand); err != nil {
			p.log.Warnf("发送停止指令失败，将直接结束进程：%v", err)
		} else {
			p.log.Infof("已向服务端发送 %q，等待优雅退出（最多 %s）", stopCommand, timeout)
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-exited:
		return nil
	case <-ctx.Done():
	case <-timer.C:
	}

	p.log.Warnf("等待超时，强制结束服务端进程")
	if err := p.Kill(); err != nil {
		return err
	}
	timer2 := time.NewTimer(killTimeout)
	defer timer2.Stop()
	select {
	case <-exited:
		return nil
	case <-timer2.C:
		return fmt.Errorf("强制结束服务端进程后仍未退出")
	}
}

// Kill 强制结束服务端进程（含子进程）。
func (p *Process) Kill() error {
	p.mu.RLock()
	cmd := p.cmd
	running := p.running
	p.mu.RUnlock()
	if !running || cmd == nil || cmd.Process == nil {
		return nil
	}
	return killTree(cmd)
}

// Running 返回进程是否在运行。
func (p *Process) Running() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.running
}

// PID 返回进程号。
func (p *Process) PID() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// Uptime 返回运行时长。
func (p *Process) Uptime() time.Duration {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !p.running || p.startedAt.IsZero() {
		return 0
	}
	return time.Since(p.startedAt)
}

// StartedAt 返回启动时刻。
func (p *Process) StartedAt() time.Time {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.startedAt
}

// ExitCode 返回最近一次退出码。
func (p *Process) ExitCode() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.exitCode
}

// LastExitError 返回最近一次退出错误。
func (p *Process) LastExitError() error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.exitErr
}

// Snapshot 返回日志解析出的服务端状态。
func (p *Process) Snapshot() logwatch.Snapshot { return p.parser.Snapshot() }

// Ready 表示服务端已完成启动。
func (p *Process) Ready() bool { return p.parser.Snapshot().Ready }

// Lines 返回最近 n 行控制台输出。
func (p *Process) Lines(n int) []string { return p.ring.Lines(n) }

// Subscribe 订阅实时输出行，返回取消订阅函数。
func (p *Process) Subscribe(fn func(string)) func() {
	if fn == nil {
		return func() {}
	}
	p.subMu.Lock()
	id := p.nextSub
	p.nextSub++
	p.subs[id] = fn
	p.subMu.Unlock()
	return func() {
		p.subMu.Lock()
		delete(p.subs, id)
		p.subMu.Unlock()
	}
}

// MemoryUsedMB 返回进程常驻内存（平台不支持时返回 0）。
func (p *Process) MemoryUsedMB() int64 {
	pid := p.PID()
	if pid <= 0 {
		return 0
	}
	kb, err := residentKB(pid)
	if err != nil {
		return 0
	}
	return kb / 1024
}

// CloseConsole 关闭控制台日志文件。
func (p *Process) CloseConsole() error {
	p.mu.Lock()
	w := p.console
	p.console = nil
	p.mu.Unlock()
	if w != nil {
		return w.Close()
	}
	return nil
}

// Close 结束进程并关闭日志。
func (p *Process) Close() error {
	_ = p.Kill()
	return p.CloseConsole()
}

// WaitExit 等待进程退出，返回退出码。
func (p *Process) WaitExit(ctx context.Context) (int, error) {
	p.mu.RLock()
	exited := p.exited
	p.mu.RUnlock()
	select {
	case <-exited:
		return p.ExitCode(), p.LastExitError()
	case <-ctx.Done():
		return p.ExitCode(), ctx.Err()
	}
}

// RunningJar 返回服务端主 jar 是否就位（启动前的自检用）。
func (p *Process) RunningJar() bool { return p.inst.JarExists() }

// jarPath 便于日志输出。
func (p *Process) jarPath() string { return p.inst.Res.JarPath }

// ensureDir 确保目录存在。
func ensureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建目录 %s 失败: %w", dir, err)
	}
	return nil
}

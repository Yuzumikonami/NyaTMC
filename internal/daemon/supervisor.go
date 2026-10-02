package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/yuzumikonami/nyatmc/internal/backup"
	"github.com/yuzumikonami/nyatmc/internal/config"
	"github.com/yuzumikonami/nyatmc/internal/instance"
	"github.com/yuzumikonami/nyatmc/pkg/logger"
	"github.com/yuzumikonami/nyatmc/pkg/socket"
)

// EnsureServerJarFunc 用于“服务端文件缺失时自动获取”。
//
// 由 cmd 层注入（cmd → daemon + serverjar），这样 daemon 不必直接依赖下载器，
// 也让单元测试可以用假的下载器。
type EnsureServerJarFunc func(ctx context.Context, inst *instance.Instance, force bool, log *logger.Logger) error

var (
	ensureJarMu sync.RWMutex
	ensureJar   = func(ctx context.Context, inst *instance.Instance, force bool, log *logger.Logger) error {
		return fmt.Errorf("未装配服务端下载器，请用 nyatmc server download 手动获取")
	}
)

// SetServerJarProvider 注入服务端 jar 获取实现（在 cmd 层的 init 中调用一次）。
func SetServerJarProvider(fn EnsureServerJarFunc) {
	ensureJarMu.Lock()
	defer ensureJarMu.Unlock()
	if fn != nil {
		ensureJar = fn
	}
}

func ensureServerJar(ctx context.Context, inst *instance.Instance, force bool, log *logger.Logger) error {
	ensureJarMu.RLock()
	fn := ensureJar
	ensureJarMu.RUnlock()
	return fn(ctx, inst, force, log)
}

// SupervisorOptions 控制 supervisor 行为。
type SupervisorOptions struct {
	// Attach 为 true 时把服务端输出镜像到当前终端，并接受键盘输入（前台模式）。
	Attach bool
	// NoStart 只启动守护，不拉起服务端进程。
	NoStart bool
	// Watchdog 覆盖配置里的看门狗开关。
	Watchdog *bool
	// Logger 留空时写入实例的 supervisor.log。
	Logger *logger.Logger
}

// Supervisor 是单个实例的守护进程。
type Supervisor struct {
	name string
	mgr  *config.Manager
	inst *instance.Instance
	log  *logger.Logger
	opts SupervisorOptions

	ipc  *socket.Server
	proc *Process

	mu            sync.RWMutex
	state         State
	ready         bool
	note          string
	lastError     string
	startedAt     time.Time
	lastExit      ExitEvent
	restartTimes  []time.Time
	shuttingDown  bool
	lastRotateAt  time.Time
	archivedBytes int64

	subMu      sync.Mutex
	statusSubs map[int]func(Status)
	nextSub    int

	quit     chan struct{}
	quitOnce sync.Once
	wg       sync.WaitGroup
}

// RunSupervisor 运行一个实例的守护进程，阻塞直到实例被停止。
func RunSupervisor(ctx context.Context, cfgPath, instanceName string, opts SupervisorOptions) (err error) {
	mgr, err := config.NewManager(cfgPath)
	if err != nil {
		return err
	}
	defer mgr.Close()

	cfg := mgr.Current()
	inst, err := instance.New(cfg, instanceName)
	if err != nil {
		return err
	}
	if !inst.Registered() {
		return fmt.Errorf("实例 %q 未在配置中登记，请先执行 nyatmc instance create %s", inst.Name, inst.Name)
	}

	log := opts.Logger
	if log == nil {
		fileLogger, _, ferr := logger.NewFile(inst.Res.SupLog, logger.LevelInfo, logger.RotateOptions{
			MaxSize:    inst.Res.Log.RotateSize.Bytes(),
			MaxBackups: inst.Res.Log.KeepFiles,
			Compress:   inst.Res.Log.CompressEnabled(),
		})
		if ferr != nil {
			log = logger.Default()
			log.Warnf("无法写入 supervisor 日志 %s（改用标准输出）：%v", inst.Res.SupLog, ferr)
		} else {
			log = fileLogger
			defer log.Close()
		}
	}
	if log == nil { // 防御：任何情况下都不能让 supervisor 因为日志而崩
		log = logger.Discard()
	}

	s := &Supervisor{
		name:       inst.Name,
		mgr:        mgr,
		inst:       inst,
		log:        log,
		opts:       opts,
		state:      StateStopped,
		statusSubs: map[int]func(Status){},
		quit:       make(chan struct{}),
	}

	// 所有退出路径都必须留下痕迹：后台运行时标准错误指向空设备，
	// 如果这里不记日志，排查起来只能看到一个“启动后立即退出”。
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("supervisor 发生 panic：%v\n%s", r, debug.Stack())
			_ = WriteStatus(inst.Res.StateFile, Status{
				Instance:  inst.Name,
				State:     StateCrashed,
				LastError: fmt.Sprintf("supervisor 内部错误：%v", r),
				UpdatedAt: time.Now(),
			})
			err = fmt.Errorf("supervisor 内部错误（已记录到 %s）：%v", inst.Res.SupLog, r)
			return
		}
		if err != nil {
			log.Errorf("supervisor 退出：%v", err)
		}
	}()

	log.Infof("supervisor 启动：实例=%s pid=%d 配置文件=%s", inst.Name, os.Getpid(), cfgPath)
	return s.run(ctx)
}

func (s *Supervisor) run(ctx context.Context) error {
	res := s.inst.Res
	if err := s.inst.EnsureDirs(); err != nil {
		return err
	}

	// 已经在运行就不再重复守护
	if socket.PingPath(res.Endpoint, 800*time.Millisecond) {
		return fmt.Errorf("实例 %s 已有 supervisor 在运行（端点 %s）", s.name, res.Endpoint)
	}
	_ = socket.RemoveEndpoint(res.Endpoint)

	exe, _ := os.Executable()
	ipc, err := socket.Listen(socket.Endpoint{
		Instance:  s.name,
		Network:   "tcp",
		Address:   "127.0.0.1:0",
		PID:       os.Getpid(),
		StartedAt: time.Now(),
		Binary:    exe,
	}, s.handle)
	if err != nil {
		return err
	}
	s.ipc = ipc
	ipc.SetErrorHandler(func(err error) { s.log.Debugf("控制通道错误：%v", err) })

	if err := socket.WriteEndpoint(res.Endpoint, ipc.Endpoint()); err != nil {
		_ = ipc.Close()
		return err
	}
	if err := writePIDFile(res.PIDFile, os.Getpid()); err != nil {
		s.log.Warnf("写入 pid 文件失败：%v", err)
	}

	defer func() {
		_ = socket.RemoveEndpoint(res.Endpoint)
		_ = os.Remove(res.PIDFile)
		_ = ipc.Close()
	}()

	s.log.Infof("supervisor 已就绪：实例=%s pid=%d 控制端口=%s", s.name, os.Getpid(), ipc.Endpoint().Address)
	s.setState(StateStopped, "supervisor 已启动")

	// 配置热重载
	if err := s.mgr.Start(ctx); err != nil {
		s.log.Warnf("配置热重载不可用：%v", err)
	}
	s.mgr.SetErrorHandler(func(err error) { s.log.Warnf("配置热重载失败（继续使用旧配置）：%v", err) })
	s.mgr.Subscribe(func(cfg *config.Config) {
		s.mu.Lock()
		s.inst.Cfg = cfg
		s.mu.Unlock()
		s.log.Infof("配置已热重载")
	})

	// 信号：第一次优雅停止，第二次强制
	sigCh := shutdownSignals()
	defer releaseSignals(sigCh)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		count := 0
		for {
			select {
			case <-s.quit:
				// 必须同时监听 quit，否则 wg.Wait() 会等这个 goroutine，
				// 而它又在等 channel 关闭，形成死锁（退出流程永远走不完）。
				return
			case sig, ok := <-sigCh:
				if !ok {
					return
				}
				count++
				if count == 1 {
					s.log.Infof("收到信号 %s，开始优雅关闭…", sig)
					go s.requestStop(StopArgs{})
					continue
				}
				s.log.Warnf("再次收到信号 %s，强制退出", sig)
				if p := s.process(); p != nil {
					_ = p.Kill()
				}
				s.shutdown()
				return
			}
		}
	}()

	if s.opts.Attach {
		s.attachConsole()
	}

	if !s.opts.NoStart {
		if err := s.startServer(ctx); err != nil {
			s.log.Errorf("启动服务端失败：%v", err)
		}
	}

	// 维护循环
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		statusTick := time.NewTicker(3 * time.Second)
		rotateTick := time.NewTicker(30 * time.Second)
		defer statusTick.Stop()
		defer rotateTick.Stop()
		for {
			select {
			case <-s.quit:
				return
			case <-ctx.Done():
				return
			case <-statusTick.C:
				s.refreshStatus()
			case <-rotateTick.C:
				s.checkLogRotation()
			}
		}
	}()

	select {
	case <-s.quit:
	case <-ctx.Done():
		s.requestStop(StopArgs{})
		<-s.quit
	}

	s.wg.Wait()
	s.log.Infof("supervisor 已退出：实例=%s", s.name)
	_ = WriteStatus(res.StateFile, s.buildStatus())
	return nil
}

// attachConsole 前台模式下镜像输出并转发键盘输入。
func (s *Supervisor) attachConsole() {
	s.log.Infof("前台模式：直接输入指令回车发送给服务端，Ctrl+C 优雅关闭")
	if p := s.process(); p != nil {
		p.Subscribe(func(line string) { fmt.Fprintln(os.Stdout, line) })
	}
	// 刻意不放进 WaitGroup：读取键盘本质上是阻塞的，放进 wg 会让退出流程卡住
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			select {
			case <-s.quit:
				return
			default:
			}
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			p := s.process()
			if p == nil || !p.Running() {
				fmt.Fprintln(os.Stdout, "[nyatmc] 服务端未在运行")
				continue
			}
			if err := p.Send(line); err != nil {
				fmt.Fprintf(os.Stdout, "[nyatmc] 发送失败：%v\n", err)
			}
		}
	}()
}

// ---------------------------------------------------------------- 状态

func (s *Supervisor) res() config.Resolved {
	if s.mgr != nil {
		return s.mgr.Resolve(s.name)
	}
	return s.inst.Res
}

func (s *Supervisor) process() *Process {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.proc
}

func (s *Supervisor) watchdogEnabled() bool {
	if s.opts.Watchdog != nil {
		return *s.opts.Watchdog
	}
	return s.res().Daemon.WatchdogEnabled()
}

func (s *Supervisor) setState(state State, note string) {
	s.mu.Lock()
	s.state = state
	s.note = note
	s.mu.Unlock()
	s.persistStatus()
	s.broadcastStatus()
}

func (s *Supervisor) fail(err error) {
	s.mu.Lock()
	s.state = StateCrashed
	s.lastError = err.Error()
	s.mu.Unlock()
	s.persistStatus()
	s.broadcastStatus()
}

func (s *Supervisor) buildStatus() Status {
	s.mu.RLock()
	state := s.state
	ready := s.ready
	note := s.note
	lastErr := s.lastError
	startedAt := s.startedAt
	restarts := len(s.restartTimes)
	lastExit := s.lastExit
	proc := s.proc
	s.mu.RUnlock()

	res := s.res()
	st := Status{
		Instance:      s.name,
		State:         state,
		Ready:         ready,
		SupervisorPID: os.Getpid(),
		StartedAt:     startedAt,
		Restarts:      restarts,
		LastExitCode:  lastExit.Code,
		LastError:     lastErr,
		Note:          note,
		ServerType:    res.Server.Type,
		MemoryLimit:   res.Server.Memory,
		ServerDir:     res.Path,
		Watchdog:      s.watchdogEnabled(),
		ConsoleLines:  res.Log.ConsoleLines,
		MaxPlayers:    res.Server.MaxPlayers,
	}
	if s.mgr != nil {
		st.ConfigPath = s.mgr.Path()
	}

	if proc != nil {
		st.PID = proc.PID()
		st.UptimeSeconds = int64(proc.Uptime().Seconds())
		st.ConsoleLines = len(proc.Lines(0))
		snap := proc.Snapshot()
		st.Players = snap.Players
		st.TPS = snap.TPS
		st.TPSKnown = snap.TPSKnown
		st.TickBehindMS = snap.TickBehindMS
		st.Version = snap.Version
		st.Warnings = snap.Warnings
		st.Errors = snap.Errors
		st.MemoryUsedMB = proc.MemoryUsedMB()
		if snap.MaxPlayers > 0 {
			st.MaxPlayers = snap.MaxPlayers
		}
		if st.LastError == "" && snap.LastError != "" {
			st.LastError = snap.LastError
		}
		if state == StateRunning && !proc.Running() {
			st.State = StateStopped
		}
	}
	if st.Players == nil {
		st.Players = []string{}
	}
	st.UpdatedAt = time.Now()
	return st
}

func (s *Supervisor) refreshStatus() {
	// 进程已经不在但状态还停在 running（例如被外部 kill）时复位
	if p := s.process(); p != nil && !p.Running() {
		s.mu.Lock()
		s.ready = false
		s.mu.Unlock()
	}
	s.persistStatus()
	s.broadcastStatus()
}

func (s *Supervisor) persistStatus() {
	st := s.buildStatus()
	if err := WriteStatus(s.inst.Res.StateFile, st); err != nil {
		s.log.Debugf("写入状态文件失败：%v", err)
	}
}

func (s *Supervisor) subscribeStatus(fn func(Status)) func() {
	if fn == nil {
		return func() {}
	}
	s.subMu.Lock()
	id := s.nextSub
	s.nextSub++
	s.statusSubs[id] = fn
	s.subMu.Unlock()
	return func() {
		s.subMu.Lock()
		delete(s.statusSubs, id)
		s.subMu.Unlock()
	}
}

func (s *Supervisor) broadcastStatus() {
	st := s.buildStatus()
	s.subMu.Lock()
	subs := make([]func(Status), 0, len(s.statusSubs))
	for _, fn := range s.statusSubs {
		subs = append(subs, fn)
	}
	s.subMu.Unlock()
	for _, fn := range subs {
		func() {
			defer func() { recover() }()
			fn(st)
		}()
	}
}

// ---------------------------------------------------------------- 启停

func (s *Supervisor) startServer(ctx context.Context) error {
	s.mu.Lock()
	if s.proc != nil && s.proc.Running() {
		s.mu.Unlock()
		return fmt.Errorf("服务端已在运行")
	}
	s.mu.Unlock()

	res := s.res()
	if !res.Enabled {
		return fmt.Errorf("实例 %s 已被禁用（instances.%s.enabled = false）", s.name, s.name)
	}

	if res.Daemon.BackupBeforeStartEnabled() {
		s.log.Infof("按配置在启动前备份…")
		if _, _, err := backup.CreateAndPrune(s.inst, "before-start", s.log); err != nil {
			s.log.Warnf("启动前备份失败（继续启动）：%v", err)
		}
	}

	if err := s.inst.WriteEULA(false); err != nil {
		s.log.Warnf("写入 eula.txt 失败：%v", err)
	}

	if !s.inst.JarExists() {
		if res.Server.AutoDownloadEnabled() {
			s.log.Infof("服务端文件缺失，开始自动获取…")
			if err := ensureServerJar(ctx, s.inst, false, s.log); err != nil {
				s.fail(fmt.Errorf("自动获取服务端失败：%w", err))
				return fmt.Errorf("自动获取服务端失败：%w", err)
			}
		} else {
			err := fmt.Errorf("服务端文件不存在：%s（可执行 nyatmc server download 获取，或打开 server.auto_download）", s.inst.Res.JarPath)
			s.fail(err)
			return err
		}
	}
	if !s.inst.JarExists() {
		err := fmt.Errorf("服务端文件仍然不存在：%s", s.inst.Res.JarPath)
		s.fail(err)
		return err
	}

	s.rotateLogsOnStart()
	s.warnPortBusy(res.Server.Port)

	s.setState(StateStarting, "正在启动服务端")
	proc := NewProcess(s.inst, s.log)
	proc.SetExitHandler(s.handleExit)
	if s.opts.Attach {
		proc.Subscribe(func(line string) { fmt.Fprintln(os.Stdout, line) })
	}
	if err := proc.Start(ctx); err != nil {
		s.fail(err)
		return err
	}

	s.mu.Lock()
	s.proc = proc
	s.startedAt = time.Now()
	s.ready = false
	s.state = StateRunning
	s.note = ""
	s.lastError = ""
	s.mu.Unlock()
	s.persistStatus()
	s.broadcastStatus()

	s.log.Infof("服务端已启动：pid=%d", proc.PID())
	go s.watchReady(proc)
	return nil
}

// watchReady 等待服务端打印 Done，超时只提醒不干预。
func (s *Supervisor) watchReady(proc *Process) {
	res := s.res()
	timeout := res.Daemon.StartupTimeout.Std()
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if !proc.Running() {
			return
		}
		if proc.Ready() {
			s.mu.Lock()
			if !s.ready {
				s.ready = true
			}
			s.mu.Unlock()
			s.log.Infof("服务端已就绪（启动耗时 %s）", proc.Uptime().Round(time.Second))
			s.persistStatus()
			s.broadcastStatus()
			return
		}
		if time.Now().After(deadline) {
			s.log.Warnf("等待服务端就绪超时（%s），进程仍在运行，请查看日志确认", timeout)
			s.setState(s.currentState(), fmt.Sprintf("启动超过 %s 仍未就绪，请检查日志", timeout))
			return
		}
	}
}

func (s *Supervisor) currentState() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

func (s *Supervisor) stopServer(ctx context.Context, args StopArgs) error {
	proc := s.process()
	if proc == nil || !proc.Running() {
		s.setState(StateStopped, "")
		return nil
	}
	s.setState(StateStopping, "正在停止服务端")
	if args.Force {
		s.log.Warnf("强制结束服务端进程")
		if err := proc.Kill(); err != nil {
			return fmt.Errorf("强制结束失败：%w", err)
		}
		return nil
	}
	res := s.res()
	timeout := args.Timeout
	if timeout <= 0 {
		timeout = res.Daemon.StopTimeout.Std()
	}
	if err := proc.Stop(ctx, res.Daemon.StopCommand, timeout, res.Daemon.KillTimeout.Std()); err != nil {
		return err
	}
	s.setState(StateStopped, "已被请求停止")
	return nil
}

func (s *Supervisor) requestStop(args StopArgs) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := s.stopServer(ctx, args); err != nil {
		s.log.Errorf("停止服务端失败：%v", err)
	}
	if args.KeepSupervisor {
		return
	}
	s.shutdown()
}

func (s *Supervisor) requestRestart(args RestartArgs, reason string) {
	delay := args.Delay
	if delay <= 0 {
		delay = 2 * time.Second
	}
	s.setState(StateRestarting, reason)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := s.stopServer(ctx, StopArgs{Force: args.Force}); err != nil {
		s.log.Errorf("重启前停止失败：%v", err)
	}
	select {
	case <-time.After(delay):
	case <-s.quit:
		return
	}
	s.mu.RLock()
	shutting := s.shuttingDown
	s.mu.RUnlock()
	if shutting {
		return
	}
	if err := s.startServer(context.Background()); err != nil {
		s.log.Errorf("重启失败：%v", err)
	}
}

func (s *Supervisor) shutdown() {
	s.mu.Lock()
	if s.shuttingDown {
		s.mu.Unlock()
		return
	}
	s.shuttingDown = true
	s.mu.Unlock()
	s.quitOnce.Do(func() { close(s.quit) })
}

// handleExit 处理服务端退出：看门狗在这里决定是否自动重启。
func (s *Supervisor) handleExit(ev ExitEvent) {
	s.mu.Lock()
	s.lastExit = ev
	unexpected := !ev.Expected
	s.ready = false
	if unexpected {
		s.state = StateCrashed
		if ev.Err != "" {
			s.lastError = fmt.Sprintf("服务端异常退出（退出码 %d）：%s", ev.Code, ev.Err)
		} else {
			s.lastError = fmt.Sprintf("服务端异常退出（退出码 %d）", ev.Code)
		}
	} else {
		s.state = StateStopped
	}
	shutting := s.shuttingDown
	watchdog := unexpected && !shutting
	maxRestarts := s.res().Daemon.MaxRestarts
	window := s.res().Daemon.RestartWindow.Std()
	s.mu.Unlock()
	s.persistStatus()
	s.broadcastStatus()

	if !watchdog {
		if unexpected {
			s.log.Errorf("服务端异常退出（退出码 %d），看门狗已关闭，不再自动重启", ev.Code)
		}
		return
	}

	if window <= 0 {
		window = time.Hour
	}
	cut := time.Now().Add(-window)
	s.mu.Lock()
	kept := s.restartTimes[:0]
	for _, t := range s.restartTimes {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	s.restartTimes = kept
	over := maxRestarts > 0 && len(kept) >= maxRestarts
	s.mu.Unlock()

	if over {
		s.setState(StateCrashed, fmt.Sprintf("%s 内已重启 %d 次，达到上限，停止自动重启", window, len(kept)))
		s.log.Errorf("看门狗放弃：%s 内已重启 %d 次（上限 %d），请检查日志后手动 nyatmc start", window, len(kept), maxRestarts)
		return
	}

	delay := s.res().Daemon.RestartDelay.Std()
	s.mu.Lock()
	s.restartTimes = append(s.restartTimes, time.Now())
	s.mu.Unlock()
	s.setState(StateRestarting, fmt.Sprintf("看门狗将在 %s 后自动重启（第 %d 次）", delay, len(kept)+1))
	s.log.Warnf("看门狗：服务端退出，%s 后自动重启", delay)

	go func() {
		select {
		case <-time.After(delay):
		case <-s.quit:
			return
		}
		s.mu.RLock()
		shutting := s.shuttingDown
		s.mu.RUnlock()
		if shutting {
			return
		}
		if err := s.startServer(context.Background()); err != nil {
			s.log.Errorf("看门狗重启失败：%v", err)
		}
	}()
}

// ---------------------------------------------------------------- 维护

// warnPortBusy 启动前提醒端口占用（只提醒，不阻止启动）。
func (s *Supervisor) warnPortBusy(port int) {
	if port <= 0 {
		return
	}
	if err := probePort(port); err != nil {
		s.log.Warnf("端口 %d 当前无法绑定（%v），如果服务端启动失败请检查是否被其它程序占用", port, err)
	}
}

// rotateLogsOnStart 在 JVM 尚未持有 logs/latest.log 时完成一次真正的切割。
func (s *Supervisor) rotateLogsOnStart() {
	res := s.res()
	limit := res.Log.RotateSize.Bytes()
	if limit <= 0 {
		return
	}
	latest := s.inst.MinecraftLogPath()
	st, err := os.Stat(latest)
	if err != nil || st.Size() < limit {
		return
	}
	archiveDir := filepath.Join(res.LogsDir, "nyatmc-archive")
	dst, err := logger.ArchiveFile(latest, archiveDir, res.Log.CompressEnabled())
	if err != nil {
		s.log.Warnf("归档服务端日志失败：%v", err)
		return
	}
	if err := os.Truncate(latest, 0); err != nil {
		s.log.Warnf("清空 latest.log 失败（将继续追加）：%v", err)
	} else {
		s.log.Infof("启动前已切割日志：%s（上限 %s）", dst, backup.HumanSize(limit))
	}
	s.pruneLogArchives(archiveDir)
}

func (s *Supervisor) pruneLogArchives(dir string) {
	res := s.res()
	maxAge := time.Duration(res.Log.MaxAgeDays) * 24 * time.Hour
	if _, err := logger.PruneDir(dir, res.Log.KeepFiles, maxAge); err != nil {
		s.log.Debugf("清理日志归档失败：%v", err)
	}
}

// checkLogRotation 监控 logs/latest.log 大小：归档留存，必要时（无人在线）优雅重启切割。
func (s *Supervisor) checkLogRotation() {
	res := s.res()
	limit := res.Log.RotateSize.Bytes()
	if limit <= 0 {
		return
	}
	latest := s.inst.MinecraftLogPath()
	st, err := os.Stat(latest)
	if err != nil || st.Size() < limit {
		return
	}
	archiveDir := filepath.Join(res.LogsDir, "nyatmc-archive")

	s.mu.RLock()
	archived := s.archivedBytes
	s.mu.RUnlock()
	if st.Size() > archived {
		dst, err := logger.ArchiveFile(latest, archiveDir, res.Log.CompressEnabled())
		if err != nil {
			s.log.Warnf("归档服务端日志失败：%v", err)
		} else {
			s.mu.Lock()
			s.archivedBytes = st.Size()
			s.mu.Unlock()
			s.log.Warnf("logs/latest.log 已达 %s（上限 %s），已归档一份到 %s", backup.HumanSize(st.Size()), backup.HumanSize(limit), dst)
			s.pruneLogArchives(archiveDir)
		}
	}

	if !res.Log.RotateRestartEnabled() {
		return
	}
	proc := s.process()
	if proc == nil || !proc.Running() {
		return
	}
	if len(proc.Snapshot().Players) > 0 {
		s.log.Warnf("日志已超限，但当前有 %d 名玩家在线，推迟切割（避免打断玩家）", len(proc.Snapshot().Players))
		return
	}
	minInterval := res.Log.MinRotateInterval.Std()
	s.mu.RLock()
	last := s.lastRotateAt
	s.mu.RUnlock()
	if !last.IsZero() && time.Since(last) < minInterval {
		return
	}
	s.mu.Lock()
	s.lastRotateAt = time.Now()
	s.mu.Unlock()
	s.log.Warnf("当前无玩家在线，执行优雅重启以切割日志（保留 %s）", backup.HumanSize(limit))
	s.requestRestart(RestartArgs{Delay: 3 * time.Second}, "日志轮转：优雅重启切割 latest.log")
}

// ---------------------------------------------------------------- 控制通道

func (s *Supervisor) handle(ctx context.Context, req socket.Request, send func(socket.Response) error) error {
	switch req.Cmd {
	case CmdPing, CmdStatus, CmdInfo:
		return sendJSON(send, s.buildStatus())

	case CmdStop:
		var args StopArgs
		_ = decodeArgs(req, &args)
		if err := sendJSON(send, s.buildStatus()); err != nil {
			return err
		}
		go s.requestStop(args)
		return nil

	case CmdRestart:
		var args RestartArgs
		_ = decodeArgs(req, &args)
		if err := sendJSON(send, s.buildStatus()); err != nil {
			return err
		}
		go s.requestRestart(args, "收到重启请求")
		return nil

	case CmdStart:
		proc := s.process()
		if proc != nil && proc.Running() {
			return fmt.Errorf("服务端已在运行（pid %d）", proc.PID())
		}
		if err := s.startServer(ctx); err != nil {
			return err
		}
		return sendJSON(send, s.buildStatus())

	case CmdCommand:
		var args CommandArgs
		if err := decodeArgs(req, &args); err != nil {
			return err
		}
		proc := s.process()
		if proc == nil || !proc.Running() {
			return fmt.Errorf("服务端未在运行，无法发送指令")
		}
		if err := proc.Send(args.Line); err != nil {
			return err
		}
		return sendJSON(send, map[string]any{"sent": args.Line})

	case CmdLogs:
		var args LogsArgs
		_ = decodeArgs(req, &args)
		n := args.Lines
		if n <= 0 {
			n = 200
		}
		return sendJSON(send, LogsResult{Lines: s.logLines(n)})

	case CmdSubscribe:
		return s.streamEvents(ctx, send)

	default:
		return fmt.Errorf("未知控制命令 %q", req.Cmd)
	}
}

func (s *Supervisor) streamEvents(ctx context.Context, send func(socket.Response) error) error {
	sendEvent := func(ev LogEvent) error { return sendJSON(send, ev) }
	if err := sendEvent(LogEvent{Kind: "status", Status: statusPtr(s.buildStatus()), Time: time.Now()}); err != nil {
		return err
	}

	lines := make(chan string, 256)
	proc := s.process()
	var unsubProc func()
	if proc != nil {
		unsubProc = proc.Subscribe(func(line string) {
			select {
			case lines <- line:
			default:
			}
		})
		defer unsubProc()
	}

	stCh := make(chan Status, 16)
	unsubStatus := s.subscribeStatus(func(st Status) {
		select {
		case stCh <- st:
		default:
		}
	})
	defer unsubStatus()

	poll := time.NewTicker(10 * time.Second)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.quit:
			return nil
		case line := <-lines:
			if err := sendEvent(LogEvent{Kind: "log", Text: line, Time: time.Now()}); err != nil {
				return nil
			}
		case st := <-stCh:
			if err := sendEvent(LogEvent{Kind: "status", Status: statusPtr(st), Time: time.Now()}); err != nil {
				return nil
			}
		case <-poll.C:
			// 定期推送心跳状态，保证 TUI/Web 的内存、TPS 等指标持续更新
			if err := sendEvent(LogEvent{Kind: "status", Status: statusPtr(s.buildStatus()), Time: time.Now()}); err != nil {
				return nil
			}
		}
	}
}

// logLines 返回最近 n 行控制台日志（进程不在时退回读文件）。
func (s *Supervisor) logLines(n int) []string {
	if proc := s.process(); proc != nil {
		if lines := proc.Lines(n); len(lines) > 0 {
			return lines
		}
	}
	lines, err := readTailLines(s.inst.ConsoleLogPath(), n, 512*1024)
	if err != nil {
		return nil
	}
	return lines
}

func statusPtr(st Status) *Status { return &st }

func sendJSON(send func(socket.Response) error, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return send(socket.Response{OK: true, Data: data})
}

func decodeArgs(req socket.Request, out any) error {
	if len(req.Args) == 0 {
		return nil
	}
	if err := json.Unmarshal(req.Args, out); err != nil {
		return fmt.Errorf("解析控制参数失败：%w", err)
	}
	return nil
}

// Package tunnel 封装 cloudflared，为 nyatmc 提供开箱即用的 Cloudflare 内网穿透。
//
// 只使用标准库，不引入任何第三方依赖。
//
// # 跨进程管理方案
//
// 隧道进程不能随 CLI 一起退出，因此这里没有让 nyatmc 自己常驻，而是：
//
//  1. 把 cloudflared 作为独立子进程派生（Unix 用 Setsid、Windows 用 DETACHED_PROCESS），
//     标准输出与标准错误重定向到 <state>/tunnel.log 文件，之后父进程立刻放手；
//  2. 运行状态（pid、公网地址、目标、模式、日志路径）持久化到 <state>/tunnel.json，
//     公网地址与环形缓冲里的最近 200 行输出都从 tunnel.log 尾部解析；
//  3. 同一进程内还会把句柄登记到包级注册表，便于 Web 进程直接查询自己启动的隧道。
//
// 于是 `nyatmc tunnel start` 退出后隧道继续运行，`nyatmc tunnel stop` /
// `nyatmc tunnel status` 无论由哪个进程发起，看到的都是同一份状态。
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yuzumikonami/nyatmc/internal/config"
	"github.com/yuzumikonami/nyatmc/pkg/logger"
)

// 隧道模式。
const (
	// ModeQuick 是无需账号的快速隧道（随机 trycloudflare.com 域名）。
	ModeQuick = "quick"
	// ModeNamed 是使用 token 的命名隧道。
	ModeNamed = "named"
)

// DefaultProtocol 是 cloudflared 默认使用的传输协议。
const DefaultProtocol = "quic"

const (
	// maxLogLines 是状态里保留的 cloudflared 输出行数（环形缓冲容量）。
	maxLogLines = 200
	// maxLogBytes 是读取日志尾部时最多读入的字节数。
	maxLogBytes = 256 * 1024
	// defaultWaitURL 是等待公网地址的默认超时。
	defaultWaitURL = 60 * time.Second
	// stopGrace 是 Stop 时等待 cloudflared 自行退出的时间，超时后强杀。
	stopGrace = 5 * time.Second
)

// ErrNotRunning 表示当前没有正在运行的隧道。
var ErrNotRunning = errors.New("隧道未在运行")

// quickURLRe 匹配快速隧道分配的公网地址。
var quickURLRe = regexp.MustCompile(`https://[a-zA-Z0-9-]+\.trycloudflare\.com`)

// Options 描述一次隧道启动请求。
type Options struct {
	// Bin 是 cloudflared 路径，留空则自动查找 / 下载。
	Bin string
	// Port 是本地要暴露的端口。
	Port int
	// Target 是完整目标 URL，优先于 Port（例如 http://127.0.0.1:8080）。
	Target string
	// Protocol 是 cloudflared 传输协议：quic / http2 / auto。
	Protocol string
	// Token 是命名隧道 token，留空则使用快速隧道。
	Token string
	// Hostname 是命名隧道域名（仅用于展示）。
	Hostname string
	// Dir 是存放下载的 cloudflared 的目录，留空表示 ~/.nyatmc/bin。
	Dir string
	// ExtraArgs 是追加给 cloudflared 的参数。
	ExtraArgs []string
	// AutoInstall 表示缺少 cloudflared 时自动下载。
	AutoInstall bool
	// Logger 是日志输出，留空使用全局日志器。
	Logger *logger.Logger
}

// Status 是隧道的运行状态（可安全地序列化为 JSON 供 Web 使用）。
type Status struct {
	Running   bool      `json:"running"`
	URL       string    `json:"url"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	// Uptime 是运行时长；JSON 里用 UptimeSeconds 表达，避免纳秒可读性问题。
	Uptime        time.Duration `json:"-"`
	UptimeSeconds int64         `json:"uptime_seconds"`
	Target        string        `json:"target"`
	// Mode 取值为 quick 或 named。
	Mode      string `json:"mode"`
	BinPath   string `json:"bin_path"`
	Protocol  string `json:"protocol,omitempty"`
	Hostname  string `json:"hostname,omitempty"`
	LogPath   string `json:"log_path,omitempty"`
	LastError string `json:"last_error"`

	// LogLines 是最近的 cloudflared 输出（最多 200 行），不参与 JSON 序列化。
	LogLines []string `json:"-"`
}

// Tunnel 是一个已启动（或从状态文件接管的）隧道句柄。
type Tunnel struct {
	opts      Options
	target    string
	mode      string
	protocol  string
	hostname  string
	binPath   string
	logPath   string
	pid       int
	startedAt time.Time
	log       *logger.Logger

	mu      sync.Mutex
	url     string
	lastErr string
	exited  bool
}

// ---------------------------------------------------------------- 启动

// Start 启动隧道；同一目标已经在运行时直接返回现有句柄（幂等）。
func (o Options) Start(ctx context.Context) (*Tunnel, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	log := o.Logger
	if log == nil {
		log = logger.Default()
	}

	target, err := o.resolveTarget()
	if err != nil {
		return nil, err
	}
	mode := ModeQuick
	if strings.TrimSpace(o.Token) != "" {
		mode = ModeNamed
	}

	// 幂等：先看本进程注册表，再看其它进程留下的状态文件。
	if t := lookupAlive(target); t != nil {
		return t, nil
	}
	if st, err := loadState(); err == nil && st.Running && st.Target == target && processAlive(st.PID) {
		t := tunnelFromState(o, st)
		register(t)
		log.Infof("目标 %s 的隧道已在运行（pid %d），直接复用", target, st.PID)
		return t, nil
	}

	bin, err := o.resolveBinary(ctx, log)
	if err != nil {
		return nil, err
	}

	logPath := LogPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, fmt.Errorf("创建状态目录 %s 失败: %w", filepath.Dir(logPath), err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("打开隧道日志 %s 失败: %w", logPath, err)
	}
	defer logFile.Close()

	args := o.commandArgs(target, mode)
	cmd := exec.Command(bin, args...)
	cmd.SysProcAttr = detachAttr()
	cmd.Dir = filepath.Dir(logPath)

	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("打开 %s 失败: %w", os.DevNull, err)
	}
	defer devnull.Close()
	cmd.Stdin = devnull
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 cloudflared 失败（%s）：%w", bin, err)
	}
	pid := cmd.Process.Pid
	// 后台回收子进程，避免它退出后变成僵尸；nyatmc 进程结束后 cloudflared
	// 因为已脱离终端（Setsid / DETACHED_PROCESS）而继续运行。
	go func() { _ = cmd.Wait() }()

	t := &Tunnel{
		opts:      o,
		target:    target,
		mode:      mode,
		protocol:  o.protocol(),
		hostname:  strings.TrimSpace(o.Hostname),
		binPath:   bin,
		logPath:   logPath,
		pid:       pid,
		startedAt: time.Now(),
		log:       log,
		url:       o.initialURL(mode),
	}
	register(t)
	if err := t.saveState(); err != nil {
		log.Warnf("写入隧道状态文件失败：%v", err)
	}
	log.Infof("cloudflared 已启动（pid %d，模式 %s，目标 %s）", pid, mode, target)
	return t, nil
}

// resolveTarget 计算本地目标地址。
func (o Options) resolveTarget() (string, error) {
	if t := strings.TrimSpace(o.Target); t != "" {
		return normalizeTarget(t), nil
	}
	if o.Port > 0 {
		if o.Port > 65535 {
			return "", fmt.Errorf("端口 %d 非法，应在 1-65535 之间", o.Port)
		}
		return fmt.Sprintf("http://127.0.0.1:%d", o.Port), nil
	}
	return "", errors.New("缺少要暴露的目标：请指定本地端口（--port）或完整地址（--target）")
}

// normalizeTarget 补全协议前缀。
func normalizeTarget(t string) string {
	t = strings.TrimSpace(t)
	if t == "" || strings.Contains(t, "://") {
		return t
	}
	return "http://" + t
}

// protocol 返回实际使用的传输协议。
func (o Options) protocol() string {
	p := strings.TrimSpace(o.Protocol)
	if p == "" {
		return DefaultProtocol
	}
	return p
}

// commandArgs 组装 cloudflared 参数。
func (o Options) commandArgs(target, mode string) []string {
	args := []string{"tunnel", "--no-autoupdate"}
	if mode == ModeNamed {
		args = append(args, "run", "--token", strings.TrimSpace(o.Token))
	} else {
		args = append(args, "--url", target, "--protocol", o.protocol())
	}
	for _, a := range o.ExtraArgs {
		if strings.TrimSpace(a) == "" {
			continue
		}
		args = append(args, a)
	}
	return args
}

// initialURL 返回启动时就能确定的公网地址（命名隧道配置了域名时）。
func (o Options) initialURL(mode string) string {
	if mode != ModeNamed {
		return ""
	}
	h := strings.TrimSpace(o.Hostname)
	switch {
	case h == "":
		return ""
	case strings.Contains(h, "://"):
		return h
	default:
		return "https://" + h
	}
}

// resolveBinary 定位 cloudflared，必要时自动下载。
func (o Options) resolveBinary(ctx context.Context, log *logger.Logger) (string, error) {
	bin, err := FindBinary(o.Bin, filepath.Join(o.binDir(), localBinaryName()))
	if err == nil {
		return bin, nil
	}
	if !o.AutoInstall {
		return "", fmt.Errorf("%w；也可以设置 tunnel.auto_install = true 让 nyatmc 自动下载", err)
	}
	log.Infof("未找到 cloudflared，准备自动下载…")
	path, derr := EnsureBinary(ctx, o.binDir(), log)
	if derr != nil {
		return "", derr
	}
	return path, nil
}

// binDir 返回存放 cloudflared 的目录。
func (o Options) binDir() string {
	if d := strings.TrimSpace(o.Dir); d != "" {
		return config.ExpandPath(d)
	}
	return DefaultBinDir()
}

// ---------------------------------------------------------------- 状态与生命周期

// Status 返回当前运行状态（含最近日志）。
func (t *Tunnel) Status() Status {
	t.mu.Lock()
	url := t.url
	lastErr := t.lastErr
	exited := t.exited
	t.mu.Unlock()

	alive := !exited && processAlive(t.pid)
	st := Status{
		Running:   alive,
		URL:       url,
		PID:       t.pid,
		StartedAt: t.startedAt,
		Target:    t.target,
		Mode:      t.mode,
		BinPath:   t.binPath,
		Protocol:  t.protocol,
		Hostname:  t.hostname,
		LogPath:   t.logPath,
		LastError: lastErr,
	}
	if alive {
		st.Uptime = time.Since(t.startedAt)
		st.UptimeSeconds = int64(st.Uptime.Seconds())
	} else if st.LastError == "" && t.pid > 0 {
		st.LastError = "cloudflared 已退出"
	}
	st.LogLines = tailLines(t.logPath, maxLogLines)
	if st.URL == "" {
		st.URL = ExtractURL(strings.Join(st.LogLines, "\n"))
	}
	return st
}

// URL 返回公网地址（必要时从 cloudflared 输出里再解析一次）。
func (t *Tunnel) URL() string {
	t.mu.Lock()
	u := t.url
	t.mu.Unlock()
	if u != "" {
		return u
	}
	if found := t.scanURL(); found != "" {
		t.setURL(found)
		return found
	}
	return ""
}

// WaitURL 等待公网地址出现。
func (t *Tunnel) WaitURL(ctx context.Context, timeout time.Duration) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = defaultWaitURL
	}
	if u := t.URL(); u != "" {
		return u, nil
	}

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		if u := t.scanURL(); u != "" {
			t.setURL(u)
			if err := t.saveState(); err != nil && t.log != nil {
				t.log.Warnf("写入隧道状态文件失败：%v", err)
			}
			return u, nil
		}
		if !t.alive() {
			lines := tailLines(t.logPath, 3)
			detail := strings.Join(lines, " | ")
			if detail == "" {
				detail = "cloudflared 没有任何输出"
			}
			return "", fmt.Errorf("cloudflared 已退出，未能取得公网地址（%s）；完整日志：%s", detail, t.logPath)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
		if time.Now().After(deadline) {
			if t.mode == ModeNamed {
				return "", fmt.Errorf("命名隧道无法从输出推断公网地址，请在配置里填写 tunnel.hostname（当前为空）；日志：%s", t.logPath)
			}
			return "", fmt.Errorf("等待公网地址超时（%s）；用 nyatmc tunnel status 查看日志：%s", timeout, t.logPath)
		}
	}
}

// Stop 结束隧道进程，并清理状态文件。
func (t *Tunnel) Stop() error {
	t.mu.Lock()
	pid := t.pid
	t.mu.Unlock()

	if pid <= 0 {
		return ErrNotRunning
	}
	if !processAlive(pid) {
		t.markExited(nil)
		unregister(t.target)
		return writeStoppedState(t.target, "cloudflared 早已退出")
	}
	if err := killGraceful(pid); err != nil {
		return fmt.Errorf("结束 cloudflared（pid %d）失败：%w", pid, err)
	}
	t.markExited(nil)
	unregister(t.target)
	if t.log != nil {
		t.log.Infof("隧道（pid %d，目标 %s）已停止", pid, t.target)
	}
	return writeStoppedState(t.target, "")
}

// alive 判断隧道进程是否仍然存活。
func (t *Tunnel) alive() bool {
	t.mu.Lock()
	exited := t.exited
	t.mu.Unlock()
	return !exited && processAlive(t.pid)
}

func (t *Tunnel) setURL(u string) {
	t.mu.Lock()
	t.url = u
	t.mu.Unlock()
}

func (t *Tunnel) markExited(err error) {
	t.mu.Lock()
	t.exited = true
	if err != nil {
		t.lastErr = err.Error()
	}
	t.mu.Unlock()
}

// scanURL 从 cloudflared 输出里解析公网地址。
func (t *Tunnel) scanURL() string {
	return ExtractURL(strings.Join(tailLines(t.logPath, maxLogLines), "\n"))
}

// saveState 把当前状态写入 state/tunnel.json。
func (t *Tunnel) saveState() error {
	st := t.Status()
	return writeState(State{
		Running:   st.Running,
		URL:       st.URL,
		PID:       st.PID,
		StartedAt: t.startedAt,
		Target:    t.target,
		Mode:      t.mode,
		Protocol:  t.protocol,
		Hostname:  t.hostname,
		BinPath:   t.binPath,
		LogPath:   t.logPath,
		LastError: st.LastError,
	})
}

// tunnelFromState 用状态文件里的信息接管一个由其它进程启动的隧道。
func tunnelFromState(o Options, st State) *Tunnel {
	log := o.Logger
	if log == nil {
		log = logger.Default()
	}
	return &Tunnel{
		opts:      o,
		target:    st.Target,
		mode:      st.Mode,
		protocol:  st.Protocol,
		hostname:  st.Hostname,
		binPath:   st.BinPath,
		logPath:   stateLogPath(st),
		pid:       st.PID,
		startedAt: st.StartedAt,
		log:       log,
		url:       st.URL,
		lastErr:   st.LastError,
	}
}

// ---------------------------------------------------------------- 注册表

var (
	regMu    sync.Mutex
	registry = map[string]*Tunnel{}
)

func register(t *Tunnel) {
	if t == nil || t.target == "" {
		return
	}
	regMu.Lock()
	registry[t.target] = t
	regMu.Unlock()
}

func unregister(target string) {
	regMu.Lock()
	delete(registry, target)
	regMu.Unlock()
}

func lookup(target string) *Tunnel {
	regMu.Lock()
	defer regMu.Unlock()
	return registry[target]
}

func lookupAlive(target string) *Tunnel {
	t := lookup(target)
	if t != nil && t.alive() {
		return t
	}
	return nil
}

func registered() []*Tunnel {
	regMu.Lock()
	defer regMu.Unlock()
	out := make([]*Tunnel, 0, len(registry))
	for _, t := range registry {
		out = append(out, t)
	}
	return out
}

// Running 返回当前所有运行中的隧道（含由其它进程启动、记录在状态文件里的）。
func Running() []Status {
	var out []Status
	seen := map[string]bool{}
	for _, t := range registered() {
		st := t.Status()
		if !st.Running {
			continue
		}
		seen[st.Target] = true
		out = append(out, st)
	}
	if st, ok := StatusFromState(); ok && st.Running && !seen[st.Target] {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Target < out[j].Target })
	return out
}

// Current 返回当前隧道状态（优先跨进程的状态文件，其次进程内注册表）。
func Current() (Status, bool) {
	if st, ok := StatusFromState(); ok {
		return st, true
	}
	for _, t := range registered() {
		return t.Status(), true
	}
	return Status{}, false
}

// StatusFromState 直接读取状态文件得到隧道状态（跨进程查询用）。
func StatusFromState() (Status, bool) {
	s, err := loadState()
	if err != nil || strings.TrimSpace(s.Target) == "" {
		return Status{}, false
	}
	alive := s.Running && processAlive(s.PID)
	st := Status{
		Running:   alive,
		URL:       s.URL,
		PID:       s.PID,
		StartedAt: s.StartedAt,
		Target:    s.Target,
		Mode:      s.Mode,
		BinPath:   s.BinPath,
		Protocol:  s.Protocol,
		Hostname:  s.Hostname,
		LogPath:   stateLogPath(s),
		LastError: s.LastError,
	}
	if alive {
		st.Uptime = time.Since(s.StartedAt)
		if st.Uptime < 0 {
			st.Uptime = 0
		}
		st.UptimeSeconds = int64(st.Uptime.Seconds())
	} else if st.LastError == "" && s.Running {
		st.LastError = fmt.Sprintf("cloudflared（pid %d）已退出", s.PID)
	}
	st.LogLines = tailLines(st.LogPath, maxLogLines)
	if st.URL == "" {
		st.URL = ExtractURL(strings.Join(st.LogLines, "\n"))
	}
	return st, true
}

// StopAll 停止当前记录的隧道（无论由哪个进程启动）。
func StopAll() error {
	var errs []error
	stopped := false
	for _, t := range registered() {
		err := t.Stop()
		switch {
		case err == nil:
			stopped = true
		case errors.Is(err, ErrNotRunning):
		default:
			errs = append(errs, err)
		}
	}

	// 处理只存在于状态文件里的隧道（由别的进程 / 上一次 CLI 启动）。
	if s, err := loadState(); err == nil && s.PID > 0 && processAlive(s.PID) {
		if err := killGraceful(s.PID); err != nil {
			errs = append(errs, err)
		} else {
			stopped = true
			if err := writeStoppedState(s.Target, ""); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	if !stopped {
		return ErrNotRunning
	}
	return nil
}

// killGraceful 先发信号让 cloudflared 自行退出，超时后强杀。
func killGraceful(pid int) error {
	if pid <= 0 || !processAlive(pid) {
		return nil
	}
	if err := terminate(pid); err != nil {
		return err
	}
	deadline := time.Now().Add(stopGrace)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !processAlive(pid) {
		return nil
	}
	return forceKill(pid)
}

// ---------------------------------------------------------------- 小工具

// ExtractURL 从一段 cloudflared 输出里提取快速隧道的公网地址。
func ExtractURL(text string) string {
	return quickURLRe.FindString(text)
}

// tailLines 读取文件末尾最多 maxLogBytes 字节，返回最后 n 行（相当于环形缓冲）。
func tailLines(path string, n int) []string {
	if n <= 0 {
		n = maxLogLines
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil
	}
	start := int64(0)
	if st.Size() > maxLogBytes {
		start = st.Size() - maxLogBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	text := string(data)
	if start > 0 {
		// 从中间截断时首行可能不完整，丢掉它
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	lines := splitLines(text)
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// splitLines 按行切分并丢弃空行。
func splitLines(text string) []string {
	raw := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(raw))
	for _, l := range raw {
		l = strings.TrimRight(l, "\r")
		if strings.TrimSpace(l) == "" {
			continue
		}
		out = append(out, l)
	}
	return out
}

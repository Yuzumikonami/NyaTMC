package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/yuzumikonami/nyatmc/internal/config"
	"github.com/yuzumikonami/nyatmc/internal/daemon"
	"github.com/yuzumikonami/nyatmc/internal/instance"
	"github.com/yuzumikonami/nyatmc/pkg/logger"
)

// Options 是终端看板的启动参数。
type Options struct {
	// Instance 是初始实例名；留空表示用配置里的默认实例。
	Instance string
	// Refresh 是状态与日志的刷新周期（默认 1s）。
	Refresh time.Duration
	// NoAltScreen 为 true 时不进入备用屏幕缓冲区（调试用）。
	NoAltScreen bool
	// Instances 只监控这些实例（为空表示配置里的全部实例）。
	Instances []string
}

// logBufferLines 是界面里日志区最多保留的行数（内存占用恒定）。
const logBufferLines = 600

// toastTTL 是操作结果提示在界面上停留的时间。
const toastTTL = 4 * time.Second

// minDrawInterval 是两次刷新之间的最小间隔，避免日志刷屏时吃满 CPU。
const minDrawInterval = 45 * time.Millisecond

// ErrNotTTY 表示当前输出不是终端，无法运行全屏看板。
var ErrNotTTY = errors.New("当前不是交互式终端，无法运行全屏看板")

// Run 启动终端看板，直到用户退出或 ctx 取消。
func Run(ctx context.Context, cfg *config.Config, opts Options) (err error) {
	if cfg == nil {
		return errors.New("配置为空，无法启动看板")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	term, terr := openTerminal()
	if terr != nil {
		return fmt.Errorf("进入终端原始模式失败（可以试试 --no-alt-screen）：%w", terr)
	}
	if !term.tty {
		term.close()
		return ErrNotTTY
	}

	// 无论正常退出、提前返回还是 panic，都要把终端恢复原样。
	defer func() {
		if r := recover(); r != nil {
			restoreScreen(term, opts.NoAltScreen)
			term.close()
			err = fmt.Errorf("看板内部错误（终端已恢复）：%v", r)
			return
		}
		restoreScreen(term, opts.NoAltScreen)
		term.close()
	}()

	setupScreen(term, opts.NoAltScreen)

	c := newController(ctx, cfg, term, opts)
	c.log.Infof("nyatmc 看板已启动（实例 %s）", c.inst.Name)
	defer c.shutdown()
	return c.run()
}

// setupScreen 进入备用屏幕缓冲区、隐藏光标、清屏。
func setupScreen(t *terminal, noAlt bool) {
	var b strings.Builder
	if noAlt {
		// 调试模式：不进备用屏，只在原地刷新
		b.WriteString("\x1b[2J")
	} else {
		b.WriteString("\x1b[?1049h")
	}
	b.WriteString("\x1b[?25l") // 隐藏光标
	b.WriteString("\x1b[H")
	b.WriteString("\x1b[2J")
	_, _ = io.WriteString(t.out, b.String())
}

// restoreScreen 退出备用屏幕、恢复光标。
func restoreScreen(t *terminal, noAlt bool) {
	if t == nil {
		return
	}
	var b strings.Builder
	b.WriteString("\x1b[0m")
	b.WriteString("\x1b[?25h") // 恢复光标
	if !noAlt {
		b.WriteString("\x1b[?1049l")
	} else {
		b.WriteString("\x1b[2J\x1b[H")
	}
	_, _ = io.WriteString(t.out, b.String())
}

// runMode 是看板的主体模式。
type runMode int

const (
	dashDefault runMode = iota // 日志 + 玩家
	dashLogsOnly
	dashPlayersOnly
)

// controller 是看板的全部可变状态。
//
// 只有主循环会写这些字段，数据源与操作 goroutine 都通过 channel 通信，
// 因此不需要加锁。
type controller struct {
	ctx  context.Context
	cfg  *config.Config
	term *terminal
	log  *logger.Logger
	opts Options

	instances []*instance.Instance
	idx       int
	inst      *instance.Instance

	width  int
	height int

	status  daemon.Status
	logs    *LogRing
	offset  int
	mode    runMode
	overlay viewMode

	dirty bool

	opLabel string
	opBusy  bool
	ops     chan opResult
	opSeq   int

	toast   string
	toastAt time.Time

	cfs *configState

	keys   chan Key
	events chan event
	sigs   chan os.Signal
	resize chan os.Signal

	sources map[string]*dataSource

	cancel context.CancelFunc
}

type opResult struct {
	tag  int
	msg  string
	fail bool
}

func newController(ctx context.Context, cfg *config.Config, term *terminal, opts Options) *controller {
	if opts.Refresh <= 0 {
		opts.Refresh = time.Second
	}
	c := &controller{
		cfg:     cfg,
		term:    term,
		log:     logger.Default(),
		opts:    opts,
		logs:    NewLogRing(logBufferLines),
		ops:     make(chan opResult, 4),
		keys:    make(chan Key, 64),
		events:  make(chan event, 128),
		sigs:    make(chan os.Signal, 2),
		resize:  make(chan os.Signal, 4),
		sources: map[string]*dataSource{},
		overlay: modeDashboard,
	}
	c.ctx, c.cancel = context.WithCancel(ctx)
	signal.Notify(c.sigs, os.Interrupt)

	c.instances = c.resolveInstances()
	c.idx = 0
	c.inst = c.instances[0]
	c.status = daemon.Status{Instance: c.inst.Name, State: daemon.StateUnknown}
	c.cfs = newConfigState(c.inst.Res, cfg)
	c.attach(c.inst)
	return c
}

// resolveInstances 解析要监控的实例列表（--instances 优先，其次配置里的全部）。
func (c *controller) resolveInstances() []*instance.Instance {
	var names []string
	if len(c.opts.Instances) > 0 {
		for _, n := range c.opts.Instances {
			if n = strings.TrimSpace(n); n != "" {
				names = append(names, n)
			}
		}
	}
	if len(names) == 0 {
		names = c.cfg.InstanceNames()
	}
	out := make([]*instance.Instance, 0, len(names))
	for _, n := range names {
		inst, err := instance.New(c.cfg, n)
		if err != nil {
			c.log.Warnf("跳过实例 %s：%v", n, err)
			continue
		}
		out = append(out, inst)
	}
	if len(out) == 0 {
		// 兜底：即使实例列表为空也要有一个可显示的实例句柄
		if inst, err := instance.New(c.cfg, c.opts.Instance); err == nil {
			out = append(out, inst)
		} else {
			out = append(out, &instance.Instance{Name: "未命名", Cfg: c.cfg, Res: c.cfg.Resolve("")})
		}
	}
	// 把初始实例放到最前面，保证启动时看到的就是用户指定的实例
	if want := strings.TrimSpace(c.opts.Instance); want != "" {
		for i, inst := range out {
			if inst.Name == want {
				out[0], out[i] = out[i], out[0]
				break
			}
		}
	}
	return out
}

// attach 切换当前实例：重置日志与滚动位置，并挂上数据源。
func (c *controller) attach(inst *instance.Instance) {
	c.inst = inst
	c.logs.Reset()
	c.offset = 0
	c.status = daemon.Status{Instance: inst.Name, State: daemon.StateUnknown}
	c.cfs = newConfigState(inst.Res, c.cfg)
	ds := c.sourceFor(inst)
	go c.pump(ds)
}

// pump 把数据源事件搬到主循环的事件通道。
func (c *controller) pump(ds *dataSource) {
	for {
		select {
		case <-c.ctx.Done():
			return
		case ev, ok := <-ds.Events():
			if !ok {
				return
			}
			select {
			case c.events <- ev:
			case <-c.ctx.Done():
				return
			}
		}
	}
}

// shutdown 停止数据源并恢复终端。
func (c *controller) shutdown() {
	c.closeSources()
	if c.cancel != nil {
		c.cancel()
	}
	signal.Stop(c.sigs)
}

// ------------------------------------------------------------------ 主循环

func (c *controller) run() error {
	keyParser := &keyParser{}
	tick := time.NewTicker(c.opts.Refresh)
	defer tick.Stop()
	poll := time.NewTicker(resizeTick())
	defer poll.Stop()
	lastDraw := time.Time{}

	c.dirty = true
	for {
		if c.dirty {
			if minDrawInterval <= 0 || time.Since(lastDraw) >= minDrawInterval {
				c.draw()
				lastDraw = time.Now()
				c.dirty = false
			}
		}
		select {
		case <-c.ctx.Done():
			return nil
		case <-c.sigs:
			return nil
		case chunk := <-c.term.keys:
			parser := keyParser
			for _, r := range c.handleInput(parser, chunk) {
				if quit := c.onKey(r); quit {
					return nil
				}
			}
		case <-c.term.resize:
			c.dirty = true
		case <-poll.C:
			c.checkResize()
		case <-tick.C:
			c.pollSources()
			c.expireToast()
			if c.toastActive() {
				c.dirty = true
			}
		case res := <-c.ops:
			c.opBusy = false
			c.opLabel = ""
			c.flash(res.msg, res.fail)
			c.dirty = true
		}
		c.drainEvents()
	}
}

// handleInput 把一段输入切成按键交给主循环。
func (c *controller) handleInput(parser *keyParser, chunk []byte) []Key {
	if len(chunk) == 0 {
		// 输入被关闭（Ctrl+D 或管道）：直接退出，避免界面卡住
		return []Key{{Kind: KeyEOF}}
	}
	var out []Key
	for {
		k, ok := parser.Next(chunk, false)
		chunk = nil
		if !ok {
			break
		}
		out = append(out, k)
	}
	return out
}

// drainEvents 处理数据源推来的事件。
func (c *controller) drainEvents() {
	for {
		select {
		case ev := <-c.events:
			c.applyEvent(ev)
		default:
			return
		}
	}
}

func (c *controller) applyEvent(ev event) {
	if ev.inst != nil && ev.inst.Name != c.inst.Name {
		return // 切换实例后残留的旧事件
	}
	switch {
	case ev.err != nil:
		c.status.LastError = ev.err.Error()
	case ev.st != nil:
		st := *ev.st
		// 保留日志末尾等订阅流不带的信息
		if st.MaxPlayers == 0 {
			st.MaxPlayers = c.status.MaxPlayers
		}
		c.status = st
		c.dirty = true
	case len(ev.logs) > 0:
		follow := c.offset == 0
		for _, raw := range ev.logs {
			for _, line := range strings.Split(Sanitize(raw), "\n") {
				c.logs.Append(line)
			}
		}
		if follow {
			c.offset = 0
		}
		c.dirty = true
	}
}

// pollSources 按刷新周期主动拉一次数据（订阅不可用时的主要数据来源）。
func (c *controller) pollSources() {
	ds := c.sources[c.inst.Name]
	if ds == nil {
		return
	}
	if ds.streamActive() {
		return
	}
	go ds.poll()
}

// checkResize 轮询终端尺寸（Windows 没有 SIGWINCH）。
func (c *controller) checkResize() {
	w, h := c.term.size()
	if w != c.width || h != c.height {
		c.dirty = true
	}
}

func (c *controller) expireToast() {
	if c.toast != "" && time.Since(c.toastAt) > toastTTL {
		c.toast = ""
		c.dirty = true
	}
}

func (c *controller) toastActive() bool { return c.toast != "" }

// flash 显示一条操作结果提示。
func (c *controller) flash(msg string, fail bool) {
	if msg == "" {
		return
	}
	if fail {
		msg = "失败：" + msg
	}
	c.toast = msg
	c.toastAt = time.Now()
}

// draw 渲染一帧。
func (c *controller) draw() {
	w, h := c.term.size()
	c.width, c.height = w, h
	v := &view{
		mode:      c.overlay,
		width:     w,
		height:    h,
		inst:      c.inst,
		status:    c.status,
		logs:      c.logs,
		offset:    c.offset,
		instances: c.instanceNames(),
		instIndex: c.idx,
		opLabel:   c.opLabel,
		toast:     c.toast,
		toastAt:   c.toastAt,
		cfg:       c.cfs,
	}
	v.hideLogs = c.mode == dashPlayersOnly
	v.hidePlayers = c.mode == dashLogsOnly
	r := &renderer{cfg: c.cfg}
	cursor := &cursorPos{}
	out := r.draw(v, cursor, true)
	// 配置模式编辑时需要光标可见并停在输入处
	if c.overlay == modeConfig {
		var b strings.Builder
		b.WriteString(out)
		if cursor.row > 0 || cursor.col > 0 {
			fmt.Fprintf(&b, "\x1b[?25h\x1b[%d;%dH", cursor.row+1, cursor.col+1)
		}
		_, _ = io.WriteString(c.term.out, b.String())
		return
	}
	_, _ = io.WriteString(c.term.out, "\x1b[?25l"+out)
}

// instanceNames 返回实例名列表（用于界面上的多实例提示）。
func (c *controller) instanceNames() []string {
	out := make([]string, 0, len(c.instances))
	for _, inst := range c.instances {
		out = append(out, inst.Name)
	}
	return out
}

// viewToastText 读取提示文本（供渲染器使用）。
func (v *view) toastText() string { return v.toast }

// width/height 由 controller 缓存，便于检测尺寸变化。
func (c *controller) currentSize() (int, int) {
	return c.width, c.height
}

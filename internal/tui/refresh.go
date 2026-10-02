package tui

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/yuzumikonami/nyatmc/internal/config"
	"github.com/yuzumikonami/nyatmc/internal/daemon"
	"github.com/yuzumikonami/nyatmc/internal/instance"
	"github.com/yuzumikonami/nyatmc/pkg/logger"
)

// event 是数据源推给主循环的一条事件。
type event struct {
	inst *instance.Instance
	st   *daemon.Status
	logs []string
	err  error
}

// dataSource 负责一个实例的数据采集。
//
// 优先用 daemon.Subscribe 订阅日志与状态；拿不到流（实例没运行、控制通道不支持）
// 时退化为刷新周期轮询，并且会在实例重新启动后自动再次尝试订阅。
type dataSource struct {
	ctx  context.Context
	cfg  *config.Config
	inst *instance.Instance
	log  *logger.Logger

	events chan event
	buf    int

	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	streamed bool // 是否处于订阅模式
}

// openSource 构造数据源并立即做一次快照（主循环不阻塞）。
func openSource(ctx context.Context, cfg *config.Config, inst *instance.Instance, log *logger.Logger, lines int) *dataSource {
	if lines <= 0 {
		lines = 500
	}
	ds := &dataSource{
		ctx:    ctx,
		cfg:    cfg,
		inst:   inst,
		log:    log,
		events: make(chan event, 64),
		buf:    lines,
	}
	ds.snapshot()
	ds.start()
	return ds
}

// Events 返回事件通道。
func (d *dataSource) Events() <-chan event { return d.events }

// send 发送事件，通道满时丢弃最旧的一条，保证采集端不会被界面拖住。
func (d *dataSource) send(ev event) {
	select {
	case d.events <- ev:
		return
	default:
	}
	select {
	case <-d.events:
	default:
	}
	select {
	case d.events <- ev:
	default:
	}
}

// snapshot 主动拉一次状态与日志（带超时，避免卡住主循环）。
func (d *dataSource) snapshot() {
	ctx, cancel := context.WithTimeout(d.ctx, 3*time.Second)
	defer cancel()

	st, err := daemon.GetStatus(ctx, d.cfg, d.inst)
	if err != nil {
		d.send(event{inst: d.inst, err: err})
		return
	}
	d.send(event{inst: d.inst, st: &st})

	if lines, err := daemon.Logs(ctx, d.inst, d.buf); err == nil && len(lines) > 0 {
		d.send(event{inst: d.inst, logs: lines})
	}
}

// start 启动订阅或轮询。
func (d *dataSource) start() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithCancel(d.ctx)
	d.cancel = cancel
	d.done = make(chan struct{})
	go d.run(ctx, d.done)
}

// close 停止采集。
func (d *dataSource) close() {
	d.mu.Lock()
	cancel := d.cancel
	done := d.done
	d.cancel = nil
	d.done = nil
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
}

// run 是采集循环：能订阅就订阅；订阅不可用时按刷新周期轮询。
func (d *dataSource) run(ctx context.Context, done chan struct{}) {
	defer close(done)

	for {
		if ctx.Err() != nil {
			return
		}
		err := daemon.Subscribe(ctx, d.inst, func(ev daemon.LogEvent) error {
			d.setStreamed(true)
			switch ev.Kind {
			case "log":
				if ev.Text != "" {
					d.send(event{inst: d.inst, logs: []string{ev.Text}})
				}
			case "status":
				if ev.Status != nil {
					st := *ev.Status
					st.Instance = d.inst.Name
					d.send(event{inst: d.inst, st: &st})
				}
			default:
				if ev.Text != "" {
					d.send(event{inst: d.inst, logs: []string{ev.Text}})
				}
			}
			return nil
		})
		d.setStreamed(false)
		if ctx.Err() != nil {
			return
		}

		// 订阅结束（实例没运行、控制通道断开、服务端停止……）：
		// 先刷新一次状态与日志，等一会儿再尝试重新订阅。
		if err != nil {
			d.log.Debugf("tui: 订阅实例 %s 的事件流不可用，退化为轮询：%v", d.inst.Name, err)
		}
		d.snapshot()
		select {
		case <-ctx.Done():
			return
		case <-time.After(1500 * time.Millisecond):
		}
	}
}

// poll 由主循环按刷新周期调用：轮询模式下取状态与日志尾巴。
//
// 订阅模式下这个函数什么都不做（事件已经在推）。
func (d *dataSource) poll() {
	if d.streamActive() {
		return
	}
	d.snapshot()
}

// streamActive 判断是否正在订阅（订阅成功时不需要轮询）。
func (d *dataSource) streamActive() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.streamed
}

// setStreamed 记录订阅状态。
func (d *dataSource) setStreamed(v bool) {
	d.mu.Lock()
	d.streamed = v
	d.mu.Unlock()
}

// readTailFile 读取文件末尾至多 n 行，最多读 256KB（Termux 上内存很宝贵）。
func readTailFile(path string, n int) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	const maxBytes = 256 * 1024
	start := int64(0)
	if st.Size() > maxBytes {
		start = st.Size() - maxBytes
	}
	if _, err := f.Seek(start, 0); err != nil {
		return nil, err
	}
	data := make([]byte, st.Size()-start)
	total := 0
	for total < len(data) {
		read, rerr := f.Read(data[total:])
		total += read
		if rerr != nil {
			break
		}
	}
	text := string(data[:total])
	if start > 0 {
		if idx := strings.IndexByte(text, '\n'); idx >= 0 {
			text = text[idx+1:]
		}
	}
	return tailLines(text, n), nil
}

// tailLines 返回文本末尾至多 n 行。
func tailLines(text string, n int) []string {
	if n <= 0 {
		return nil
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, strings.TrimRight(l, "\r"))
	}
	return out
}

// sourceFor 返回实例对应的数据源（按实例名缓存）。
func (c *controller) sourceFor(inst *instance.Instance) *dataSource {
	if c.sources == nil {
		c.sources = map[string]*dataSource{}
	}
	if ds, ok := c.sources[inst.Name]; ok {
		return ds
	}
	lines := inst.Res.Log.ConsoleLines
	if lines <= 0 {
		lines = logBufferLines
	}
	if lines > logBufferLines {
		// 界面只展示末尾若干行，抓太多只是浪费内存（Termux 上很宝贵）
		lines = logBufferLines
	}
	ds := openSource(c.ctx, c.cfg, inst, c.log, lines)
	c.sources[inst.Name] = ds
	return ds
}

// closeSources 关闭全部数据源。
func (c *controller) closeSources() {
	for _, ds := range c.sources {
		ds.close()
	}
	c.sources = map[string]*dataSource{}
}

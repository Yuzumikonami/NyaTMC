// Package scheduler 是 nyatmc 的内置定时任务调度器。
//
// 任务定义在 config.toml 的 [[scheduler.jobs]] 里，支持 backup / restart / stop /
// start / command 五种动作，调度本身用 github.com/robfig/cron/v3 实现。
//
// 设计要点：
//
//   - Runner.Start 阻塞运行，ctx 结束即停止；期间每 30 秒重新读盘，新增 / 修改 /
//     删除的任务即时生效（相当于配置热重载）；
//   - 单个任务执行失败只记日志与状态，绝不让调度器退出；单次执行有 30 分钟超时；
//   - 同名任务不会并发执行（Running 标记），上一次还在跑时本次直接跳过；
//   - 执行结果写入 state/scheduler.json，供 `nyatmc schedule status` 与 Web 展示，
//     文件里的 pid 也用于判断调度器守护进程是否存活。
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/yuzumikonami/nyatmc/internal/config"
	"github.com/yuzumikonami/nyatmc/pkg/logger"
)

// 任务动作。
const (
	// ActionBackup 备份目标实例。
	ActionBackup = "backup"
	// ActionRestart 重启目标实例。
	ActionRestart = "restart"
	// ActionStop 停止目标实例。
	ActionStop = "stop"
	// ActionStart 启动目标实例。
	ActionStart = "start"
	// ActionCommand 向目标实例控制台发送一条指令。
	ActionCommand = "command"
)

// Actions 列出全部支持的动作（CLI 帮助与校验共用）。
var Actions = []string{ActionBackup, ActionRestart, ActionStop, ActionStart, ActionCommand}

const (
	// jobTimeout 是单次任务执行的时间上限。
	jobTimeout = 30 * time.Minute
	// defaultPollInterval 是重新读盘配置的间隔。
	defaultPollInterval = 30 * time.Second
)

// scheduleParser 同时接受 5 段（分 时 日 月 周）与 6 段（含秒）cron 表达式，以及 @daily 之类的宏。
var scheduleParser = cron.NewParser(
	cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// Options 是 Runner 的构造参数。
type Options struct {
	// ConfigPath 是配置文件路径，留空取 cfg.Path 或 config.ConfigPath()。
	ConfigPath string
	// Logger 是日志输出，留空使用全局日志器。
	Logger *logger.Logger
	// Instances 只运行指定实例的任务；空表示全部。
	Instances []string
}

// JobStatus 是单个任务的运行状态。
type JobStatus struct {
	config.Job
	// NextRun 是下次执行时间（未启用或未调度时为零值）。
	NextRun time.Time `json:"next_run"`
	// LastRun 是上次执行时间。
	LastRun time.Time `json:"last_run"`
	// LastError 是上次执行的错误信息，成功后清空。
	LastError string `json:"last_error"`
	// Running 表示该任务此刻正在执行。
	Running bool `json:"running"`
	// Runs 是累计执行次数。
	Runs int `json:"runs"`
}

// Runner 是一个内置调度器实例。
type Runner struct {
	mu         sync.Mutex
	cfg        *config.Config
	log        *logger.Logger
	configPath string
	instances  []string
	loc        *time.Location
	parser     cron.Parser

	cron      *cron.Cron
	schedules map[string]cron.Schedule
	states    map[string]*JobStatus
	order     []string

	started      bool
	startedAt    time.Time
	pid          int
	ctx          context.Context
	pollInterval time.Duration
	now          func() time.Time
}

// New 构造调度器：校验任务定义并注册 cron 表达式，但不会开始运行。
func New(cfg *config.Config, opts Options) (*Runner, error) {
	if cfg == nil {
		return nil, errors.New("配置为空")
	}
	path := strings.TrimSpace(opts.ConfigPath)
	if path == "" {
		path = strings.TrimSpace(cfg.Path)
	}
	if path == "" {
		path = config.ConfigPath()
	}
	log := opts.Logger
	if log == nil {
		log = logger.Default()
	}
	loc, err := Timezone(cfg)
	if err != nil {
		return nil, err
	}

	r := &Runner{
		cfg:          cfg,
		log:          log,
		configPath:   config.ExpandPath(path),
		instances:    cleanNames(opts.Instances),
		loc:          loc,
		parser:       scheduleParser,
		schedules:    map[string]cron.Schedule{},
		states:       map[string]*JobStatus{},
		pollInterval: defaultPollInterval,
		now:          time.Now,
	}
	if err := r.build(cfg); err != nil {
		return nil, err
	}
	return r, nil
}

// Timezone 解析调度时区，timezone 留空表示本机时区。
func Timezone(cfg *config.Config) (*time.Location, error) {
	if cfg == nil {
		return nil, errors.New("配置为空")
	}
	tz := strings.TrimSpace(cfg.Scheduler.Timezone)
	if tz == "" {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("scheduler.timezone = %q 无效: %w", tz, err)
	}
	return loc, nil
}

// Path 返回调度器读取的配置文件路径。
func (r *Runner) Path() string { return r.configPath }

// build 校验全部任务并重建调度表；任何错误都不会改动现有状态。
func (r *Runner) build(cfg *config.Config) error {
	schedules := map[string]cron.Schedule{}
	states := map[string]*JobStatus{}
	order := make([]string, 0, len(cfg.Scheduler.Jobs))
	var errs []error

	for _, job := range cfg.Scheduler.Jobs {
		name := strings.TrimSpace(job.Name)
		if name == "" {
			errs = append(errs, errors.New("存在没有 name 的调度任务"))
			continue
		}
		if _, dup := states[name]; dup {
			errs = append(errs, fmt.Errorf("调度任务名 %q 重复", name))
			continue
		}
		order = append(order, name)

		job.Name = name
		st := &JobStatus{Job: job}
		if prev, ok := r.states[name]; ok {
			st.LastRun, st.LastError, st.Runs = prev.LastRun, prev.LastError, prev.Runs
		}
		states[name] = st

		if !job.IsEnabled() {
			continue
		}
		if err := ValidateJob(job); err != nil {
			errs = append(errs, err)
			continue
		}
		// 指定了实例范围时不调度其它实例的任务。
		if len(r.instances) > 0 && job.Instance != "" && !containsString(r.instances, job.Instance) {
			continue
		}
		sched, err := r.parser.Parse(job.Schedule)
		if err != nil {
			errs = append(errs, fmt.Errorf("任务 %s 的 cron 表达式 %q 无法解析: %w", name, job.Schedule, err))
			continue
		}
		schedules[name] = sched
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	r.cfg = cfg
	r.schedules = schedules
	r.states = states
	r.order = order
	return nil
}

// newCronLocked 依据当前调度表构造 cron 实例（未启动），调用方需持有锁。
func (r *Runner) newCronLocked() *cron.Cron {
	c := cron.New(
		cron.WithLocation(r.loc),
		cron.WithParser(r.parser),
		cron.WithLogger(cronLogger{log: r.log}),
	)
	for _, name := range r.order {
		sched, ok := r.schedules[name]
		if !ok {
			continue
		}
		st, ok := r.states[name]
		if !ok {
			continue
		}
		job := st.Job
		c.Schedule(sched, cron.FuncJob(func() { r.execute(name, job) }))
	}
	return c
}

// Start 阻塞运行调度器，ctx 结束则停止。
func (r *Runner) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return errors.New("调度器已经在运行")
	}
	r.started = true
	r.startedAt = r.now()
	r.pid = os.Getpid()
	r.ctx = ctx
	c := r.newCronLocked()
	r.cron = c
	count := len(r.schedules)
	loc := r.loc.String()
	pid := r.pid
	r.mu.Unlock()

	c.Start()
	r.log.Infof("调度器已启动：%d 个任务，时区 %s，pid %d", count, loc, pid)
	r.writeState()

	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			r.mu.Lock()
			old := r.cron
			r.cron = nil
			r.started = false
			r.mu.Unlock()
			if old != nil {
				// 不再等待正在执行的任务：进程正在退出，任务自身的超时由 ctx 负责。
				old.Stop()
			}
			r.writeState()
			r.log.Infof("调度器已停止")
			return nil
		case <-ticker.C:
			r.pollConfig()
		}
	}
}

// pollConfig 定时重新读盘，实现“改完配置即时生效”。
func (r *Runner) pollConfig() {
	if strings.TrimSpace(r.configPath) == "" {
		return
	}
	cfg, err := config.Load(r.configPath)
	if err != nil {
		r.log.Warnf("调度器重新读取配置失败：%v", err)
		return
	}
	r.mu.Lock()
	changed := !sameJobs(r.cfg.Scheduler.Jobs, cfg.Scheduler.Jobs) ||
		strings.TrimSpace(r.cfg.Scheduler.Timezone) != strings.TrimSpace(cfg.Scheduler.Timezone)
	r.mu.Unlock()
	if !changed {
		return
	}
	if err := r.Reload(cfg); err != nil {
		r.log.Warnf("检测到配置变化，但任务无法应用：%v", err)
		return
	}
	r.mu.Lock()
	count := len(r.schedules)
	r.mu.Unlock()
	r.log.Infof("检测到配置变化，调度器已重新加载：%d 个任务", count)
}

// Reload 用新配置重建调度表（热重载）；失败时保持原样。
func (r *Runner) Reload(cfg *config.Config) error {
	if cfg == nil {
		return errors.New("配置为空")
	}
	loc, err := Timezone(cfg)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.build(cfg); err != nil {
		return err
	}
	r.loc = loc
	if !r.started {
		return nil
	}
	next := r.newCronLocked()
	old := r.cron
	r.cron = next
	next.Start()
	if old != nil {
		old.Stop()
	}
	return nil
}

// Status 返回全部任务的状态（含下次执行时间；未运行时合并状态文件里的历史记录）。
func (r *Runner) Status(ctx context.Context) ([]JobStatus, error) {
	_ = ctx
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	var persisted map[string]JobStatus
	if !r.started {
		persisted = readStateJobs()
	}

	out := make([]JobStatus, 0, len(r.order))
	for _, name := range r.order {
		st, ok := r.states[name]
		if !ok {
			continue
		}
		item := *st
		if sched, ok := r.schedules[name]; ok {
			item.NextRun = sched.Next(now.In(r.loc))
		}
		if p, ok := persisted[name]; ok && (p.Runs > item.Runs || (p.Runs == item.Runs && item.LastRun.IsZero())) {
			item.LastRun, item.LastError, item.Runs = p.LastRun, p.LastError, p.Runs
		}
		out = append(out, item)
	}
	return out, nil
}

// execute 是 cron 的触发入口：标记执行中、跑任务、记录结果。
func (r *Runner) execute(name string, job config.Job) {
	if !r.markRunning(name) {
		r.log.Warnf("任务 %s：上一次执行尚未结束，跳过本次触发", name)
		return
	}
	defer r.clearRunning(name)

	r.mu.Lock()
	base := r.ctx
	cfg := r.cfg
	r.mu.Unlock()
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, jobTimeout)
	defer cancel()

	start := r.now()
	err := runJob(ctx, cfg, job, r.instances, r.log)
	// 先清掉“执行中”标记再落盘，避免状态文件里残留 Running=true。
	r.clearRunning(name)
	r.record(name, err)
	cost := r.now().Sub(start).Round(time.Second)
	if err != nil {
		r.log.Errorf("任务 %s 执行失败（耗时 %s）：%v", name, cost, err)
		return
	}
	r.log.Infof("任务 %s 执行完成（耗时 %s）", name, cost)
}

// markRunning 标记任务开始执行，返回 false 表示上一次还没结束。
func (r *Runner) markRunning(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.states[name]
	if !ok {
		return true
	}
	if st.Running {
		return false
	}
	st.Running = true
	return true
}

// clearRunning 清除执行标记。
func (r *Runner) clearRunning(name string) {
	r.mu.Lock()
	if st, ok := r.states[name]; ok {
		st.Running = false
	}
	r.mu.Unlock()
}

// record 记录一次执行结果并落盘。
func (r *Runner) record(name string, err error) {
	r.mu.Lock()
	if st, ok := r.states[name]; ok {
		st.LastRun = r.now()
		st.Runs++
		if err != nil {
			st.LastError = err.Error()
		} else {
			st.LastError = ""
		}
	}
	r.mu.Unlock()
	r.writeState()
}

// writeState 把当前状态写入 state/scheduler.json。
func (r *Runner) writeState() {
	r.mu.Lock()
	fs := FileState{UpdatedAt: r.now()}
	if r.started {
		fs.PID = r.pid
		fs.StartedAt = r.startedAt
	}
	for _, name := range r.order {
		if st, ok := r.states[name]; ok {
			fs.Jobs = append(fs.Jobs, *st)
		}
	}
	log := r.log
	r.mu.Unlock()

	if err := WriteState(fs); err != nil && log != nil {
		log.Warnf("写入调度器状态失败：%v", err)
	}
}

// ---------------------------------------------------------------- 辅助

// cronLogger 把 robfig/cron 的内部日志接到 nyatmc 的日志器上。
type cronLogger struct{ log *logger.Logger }

// Info 输出 cron 的常规信息（调试级别）。
func (l cronLogger) Info(msg string, keysAndValues ...any) {
	if l.log != nil {
		l.log.Debugf("cron: %s %v", msg, keysAndValues)
	}
}

// Error 输出 cron 的错误信息。
func (l cronLogger) Error(err error, msg string, keysAndValues ...any) {
	if l.log != nil {
		l.log.Warnf("cron: %s（%v）%v", msg, err, keysAndValues)
	}
}

// jobLabel 返回用于日志的任务名。
func jobLabel(job config.Job) string {
	name := strings.TrimSpace(job.Name)
	if name == "" {
		return "(未命名)"
	}
	return name
}

// cleanNames 去空白并去重。
func cleanNames(in []string) []string {
	out := make([]string, 0, len(in))
	for _, n := range in {
		n = strings.TrimSpace(n)
		if n == "" || containsString(out, n) {
			continue
		}
		out = append(out, n)
	}
	return out
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// sameJobs 判断两份任务列表是否等价（用于热重载检测）。
func sameJobs(a, b []config.Job) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameJob(a[i], b[i]) {
			return false
		}
	}
	return true
}

func sameJob(a, b config.Job) bool {
	return strings.TrimSpace(a.Name) == strings.TrimSpace(b.Name) &&
		strings.TrimSpace(a.Schedule) == strings.TrimSpace(b.Schedule) &&
		strings.ToLower(strings.TrimSpace(a.Action)) == strings.ToLower(strings.TrimSpace(b.Action)) &&
		strings.TrimSpace(a.Instance) == strings.TrimSpace(b.Instance) &&
		a.Command == b.Command &&
		strings.TrimSpace(a.Label) == strings.TrimSpace(b.Label) &&
		a.IsEnabled() == b.IsEnabled()
}

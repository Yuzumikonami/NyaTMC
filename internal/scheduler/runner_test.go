package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuzumikonami/nyatmc/internal/config"
	"github.com/yuzumikonami/nyatmc/internal/instance"
	"github.com/yuzumikonami/nyatmc/pkg/logger"
)

func newRunner(t *testing.T, cfg *config.Config) *Runner {
	t.Helper()
	r, err := New(cfg, Options{ConfigPath: cfg.Path, Logger: logger.Discard()})
	if err != nil {
		t.Fatalf("构造调度器失败：%v", err)
	}
	return r
}

func TestNewRejectsInvalidJobs(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Scheduler.Jobs = []config.Job{
		{Name: "ok", Schedule: "0 4 * * *", Action: "backup"},
		{Name: "bad", Schedule: "0 25 * * *", Action: "backup"},
	}
	if _, err := New(cfg, Options{Logger: logger.Discard()}); err == nil {
		t.Fatal("非法 cron 应让 New 报错")
	}

	cfg.Scheduler.Jobs = []config.Job{
		{Name: "dup", Schedule: "0 4 * * *", Action: "backup"},
		{Name: "dup", Schedule: "0 5 * * *", Action: "backup"},
	}
	if _, err := New(cfg, Options{Logger: logger.Discard()}); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("重名任务应让 New 报错，实际 %v", err)
	}

	cfg.Scheduler.Jobs = []config.Job{{Name: "", Schedule: "0 4 * * *", Action: "backup"}}
	if _, err := New(cfg, Options{Logger: logger.Discard()}); err == nil {
		t.Fatal("缺少 name 的任务应让 New 报错")
	}
}

func TestStatusNextRun(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Scheduler.Jobs = []config.Job{
		{Name: "daily", Schedule: "0 4 * * *", Action: "backup"},
		{Name: "off", Schedule: "0 5 * * *", Action: "backup", Enabled: config.BoolPtr(false)},
	}
	r := newRunner(t, cfg)

	sts, err := r.Status(context.Background())
	if err != nil {
		t.Fatalf("Status 失败：%v", err)
	}
	if len(sts) != 2 {
		t.Fatalf("应返回 2 条任务，实际 %d", len(sts))
	}

	daily := sts[0]
	if daily.NextRun.IsZero() {
		t.Fatal("启用的任务应有下次执行时间")
	}
	if daily.NextRun.Hour() != 4 || daily.NextRun.Minute() != 0 || daily.NextRun.Second() != 0 {
		t.Fatalf("下次执行时间应为 04:00:00，实际 %s", daily.NextRun)
	}
	if !daily.NextRun.After(time.Now()) {
		t.Fatalf("下次执行时间应在未来，实际 %s", daily.NextRun)
	}
	if daily.Runs != 0 || daily.Running {
		t.Fatalf("初始状态应为未执行：%+v", daily)
	}

	off := sts[1]
	if !off.NextRun.IsZero() {
		t.Fatalf("停用的任务不应有下次执行时间，实际 %s", off.NextRun)
	}
}

func TestStatusHonoursTimezone(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Scheduler.Timezone = "Asia/Shanghai"
	cfg.Scheduler.Jobs = []config.Job{{Name: "daily", Schedule: "0 4 * * *", Action: "backup"}}
	r := newRunner(t, cfg)

	sts, err := r.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := time.Now().In(time.FixedZone("CST", 8*3600)).Hour()
	got := sts[0].NextRun.In(time.FixedZone("CST", 8*3600))
	if got.Hour() != 4 || got.Minute() != 0 {
		t.Fatalf("下次执行时间应按 Asia/Shanghai 解释：%s（当前小时 %d）", got, want)
	}
}

func TestStatusMergesStateFile(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Scheduler.Jobs = []config.Job{{Name: "daily", Schedule: "0 4 * * *", Action: "backup"}}

	// 伪造一份由守护进程写下的历史记录
	last := time.Now().Add(-3 * time.Hour).Round(time.Second)
	if err := WriteState(FileState{
		PID: 4111111,
		Jobs: []JobStatus{
			{Job: config.Job{Name: "daily"}, LastRun: last, Runs: 7, LastError: "上次失败"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	r := newRunner(t, cfg)
	sts, err := r.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sts) != 1 {
		t.Fatalf("应返回 1 条任务，实际 %d", len(sts))
	}
	if !sts[0].LastRun.Equal(last) {
		t.Fatalf("应从状态文件恢复上次执行时间：%s != %s", sts[0].LastRun, last)
	}
	if sts[0].Runs != 7 || sts[0].LastError != "上次失败" {
		t.Fatalf("应从状态文件恢复历史：%+v", sts[0])
	}
}

func TestExecuteRecordsResultAndStateFile(t *testing.T) {
	cfg := newTestConfig(t)
	job := config.Job{Name: "stop-job", Schedule: "0 4 * * *", Action: "stop", Instance: "survival"}
	cfg.Scheduler.Jobs = []config.Job{job}
	r := newRunner(t, cfg)

	// 模拟“正在运行中的调度器”，让状态文件带上 pid
	r.mu.Lock()
	r.started = true
	r.pid = os.Getpid()
	r.startedAt = time.Now()
	r.ctx = context.Background()
	r.mu.Unlock()

	stubRunning(t, func(*instance.Instance) bool { return false })
	r.execute("stop-job", job)

	got, ok := StateJob("stop-job")
	if !ok {
		t.Fatal("执行结果应写入状态文件")
	}
	if got.Runs != 1 {
		t.Fatalf("执行次数应为 1，实际 %d", got.Runs)
	}
	if got.LastRun.IsZero() {
		t.Fatal("应记录上次执行时间")
	}
	if got.LastError != "" {
		t.Fatalf("跳过不应算失败，实际错误：%q", got.LastError)
	}
	if !Running() {
		t.Fatal("状态文件里 pid 是自己，应认为调度器在运行")
	}

	// 上一次还在执行时应跳过本次触发
	r.mu.Lock()
	r.states["stop-job"].Running = true
	r.mu.Unlock()
	r.execute("stop-job", job)
	got, _ = StateJob("stop-job")
	if got.Runs != 1 {
		t.Fatalf("并发触发应被跳过，实际执行次数 %d", got.Runs)
	}
	r.mu.Lock()
	r.states["stop-job"].Running = false
	r.mu.Unlock()

	// 失败要记录下来（实例未运行时 command 动作必定失败）
	failing := config.Job{Name: "stop-job", Schedule: "0 4 * * *", Action: "command", Instance: "survival", Command: "say hi"}
	r.execute("stop-job", failing)
	got, _ = StateJob("stop-job")
	if got.Runs != 2 || !strings.Contains(got.LastError, "未在运行") {
		t.Fatalf("失败应记入状态文件：%+v", got)
	}
}

func TestStartAndReloadPicksUpNewJobs(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Scheduler.Jobs = []config.Job{{Name: "first", Schedule: "0 4 * * *", Action: "backup"}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	r := newRunner(t, cfg)
	r.pollInterval = 50 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()

	// 启动后状态文件应立刻带上 pid
	if !waitFor(3*time.Second, func() bool { return Running() }) {
		cancel()
		t.Fatal("启动后应写入带 pid 的状态文件")
	}

	// 从磁盘上新增任务，调度器应在轮询后自动加载
	fresh, err := config.Load(cfg.Path)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := Add(fresh, config.Job{Name: "second", Schedule: "@every 1h", Action: "backup"}); err != nil {
		cancel()
		t.Fatalf("添加任务失败：%v", err)
	}

	ok := waitFor(5*time.Second, func() bool {
		sts, serr := r.Status(context.Background())
		if serr != nil {
			return false
		}
		for _, st := range sts {
			if st.Name == "second" && !st.NextRun.IsZero() {
				return true
			}
		}
		return false
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Start 应正常返回：%v", err)
	}
	if !ok {
		t.Fatal("新增的任务没有在轮询后生效")
	}
	if Running() {
		t.Fatal("停止后状态文件里的 pid 应清零")
	}
}

func TestReloadKeepsOldScheduleOnError(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Scheduler.Jobs = []config.Job{{Name: "daily", Schedule: "0 4 * * *", Action: "backup"}}
	r := newRunner(t, cfg)

	bad, lerr := config.Load(cfg.Path)
	if lerr != nil {
		t.Fatal(lerr)
	}
	bad.Scheduler.Jobs = []config.Job{{Name: "daily", Schedule: "0 25 * * *", Action: "backup"}}
	if err := r.Reload(bad); err == nil {
		t.Fatal("非法任务应让 Reload 报错")
	}

	sts, err := r.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sts) != 1 || sts[0].NextRun.IsZero() {
		t.Fatalf("Reload 失败后应保留原有调度：%+v", sts)
	}
}

func TestStartTwiceRejected(t *testing.T) {
	cfg := newTestConfig(t)
	r := newRunner(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()

	if !waitFor(3*time.Second, func() bool { return Running() }) {
		cancel()
		t.Fatal("调度器没有启动")
	}
	if err := r.Start(context.Background()); err == nil {
		t.Fatal("重复启动应报错")
	}
	cancel()
	<-done
}

func TestSameJobs(t *testing.T) {
	a := []config.Job{{Name: "x", Schedule: "0 4 * * *", Action: "backup"}}
	if !sameJobs(a, []config.Job{{Name: "x", Schedule: "0 4 * * *", Action: "BACKUP"}}) {
		t.Fatal("动作大小写不同应视为同一任务")
	}
	if sameJobs(a, []config.Job{{Name: "x", Schedule: "0 5 * * *", Action: "backup"}}) {
		t.Fatal("表达式变化应被检测到")
	}
	if sameJobs(a, nil) {
		t.Fatal("任务数量变化应被检测到")
	}
}

func TestStatePathsInStateDir(t *testing.T) {
	newTestConfig(t)
	// 状态文件与日志都必须落在（临时）数据目录里，不能写到别处。
	if filepath.Dir(StatePath()) != config.StateDir() {
		t.Fatalf("状态文件应在 StateDir 下，实际 %s", StatePath())
	}
	if filepath.Dir(LogPath()) != config.StateDir() {
		t.Fatalf("日志应在 StateDir 下，实际 %s", LogPath())
	}
	if !strings.HasSuffix(StatePath(), "scheduler.json") {
		t.Fatalf("状态文件名应为 scheduler.json，实际 %s", StatePath())
	}
}

// waitFor 轮询等待条件成立。
func waitFor(timeout time.Duration, fn func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fn()
}

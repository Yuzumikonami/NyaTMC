package scheduler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuzumikonami/nyatmc/internal/config"
)

// newTestConfig 构造一个使用临时目录的假配置（含两个实例），不会启动任何服务器。
func newTestConfig(t *testing.T) *config.Config {
	t.Helper()
	home := t.TempDir()
	config.SetHome(home)

	cfg := config.Default()
	if err := cfg.AddInstance("survival", config.InstanceConfig{}); err != nil {
		t.Fatalf("添加测试实例失败：%v", err)
	}
	if err := cfg.AddInstance("creative", config.InstanceConfig{}); err != nil {
		t.Fatalf("添加测试实例失败：%v", err)
	}
	cfg.Path = filepath.Join(home, "config.toml")
	if err := cfg.Save(); err != nil {
		t.Fatalf("写入测试配置失败：%v", err)
	}
	return cfg
}

func TestValidateJob(t *testing.T) {
	cases := []struct {
		name    string
		job     config.Job
		wantErr string
	}{
		{"合法备份", config.Job{Name: "daily", Schedule: "0 4 * * *", Action: "backup"}, ""},
		{"含秒", config.Job{Name: "half", Schedule: "*/30 * * * * *", Action: "restart"}, ""},
		{"宏", config.Job{Name: "hourly", Schedule: "@every 1h", Action: "stop"}, ""},
		{"指令", config.Job{Name: "say", Schedule: "*/5 * * * *", Action: "command", Command: "say hi"}, ""},
		{"缺名字", config.Job{Schedule: "0 4 * * *", Action: "backup"}, "名字不能为空"},
		{"名字含换行", config.Job{Name: "a\nb", Schedule: "0 4 * * *", Action: "backup"}, "换行"},
		{"缺 cron", config.Job{Name: "x", Action: "backup"}, "cron"},
		{"cron 段数不对", config.Job{Name: "x", Schedule: "0 4 *", Action: "backup"}, "cron"},
		{"cron 超范围", config.Job{Name: "x", Schedule: "0 25 * * *", Action: "backup"}, "无法解析"},
		{"缺动作", config.Job{Name: "x", Schedule: "0 4 * * *"}, "缺少动作"},
		{"动作非法", config.Job{Name: "x", Schedule: "0 4 * * *", Action: "explode"}, "非法"},
		{"command 缺指令", config.Job{Name: "x", Schedule: "0 4 * * *", Action: "command"}, "必须同时给出"},
		{"实例名非法", config.Job{Name: "x", Schedule: "0 4 * * *", Action: "backup", Instance: "../etc"}, "实例名非法"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateJob(c.job)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("期望通过校验，实际 %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("期望错误包含 %q，实际通过校验", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("期望错误包含 %q，实际 %q", c.wantErr, err.Error())
			}
		})
	}
}

func TestAddFindRemoveSetEnabled(t *testing.T) {
	cfg := newTestConfig(t)
	job := config.Job{Name: "daily-backup", Schedule: "0 4 * * *", Action: "backup", Label: "auto"}

	if err := Add(cfg, job); err != nil {
		t.Fatalf("添加任务失败：%v", err)
	}
	got, ok := Find(cfg, "daily-backup")
	if !ok {
		t.Fatal("添加后应能查到任务")
	}
	if got.Schedule != "0 4 * * *" || got.Action != "backup" || !got.IsEnabled() {
		t.Fatalf("任务写入不正确：%+v", got)
	}

	// 重名必须被拒绝
	if err := Add(cfg, job); err == nil {
		t.Fatal("重名任务应被拒绝")
	} else if !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("重名错误信息不友好：%v", err)
	}

	// 非法 cron 不能写进配置
	if err := Add(cfg, config.Job{Name: "bad", Schedule: "不是 cron", Action: "backup"}); err == nil {
		t.Fatal("非法 cron 应被拒绝")
	}

	// 必须真的落盘
	reloaded, err := config.Load(cfg.Path)
	if err != nil {
		t.Fatalf("重新加载配置失败：%v", err)
	}
	if _, ok := Find(reloaded, "daily-backup"); !ok {
		t.Fatal("任务没有写入配置文件")
	}
	if len(reloaded.Scheduler.Jobs) != 1 {
		t.Fatalf("配置文件里应只有 1 条任务，实际 %d", len(reloaded.Scheduler.Jobs))
	}

	// 停用 / 启用
	if err := SetEnabled(cfg, "daily-backup", false); err != nil {
		t.Fatalf("停用失败：%v", err)
	}
	got, _ = Find(cfg, "daily-backup")
	if got.IsEnabled() {
		t.Fatal("停用后 IsEnabled 应为 false")
	}
	if err := SetEnabled(cfg, "daily-backup", true); err != nil {
		t.Fatalf("启用失败：%v", err)
	}
	got, _ = Find(cfg, "daily-backup")
	if !got.IsEnabled() {
		t.Fatal("启用后 IsEnabled 应为 true")
	}
	if err := SetEnabled(cfg, "不存在", true); err == nil {
		t.Fatal("对不存在的任务设置状态应报错")
	}

	// 删除
	if err := Remove(cfg, "daily-backup"); err != nil {
		t.Fatalf("删除失败：%v", err)
	}
	if _, ok := Find(cfg, "daily-backup"); ok {
		t.Fatal("删除后不应还能查到任务")
	}
	if err := Remove(cfg, "daily-backup"); err == nil {
		t.Fatal("删除不存在的任务应报错")
	}
}

func TestTimezone(t *testing.T) {
	cfg := newTestConfig(t)
	loc, err := Timezone(cfg)
	if err != nil {
		t.Fatalf("空时区应回退到本机时区：%v", err)
	}
	if loc == nil {
		t.Fatal("时区不应为 nil")
	}

	cfg.Scheduler.Timezone = "Asia/Shanghai"
	loc, err = Timezone(cfg)
	if err != nil {
		t.Fatalf("合法时区解析失败：%v", err)
	}
	if loc.String() != "Asia/Shanghai" {
		t.Fatalf("时区解析错误：%s", loc)
	}

	cfg.Scheduler.Timezone = "Not/AZone"
	if _, err := Timezone(cfg); err == nil {
		t.Fatal("非法时区应报错")
	}
}

func TestStateFile(t *testing.T) {
	newTestConfig(t)

	if pid := RunningPID(); pid != 0 {
		t.Fatalf("没有状态文件时不应认为在运行，实际 pid=%d", pid)
	}
	st, err := ReadState()
	if err != nil {
		t.Fatalf("读取不存在的状态文件不应报错：%v", err)
	}
	if !st.UpdatedAt.IsZero() || st.PID != 0 {
		t.Fatalf("不存在的状态文件应返回零值：%+v", st)
	}

	want := FileState{
		PID: os.Getpid(),
		Jobs: []JobStatus{
			{Job: config.Job{Name: "daily", Schedule: "0 4 * * *", Action: "backup"}, Runs: 3, LastError: "上次失败"},
		},
	}
	if err := WriteState(want); err != nil {
		t.Fatalf("写入状态失败：%v", err)
	}
	got, err := ReadState()
	if err != nil {
		t.Fatalf("读取状态失败：%v", err)
	}
	if got.PID != want.PID || len(got.Jobs) != 1 || got.Jobs[0].Runs != 3 {
		t.Fatalf("状态往返不一致：%+v", got)
	}
	if !Running() {
		t.Fatal("pid 为当前进程时应认为调度器在运行")
	}
	if pid := RunningPID(); pid != os.Getpid() {
		t.Fatalf("RunningPID() = %d，期望 %d", pid, os.Getpid())
	}
	job, ok := StateJob("daily")
	if !ok || job.Runs != 3 || job.LastError != "上次失败" {
		t.Fatalf("StateJob 结果不正确：%+v ok=%v", job, ok)
	}
	if _, ok := StateJob("不存在"); ok {
		t.Fatal("不存在的任务不应返回记录")
	}
}

func TestRecordRun(t *testing.T) {
	newTestConfig(t)
	job := config.Job{Name: "manual", Schedule: "0 4 * * *", Action: "backup"}

	RecordRun(job, nil)
	got, ok := StateJob("manual")
	if !ok || got.Runs != 1 || got.LastError != "" || got.LastRun.IsZero() {
		t.Fatalf("手动执行记录不正确：%+v", got)
	}

	RecordRun(job, errTest)
	got, _ = StateJob("manual")
	if got.Runs != 2 || !strings.Contains(got.LastError, "测试错误") {
		t.Fatalf("失败记录不正确：%+v", got)
	}

	// 其它任务的历史不能丢
	RecordRun(config.Job{Name: "other", Schedule: "0 5 * * *", Action: "stop"}, nil)
	if _, ok := StateJob("manual"); !ok {
		t.Fatal("写入新任务时不应丢掉旧任务的记录")
	}
	if _, ok := StateJob("other"); !ok {
		t.Fatal("新任务记录应写入")
	}
}

var errTest = &testError{}

type testError struct{}

func (e *testError) Error() string { return "测试错误" }

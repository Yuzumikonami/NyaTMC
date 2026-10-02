package scheduler

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yuzumikonami/nyatmc/internal/backup"
	"github.com/yuzumikonami/nyatmc/internal/config"
	"github.com/yuzumikonami/nyatmc/internal/daemon"
	"github.com/yuzumikonami/nyatmc/internal/instance"
	"github.com/yuzumikonami/nyatmc/pkg/logger"
)

// ---------------------------------------------------------------- 测试替身

func stubRunning(t *testing.T, fn func(*instance.Instance) bool) {
	t.Helper()
	old := daemonRunning
	daemonRunning = fn
	t.Cleanup(func() { daemonRunning = old })
}

func stubStart(t *testing.T, fn func(context.Context, *config.Config, *instance.Instance, daemon.StartOptions) (daemon.Status, error)) {
	t.Helper()
	old := daemonStart
	daemonStart = fn
	t.Cleanup(func() { daemonStart = old })
}

func stubStop(t *testing.T, fn func(context.Context, *config.Config, *instance.Instance, daemon.StopOptions) error) {
	t.Helper()
	old := daemonStop
	daemonStop = fn
	t.Cleanup(func() { daemonStop = old })
}

func stubRestart(t *testing.T, fn func(context.Context, *config.Config, *instance.Instance, daemon.RestartOptions) (daemon.Status, error)) {
	t.Helper()
	old := daemonRestart
	daemonRestart = fn
	t.Cleanup(func() { daemonRestart = old })
}

func stubSendCommand(t *testing.T, fn func(context.Context, *instance.Instance, string) error) {
	t.Helper()
	old := daemonSendCommand
	daemonSendCommand = fn
	t.Cleanup(func() { daemonSendCommand = old })
}

func stubBackup(t *testing.T, fn func(*instance.Instance, string, *logger.Logger) (backup.Archive, []string, error)) {
	t.Helper()
	old := backupCreateAndPrune
	backupCreateAndPrune = fn
	t.Cleanup(func() { backupCreateAndPrune = old })
}

// ---------------------------------------------------------------- 用例

func TestRunJobUnknownAction(t *testing.T) {
	cfg := newTestConfig(t)
	err := RunJob(context.Background(), cfg, config.Job{Name: "x", Action: "explode"}, logger.Discard())
	if err == nil || !strings.Contains(err.Error(), "不支持") {
		t.Fatalf("未知动作应报错，实际 %v", err)
	}
}

func TestRunJobMissingInstance(t *testing.T) {
	cfg := newTestConfig(t)
	job := config.Job{Name: "x", Schedule: "0 4 * * *", Action: "backup", Instance: "不存在"}
	err := RunJob(context.Background(), cfg, job, logger.Discard())
	if err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("目标实例不存在应报错，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "survival") {
		t.Fatalf("错误信息应提示可用实例，实际 %v", err)
	}
}

func TestRunJobNoInstances(t *testing.T) {
	home := t.TempDir()
	config.SetHome(home)
	cfg := config.Default()
	cfg.Path = home + "/config.toml"

	job := config.Job{Name: "x", Schedule: "0 4 * * *", Action: "backup"}
	err := RunJob(context.Background(), cfg, job, logger.Discard())
	if err == nil || !strings.Contains(err.Error(), "没有任何实例") {
		t.Fatalf("没有实例时应报错，实际 %v", err)
	}
}

func TestRunJobCommandWhenNotRunning(t *testing.T) {
	cfg := newTestConfig(t)
	stubRunning(t, func(*instance.Instance) bool { return false })

	job := config.Job{Name: "say", Schedule: "*/5 * * * *", Action: "command", Command: "say hi", Instance: "survival"}
	err := RunJob(context.Background(), cfg, job, logger.Discard())
	if err == nil {
		t.Fatal("实例未运行时 command 动作应记为失败")
	}
	if !strings.Contains(err.Error(), "未在运行") {
		t.Fatalf("错误信息应说明实例未运行，实际 %v", err)
	}
}

func TestRunJobCommandWhenRunning(t *testing.T) {
	cfg := newTestConfig(t)
	stubRunning(t, func(*instance.Instance) bool { return true })
	var got []string
	stubSendCommand(t, func(_ context.Context, inst *instance.Instance, line string) error {
		got = append(got, inst.Name+":"+line)
		return nil
	})

	job := config.Job{Name: "say", Schedule: "*/5 * * * *", Action: "command", Command: "say hi", Instance: "survival"}
	if err := RunJob(context.Background(), cfg, job, logger.Discard()); err != nil {
		t.Fatalf("发送指令应成功：%v", err)
	}
	if len(got) != 1 || got[0] != "survival:say hi" {
		t.Fatalf("指令发送记录不正确：%v", got)
	}

	// 空指令必须报错
	bad := config.Job{Name: "say", Schedule: "*/5 * * * *", Action: "command", Instance: "survival"}
	if err := RunJob(context.Background(), cfg, bad, logger.Discard()); err == nil {
		t.Fatal("command 动作缺指令应报错")
	}
}

func TestRunJobStartSkipsWhenAlreadyRunning(t *testing.T) {
	cfg := newTestConfig(t)
	stubRunning(t, func(*instance.Instance) bool { return true })
	called := false
	stubStart(t, func(context.Context, *config.Config, *instance.Instance, daemon.StartOptions) (daemon.Status, error) {
		called = true
		return daemon.Status{}, nil
	})

	job := config.Job{Name: "boot", Schedule: "0 4 * * *", Action: "start", Instance: "survival"}
	if err := RunJob(context.Background(), cfg, job, logger.Discard()); err != nil {
		t.Fatalf("实例已在运行时 start 应记为跳过而不是失败：%v", err)
	}
	if called {
		t.Fatal("实例已在运行时不应再调用启动")
	}
}

func TestRunJobStopSkipsWhenStopped(t *testing.T) {
	cfg := newTestConfig(t)
	stubRunning(t, func(*instance.Instance) bool { return false })
	called := false
	stubStop(t, func(context.Context, *config.Config, *instance.Instance, daemon.StopOptions) error {
		called = true
		return daemon.ErrNotRunning
	})

	job := config.Job{Name: "shutdown", Schedule: "0 4 * * *", Action: "stop", Instance: "survival"}
	if err := RunJob(context.Background(), cfg, job, logger.Discard()); err != nil {
		t.Fatalf("实例本来就没运行时 stop 应记为跳过：%v", err)
	}
	if called {
		t.Fatal("实例未运行时不应调用停止")
	}
}

func TestRunJobStopReportsRealFailure(t *testing.T) {
	cfg := newTestConfig(t)
	stubRunning(t, func(*instance.Instance) bool { return true })
	stubStop(t, func(context.Context, *config.Config, *instance.Instance, daemon.StopOptions) error {
		return errors.New("控制通道无响应")
	})

	job := config.Job{Name: "shutdown", Schedule: "0 4 * * *", Action: "stop", Instance: "survival"}
	err := RunJob(context.Background(), cfg, job, logger.Discard())
	if err == nil || !strings.Contains(err.Error(), "控制通道无响应") {
		t.Fatalf("真实失败应向上报错，实际 %v", err)
	}
}

func TestRunJobRestartStartsWhenStopped(t *testing.T) {
	cfg := newTestConfig(t)
	stubRunning(t, func(*instance.Instance) bool { return false })
	started := false
	stubStart(t, func(context.Context, *config.Config, *instance.Instance, daemon.StartOptions) (daemon.Status, error) {
		started = true
		return daemon.Status{}, nil
	})
	stubRestart(t, func(context.Context, *config.Config, *instance.Instance, daemon.RestartOptions) (daemon.Status, error) {
		t.Fatal("实例未运行时不应走重启通道")
		return daemon.Status{}, nil
	})

	job := config.Job{Name: "hot-restart", Schedule: "0 6 * * *", Action: "restart", Instance: "survival"}
	if err := RunJob(context.Background(), cfg, job, logger.Discard()); err != nil {
		t.Fatalf("重启未运行的实例应退化为启动：%v", err)
	}
	if !started {
		t.Fatal("应调用启动")
	}
}

func TestRunJobRestartWhenRunning(t *testing.T) {
	cfg := newTestConfig(t)
	stubRunning(t, func(*instance.Instance) bool { return true })
	restarted := false
	stubRestart(t, func(context.Context, *config.Config, *instance.Instance, daemon.RestartOptions) (daemon.Status, error) {
		restarted = true
		return daemon.Status{}, nil
	})

	job := config.Job{Name: "hot-restart", Schedule: "0 6 * * *", Action: "restart", Instance: "survival"}
	if err := RunJob(context.Background(), cfg, job, logger.Discard()); err != nil {
		t.Fatalf("重启应成功：%v", err)
	}
	if !restarted {
		t.Fatal("应调用重启")
	}
}

func TestRunJobBackupAllInstances(t *testing.T) {
	cfg := newTestConfig(t)
	var got []string
	stubBackup(t, func(inst *instance.Instance, label string, _ *logger.Logger) (backup.Archive, []string, error) {
		got = append(got, inst.Name+"/"+label)
		return backup.Archive{Name: inst.Name + "-auto.tar.gz", Size: 1024}, []string{"old.tar.gz"}, nil
	})

	job := config.Job{Name: "nightly", Schedule: "0 4 * * *", Action: "backup"}
	if err := RunJob(context.Background(), cfg, job, logger.Discard()); err != nil {
		t.Fatalf("备份应成功：%v", err)
	}
	if len(got) != 2 || !containsString(got, "survival/auto") || !containsString(got, "creative/auto") {
		t.Fatalf("实例留空时应备份全部实例，实际 %v", got)
	}

	// 自定义标签
	got = nil
	job.Label = "weekly"
	if err := RunJob(context.Background(), cfg, job, logger.Discard()); err != nil {
		t.Fatalf("备份应成功：%v", err)
	}
	if !containsString(got, "survival/weekly") {
		t.Fatalf("自定义标签未生效：%v", got)
	}
}

func TestRunJobBackupReportsFailure(t *testing.T) {
	cfg := newTestConfig(t)
	stubBackup(t, func(inst *instance.Instance, _ string, _ *logger.Logger) (backup.Archive, []string, error) {
		if inst.Name == "creative" {
			return backup.Archive{}, nil, errors.New("磁盘写满")
		}
		return backup.Archive{Name: "ok.tar.gz", Size: 1}, nil, nil
	})

	job := config.Job{Name: "nightly", Schedule: "0 4 * * *", Action: "backup"}
	err := RunJob(context.Background(), cfg, job, logger.Discard())
	if err == nil || !strings.Contains(err.Error(), "磁盘写满") {
		t.Fatalf("部分实例失败应向上报错，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "creative") {
		t.Fatalf("错误信息应指出失败的实例，实际 %v", err)
	}
}

func TestRunJobInstanceFilter(t *testing.T) {
	cfg := newTestConfig(t)
	stubRunning(t, func(*instance.Instance) bool { return true })
	var stopped []string
	stubStop(t, func(_ context.Context, _ *config.Config, inst *instance.Instance, _ daemon.StopOptions) error {
		stopped = append(stopped, inst.Name)
		return nil
	})

	// 限定只运行 creative：survival 的任务应被跳过
	survival := config.Job{Name: "stop-survival", Schedule: "0 4 * * *", Action: "stop", Instance: "survival"}
	if err := runJob(context.Background(), cfg, survival, []string{"creative"}, logger.Discard()); err != nil {
		t.Fatalf("范围外的任务应被跳过而不是报错：%v", err)
	}
	if len(stopped) != 0 {
		t.Fatalf("范围外的实例不应被操作：%v", stopped)
	}

	// 实例留空时只作用于范围内的实例
	all := config.Job{Name: "stop-all", Schedule: "0 4 * * *", Action: "stop"}
	if err := runJob(context.Background(), cfg, all, []string{"creative"}, logger.Discard()); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if len(stopped) != 1 || stopped[0] != "creative" {
		t.Fatalf("应只停止 creative，实际 %v", stopped)
	}
}

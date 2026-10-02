package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/yuzumikonami/nyatmc/internal/backup"
	"github.com/yuzumikonami/nyatmc/internal/config"
	"github.com/yuzumikonami/nyatmc/internal/daemon"
	"github.com/yuzumikonami/nyatmc/internal/instance"
	"github.com/yuzumikonami/nyatmc/pkg/logger"
)

// errFiltered 表示任务的目标不在本次运行范围内：应当跳过，而不是算失败。
var errFiltered = errors.New("目标实例不在本次运行范围内")

// 间接层：默认就是真实实现，单元测试可以替换它们以避开真实进程。
var (
	daemonRunning        = daemon.Running
	daemonStart          = daemon.Start
	daemonStop           = daemon.Stop
	daemonRestart        = daemon.Restart
	daemonSendCommand    = daemon.SendCommand
	backupCreateAndPrune = backup.CreateAndPrune
)

// RunJob 立即执行一条任务（手动触发与 cron 触发共用同一条路径）。
func RunJob(ctx context.Context, cfg *config.Config, job config.Job, log *logger.Logger) error {
	return runJob(ctx, cfg, job, nil, log)
}

// runJob 是 RunJob 的内部实现：targets 非空时只对这些实例生效（Runner 的实例过滤）。
func runJob(ctx context.Context, cfg *config.Config, job config.Job, targets []string, log *logger.Logger) error {
	if cfg == nil {
		return errors.New("配置为空")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if log == nil {
		log = logger.Default()
	}

	name := jobLabel(job)
	action := strings.ToLower(strings.TrimSpace(job.Action))
	log.Infof("任务 %s：开始执行（动作 %s）", name, action)

	switch action {
	case ActionBackup:
		return runBackup(cfg, job, targets, log)
	case ActionStart, ActionStop, ActionRestart:
		return runLifecycle(ctx, cfg, job, targets, log)
	case ActionCommand:
		return runCommand(ctx, cfg, job, targets, log)
	default:
		return fmt.Errorf("任务 %s 的动作 %q 不支持（可选：%s）", name, job.Action, strings.Join(Actions, "/"))
	}
}

// runBackup 备份目标实例。
func runBackup(cfg *config.Config, job config.Job, targets []string, log *logger.Logger) error {
	name := jobLabel(job)
	insts, err := targetInstances(cfg, job, targets)
	if err != nil {
		if errors.Is(err, errFiltered) {
			log.Infof("任务 %s：目标实例 %s 不在本次运行范围内，跳过", name, job.Instance)
			return nil
		}
		return err
	}
	label := strings.TrimSpace(job.Label)
	if label == "" {
		label = "auto"
	}

	var errs []error
	for _, inst := range insts {
		arch, pruned, err := backupCreateAndPrune(inst, label, log)
		if err != nil {
			log.Errorf("任务 %s：实例 %s 备份失败：%v", name, inst.Name, err)
			errs = append(errs, fmt.Errorf("实例 %s 备份失败: %w", inst.Name, err))
			continue
		}
		log.Infof("任务 %s：实例 %s 备份完成 %s（%s）", name, inst.Name, arch.Name, backup.HumanSize(arch.Size))
		if len(pruned) > 0 {
			log.Infof("任务 %s：实例 %s 清理了 %d 个过期备份", name, inst.Name, len(pruned))
		}
	}
	return errors.Join(errs...)
}

// runLifecycle 处理 start / stop / restart：实例已在目标状态时记为“跳过”。
func runLifecycle(ctx context.Context, cfg *config.Config, job config.Job, targets []string, log *logger.Logger) error {
	name := jobLabel(job)
	action := strings.ToLower(strings.TrimSpace(job.Action))
	insts, err := targetInstances(cfg, job, targets)
	if err != nil {
		if errors.Is(err, errFiltered) {
			log.Infof("任务 %s：目标实例 %s 不在本次运行范围内，跳过", name, job.Instance)
			return nil
		}
		return err
	}

	var errs []error
	for _, inst := range insts {
		running := daemonRunning(inst)
		switch action {
		case ActionStart:
			if running {
				log.Infof("任务 %s：实例 %s 已在运行，跳过", name, inst.Name)
				continue
			}
			if _, err := daemonStart(ctx, cfg, inst, daemon.StartOptions{}); err != nil {
				log.Errorf("任务 %s：实例 %s 启动失败：%v", name, inst.Name, err)
				errs = append(errs, fmt.Errorf("实例 %s 启动失败: %w", inst.Name, err))
				continue
			}
			log.Infof("任务 %s：实例 %s 已启动", name, inst.Name)

		case ActionStop:
			if !running {
				log.Infof("任务 %s：实例 %s 本来就没有运行，跳过", name, inst.Name)
				continue
			}
			err := daemonStop(ctx, cfg, inst, daemon.StopOptions{})
			switch {
			case err == nil:
				log.Infof("任务 %s：实例 %s 已停止", name, inst.Name)
			case errors.Is(err, daemon.ErrNotRunning):
				log.Infof("任务 %s：实例 %s 刚刚已停止，跳过", name, inst.Name)
			default:
				log.Errorf("任务 %s：实例 %s 停止失败：%v", name, inst.Name, err)
				errs = append(errs, fmt.Errorf("实例 %s 停止失败: %w", inst.Name, err))
			}

		case ActionRestart:
			if !running {
				// 停着的实例“重启”等同启动一次，避免计划任务把服务端永久留在停止状态。
				log.Infof("任务 %s：实例 %s 当前未运行，改为直接启动", name, inst.Name)
				if _, err := daemonStart(ctx, cfg, inst, daemon.StartOptions{}); err != nil {
					log.Errorf("任务 %s：实例 %s 启动失败：%v", name, inst.Name, err)
					errs = append(errs, fmt.Errorf("实例 %s 启动失败: %w", inst.Name, err))
					continue
				}
				log.Infof("任务 %s：实例 %s 已启动", name, inst.Name)
				continue
			}
			if _, err := daemonRestart(ctx, cfg, inst, daemon.RestartOptions{}); err != nil {
				log.Errorf("任务 %s：实例 %s 重启失败：%v", name, inst.Name, err)
				errs = append(errs, fmt.Errorf("实例 %s 重启失败: %w", inst.Name, err))
				continue
			}
			log.Infof("任务 %s：实例 %s 已重启", name, inst.Name)
		}
	}
	return errors.Join(errs...)
}

// runCommand 向实例控制台发送一条指令：实例必须正在运行。
func runCommand(ctx context.Context, cfg *config.Config, job config.Job, targets []string, log *logger.Logger) error {
	name := jobLabel(job)
	line := strings.TrimSpace(job.Command)
	if line == "" {
		return fmt.Errorf("任务 %s 的动作是 command，但没有配置 command", name)
	}
	insts, err := targetInstances(cfg, job, targets)
	if err != nil {
		if errors.Is(err, errFiltered) {
			log.Infof("任务 %s：目标实例 %s 不在本次运行范围内，跳过", name, job.Instance)
			return nil
		}
		return err
	}

	var errs []error
	for _, inst := range insts {
		if !daemonRunning(inst) {
			err := fmt.Errorf("实例 %s 未在运行，无法发送指令 %q", inst.Name, line)
			log.Errorf("任务 %s：%v", name, err)
			errs = append(errs, err)
			continue
		}
		if err := daemonSendCommand(ctx, inst, line); err != nil {
			log.Errorf("任务 %s：向实例 %s 发送指令失败：%v", name, inst.Name, err)
			errs = append(errs, fmt.Errorf("实例 %s 发送指令失败: %w", inst.Name, err))
			continue
		}
		log.Infof("任务 %s：已向实例 %s 发送指令 %q", name, inst.Name, line)
	}
	return errors.Join(errs...)
}

// targetInstances 解析任务的目标实例。
//
//   - job.Instance 非空时只针对该实例；
//   - 为空表示所有实例（targets 非空时限定在 targets 之内）。
func targetInstances(cfg *config.Config, job config.Job, targets []string) ([]*instance.Instance, error) {
	name := strings.TrimSpace(job.Instance)
	if name != "" {
		if len(targets) > 0 && !containsString(targets, name) {
			return nil, errFiltered
		}
		inst, err := newTarget(cfg, job, name)
		if err != nil {
			return nil, err
		}
		return []*instance.Instance{inst}, nil
	}

	names := targets
	if len(names) == 0 {
		names = cfg.InstanceNames()
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("任务 %s：配置里没有任何实例，无法执行", jobLabel(job))
	}
	out := make([]*instance.Instance, 0, len(names))
	for _, n := range names {
		inst, err := newTarget(cfg, job, n)
		if err != nil {
			return nil, err
		}
		out = append(out, inst)
	}
	return out, nil
}

// newTarget 校验实例存在并构造句柄。
func newTarget(cfg *config.Config, job config.Job, name string) (*instance.Instance, error) {
	if !cfg.HasInstance(name) {
		available := cfg.InstanceNames()
		hint := "配置里还没有实例"
		if len(available) > 0 {
			hint = "可用实例：" + strings.Join(available, "、")
		}
		return nil, fmt.Errorf("任务 %s：目标实例 %q 不存在（%s）", jobLabel(job), name, hint)
	}
	inst, err := instance.New(cfg, name)
	if err != nil {
		return nil, fmt.Errorf("任务 %s：%w", jobLabel(job), err)
	}
	return inst, nil
}

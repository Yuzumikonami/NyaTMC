package scheduler

import (
	"errors"
	"fmt"
	"strings"

	"github.com/yuzumikonami/nyatmc/internal/config"
)

// ValidateJob 校验单条任务的合法性（名字、cron 表达式、动作、实例、指令）。
func ValidateJob(job config.Job) error {
	name := strings.TrimSpace(job.Name)
	if name == "" {
		return errors.New("任务名字不能为空")
	}
	if strings.ContainsAny(name, "\r\n\t") {
		return fmt.Errorf("任务名字 %q 不能包含换行或制表符", name)
	}
	if len([]rune(name)) > 64 {
		return fmt.Errorf("任务名字 %q 过长（最多 64 个字符）", name)
	}

	spec := strings.TrimSpace(job.Schedule)
	if err := config.ValidateCron(spec); err != nil {
		return fmt.Errorf("任务 %s 的 cron 表达式非法: %w", name, err)
	}
	if _, err := scheduleParser.Parse(spec); err != nil {
		return fmt.Errorf("任务 %s 的 cron 表达式无法解析（%q）: %w", name, spec, err)
	}

	action := strings.ToLower(strings.TrimSpace(job.Action))
	switch {
	case action == "":
		return fmt.Errorf("任务 %s 缺少动作（可选：%s）", name, strings.Join(Actions, "/"))
	case !containsString(Actions, action):
		return fmt.Errorf("任务 %s 的动作 %q 非法（可选：%s）", name, job.Action, strings.Join(Actions, "/"))
	case action == ActionCommand && strings.TrimSpace(job.Command) == "":
		return fmt.Errorf("任务 %s 的动作是 command，必须同时给出 --command", name)
	}

	if inst := strings.TrimSpace(job.Instance); inst != "" {
		if err := config.ValidateInstanceName(inst); err != nil {
			return fmt.Errorf("任务 %s 的实例名非法: %w", name, err)
		}
	}
	return nil
}

// Add 校验并把任务写入配置（立即落盘）。
func Add(cfg *config.Config, job config.Job) error {
	if cfg == nil {
		return errors.New("配置为空")
	}
	if err := ValidateJob(job); err != nil {
		return err
	}
	name := strings.TrimSpace(job.Name)
	if _, exists := Find(cfg, name); exists {
		return fmt.Errorf("任务 %q 已存在；换个名字，或先运行 nyatmc schedule remove %s", name, name)
	}

	job.Name = name
	job.Schedule = strings.TrimSpace(job.Schedule)
	job.Action = strings.ToLower(strings.TrimSpace(job.Action))
	job.Instance = strings.TrimSpace(job.Instance)
	job.Label = strings.TrimSpace(job.Label)
	cfg.Scheduler.Jobs = append(cfg.Scheduler.Jobs, job)
	return saveConfig(cfg)
}

// Remove 删除指定任务。
func Remove(cfg *config.Config, name string) error {
	if cfg == nil {
		return errors.New("配置为空")
	}
	name = strings.TrimSpace(name)
	for i, job := range cfg.Scheduler.Jobs {
		if strings.TrimSpace(job.Name) != name {
			continue
		}
		cfg.Scheduler.Jobs = append(cfg.Scheduler.Jobs[:i], cfg.Scheduler.Jobs[i+1:]...)
		return saveConfig(cfg)
	}
	return fmt.Errorf("调度任务 %q 不存在", name)
}

// SetEnabled 启用或停用指定任务。
func SetEnabled(cfg *config.Config, name string, enabled bool) error {
	if cfg == nil {
		return errors.New("配置为空")
	}
	name = strings.TrimSpace(name)
	for i := range cfg.Scheduler.Jobs {
		if strings.TrimSpace(cfg.Scheduler.Jobs[i].Name) != name {
			continue
		}
		cfg.Scheduler.Jobs[i].Enabled = config.BoolPtr(enabled)
		return saveConfig(cfg)
	}
	return fmt.Errorf("调度任务 %q 不存在", name)
}

// Find 按名字查找任务。
func Find(cfg *config.Config, name string) (config.Job, bool) {
	if cfg == nil {
		return config.Job{}, false
	}
	name = strings.TrimSpace(name)
	for _, job := range cfg.Scheduler.Jobs {
		if strings.TrimSpace(job.Name) == name {
			return job, true
		}
	}
	return config.Job{}, false
}

// saveConfig 写回 config.toml。
func saveConfig(cfg *config.Config) error {
	cfg.CleanForSave()
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("写入配置失败: %w", err)
	}
	return nil
}

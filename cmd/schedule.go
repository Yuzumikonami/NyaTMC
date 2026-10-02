package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/yuzumikonami/nyatmc/internal/config"
	"github.com/yuzumikonami/nyatmc/internal/scheduler"
	"github.com/yuzumikonami/nyatmc/pkg/logger"
)

var (
	scheduleAddInstance     string
	scheduleAddCommand      string
	scheduleAddLabel        string
	scheduleAddDisabled     bool
	scheduleDaemonDetach    bool
	scheduleDaemonInstances []string
	scheduleHiddenInstances []string
)

// scheduleRunTimeout 是手动执行任务的时间上限（与调度器内部一致）。
const scheduleRunTimeout = 30 * time.Minute

var scheduleCmd = &cobra.Command{
	Use:   "schedule",
	Short: "内置定时任务调度器（自动备份 / 重启 / 执行指令）",
	Long: `管理 nyatmc 的内置定时任务，任务写在 config.toml 的 [[scheduler.jobs]] 里。

动作（action）：
  backup    备份目标实例（实例留空表示所有实例）
  restart   重启目标实例（未运行时等同启动，避免计划任务把服务端留在停止状态）
  stop      停止目标实例（本来就没运行则记为跳过）
  start     启动目标实例（已在运行则记为跳过）
  command   向实例控制台发送一条指令（实例必须在运行）

示例：
  nyatmc schedule add daily-backup "0 4 * * *" backup
  nyatmc schedule add hot-restart "0 6 * * 1" restart --instance survival
  nyatmc schedule add announce "*/30 * * * *" command --command "say 服务器维护中"
  nyatmc schedule list
  nyatmc schedule daemon --detach     # 后台常驻，负责到点执行任务`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

var scheduleAddCmd = &cobra.Command{
	Use:   "add <名字> <cron表达式> <动作>",
	Short: "添加一条定时任务",
	Long: `添加一条定时任务并写入 config.toml。

cron 表达式支持 5 段（分 时 日 月 周）、6 段（含秒）以及 @daily / @hourly / @every 1h 等宏。`,
	Args: cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		job := config.Job{
			Name:     args[0],
			Schedule: args[1],
			Action:   args[2],
			Instance: scheduleAddInstance,
			Command:  scheduleAddCommand,
			Label:    scheduleAddLabel,
		}
		if scheduleAddDisabled {
			job.Enabled = config.BoolPtr(false)
		}
		if err := scheduler.Add(app.Cfg, job); err != nil {
			return err
		}
		if app.JSON {
			saved, _ := scheduler.Find(app.Cfg, args[0])
			return app.JSONOut(saved)
		}
		app.OK("已添加任务 %s：%s → %s", strings.TrimSpace(args[0]), strings.TrimSpace(args[1]), strings.ToLower(strings.TrimSpace(args[2])))
		if name := strings.TrimSpace(scheduleAddInstance); name != "" {
			app.Out("  目标实例：%s", name)
		} else {
			app.Out("  目标实例：全部实例")
		}
		if line := strings.TrimSpace(scheduleAddCommand); line != "" {
			app.Out("  执行指令：%s", line)
		}
		if scheduleAddDisabled {
			app.Out("  状态：已停用（用 nyatmc schedule enable %s 启用）", strings.TrimSpace(args[0]))
		}
		app.Out("配置已写入 %s", app.Cfg.Path)
		app.Out("提示：正在运行的调度器会在 30 秒内自动加载新任务。")
		return nil
	},
}

var scheduleListCmd = &cobra.Command{
	Use:   "list",
	Short: "列出全部定时任务与执行情况",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		sts, err := scheduleStatuses(app)
		if err != nil {
			return err
		}
		if app.JSON {
			return app.JSONOut(sts)
		}
		if len(sts) == 0 {
			app.Out("还没有任何定时任务。示例：nyatmc schedule add daily-backup \"0 4 * * *\" backup")
			return nil
		}
		printScheduleJobs(app, sts)
		return nil
	},
}

var scheduleRemoveCmd = &cobra.Command{
	Use:   "remove <名字>",
	Short: "删除一条定时任务",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		if err := scheduler.Remove(app.Cfg, args[0]); err != nil {
			return err
		}
		app.OK("已删除任务 %s", strings.TrimSpace(args[0]))
		return nil
	},
}

var scheduleEnableCmd = &cobra.Command{
	Use:   "enable <名字>",
	Short: "启用一条定时任务",
	Args:  cobra.ExactArgs(1),
	RunE:  scheduleSetEnabledRun(true),
}

var scheduleDisableCmd = &cobra.Command{
	Use:   "disable <名字>",
	Short: "停用一条定时任务",
	Args:  cobra.ExactArgs(1),
	RunE:  scheduleSetEnabledRun(false),
}

var scheduleRunCmd = &cobra.Command{
	Use:   "run <名字>",
	Short: "立刻手动执行一次任务",
	Long: `立刻执行一次指定任务（即使它已被停用），并把结果记入状态文件。

执行结果与调度器自动触发时完全一致，最多等待 30 分钟。`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		job, ok := scheduler.Find(app.Cfg, args[0])
		if !ok {
			return fmt.Errorf("调度任务 %q 不存在；用 nyatmc schedule list 查看全部任务", strings.TrimSpace(args[0]))
		}
		log := app.Log
		if log == nil {
			log = logger.Default()
		}
		log.Infof("手动触发任务 %s（动作 %s）", job.Name, job.Action)
		if !app.JSON {
			app.Out("正在执行任务 %s（动作 %s）…", job.Name, job.Action)
		}

		ctx, cancel := context.WithTimeout(app.Ctx, scheduleRunTimeout)
		defer cancel()
		start := time.Now()
		runErr := scheduler.RunJob(ctx, app.Cfg, job, log)
		scheduler.RecordRun(job, runErr)
		cost := time.Since(start)

		if runErr != nil {
			if app.JSON {
				_ = app.JSONOut(map[string]any{"job": job.Name, "ok": false, "error": runErr.Error(), "cost_seconds": cost.Seconds()})
			}
			return fmt.Errorf("任务 %s 执行失败：%w", job.Name, runErr)
		}
		if app.JSON {
			return app.JSONOut(map[string]any{"job": job.Name, "ok": true, "cost_seconds": cost.Seconds()})
		}
		app.OK("任务 %s 执行完成（耗时 %s）", job.Name, HumanDuration(cost))
		return nil
	},
}

var scheduleValidateCmd = &cobra.Command{
	Use:   "validate",
	Short: "校验配置里所有定时任务",
	Long: `逐条校验 scheduler.jobs：名字、cron 表达式、动作、实例名与指令是否合法。

配置本身有问题时也能运行（不会因为任务非法而拒绝加载），便于定位错误。`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := scheduleValidateApp(cmd)
		if err != nil {
			return err
		}
		jobs := app.Cfg.Scheduler.Jobs
		if len(jobs) == 0 {
			app.Out("配置里没有任何定时任务，无需校验。")
			return nil
		}

		seen := map[string]bool{}
		var problems []string
		rows := make([][]string, 0, len(jobs))
		for i, job := range jobs {
			name := strings.TrimSpace(job.Name)
			var issues []string
			if name == "" {
				issues = append(issues, fmt.Sprintf("第 %d 条任务缺少 name", i+1))
			} else {
				if seen[name] {
					issues = append(issues, "任务名重复")
				}
				seen[name] = true
			}
			if err := scheduler.ValidateJob(job); err != nil {
				issues = append(issues, err.Error())
			}
			label := name
			if label == "" {
				label = fmt.Sprintf("(第 %d 条)", i+1)
			}
			if len(issues) == 0 {
				state := "启用"
				if !job.IsEnabled() {
					state = "停用"
				}
				rows = append(rows, []string{label, app.paint("32", "✓ 通过"), state, job.Schedule})
				continue
			}
			problems = append(problems, strings.Join(issues, "；"))
			rows = append(rows, []string{label, app.paint("31", "✗ 失败"), job.Action, strings.Join(issues, "；")})
		}

		if app.JSON {
			if err := app.JSONOut(map[string]any{"total": len(jobs), "failed": len(problems), "problems": problems}); err != nil {
				return err
			}
		} else {
			app.Table([]string{"任务", "结果", "动作/启用", "说明"}, rows)
		}
		if len(problems) > 0 {
			return ExitWith(1, "有 %d 条任务不合法", len(problems))
		}
		if !app.JSON {
			app.OK("全部 %d 条任务校验通过", len(jobs))
		}
		return nil
	},
}

var scheduleStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "查看调度器是否在运行与各任务情况",
	Long: `查看调度器守护进程是否在运行（依据状态文件里的 pid + 进程存活判断），
并列出每个任务的上次执行时间、下次执行时间、累计次数与上次错误。

退出码：0 = 调度器在运行，3 = 未运行。`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		state, err := scheduler.ReadState()
		if err != nil {
			return err
		}
		pid := scheduler.RunningPID()
		sts, err := scheduleStatuses(app)
		if err != nil {
			return err
		}

		if app.JSON {
			if err := app.JSONOut(map[string]any{
				"running":    pid > 0,
				"pid":        pid,
				"started_at": state.StartedAt,
				"updated_at": state.UpdatedAt,
				"jobs":       sts,
			}); err != nil {
				return err
			}
			if pid == 0 {
				return ExitWith(3, "调度器未在运行")
			}
			return nil
		}

		switch {
		case pid > 0:
			app.OK("调度器正在运行（pid %d，启动于 %s，已运行 %s）", pid, scheduleTime(state.StartedAt), HumanDuration(time.Since(state.StartedAt)))
			app.Out("  日志：%s", scheduler.LogPath())
		case !state.UpdatedAt.IsZero():
			app.Warn("调度器未在运行（最近一次活动：%s）", scheduleTime(state.UpdatedAt))
		default:
			app.Warn("调度器未在运行（还没有状态记录）")
		}
		if len(sts) > 0 {
			app.Out("")
			printScheduleJobs(app, sts)
		}
		if pid == 0 {
			app.Out("")
			app.Out("  后台启动：nyatmc schedule daemon --detach")
			return ExitWith(3, "调度器未在运行")
		}
		return nil
	},
}

var scheduleDaemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "前台运行调度器（Ctrl+C 退出）",
	Long: `运行内置调度器：到点执行 [[scheduler.jobs]] 里的任务。

默认前台运行，Ctrl+C 退出；加 --detach 则派生出脱离终端的守护进程，
进程号与执行历史记录在 ~/.nyatmc/state/scheduler.json。`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		if !app.Cfg.Scheduler.Enabled {
			app.Warn("配置里 scheduler.enabled = false，本次仍按命令行要求运行")
		}

		if scheduleDaemonDetach {
			if pid := scheduler.RunningPID(); pid > 0 {
				app.Warn("已经有调度器在运行（pid %d），不再重复启动", pid)
				return nil
			}
			pid, err := scheduler.Spawn(app.Cfg.Path, scheduleDaemonInstances)
			if err != nil {
				return err
			}
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if running := scheduler.RunningPID(); running > 0 {
					app.OK("调度器已在后台运行（pid %d）", running)
					app.Out("  日志：%s", scheduler.LogPath())
					app.Out("  停止：nyatmc schedule status 查看，或用系统的进程管理结束该 pid")
					return nil
				}
				time.Sleep(100 * time.Millisecond)
			}
			app.Warn("调度器进程 %d 已派生，但还没有写入状态；用 nyatmc schedule status 确认，日志：%s", pid, scheduler.LogPath())
			return nil
		}

		r, err := scheduler.New(app.Cfg, scheduler.Options{
			ConfigPath: app.Cfg.Path,
			Logger:     app.Log,
			Instances:  scheduleDaemonInstances,
		})
		if err != nil {
			return err
		}
		loc, _ := scheduler.Timezone(app.Cfg)
		app.Out("调度器启动中：时区 %s，Ctrl+C 退出（日志同时写入 %s）", loc, scheduler.LogPath())
		ctx, stop := scheduleSignalContext(cmd)
		defer stop()
		return r.Start(ctx)
	},
}

// scheduleServeCmd 是 --detach 时派生的隐藏子命令。
var scheduleServeCmd = &cobra.Command{
	Use:    "__schedule",
	Short:  "内部命令：运行调度器守护进程（由 nyatmc schedule daemon --detach 派生）",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		r, err := scheduler.New(app.Cfg, scheduler.Options{
			ConfigPath: app.Cfg.Path,
			Logger:     app.Log,
			Instances:  scheduleHiddenInstances,
		})
		if err != nil {
			return err
		}
		ctx, stop := scheduleSignalContext(cmd)
		defer stop()
		return r.Start(ctx)
	},
}

// scheduleSetEnabledRun 生成 enable / disable 的执行函数。
func scheduleSetEnabledRun(enabled bool) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		if err := scheduler.SetEnabled(app.Cfg, args[0], enabled); err != nil {
			return err
		}
		if enabled {
			app.OK("已启用任务 %s", strings.TrimSpace(args[0]))
		} else {
			app.OK("已停用任务 %s", strings.TrimSpace(args[0]))
		}
		return nil
	}
}

// scheduleStatuses 汇总任务状态（合并状态文件里的执行历史）。
func scheduleStatuses(app *App) ([]scheduler.JobStatus, error) {
	r, err := scheduler.New(app.Cfg, scheduler.Options{
		ConfigPath: app.Cfg.Path,
		Logger:     logger.Discard(),
	})
	if err != nil {
		return nil, err
	}
	return r.Status(app.Ctx)
}

// printScheduleJobs 输出任务表格。
func printScheduleJobs(app *App, sts []scheduler.JobStatus) {
	rows := make([][]string, 0, len(sts))
	for _, st := range sts {
		instance := strings.TrimSpace(st.Instance)
		if instance == "" {
			instance = "全部"
		}
		enabled := app.paint("32", "是")
		if !st.IsEnabled() {
			enabled = app.paint("90", "否")
		}
		action := st.Action
		if st.Running {
			action += "（执行中）"
		}
		rows = append(rows, []string{
			st.Name,
			st.Schedule,
			action,
			instance,
			enabled,
			scheduleTime(st.LastRun),
			scheduleTime(st.NextRun),
			scheduleShort(st.LastError, 40),
		})
	}
	app.Table([]string{"名字", "表达式", "动作", "实例", "启用", "上次执行", "下次执行", "上次错误"}, rows)
}

// scheduleValidateApp 加载配置；配置里有非法任务时也不放弃（否则无法给出诊断）。
func scheduleValidateApp(cmd *cobra.Command) (*App, error) {
	app, err := newApp(cmd)
	if err == nil {
		return app, nil
	}
	config.SetHome(flagHome)
	cfg, lerr := config.LoadOrCreate(flagConfig)
	if lerr != nil {
		return nil, err
	}
	log := logger.Default()
	if flagVerbose {
		log.SetLevel(logger.LevelDebug)
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	return &App{
		Ctx:   ctx,
		Cfg:   cfg,
		Log:   log,
		JSON:  flagJSON,
		Color: !flagNoColor && cfg.General.Color && isStdoutTerminal(),
	}, nil
}

// scheduleSignalContext 返回一个 Ctrl+C / SIGTERM 会取消的上下文，以及取消函数。
func scheduleSignalContext(cmd *cobra.Command) (context.Context, context.CancelFunc) {
	base := cmd.Context()
	if base == nil {
		base = context.Background()
	}
	return signal.NotifyContext(base, os.Interrupt, syscall.SIGTERM)
}

// scheduleTime 格式化时间，零值显示为 -。
func scheduleTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02 15:04:05")
}

// scheduleShort 截断过长的文本，保持表格可读。
func scheduleShort(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if s == "" {
		return "-"
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n <= 1 {
		return string(runes[:n])
	}
	return string(runes[:n-1]) + "…"
}

func init() {
	scheduleAddCmd.Flags().StringVar(&scheduleAddInstance, "instance", "", "目标实例名（留空表示所有实例）")
	scheduleAddCmd.Flags().StringVar(&scheduleAddCommand, "command", "", "action 为 command 时要发送的指令")
	scheduleAddCmd.Flags().StringVar(&scheduleAddLabel, "label", "", "备份文件标签（默认 auto）")
	scheduleAddCmd.Flags().BoolVar(&scheduleAddDisabled, "disabled", false, "添加后先停用")

	scheduleDaemonCmd.Flags().BoolVar(&scheduleDaemonDetach, "detach", false, "后台运行（派生脱离终端的守护进程）")
	scheduleDaemonCmd.Flags().StringSliceVar(&scheduleDaemonInstances, "instances", nil, "只运行这些实例的任务（可重复或逗号分隔）")

	scheduleServeCmd.Flags().StringSliceVar(&scheduleHiddenInstances, "instances", nil, "只运行这些实例的任务")

	scheduleCmd.AddCommand(
		scheduleAddCmd,
		scheduleListCmd,
		scheduleRemoveCmd,
		scheduleEnableCmd,
		scheduleDisableCmd,
		scheduleRunCmd,
		scheduleValidateCmd,
		scheduleDaemonCmd,
		scheduleStatusCmd,
	)
	rootCmd.AddCommand(scheduleCmd, scheduleServeCmd)
}

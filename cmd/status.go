package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yuzumikonami/nyatmc/internal/daemon"
	"github.com/yuzumikonami/nyatmc/internal/instance"
)

var (
	statusAll   bool
	statusWatch bool
	statusLines int
)

var statusCmd = &cobra.Command{
	Use:   "status [实例名]",
	Short: "查看实例运行状态",
	Long: `查看实例状态：运行状态、PID、运行时长、版本、内存、TPS 与在线玩家。

退出码：0 = 运行中，3 = 已停止，4 = 崩溃/启动失败（便于脚本判断）。`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}

		if statusWatch {
			return watchStatus(app, args)
		}

		instances, err := app.resolveInstances(args, statusAll)
		if err != nil {
			return err
		}

		var statuses []daemon.Status
		for _, inst := range instances {
			st, err := daemon.GetStatus(app.Ctx, app.Cfg, inst)
			if err != nil {
				st = daemon.Status{Instance: inst.Name, State: daemon.StateUnknown, LastError: err.Error()}
			}
			statuses = append(statuses, st)
		}

		if app.JSON {
			if len(statuses) == 1 {
				if err := app.JSONOut(statuses[0]); err != nil {
					return err
				}
			} else {
				if err := app.JSONOut(statuses); err != nil {
					return err
				}
			}
			return nil
		}

		if len(statuses) == 1 {
			printStatusDetail(app, instances[0], statuses[0], statusLines)
		} else {
			rows := make([][]string, 0, len(statuses))
			for i, st := range statuses {
				players := "-"
				if st.PlayerCount() > 0 {
					players = fmt.Sprintf("%d/%d", st.PlayerCount(), st.MaxPlayers)
				}
				pid := "-"
				if st.PID > 0 {
					pid = fmt.Sprintf("%d", st.PID)
				}
				rows = append(rows, []string{
					instances[i].Name,
					app.StateLabel(st.State),
					pid,
					HumanDuration(st.Uptime()),
					players,
					versionLabel(st),
				})
			}
			app.Table([]string{"实例", "状态", "PID", "运行时长", "在线", "版本"}, rows)
		}

		return statusExit(statuses)
	},
}

func versionLabel(st daemon.Status) string {
	v := st.Version
	if v == "" {
		v = st.ServerType
	} else if st.ServerType != "" {
		v = v + " (" + st.ServerType + ")"
	}
	if v == "" {
		return "-"
	}
	return v
}

// statusExit 按状态决定退出码，方便脚本使用。
func statusExit(statuses []daemon.Status) error {
	for _, st := range statuses {
		switch st.State {
		case daemon.StateCrashed:
			return ExitWith(4, "实例 %s 处于崩溃状态", st.Instance)
		}
	}
	for _, st := range statuses {
		if !st.State.Active() {
			return ExitWith(3, "实例 %s 未在运行", st.Instance)
		}
	}
	return nil
}

func printStatusDetail(app *App, inst *instance.Instance, st daemon.Status, tailLines int) {
	mark := func(label, value string) {
		if strings.TrimSpace(value) == "" {
			value = "-"
		}
		app.Out("  %-10s %s", label, value)
	}

	app.Out("实例 %s", inst.Name)
	mark("状态", app.StateLabel(st.State)+readySuffix(st))
	sup := "-"
	if st.SupervisorPID > 0 {
		sup = fmt.Sprintf("%d", st.SupervisorPID)
	}
	pid := "-"
	if st.PID > 0 {
		pid = fmt.Sprintf("%d", st.PID)
	}
	mark("PID", fmt.Sprintf("%s（守护进程 %s）", pid, sup))
	mark("运行时长", HumanDuration(st.Uptime()))
	mark("版本", versionLabel(st))
	mem := "-"
	if st.MemoryUsedMB > 0 {
		mem = fmt.Sprintf("%d MB / %s", st.MemoryUsedMB, st.MemoryLimit)
	} else if st.MemoryLimit != "" {
		mem = st.MemoryLimit
	}
	mark("内存", mem)
	tps := "未知（服务端未输出）"
	if st.TPSKnown {
		tps = fmt.Sprintf("%.2f", st.TPS)
		if st.TickBehindMS > 0 {
			tps += fmt.Sprintf("（曾落后 %dms）", st.TickBehindMS)
		}
	}
	mark("TPS", tps)
	if len(st.Players) > 0 {
		mark("在线玩家", fmt.Sprintf("%d/%d：%s", len(st.Players), st.MaxPlayers, strings.Join(st.Players, ", ")))
	} else {
		mark("在线玩家", fmt.Sprintf("0/%d", st.MaxPlayers))
	}
	mark("看门狗", onOff(st.Watchdog))
	mark("重启次数", fmt.Sprintf("%d", st.Restarts))
	if st.LastExitCode != 0 {
		mark("最近退出码", fmt.Sprintf("%d", st.LastExitCode))
	}
	mark("服务端目录", st.ServerDir)
	mark("配置文件", st.ConfigPath)
	if st.LastError != "" {
		mark("最近问题", st.LastError)
	}
	if st.Note != "" {
		mark("备注", st.Note)
	}
	if st.Stale {
		mark("提示", "状态来自落盘文件（守护进程未响应），可能不是最新")
	}

	if tailLines > 0 {
		lines := readLogLines(inst, tailLines)
		if len(lines) > 0 {
			app.Out("\n最近日志：")
			for _, l := range lines {
				app.Out("  %s", l)
			}
		}
	}
}

func readySuffix(st daemon.Status) string {
	switch {
	case st.State != daemon.StateRunning:
		return ""
	case st.Ready:
		return "（已就绪）"
	default:
		return "（启动中，尚未就绪）"
	}
}

func onOff(v bool) string {
	if v {
		return "已开启"
	}
	return "已关闭"
}

// watchStatus 以固定间隔刷新状态（Ctrl+C 退出）。
func watchStatus(app *App, args []string) error {
	instances, err := app.resolveInstances(args, statusAll)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	for {
		fmt.Print("\x1b[2J\x1b[H")
		app.Out("nyatmc status（每 2 秒刷新，Ctrl+C 退出）  %s", time.Now().Format("15:04:05"))
		for _, inst := range instances {
			st, err := daemon.GetStatus(ctx, app.Cfg, inst)
			if err != nil {
				st = daemon.Status{Instance: inst.Name, State: daemon.StateUnknown, LastError: err.Error()}
			}
			printStatusDetail(app, inst, st, 0)
			app.Out("")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(2 * time.Second):
		}
	}
}

// readLogLines 读取实例最近日志（优先控制台捕获，其次服务端 latest.log）。
func readLogLines(inst *instance.Instance, n int) []string {
	lines, err := readTailFile(inst.ConsoleLogPath(), n)
	if err == nil && len(lines) > 0 {
		return lines
	}
	lines, _ = readTailFile(inst.MinecraftLogPath(), n)
	return lines
}

func init() {
	statusCmd.Flags().BoolVar(&statusAll, "all", false, "显示所有实例")
	statusCmd.Flags().BoolVarP(&statusWatch, "watch", "w", false, "持续刷新")
	statusCmd.Flags().IntVarP(&statusLines, "lines", "n", 0, "额外附带最近 N 行日志")
	rootCmd.AddCommand(statusCmd)
}

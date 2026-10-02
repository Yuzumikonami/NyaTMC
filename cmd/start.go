package cmd

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/yuzumikonami/nyatmc/internal/daemon"
)

var (
	startForeground bool
	startWait       bool
	startTimeout    time.Duration
	startWatchdog   bool
	startNoWatchdog bool
	startNoStart    bool
	startAll        bool
)

var startCmd = &cobra.Command{
	Use:   "start [实例名]",
	Short: "启动实例（后台守护 + 崩溃自动重启）",
	Long: `启动实例。

默认会在后台派生一个守护进程（supervisor）：它负责启动服务端、崩溃后自动重启、
日志轮转，并提供本地控制通道给 status / tui / web 使用。

加 -f/--foreground 则把守护进程跑在当前终端里：服务端输出直接显示，
键盘输入即为服务端指令，Ctrl+C 优雅关闭（等价于发送 stop 指令）。`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		instances, err := app.resolveInstances(args, startAll)
		if err != nil {
			return err
		}

		var watchdog *bool
		switch {
		case startWatchdog && startNoWatchdog:
			return fmt.Errorf("--watchdog 与 --no-watchdog 不能同时使用")
		case startWatchdog:
			v := true
			watchdog = &v
		case startNoWatchdog:
			v := false
			watchdog = &v
		}

		var failures []string
		for _, inst := range instances {
			if app.JSON && !startForeground {
				st, serr := daemon.Start(app.Ctx, app.Cfg, inst, daemon.StartOptions{
					Wait: startWait, WaitTimeout: startTimeout, NoStart: startNoStart, Watchdog: watchdog,
				})
				if serr != nil {
					if errors.Is(serr, daemon.ErrAlreadyRunning) {
						app.Warn("实例 %s 已经在运行，跳过", inst.Name)
						continue
					}
					failures = append(failures, fmt.Sprintf("%s: %v", inst.Name, serr))
					continue
				}
				if jerr := app.JSONOut(st); jerr != nil {
					return jerr
				}
				continue
			}

			app.Out("正在启动实例 %s …", inst.Name)
			st, err := daemon.Start(app.Ctx, app.Cfg, inst, daemon.StartOptions{
				Foreground:  startForeground,
				Wait:        startWait || startForeground,
				WaitTimeout: startTimeout,
				NoStart:     startNoStart,
				Watchdog:    watchdog,
				Logger:      app.Log,
			})
			if err != nil {
				if errors.Is(err, daemon.ErrAlreadyRunning) {
					app.Warn("实例 %s 已经在运行，跳过", inst.Name)
					continue
				}
				failures = append(failures, fmt.Sprintf("%s: %v", inst.Name, err))
				app.Fail("实例 %s 启动失败：%v", inst.Name, err)
				continue
			}
			if startForeground {
				// 前台模式：daemon.Start 会一直阻塞到实例停止
				app.Out("实例 %s 已停止。", inst.Name)
				continue
			}
			if st.Ready {
				app.OK("实例 %s 已就绪（pid %d，耗时 %s）", inst.Name, st.PID, HumanDuration(st.Uptime()))
			} else {
				app.OK("实例 %s 已在后台启动（pid %d，状态 %s）", inst.Name, st.PID, st.State.Label())
				app.Out("  查看状态：nyatmc status %s", inst.Name)
				app.Out("  查看日志：nyatmc logs -f %s", inst.Name)
			}
		}

		if len(failures) > 0 {
			return fmt.Errorf("有 %d 个实例启动失败：%v", len(failures), failures)
		}
		return nil
	},
}

var stopCmd = &cobra.Command{
	Use:   "stop [实例名]",
	Short: "停止实例（发送 stop 指令，超时后强制结束）",
	Long: `优雅停止实例：向服务端控制台发送 stop 指令并等待其自行退出，
超过 daemon.stop_timeout（默认 60s）才强制结束进程，最后停掉守护进程。

用 -f/--force 可直接强杀；用 --keep-supervisor 只停服务端、保留守护进程。`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		instances, err := app.resolveInstances(args, stopAll)
		if err != nil {
			return err
		}

		var failures []string
		for _, inst := range instances {
			err := daemon.Stop(app.Ctx, app.Cfg, inst, daemon.StopOptions{
				Force:          stopForce,
				Timeout:        stopTimeout,
				KeepSupervisor: stopKeepSupervisor,
			})
			switch {
			case err == nil:
				if stopKeepSupervisor {
					app.OK("实例 %s 的服务端已停止（守护进程保留）", inst.Name)
				} else {
					app.OK("实例 %s 已停止", inst.Name)
				}
			case errors.Is(err, daemon.ErrNotRunning):
				app.Warn("实例 %s 本来就没有在运行", inst.Name)
			default:
				failures = append(failures, fmt.Sprintf("%s: %v", inst.Name, err))
				app.Fail("停止实例 %s 失败：%v", inst.Name, err)
			}
		}
		if len(failures) > 0 {
			return fmt.Errorf("有 %d 个实例停止失败", len(failures))
		}
		return nil
	},
}

var (
	stopForce          bool
	stopTimeout        time.Duration
	stopKeepSupervisor bool
	stopAll            bool
)

func init() {
	startCmd.Flags().BoolVarP(&startForeground, "foreground", "f", false, "前台运行（输出到终端，Ctrl+C 优雅关闭）")
	startCmd.Flags().BoolVar(&startWait, "wait", false, "等到服务端就绪再返回")
	startCmd.Flags().DurationVar(&startTimeout, "timeout", 0, "配合 --wait 的等待上限（默认取 server.startup_timeout）")
	startCmd.Flags().BoolVar(&startWatchdog, "watchdog", false, "强制开启崩溃自动重启")
	startCmd.Flags().BoolVar(&startNoWatchdog, "no-watchdog", false, "本次启动关闭崩溃自动重启")
	startCmd.Flags().BoolVar(&startNoStart, "no-start", false, "只启动守护进程，不拉起服务端")
	startCmd.Flags().BoolVar(&startAll, "all", false, "对所有实例执行")

	stopCmd.Flags().BoolVarP(&stopForce, "force", "f", false, "直接强制结束，不走优雅关闭")
	stopCmd.Flags().DurationVar(&stopTimeout, "timeout", 0, "优雅关闭的等待上限")
	stopCmd.Flags().BoolVar(&stopKeepSupervisor, "keep-supervisor", false, "只停服务端，保留守护进程")
	stopCmd.Flags().BoolVar(&stopAll, "all", false, "对所有实例执行")

	rootCmd.AddCommand(startCmd, stopCmd)
}

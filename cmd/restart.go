package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/yuzumikonami/nyatmc/internal/daemon"
)

var (
	restartForce   bool
	restartDelay   time.Duration
	restartWait    bool
	restartTimeout time.Duration
	restartAll     bool
)

var restartCmd = &cobra.Command{
	Use:   "restart [实例名]",
	Short: "重启实例",
	Long: `重启实例：先优雅停止服务端，等待 restart_delay（默认 2s）后重新启动。

实例本来没在运行时会直接启动它，方便脚本里“确保在运行”。`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		instances, err := app.resolveInstances(args, restartAll)
		if err != nil {
			return err
		}

		var failures []string
		for _, inst := range instances {
			app.Out("正在重启实例 %s …", inst.Name)
			st, err := daemon.Restart(app.Ctx, app.Cfg, inst, daemon.RestartOptions{
				Force:       restartForce,
				Delay:       restartDelay,
				Wait:        restartWait,
				WaitTimeout: restartTimeout,
			})
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", inst.Name, err))
				app.Fail("实例 %s 重启失败：%v", inst.Name, err)
				continue
			}
			if st.Ready {
				app.OK("实例 %s 已重启并就绪（pid %d）", inst.Name, st.PID)
			} else {
				app.OK("实例 %s 重启中（状态 %s）", inst.Name, st.State.Label())
			}
		}
		if len(failures) > 0 {
			return fmt.Errorf("有 %d 个实例重启失败", len(failures))
		}
		return nil
	},
}

func init() {
	restartCmd.Flags().BoolVarP(&restartForce, "force", "f", false, "强制结束而不是优雅关闭")
	restartCmd.Flags().DurationVar(&restartDelay, "delay", 0, "停止后等待多久再启动（默认 2s）")
	restartCmd.Flags().BoolVar(&restartWait, "wait", false, "等到服务端再次就绪")
	restartCmd.Flags().DurationVar(&restartTimeout, "timeout", 0, "配合 --wait 的等待上限")
	restartCmd.Flags().BoolVar(&restartAll, "all", false, "对所有实例执行")
	rootCmd.AddCommand(restartCmd)
}

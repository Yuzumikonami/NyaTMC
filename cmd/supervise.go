package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yuzumikonami/nyatmc/internal/daemon"
)

var (
	supNoStart  bool
	supAttach   bool
	supWatchdog bool
)

var superviseCmd = &cobra.Command{
	Use:    "__supervise",
	Short:  "内部命令：守护一个实例（由 nyatmc start 自动派生）",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		name := flagInstance
		if name == "" {
			name = app.Cfg.General.DefaultInstance
		}
		if name == "" {
			return fmt.Errorf("缺少 --instance")
		}

		opts := daemon.SupervisorOptions{NoStart: supNoStart, Attach: supAttach}
		if cmd.Flags().Changed("watchdog") {
			v := supWatchdog
			opts.Watchdog = &v
		}
		if supAttach {
			// 前台运行时日志直接打到终端
			opts.Logger = app.Log
		}
		// 后台运行时留空，让 RunSupervisor 把日志写进实例的 supervisor.log，
		// 否则脱离终端后标准错误指向空设备，出错时什么都看不到。
		return daemon.RunSupervisor(app.Ctx, app.Cfg.Path, name, opts)
	},
}

func init() {
	superviseCmd.Flags().BoolVar(&supNoStart, "no-start", false, "只启动守护，不拉起服务端进程")
	superviseCmd.Flags().BoolVar(&supAttach, "attach", false, "前台模式：输出镜像到终端并接受键盘输入")
	superviseCmd.Flags().BoolVar(&supWatchdog, "watchdog", false, "覆盖看门狗开关")
	rootCmd.AddCommand(superviseCmd)
}

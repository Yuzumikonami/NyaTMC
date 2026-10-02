package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yuzumikonami/nyatmc/internal/backup"
	"github.com/yuzumikonami/nyatmc/internal/daemon"
)

var (
	restoreYes       bool
	restoreTargets   []string
	restoreNoBackup  bool
	restoreNoRestart bool
)

var restoreCmd = &cobra.Command{
	Use:   "restore [实例名] [备份名|latest]",
	Short: "回滚到某个备份",
	Long: `回滚（恢复）实例到指定备份。

用法示例：
  nyatmc restore                    回滚默认实例到最新备份
  nyatmc restore survival latest    回滚实例 survival 到最新备份
  nyatmc restore survival survival-auto-20260101-040000.tar.gz
  nyatmc restore --target world     只回滚世界目录

安全策略：
  - 实例正在运行时会先优雅停止，回滚完成后自动重新启动（用 --no-restart 关闭）；
  - 默认在回滚前自动备份当前状态（用 --no-backup 关闭），万一回滚错了还能救回来；
  - 备份包里的路径都会被校验，绝不允许写到服务器目录之外。`,
	Args: cobra.MaximumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}

		// 参数可以是 [实例名] [备份名]，也可以是 [备份名]
		var instArg, ref string
		switch len(args) {
		case 1:
			if app.Cfg.HasInstance(args[0]) {
				instArg = args[0]
			} else {
				ref = args[0]
			}
		case 2:
			instArg, ref = args[0], args[1]
		}
		instanceHandle, err := app.resolveInstance(nilIfEmpty(instArg))
		if err != nil {
			return err
		}

		ar, err := backup.Find(instanceHandle, ref)
		if err != nil {
			return err
		}

		app.Out("将把实例 %s 回滚到：", instanceHandle.Name)
		app.Out("  %s（%s，%s）", ar.Name, ar.HumanSize(), humanTime(ar.CreatedAt))
		if len(restoreTargets) > 0 {
			app.Out("  只回滚：%s", strings.Join(restoreTargets, "、"))
		} else {
			app.Out("  回滚整包内容（服务器目录内会被覆盖）")
		}

		running := daemon.Running(instanceHandle)
		if running {
			app.Warn("实例正在运行，将先优雅停止，回滚完成后再启动")
		}
		if !restoreYes {
			if !confirm(app, "确认回滚？") {
				app.Out("已取消。")
				return nil
			}
		}

		wasRunning := running
		if wasRunning {
			app.Out("正在停止实例 %s …", instanceHandle.Name)
			if err := daemon.Stop(app.Ctx, app.Cfg, instanceHandle, daemon.StopOptions{}); err != nil && err != daemon.ErrNotRunning {
				return fmt.Errorf("停止实例失败（未开始回滚）：%w", err)
			}
		}

		opts := backup.RestoreOptions{
			BeforeBackup: !restoreNoBackup && instanceHandle.Res.Backup.BeforeRestoreEnabled(),
			Targets:      restoreTargets,
			Logger:       app.Log,
		}
		result, err := backup.Restore(instanceHandle, ar.Path, opts)
		if err != nil {
			if wasRunning && !restoreNoRestart {
				app.Warn("回滚失败，正在尝试把服务器重新启动…")
				_, _ = daemon.Start(app.Ctx, app.Cfg, instanceHandle, daemon.StartOptions{})
			}
			return err
		}

		if result.Backup != nil {
			app.Out("回滚前已自动备份当前状态：%s", result.Backup.Name)
		}
		if app.JSON {
			return app.JSONOut(map[string]any{
				"instance": instanceHandle.Name,
				"archive":  ar.Name,
				"files":    result.Files,
				"bytes":    result.Bytes,
				"backup":   result.Backup,
			})
		}

		app.OK("回滚完成：%d 个文件，%s", result.Files, backup.HumanSize(result.Bytes))

		if wasRunning && !restoreNoRestart {
			app.Out("正在重新启动实例 %s …", instanceHandle.Name)
			st, err := daemon.Start(app.Ctx, app.Cfg, instanceHandle, daemon.StartOptions{Wait: true, WaitTimeout: 5 * time.Minute})
			if err != nil {
				return fmt.Errorf("回滚成功，但重新启动失败：%w", err)
			}
			if st.Ready {
				app.OK("实例 %s 已重新启动并就绪（pid %d）", instanceHandle.Name, st.PID)
			} else {
				app.OK("实例 %s 已重新启动（状态 %s）", instanceHandle.Name, st.State.Label())
			}
		}
		return nil
	},
}

// nilIfEmpty 把空字符串转成 nil 切片，便于 resolveInstance 走默认实例。
func nilIfEmpty(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return []string{s}
}

func init() {
	restoreCmd.Flags().BoolVarP(&restoreYes, "yes", "y", false, "跳过确认")
	restoreCmd.Flags().StringSliceVar(&restoreTargets, "target", nil, "只回滚这些路径（相对服务器目录，可重复或用逗号分隔）")
	restoreCmd.Flags().BoolVar(&restoreNoBackup, "no-backup", false, "回滚前不自动备份当前状态（不推荐）")
	restoreCmd.Flags().BoolVar(&restoreNoRestart, "no-restart", false, "回滚后不要自动重启实例")
	rootCmd.AddCommand(restoreCmd)
}

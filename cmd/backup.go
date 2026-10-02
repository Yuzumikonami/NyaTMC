package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/yuzumikonami/nyatmc/internal/backup"
	"github.com/yuzumikonami/nyatmc/internal/daemon"
	"github.com/yuzumikonami/nyatmc/internal/instance"
)

var (
	backupLabel   string
	backupKeep    int
	backupAll     bool
	backupNoPrune bool
)

var backupCmd = &cobra.Command{
	Use:   "backup [实例名]",
	Short: "立即备份实例（打包世界与配置，可选自动清理旧备份）",
	Long: `备份实例。

打包内容来自配置里的 backup.include（默认包含世界目录、server.properties、
mods/plugins、各类权限文件等），排除 backup.exclude。

如果服务器正在运行：
  - 默认先执行 save-all flush，并把最新的世界数据刷盘后再打包；
  - 打开 backup.stop_before_backup 会先 save-off 暂停写入、备份完成后 save-on，
    这样能得到完全一致的世界快照。

备份文件默认放在实例目录的 backups/ 下，形如 survival-manual-20260101-040000.tar.gz。`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		instances, err := app.resolveInstances(args, backupAll)
		if err != nil {
			return err
		}

		var archives []backup.Archive
		var failures []string
		for _, inst := range instances {
			ar, err := runBackup(app, cmd, inst)
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", inst.Name, err))
				app.Fail("实例 %s 备份失败：%v", inst.Name, err)
				continue
			}
			archives = append(archives, ar)
			if !app.JSON {
				app.OK("实例 %s 备份完成：%s（%s）", inst.Name, ar.Name, ar.HumanSize())
			}
		}

		if app.JSON {
			if err := app.JSONOut(archives); err != nil {
				return err
			}
		}
		if len(failures) > 0 {
			return fmt.Errorf("有 %d 个实例备份失败", len(failures))
		}
		return nil
	},
}

var backupListCmd = &cobra.Command{
	Use:   "list [实例名]",
	Short: "列出已有备份",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		instances, err := app.resolveInstances(args, backupAll)
		if err != nil {
			return err
		}
		if app.JSON {
			out := map[string][]backup.Archive{}
			for _, inst := range instances {
				list, _ := backup.List(inst)
				out[inst.Name] = list
			}
			return app.JSONOut(out)
		}
		for _, inst := range instances {
			list, err := backup.List(inst)
			if err != nil {
				return err
			}
			app.Out("实例 %s（%s）", inst.Name, inst.Res.BackupDir)
			if len(list) == 0 {
				app.Out("  还没有备份")
				continue
			}
			rows := make([][]string, 0, len(list))
			for _, a := range list {
				rows = append(rows, []string{a.Name, a.HumanSize(), humanTime(a.CreatedAt), a.Label, a.Format})
			}
			app.Table([]string{"文件名", "大小", "创建时间", "来源", "格式"}, rows)
		}
		return nil
	},
}

var backupPruneCmd = &cobra.Command{
	Use:   "prune [实例名]",
	Short: "按配置清理旧备份（backup.keep / backup.max_age）",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		instances, err := app.resolveInstances(args, backupAll)
		if err != nil {
			return err
		}
		keep := backupKeep
		if !cmd.Flags().Changed("keep") {
			keep = -1
		}
		total := 0
		for _, inst := range instances {
			k := keep
			if k < 0 {
				k = inst.Res.Backup.Keep
			}
			removed, err := backup.Prune(inst, k, inst.Res.Backup.MaxAge.Std(), app.Log)
			if err != nil {
				return err
			}
			total += len(removed)
			if !app.JSON {
				app.Out("实例 %s：清理 %d 份旧备份（保留 %d 份）", inst.Name, len(removed), k)
			}
			for _, p := range removed {
				app.Log.Debugf("已删除 %s", p)
			}
		}
		if app.JSON {
			return app.JSONOut(map[string]any{"removed": total})
		}
		app.OK("共清理 %d 份旧备份", total)
		return nil
	},
}

// runBackup 执行一次带一致性处理的备份。
func runBackup(app *App, cmd *cobra.Command, inst *instance.Instance) (backup.Archive, error) {
	label := backupLabel
	if label == "" {
		label = "manual"
	}

	running := daemon.Running(inst)
	saveOffDone := false
	if running {
		if inst.Res.Backup.StopBeforeBackupEnabled() {
			if err := daemon.SendCommand(app.Ctx, inst, "save-off"); err == nil {
				saveOffDone = true
			} else {
				app.Warn("发送 save-off 失败，继续备份：%v", err)
			}
			if err := daemon.SendCommand(app.Ctx, inst, "save-all flush"); err != nil {
				app.Warn("发送 save-all 失败：%v", err)
			}
			// 等世界落盘
			time.Sleep(3 * time.Second)
		} else {
			if err := daemon.SendCommand(app.Ctx, inst, "save-all flush"); err != nil {
				app.Warn("发送 save-all 失败（将继续备份）：%v", err)
			} else {
				app.Out("已请求服务端 save-all flush 并等待落盘…")
				time.Sleep(3 * time.Second)
			}
		}
	}
	if saveOffDone {
		defer func() {
			if err := daemon.SendCommand(app.Ctx, inst, "save-on"); err != nil {
				app.Warn("恢复写入失败（save-on），请手动执行）：%v", err)
			}
		}()
	}

	ar, err := backup.CreateDefault(inst, label, app.Log)
	if err != nil {
		return ar, err
	}

	if !backupNoPrune {
		keep := inst.Res.Backup.Keep
		if cmd.Flags().Changed("keep") {
			keep = backupKeep
		}
		if _, err := backup.Prune(inst, keep, inst.Res.Backup.MaxAge.Std(), app.Log); err != nil {
			app.Warn("清理旧备份失败：%v", err)
		}
	}
	return ar, nil
}

func init() {
	backupCmd.Flags().StringVar(&backupLabel, "label", "", "备份来源标签（默认 manual，会写进文件名）")
	backupCmd.Flags().IntVar(&backupKeep, "keep", 0, "本次备份后保留的份数（默认按配置 backup.keep）")
	backupCmd.Flags().BoolVar(&backupNoPrune, "no-prune", false, "备份后不清理旧备份")
	backupCmd.Flags().BoolVar(&backupAll, "all", false, "对所有实例执行")

	backupListCmd.Flags().BoolVar(&backupAll, "all", false, "列出所有实例的备份")
	backupPruneCmd.Flags().IntVar(&backupKeep, "keep", 0, "保留的份数（默认按配置 backup.keep）")
	backupPruneCmd.Flags().BoolVar(&backupAll, "all", false, "对所有实例执行")

	backupCmd.AddCommand(backupListCmd, backupPruneCmd)
	rootCmd.AddCommand(backupCmd)
}

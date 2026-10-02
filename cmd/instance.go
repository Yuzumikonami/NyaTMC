package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yuzumikonami/nyatmc/internal/backup"
	"github.com/yuzumikonami/nyatmc/internal/config"
	"github.com/yuzumikonami/nyatmc/internal/daemon"
	"github.com/yuzumikonami/nyatmc/internal/instance"
)

// ---------------------------------------------------------------- create

var (
	createType       string
	createVersion    string
	createBuild      string
	createLoader     string
	createInstaller  string
	createMemory     string
	createPort       int
	createPath       string
	createDesc       string
	createEULA       bool
	createJava       string
	createNoDownload bool
	createStart      bool
)

var createCmd = &cobra.Command{
	Use:   "create <实例名>",
	Short: "创建一个新实例（多实例隔离）",
	Long: `创建一个新实例。每个实例有独立的服务器目录、端口、内存与备份目录：

  ~/.nyatmc/instances/<名字>/
  ├── .nyatmc/     运行状态、控制端点、控制台日志
  ├── backups/     备份归档
  └── server/      Minecraft 服务端工作目录

用 --path 可以把实例指向一个已存在的服务器目录（nyatmc 只接管启停与备份，
不会移动或删除你的文件）。`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		name := args[0]
		if err := config.ValidateInstanceName(name); err != nil {
			return err
		}

		inst, err := instance.Create(app.Ctx, app.Cfg, name, instance.CreateOptions{
			Type:             createType,
			MinecraftVersion: createVersion,
			Build:            createBuild,
			LoaderVersion:    createLoader,
			InstallerVersion: createInstaller,
			Memory:           createMemory,
			Port:             createPort,
			JavaPath:         createJava,
			Description:      createDesc,
			Path:             createPath,
			EULA:             createEULA,
			Logger:           app.Log,
		})
		if err != nil {
			return err
		}

		if app.JSON {
			meta, _ := inst.LoadMetadata()
			return app.JSONOut(map[string]any{
				"instance": inst.Name,
				"path":     inst.Res.Path,
				"type":     inst.Res.Server.Type,
				"version":  inst.Res.Server.MinecraftVersion,
				"port":     inst.Res.Server.Port,
				"memory":   inst.Res.Server.Memory,
				"metadata": meta,
			})
		}

		app.OK("实例 %s 已创建", inst.Name)
		app.Out("  服务器目录 %s", inst.Res.Path)
		app.Out("  类型/版本  %s / %s", inst.Res.Server.Type, inst.Res.Server.MinecraftVersion)
		app.Out("  端口/内存  %d / %s", inst.Res.Server.Port, inst.Res.Server.Memory)

		if !createEULA {
			app.Out("")
			ensureEULA(app, inst)
		}

		if createStart {
			app.Out("")
			app.Out("正在启动实例 %s …", inst.Name)
			st, err := daemon.Start(app.Ctx, app.Cfg, inst, daemon.StartOptions{Wait: true, WaitTimeout: 0})
			if err != nil {
				return err
			}
			if st.Ready {
				app.OK("实例 %s 已就绪（pid %d）", inst.Name, st.PID)
			} else {
				app.OK("实例 %s 已在后台启动（状态 %s）", inst.Name, st.State.Label())
			}
			return nil
		}

		app.Out("")
		app.Out("下一步：nyatmc start %s   （首次启动会自动下载服务端文件）", inst.Name)
		return nil
	},
}

// ---------------------------------------------------------------- list

var listJSON bool

var listCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "列出所有实例及其状态",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		list, unregistered, err := instance.Discover(app.Cfg)
		if err != nil {
			return err
		}
		if len(list) == 0 && len(unregistered) == 0 {
			app.Out("还没有任何实例。运行 nyatmc init 或 nyatmc create <名字> 开始使用。")
			return nil
		}

		ctx := app.Ctx
		rows := make([][]string, 0, len(list))
		statuses := make([]daemon.Status, 0, len(list))
		for _, inst := range list {
			st, _ := daemon.GetStatus(ctx, app.Cfg, inst)
			statuses = append(statuses, st)
			players := "-"
			if st.PlayerCount() > 0 {
				players = fmt.Sprintf("%d/%d", st.PlayerCount(), st.MaxPlayers)
			}
			pid := "-"
			if st.PID > 0 {
				pid = fmt.Sprintf("%d", st.PID)
			}
			def := ""
			if inst.Name == app.Cfg.General.DefaultInstance {
				def = "*"
			}
			rows = append(rows, []string{
				def + inst.Name,
				app.StateLabel(st.State),
				pid,
				HumanDuration(st.Uptime()),
				fmt.Sprintf("%s %s", inst.Res.Server.Type, inst.Res.Server.MinecraftVersion),
				fmt.Sprintf("%d", inst.Res.Server.Port),
				players,
			})
		}

		if app.JSON || listJSON {
			type item struct {
				Name     string        `json:"name"`
				Default  bool          `json:"default"`
				Type     string        `json:"type"`
				Version  string        `json:"version"`
				Port     int           `json:"port"`
				Memory   string        `json:"memory"`
				Path     string        `json:"path"`
				Backups  int           `json:"backups"`
				JarReady bool          `json:"jar_ready"`
				Enabled  bool          `json:"enabled"`
				Status   daemon.Status `json:"status"`
			}
			out := make([]item, 0, len(list))
			for i, inst := range list {
				ars, _ := backup.List(inst)
				out = append(out, item{
					Name:     inst.Name,
					Default:  inst.Name == app.Cfg.General.DefaultInstance,
					Type:     inst.Res.Server.Type,
					Version:  inst.Res.Server.MinecraftVersion,
					Port:     inst.Res.Server.Port,
					Memory:   inst.Res.Server.Memory,
					Path:     inst.Res.Path,
					Backups:  len(ars),
					JarReady: inst.JarExists(),
					Enabled:  inst.Res.Enabled,
					Status:   statuses[i],
				})
			}
			return app.JSONOut(out)
		}

		app.Table([]string{"实例", "状态", "PID", "运行时长", "类型/版本", "端口", "在线"}, rows)
		if len(unregistered) > 0 {
			app.Out("")
			app.Warn("发现未登记的实例目录：%s（用 nyatmc adopt <名字> 纳管，或手动确认后删除）", strings.Join(unregistered, "、"))
		}
		app.Out("\n（* 表示默认实例；用 nyatmc use <名字> 切换默认实例）")
		return nil
	},
}

// ---------------------------------------------------------------- adopt

var adoptPath string

var adoptCmd = &cobra.Command{
	Use:   "adopt <实例名>",
	Short: "把已有服务器目录纳管为一个实例",
	Long: `把已经存在（或手工拷贝进来）的服务器目录纳管为 nyatmc 实例。

默认接管 ~/.nyatmc/instances/<名字>/server；用 --path 指定其它目录。
不会移动或删除任何文件，只写入 nyatmc 自己的元数据与基础配置文件。`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		name := args[0]
		if app.Cfg.HasInstance(name) {
			return fmt.Errorf("实例 %q 已登记，无需纳管", name)
		}
		if err := config.ValidateInstanceName(name); err != nil {
			return err
		}

		inst, err := instance.Create(app.Ctx, app.Cfg, name, instance.CreateOptions{
			Path:   adoptPath,
			EULA:   false,
			Logger: app.Log,
		})
		if err != nil {
			return err
		}
		if app.JSON {
			return app.JSONOut(map[string]any{"instance": inst.Name, "path": inst.Res.Path})
		}
		app.OK("已纳管实例 %s，服务器目录 %s", inst.Name, inst.Res.Path)
		if !inst.JarExists() {
			app.Warn("该目录下没有 %s，启动时会按配置自动下载，或用 nyatmc server download %s 手动获取", inst.Res.Server.JarName, inst.Name)
		}
		return nil
	},
}

// ---------------------------------------------------------------- delete

var (
	deletePurge       bool
	deleteKeepBackups bool
	deleteYes         bool
)

var deleteCmd = &cobra.Command{
	Use:     "delete <实例名>",
	Aliases: []string{"rm", "remove"},
	Short:   "删除实例（可选连同文件一起删除）",
	Long: `删除实例。

默认只从配置里注销（文件保留，方便手工处理）；加 --purge 会删除
~/.nyatmc/instances/<名字>/ 整个目录。用 --path 指向外部目录的实例，
其服务器目录永远不会被自动删除。`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		name := args[0]
		inst, err := app.resolveInstance([]string{name})
		if err != nil {
			return err
		}
		if daemon.Running(inst) {
			return fmt.Errorf("实例 %s 正在运行，请先 nyatmc stop %s", name, name)
		}

		if deletePurge && !deleteYes {
			msg := fmt.Sprintf("将永久删除 %s（含服务器目录、世界存档与备份）", inst.Res.Root)
			app.Warn("%s", msg)
			if !confirm(app, "确认删除请输入 yes") {
				app.Out("已取消。")
				return nil
			}
		}

		if err := instance.Delete(app.Cfg, name, instance.DeleteOptions{
			Purge:       deletePurge,
			KeepBackups: deleteKeepBackups,
		}); err != nil {
			return err
		}
		if app.JSON {
			return app.JSONOut(map[string]any{"instance": name, "purged": deletePurge})
		}
		if deletePurge {
			app.OK("实例 %s 已删除（文件已清理）", name)
		} else {
			app.OK("实例 %s 已注销（文件保留在 %s）", name, inst.Res.Root)
		}
		return nil
	},
}

// ---------------------------------------------------------------- use

var useCmd = &cobra.Command{
	Use:   "use <实例名>",
	Short: "设置默认实例（后续命令省略实例名时生效）",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		name := args[0]
		inst, err := app.resolveInstance([]string{name})
		if err != nil {
			return err
		}
		changed := func(c *config.Config) error {
			c.General.DefaultInstance = inst.Name
			return nil
		}
		if err := changed(app.Cfg); err != nil {
			return err
		}
		if err := app.Cfg.Save(); err != nil {
			return err
		}
		if app.JSON {
			return app.JSONOut(map[string]any{"default_instance": inst.Name})
		}
		app.OK("默认实例已切换为 %s", inst.Name)
		return nil
	},
}

// ---------------------------------------------------------------- info

var infoCmd = &cobra.Command{
	Use:   "info [实例名]",
	Short: "显示实例的详细信息（配置、目录、备份、服务端文件）",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newAppInstance(cmd, args)
		if err != nil {
			return err
		}
		inst := app.Inst
		meta, _ := inst.LoadMetadata()
		ars, _ := backup.List(inst)
		st, _ := daemon.GetStatus(app.Ctx, app.Cfg, inst)

		var jarSize int64
		if info, err := os.Stat(inst.Res.JarPath); err == nil {
			jarSize = info.Size()
		}

		if app.JSON {
			return app.JSONOut(map[string]any{
				"name":     inst.Name,
				"metadata": meta,
				"resolved": inst.Res,
				"status":   st,
				"jar": map[string]any{
					"path":    inst.Res.JarPath,
					"exists":  inst.JarExists(),
					"size":    jarSize,
					"size_h":  backup.HumanSize(jarSize),
				},
				"backups": map[string]any{
					"count": len(ars),
					"dir":   inst.Res.BackupDir,
				},
				"dirs": map[string]string{
					"root":    inst.Res.Root,
					"server":  inst.Res.Path,
					"meta":    inst.Res.MetaDir,
					"backups": inst.Res.BackupDir,
					"logs":    inst.Res.LogsDir,
				},
			})
		}

		app.Out("实例 %s", inst.Name)
		mark := func(k, v string) { app.Out("  %-12s %s", k, v) }
		mark("状态", app.StateLabel(st.State)+readySuffix(st))
		mark("类型/版本", fmt.Sprintf("%s / %s", inst.Res.Server.Type, inst.Res.Server.MinecraftVersion))
		if meta.LoaderVersion != "" {
			mark("Fabric", fmt.Sprintf("loader %s / installer %s", meta.LoaderVersion, meta.InstallerVersion))
		}
		mark("端口/内存", fmt.Sprintf("%d / %s（最小 %s）", inst.Res.Server.Port, inst.Res.Server.Memory, inst.Res.Server.MinMemory))
		mark("Java", inst.Res.Server.JavaPath)
		mark("EULA", map[bool]string{true: "已确认", false: "未确认（服务端会拒绝启动）"}[inst.Res.Server.EULAEnabled()])
		mark("看门狗", onOff(inst.Res.Daemon.WatchdogEnabled()))
		mark("服务端文件", fmt.Sprintf("%s（%s）", jaroLabel(inst.JarExists()), backup.HumanSize(jarSize)))
		mark("服务器目录", inst.Res.Path)
		mark("备份目录", fmt.Sprintf("%s（%d 份）", inst.Res.BackupDir, len(ars)))
		mark("元数据目录", inst.Res.MetaDir)
		mark("创建时间", humanTime(meta.CreatedAt))
		if meta.Description != "" {
			mark("备注", meta.Description)
		}
		if len(ars) > 0 {
			app.Out("\n最近备份：")
			limit := len(ars)
			if limit > 5 {
				limit = 5
			}
			for _, a := range ars[:limit] {
				app.Out("  %s  %s  %s", humanTime(a.CreatedAt), a.HumanSize(), a.Name)
			}
		}
		return nil
	},
}

func jaroLabel(exists bool) string {
	if exists {
		return "已就绪"
	}
	return "缺失（启动时自动下载）"
}

func init() {
	createCmd.Flags().StringVar(&createType, "type", "", "服务端类型：paper/fabric/vanilla/spigot/purpur")
	createCmd.Flags().StringVar(&createVersion, "version", "", "Minecraft 版本，例如 1.21.4（默认 latest）")
	createCmd.Flags().StringVar(&createBuild, "build", "", "Paper/Purpur 构建号（默认最新）")
	createCmd.Flags().StringVar(&createLoader, "loader-version", "", "Fabric loader 版本（默认最新）")
	createCmd.Flags().StringVar(&createInstaller, "installer-version", "", "Fabric installer 版本（默认最新）")
	createCmd.Flags().StringVar(&createMemory, "memory", "", "最大内存，例如 2G")
	createCmd.Flags().IntVar(&createPort, "port", 0, "服务端端口（默认 25565）")
	createCmd.Flags().StringVar(&createPath, "path", "", "已存在的服务器目录（留空则用实例目录下的 server）")
	createCmd.Flags().StringVar(&createDesc, "description", "", "实例备注")
	createCmd.Flags().BoolVar(&createEULA, "eula", false, "确认已阅读并同意 Minecraft EULA")
	createCmd.Flags().StringVar(&createJava, "java", "", "java 可执行文件路径")
	createCmd.Flags().BoolVar(&createNoDownload, "no-download", false, "创建时不下载服务端（等首次启动再下载）")
	createCmd.Flags().BoolVar(&createStart, "start", false, "创建后立即启动并等待就绪")

	listCmd.Flags().BoolVar(&listJSON, "json", false, "以 JSON 输出")

	adoptCmd.Flags().StringVar(&adoptPath, "path", "", "要纳管的服务器目录（默认 ~/.nyatmc/instances/<名字>/server）")

	deleteCmd.Flags().BoolVar(&deletePurge, "purge", false, "连同实例目录一起删除（不可恢复）")
	deleteCmd.Flags().BoolVar(&deleteKeepBackups, "keep-backups", false, "配合 --purge：把备份移到 ~/.nyatmc/backups-keep/ 保留")
	deleteCmd.Flags().BoolVarP(&deleteYes, "yes", "y", false, "跳过确认")

	rootCmd.AddCommand(createCmd, listCmd, adoptCmd, deleteCmd, useCmd, infoCmd)
}

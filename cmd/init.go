package cmd

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/yuzumikonami/nyatmc/internal/config"
	"github.com/yuzumikonami/nyatmc/internal/instance"
)

var (
	initForce      bool
	initEULA       bool
	initType       string
	initVersion    string
	initMemory     string
	initPort       int
	initName       string
	initNoInstance bool
	initJavaPath   string
)

var initCmd = &cobra.Command{
	Use:   "init [数据目录]",
	Short: "初始化 nyatmc（创建数据目录、配置文件与默认实例）",
	Long: `初始化 nyatmc。

会做三件事：
  1. 创建数据目录（默认 ~/.nyatmc，可用参数或 --home / NYATMC_HOME 指定）；
  2. 生成带中文注释的配置文件 config.toml（已存在则不动）；
  3. 创建一个默认实例（默认名字 default），并写好 server.properties、eula.txt 与启动脚本。

服务端 jar 会在 nyatmc start 时按需自动下载，也可以先手动执行 nyatmc server download。`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 && strings.TrimSpace(flagHome) == "" {
			config.SetHome(args[0])
		}
		app, err := newApp(cmd)
		if err != nil {
			return err
		}

		created := app.Cfg.Created
		app.Out("数据目录  %s", config.Home())
		suffix := ""
		if created {
			suffix = "（已新建）"
		}
		app.Out("配置文件  %s%s", app.Cfg.Path, suffix)

		for _, w := range app.Cfg.Warnings() {
			app.Warn("%s", w)
		}

		name := strings.TrimSpace(initName)
		if name == "" {
			name = app.Cfg.General.DefaultInstance
		}
		if name == "" {
			name = "default"
		}

		result := map[string]any{
			"home":         config.Home(),
			"config":       app.Cfg.Path,
			"config_new":   created,
			"instance":     "",
			"instance_dir": "",
		}

		if initNoInstance {
			if app.JSON {
				return app.JSONOut(result)
			}
			app.Out("\n已跳过实例创建（--no-instance）。下一步：nyatmc create <名字>")
			return nil
		}

		if app.Cfg.HasInstance(name) && !initForce {
			if !app.JSON {
				app.Warn("实例 %s 已存在，未做改动（用 --force 可覆盖其基础配置）", name)
				app.Out("\n下一步：nyatmc start %s", name)
			}
			result["instance"] = name
			inst, _ := instance.New(app.Cfg, name)
			if inst != nil {
				result["instance_dir"] = inst.Res.Path
			}
			if app.JSON {
				return app.JSONOut(result)
			}
			return nil
		}
		if app.Cfg.HasInstance(name) && initForce {
			inst, err := instance.New(app.Cfg, name)
			if err != nil {
				return err
			}
			if err := inst.ApplyProperties(); err != nil {
				return err
			}
			if err := inst.WriteEULA(initEULA); err != nil {
				return err
			}
			if err := inst.WriteStartScripts(); err != nil {
				return err
			}
			app.OK("已按当前配置刷新实例 %s 的基础文件", name)
			result["instance"] = name
			result["instance_dir"] = inst.Res.Path
			if app.JSON {
				return app.JSONOut(result)
			}
			return nil
		}

		inst, err := instance.Create(app.Ctx, app.Cfg, name, instance.CreateOptions{
			Type:             initType,
			MinecraftVersion: initVersion,
			Memory:           initMemory,
			Port:             initPort,
			JavaPath:         initJavaPath,
			EULA:             initEULA,
			Logger:           app.Log,
		})
		if err != nil {
			return err
		}

		result["instance"] = inst.Name
		result["instance_dir"] = inst.Res.Path
		if app.JSON {
			return app.JSONOut(result)
		}

		app.OK("初始化完成")
		app.Out("")
		app.Out("实例       %s", inst.Name)
		app.Out("服务器目录 %s", inst.Res.Path)
		app.Out("备份目录   %s", inst.Res.BackupDir)
		app.Out("")
		app.Out("下一步：")
		app.Out("  1. 按需修改配置：nyatmc config edit          （或 nyatmc config show）")
		app.Out("  2. 启动服务器：  nyatmc start %s             （会自动下载服务端）", inst.Name)
		app.Out("  3. 查看看板：    nyatmc tui")
		app.Out("  4. 打开网页：    nyatmc web serve")
		return nil
	},
}

func init() {
	initCmd.Flags().BoolVar(&initForce, "force", false, "实例已存在时，用当前配置刷新其基础文件")
	initCmd.Flags().BoolVar(&initEULA, "eula", false, "确认已阅读并同意 Minecraft EULA（写入 eula.txt）")
	initCmd.Flags().StringVar(&initType, "type", "", "服务端类型：paper/fabric/vanilla/spigot/purpur")
	initCmd.Flags().StringVar(&initVersion, "version", "", "Minecraft 版本，例如 1.21.4（默认 latest）")
	initCmd.Flags().StringVar(&initMemory, "memory", "", "最大内存，例如 2G")
	initCmd.Flags().IntVar(&initPort, "port", 0, "服务端端口（默认 25565）")
	initCmd.Flags().StringVar(&initName, "name", "", "默认实例名（默认 default）")
	initCmd.Flags().StringVar(&initJavaPath, "java", "", "java 可执行文件路径（默认 java）")
	initCmd.Flags().BoolVar(&initNoInstance, "no-instance", false, "只写配置文件，不创建实例")
	rootCmd.AddCommand(initCmd)
}

// ensureEULA 提示用户 EULA 未确认（创建/启动前的统一提醒）。
func ensureEULA(app *App, inst *instance.Instance) {
	if inst.Res.Server.EULAEnabled() {
		return
	}
	app.Warn("实例 %s 尚未确认 Minecraft EULA，服务端会拒绝启动", inst.Name)
	app.Out("  确认方式：nyatmc config set server.eula true    或    nyatmc create %s --eula", inst.Name)
	app.Out("  协议原文：https://aka.ms/MinecraftEULA")
}

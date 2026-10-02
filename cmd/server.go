package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yuzumikonami/nyatmc/internal/backup"
	"github.com/yuzumikonami/nyatmc/internal/config"
	"github.com/yuzumikonami/nyatmc/internal/daemon"
	"github.com/yuzumikonami/nyatmc/internal/downloader"
	"github.com/yuzumikonami/nyatmc/internal/serverjar"
)

var (
	serverType       string
	serverVersion    string
	serverBuild      string
	serverLoader     string
	serverInstaller  string
	serverMirror     string
	serverForce      bool
	serverUpdate     bool
	serverNoVerify   bool
	serverKindList   string
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "服务端文件管理（下载、查看、版本列表）",
	Long: `管理实例的服务端文件。

nyatmc start 在发现服务端文件缺失时会自动下载（server.auto_download = true），
这个命令用于手动获取、升级或查看当前安装的版本：

  nyatmc server download                给默认实例补齐服务端文件
  nyatmc server download --version 1.21.4
  nyatmc server download --update       检查并升级到最新构建
  nyatmc server info                    查看本地已安装的服务端与版本
  nyatmc server versions paper          查看可用的 Minecraft 版本
  nyatmc server builds paper            查看可用的构建号`,
}

var serverDownloadCmd = &cobra.Command{
	Use:   "download [实例名]",
	Aliases: []string{"install", "get"},
	Short: "下载或更新服务端文件",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newAppInstance(cmd, args)
		if err != nil {
			return err
		}
		inst := app.Inst

		// 允许临时改类型 / 版本：写进实例覆盖后立即生效
		changed := false
		ic := app.Cfg.Instances[inst.Name]
		if serverType != "" {
			if err := serverjar.Available(serverType); err != nil {
				return err
			}
			ic.Type = strings.ToLower(serverType)
			changed = true
		}
		if serverVersion != "" {
			ic.MinecraftVersion = serverVersion
			changed = true
		}
		if serverLoader != "" || serverBuild != "" {
			if ic.Server == nil {
				ic.Server = &config.ServerConfig{}
			}
			if serverLoader != "" {
				ic.Server.LoaderVersion = serverLoader
			}
			if serverBuild != "" {
				ic.Server.Build = serverBuild
			}
			changed = true
		}
		if serverInstaller != "" {
			if ic.Server == nil {
				ic.Server = &config.ServerConfig{}
			}
			ic.Server.InstallerVersion = serverInstaller
			changed = true
		}
		if changed {
			app.Cfg.Instances[inst.Name] = ic
			if err := app.Cfg.Save(); err != nil {
				return err
			}
			// 重新解析实例，让新配置生效
			if inst, err = app.resolveInstance([]string{inst.Name}); err != nil {
				return err
			}
		}

		if !inst.Res.Server.EULAEnabled() && !app.JSON {
			ensureEULA(app, inst)
		}

		opts := serverjar.Options{
			Force:   serverForce,
			Update:  serverUpdate,
			Mirror:  serverMirror,
			Logger:  app.Log,
			Timeout: inst.Res.Downloader.Timeout.Std(),
			Retries: inst.Res.Downloader.Retries,
		}
		if serverNoVerify {
			v := false
			opts.Verify = &v
		}

		var lastPercent int = -1
		if !app.JSON {
			opts.Progress = func(p downloader.Progress) {
				if p.Total > 0 {
					pct := int(p.Percent)
					if pct != lastPercent && pct%5 == 0 {
						lastPercent = pct
						fmt.Fprintf(os.Stderr, "\r  %s %s %s", p.Phase, fmtPercent(p.Done, p.Total), backup.HumanSize(p.Total))
					}
					return
				}
				if p.Phase != "" {
					fmt.Fprintf(os.Stderr, "\r  %s", p.Phase)
				}
			}
		}

		art, downloaded, err := serverjar.Ensure(app.Ctx, inst, opts)
		if !app.JSON {
			fmt.Fprint(os.Stderr, "\r\033[K")
		}
		if err != nil {
			return fmt.Errorf("获取服务端失败：%w", err)
		}

		info := serverjar.Installed(inst)
		if app.JSON {
			return app.JSONOut(map[string]any{
				"instance":   inst.Name,
				"downloaded": downloaded,
				"artifact":   art,
				"installed":  info,
			})
		}
		if !downloaded {
			app.OK("实例 %s 的服务端文件已就绪：%s", inst.Name, info.Path)
			if info.Version != "" {
				app.Out("  版本 %s%s%s", info.Version, buildLabel(info.Build), loaderLabel(info.LoaderVersion))
			}
			app.Out("  需要强制重新下载：nyatmc server download --force %s", inst.Name)
			return nil
		}
		app.OK("服务端下载完成：%s", info.Path)
		app.Out("  类型 %s，版本 %s%s%s", info.Type, info.Version, buildLabel(info.Build), loaderLabel(info.LoaderVersion))
		app.Out("  大小 %s", backup.HumanSize(info.Size))
		if info.SHA256 != "" {
			app.Out("  SHA-256 %s", info.SHA256)
		}
		app.Out("")
		app.Out("下一步：nyatmc start %s", inst.Name)
		return nil
	},
}

var serverInfoCmd = &cobra.Command{
	Use:   "info [实例名]",
	Short: "查看本地已安装的服务端文件",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newAppInstance(cmd, args)
		if err != nil {
			return err
		}
		info := serverjar.Installed(app.Inst)
		if app.JSON {
			return app.JSONOut(info)
		}
		app.Out("实例 %s 的服务端", app.Inst.Name)
		if !info.Exists {
			app.Warn("文件不存在：%s", info.Path)
			app.Out("  运行 nyatmc server download %s 获取", app.Inst.Name)
			return nil
		}
		mark := func(k, v string) { app.Out("  %-10s %s", k, v) }
		mark("路径", info.Path)
		mark("类型", info.Type)
		mark("版本", info.Version)
		if info.Build != "" {
			mark("构建", info.Build)
		}
		if info.LoaderVersion != "" {
			mark("Fabric", fmt.Sprintf("loader %s / installer %s", info.LoaderVersion, info.InstallerVersion))
		}
		mark("大小", backup.HumanSize(info.Size))
		if !info.InstalledAt.IsZero() {
			mark("安装时间", humanTime(info.InstalledAt))
		}
		if info.SHA256 != "" {
			mark("SHA-256", info.SHA256)
		}
		if info.URL != "" {
			mark("来源", info.URL)
		}
		return nil
	},
}

var serverCheckCmd = &cobra.Command{
	Use:   "check [实例名]",
	Short: "只查询远端最新版本，不下载",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newAppInstance(cmd, args)
		if err != nil {
			return err
		}
		ctx, cancel := contextWithTimeout(app.Ctx, 2*time.Minute)
		defer cancel()
		art, err := serverjar.Check(ctx, app.Inst, serverjar.Options{
			Kind:             serverType,
			MinecraftVersion: serverVersion,
			Build:            serverBuild,
			LoaderVersion:    serverLoader,
			InstallerVersion: serverInstaller,
			Mirror:           serverMirror,
			Logger:           app.Log,
		})
		if err != nil {
			return err
		}
		local := serverjar.Installed(app.Inst)
		if app.JSON {
			return app.JSONOut(map[string]any{"remote": art, "local": local})
		}
		app.Out("远端最新：%s %s%s", art.Kind, art.Version, buildLabel(art.Build))
		app.Out("  下载地址 %s", art.URL)
		app.Out("  文件名   %s", art.FileName)
		if art.SHA256 != "" {
			app.Out("  SHA-256  %s", art.SHA256)
		}
		app.Out("本地已装：%s %s%s", local.Type, local.Version, buildLabel(local.Build))
		if local.Version != art.Version || (art.Build != "" && local.Build != art.Build) {
			app.Warn("远端与本地不一致，可用 nyatmc server download --update %s 升级", app.Inst.Name)
		} else {
			app.OK("本地已是最新")
		}
		return nil
	},
}

var serverVersionsCmd = &cobra.Command{
	Use:   "versions [类型]",
	Short: "列出可用的 Minecraft 版本",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		kind := downloader.Paper
		if len(args) > 0 {
			kind = downloader.Kind(strings.ToLower(args[0]))
		} else if serverKindList != "" {
			kind = downloader.Kind(strings.ToLower(serverKindList))
		}
		if err := serverjar.Available(string(kind)); err != nil {
			return err
		}
		ctx, cancel := contextWithTimeout(app.Ctx, 2*time.Minute)
		defer cancel()
		versions, err := downloader.ListVersions(ctx, kind, serverMirror)
		if err != nil {
			return err
		}
		if app.JSON {
			return app.JSONOut(versions)
		}
		limit := len(versions)
		if limit > 30 {
			limit = 30
		}
		app.Out("%s 可用版本（共 %d 个，显示最新 %d 个）：", kind, len(versions), limit)
		app.Out("  %s", strings.Join(versions[:limit], "  "))
		app.Out("\n用法：nyatmc server download --version <版本>")
		return nil
	},
}

var serverBuildsCmd = &cobra.Command{
	Use:   "builds [类型] [Minecraft 版本]",
	Short: "列出可用的构建号（Paper / Purpur）",
	Args:  cobra.MaximumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		kind := downloader.Paper
		mcVersion := serverVersion
		if len(args) > 0 {
			kind = downloader.Kind(strings.ToLower(args[0]))
		}
		if len(args) > 1 {
			mcVersion = args[1]
		}
		if mcVersion == "" {
			mcVersion = "latest"
		}
		ctx, cancel := contextWithTimeout(app.Ctx, 2*time.Minute)
		defer cancel()
		builds, err := downloader.ListBuilds(ctx, kind, mcVersion)
		if err != nil {
			return err
		}
		if app.JSON {
			return app.JSONOut(builds)
		}
		limit := len(builds)
		if limit > 30 {
			limit = 30
		}
		app.Out("%s %s 的可用构建（共 %d 个，显示最新 %d 个）：", kind, mcVersion, len(builds), limit)
		app.Out("  %s", strings.Join(builds[:limit], "  "))
		app.Out("\n用法：nyatmc server download --build <构建号>")
		return nil
	},
}

func buildLabel(build string) string {
	if strings.TrimSpace(build) == "" {
		return ""
	}
	return "（构建 " + build + "）"
}

func loaderLabel(loader string) string {
	if strings.TrimSpace(loader) == "" {
		return ""
	}
	return "，Fabric loader " + loader
}

func init() {
	serverDownloadCmd.Flags().StringVar(&serverType, "type", "", "服务端类型：paper/fabric/vanilla/spigot/purpur")
	serverDownloadCmd.Flags().StringVar(&serverVersion, "version", "", "Minecraft 版本（例如 1.21.4）")
	serverDownloadCmd.Flags().StringVar(&serverBuild, "build", "", "构建号（Paper/Purpur）")
	serverDownloadCmd.Flags().StringVar(&serverLoader, "loader-version", "", "Fabric loader 版本")
	serverDownloadCmd.Flags().StringVar(&serverInstaller, "installer-version", "", "Fabric installer 版本")
	serverDownloadCmd.Flags().StringVar(&serverMirror, "mirror", "", "下载镜像基地址（留空用官方源）")
	serverDownloadCmd.Flags().BoolVar(&serverForce, "force", false, "强制重新下载")
	serverDownloadCmd.Flags().BoolVar(&serverUpdate, "update", false, "检查并升级到最新构建")
	serverDownloadCmd.Flags().BoolVar(&serverNoVerify, "no-verify", false, "跳过哈希校验（不推荐）")

	serverCheckCmd.Flags().StringVar(&serverType, "type", "", "服务端类型")
	serverCheckCmd.Flags().StringVar(&serverVersion, "version", "", "Minecraft 版本")
	serverCheckCmd.Flags().StringVar(&serverBuild, "build", "", "构建号")
	serverCheckCmd.Flags().StringVar(&serverLoader, "loader-version", "", "Fabric loader 版本")
	serverCheckCmd.Flags().StringVar(&serverInstaller, "installer-version", "", "Fabric installer 版本")
	serverCheckCmd.Flags().StringVar(&serverMirror, "mirror", "", "下载镜像基地址")

	serverVersionsCmd.Flags().StringVar(&serverKindList, "kind", "", "服务端类型（也可作为位置参数）")
	serverVersionsCmd.Flags().StringVar(&serverMirror, "mirror", "", "下载镜像基地址")
	serverBuildsCmd.Flags().StringVar(&serverVersion, "version", "", "Minecraft 版本")

	serverCmd.AddCommand(serverDownloadCmd, serverInfoCmd, serverCheckCmd, serverVersionsCmd, serverBuildsCmd)
	rootCmd.AddCommand(serverCmd)
}

// 让 daemon 的自动下载走同一个实现（cmd 层装配，daemon 不直接依赖下载器）。
func init() {
	daemon.SetServerJarProvider(serverjar.EnsureFunc)
}

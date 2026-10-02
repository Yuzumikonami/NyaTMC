package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yuzumikonami/nyatmc/internal/backup"
	"github.com/yuzumikonami/nyatmc/internal/daemon"
	"github.com/yuzumikonami/nyatmc/internal/instance"
	"github.com/yuzumikonami/nyatmc/internal/mods"
)

var (
	modProvider  string
	modLimit     int
	modLoader    string
	modGameVer   string
	modType      string
	modPlugin    bool
	modMod       bool
	modYes       bool
	modDir       string
	modNoBackup  bool
)

var modCmd = &cobra.Command{
	Use:   "mod",
	Short: "模组 / 插件管理（Modrinth 与 CurseForge）",
	Long: `搜索、安装、列出与删除模组或插件。

默认使用 Modrinth（免注册）。要用 CurseForge 需要先在配置里填 API Key：
  nyatmc config set mods.provider curseforge
  nyatmc config set mods.curseforge_api_key <你的 Key>

服务端类型决定装到哪里：paper/spigot/purpur 装进 plugins/，fabric 等装进 mods/；
也可以显式用 --plugin / --mod 覆盖。

示例：
  nyatmc mod search jei
  nyatmc mod install jei -i survival
  nyatmc mod install fabric-api --version 1.21.4 --loader fabric
  nyatmc mod list survival
  nyatmc mod remove jei-15.2.0.27.jar -i survival`,
}

var modSearchCmd = &cobra.Command{
	Use:   "search <关键词…>",
	Aliases: []string{"find"},
	Short: "搜索模组 / 插件",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		inst := modTargetInstance(app)
		provider, err := modProviderFor(app, inst)
		if err != nil {
			return err
		}
		limit := modLimit
		if limit <= 0 {
			limit = app.Cfg.Mods.Limit
		}

		ctx, cancel := contextWithTimeout(app.Ctx, 0)
		defer cancel()
		installer := mods.Installer{
			Provider:    provider,
			APIKey:      app.Cfg.Mods.CurseForgeAPIKey,
			UserAgent:   app.Cfg.Downloader.UserAgent,
			Timeout:     app.Cfg.Downloader.Timeout.Std(),
			Retries:     app.Cfg.Downloader.Retries,
			GameVersion: modGameVersionFor(app, inst),
			Loader:      modLoaderFor(app, inst),
			Dir:         modDirFor(app, inst),
			ClassID:     modClassIDFor(app, inst),
			Logger:      app.Log,
		}
		projects, err := installer.Search(ctx, strings.Join(args, " "), limit)
		if err != nil {
			return err
		}
		if app.JSON {
			return app.JSONOut(projects)
		}
		if len(projects) == 0 {
			app.Out("没有找到匹配的项目。")
			return nil
		}
		rows := make([][]string, 0, len(projects))
		for _, p := range projects {
			rows = append(rows, []string{
				p.Title,
				string(p.Provider),
				p.ProjectType,
				truncate(p.Description, 46),
				humanCount(p.Downloads),
				p.ID,
			})
		}
		app.Table([]string{"名称", "来源", "类型", "简介", "下载量", "ID"}, rows)
		app.Out("\n安装：nyatmc mod install <ID 或名称> -i <实例>")
		return nil
	},
}

var modInstallCmd = &cobra.Command{
	Use:   "install <关键词或项目ID…>",
	Aliases: []string{"add"},
	Short: "安装模组 / 插件到实例",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		inst := modTargetInstance(app)
		if inst == nil {
			return fmt.Errorf("需要指定实例：nyatmc mod install <名字> -i <实例>（或用 nyatmc use 设置默认实例）")
		}
		provider, err := modProviderFor(app, inst)
		if err != nil {
			return err
		}
		query := strings.Join(args, " ")

		ctx, cancel := contextWithTimeout(app.Ctx, 0)
		defer cancel()
		inst0 := mods.Installer{
			Provider:    provider,
			APIKey:      app.Cfg.Mods.CurseForgeAPIKey,
			UserAgent:   app.Cfg.Downloader.UserAgent,
			Timeout:     app.Cfg.Downloader.Timeout.Std(),
			Retries:     app.Cfg.Downloader.Retries,
			GameVersion: modGameVersionFor(app, inst),
			Loader:      modLoaderFor(app, inst),
			Dir:         modDirFor(app, inst),
			ClassID:     modClassIDFor(app, inst),
			Logger:      app.Log,
		}

		var progress mods.ProgressFunc
		if !app.JSON {
			progress = func(done, total int64) {
				if total > 0 {
					fmt.Fprintf(os.Stderr, "\r  下载中 %s %s", fmtPercent(done, total), backup.HumanSize(total))
				}
			}
		}

		var file mods.InstalledFile
		if isProjectID(query) {
			file, err = inst0.Install(ctx, query, progress)
		} else {
			file, err = inst0.SearchAndInstall(ctx, query, progress)
		}
		if !app.JSON {
			fmt.Fprint(os.Stderr, "\r\033[K")
		}
		if err != nil {
			return err
		}
		if app.JSON {
			return app.JSONOut(file)
		}
		app.OK("已安装到实例 %s：%s", inst.Name, file.FileName)
		app.Out("  项目   %s（%s）", firstNonEmpty(file.Title, file.ProjectID), file.ProjectID)
		app.Out("  版本   %s", file.Version)
		app.Out("  路径   %s", file.Path)
		app.Out("  大小   %s", backup.HumanSize(file.Size))
		if file.SHA256 != "" {
			app.Out("  SHA-256 %s", file.SHA256)
		}
		if daemonRunning(app, inst) {
			app.Warn("实例正在运行：模组/插件需要重启才会生效（nyatmc restart %s）", inst.Name)
		}
		return nil
	},
}

var modListCmd = &cobra.Command{
	Use:   "list [实例名]",
	Aliases: []string{"ls"},
	Short: "列出已安装的模组 / 插件",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newAppInstance(cmd, args)
		if err != nil {
			return err
		}
		inst := app.Inst
		dir := modDirFor(app, inst)
		inst0 := mods.Installer{Dir: dir, Logger: app.Log}
		files, err := inst0.ListDir()
		if err != nil {
			return err
		}
		if app.JSON {
			return app.JSONOut(map[string]any{"instance": inst.Name, "dir": dir, "files": files})
		}
		app.Out("实例 %s 的 %s（%s）", inst.Name, filepath.Base(dir), dir)
		if len(files) == 0 {
			app.Out("  （空）")
			app.Out("\n安装：nyatmc mod install <名字> -i %s", inst.Name)
			return nil
		}
		rows := make([][]string, 0, len(files))
		var total int64
		for _, f := range files {
			total += f.Size
			rows = append(rows, []string{f.Name, backup.HumanSize(f.Size), humanTime(f.ModTime)})
		}
		app.Table([]string{"文件名", "大小", "修改时间"}, rows)
		app.Out("\n共 %d 个文件，合计 %s", len(files), backup.HumanSize(total))
		return nil
	},
}

var modRemoveCmd = &cobra.Command{
	Use:   "remove <文件名> [实例名]",
	Aliases: []string{"rm", "uninstall", "delete"},
	Short: "删除已安装的模组 / 插件",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newAppInstance(cmd, args[1:])
		if err != nil {
			return err
		}
		inst := app.Inst
		name := filepath.Base(args[0])
		if !modYes {
			if !confirm(app, fmt.Sprintf("确认从实例 %s 删除 %s？", inst.Name, name)) {
				app.Out("已取消。")
				return nil
			}
		}
		inst0 := mods.Installer{Dir: modDirFor(app, inst), Logger: app.Log}
		if err := inst0.Remove(name); err != nil {
			return err
		}
		if app.JSON {
			return app.JSONOut(map[string]any{"instance": inst.Name, "removed": name})
		}
		app.OK("已删除 %s", name)
		if daemonRunning(app, inst) {
			app.Warn("实例正在运行：重启后生效（nyatmc restart %s）", inst.Name)
		}
		return nil
	},
}

var modInfoCmd = &cobra.Command{
	Use:   "info <项目ID>",
	Short: "查看某个项目可安装的版本文件",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		inst := modTargetInstance(app)
		provider, err := modProviderFor(app, inst)
		if err != nil {
			return err
		}
		ctx, cancel := contextWithTimeout(app.Ctx, 0)
		defer cancel()
		installer := mods.Installer{
			Provider:    provider,
			APIKey:      app.Cfg.Mods.CurseForgeAPIKey,
			UserAgent:   app.Cfg.Downloader.UserAgent,
			Timeout:     app.Cfg.Downloader.Timeout.Std(),
			Retries:     app.Cfg.Downloader.Retries,
			GameVersion: modGameVersionFor(app, inst),
			Loader:      modLoaderFor(app, inst),
			Dir:         modDirFor(app, inst),
			ClassID:     modClassIDFor(app, inst),
			Logger:      app.Log,
		}
		v, err := installer.Resolve(ctx, args[0])
		if err != nil {
			return err
		}
		if app.JSON {
			return app.JSONOut(v)
		}
		app.Out("项目 %s 匹配到的文件：", args[0])
		app.Out("  文件名   %s", v.FileName)
		app.Out("  版本号   %s", v.VersionNumber)
		app.Out("  适用版本 %s", strings.Join(v.GameVersions, ","))
		app.Out("  加载器   %s", strings.Join(v.Loaders, ","))
		app.Out("  大小     %s", backup.HumanSize(v.Size))
		app.Out("  下载地址 %s", v.URL)
		return nil
	},
}

// ---------------------------------------------------------------- 辅助

// modTargetInstance 解析模组操作的目标实例（可以没有）。
func modTargetInstance(app *App) *instance.Instance {
	name := strings.TrimSpace(flagInstance)
	if name == "" {
		name = app.Cfg.General.DefaultInstance
	}
	if name == "" {
		return nil
	}
	inst, err := instance.New(app.Cfg, name)
	if err != nil || !inst.Registered() {
		return nil
	}
	return inst
}

func modProviderFor(app *App, inst *instance.Instance) (mods.Provider, error) {
	p := strings.ToLower(strings.TrimSpace(firstNonEmpty(modProvider, app.Cfg.Mods.Provider)))
	switch p {
	case "", "modrinth":
		return mods.Modrinth, nil
	case "curseforge", "cf":
		if strings.TrimSpace(app.Cfg.Mods.CurseForgeAPIKey) == "" {
			return "", fmt.Errorf("使用 CurseForge 需要 API Key：nyatmc config set mods.curseforge_api_key <你的 Key>（或改用 --provider modrinth）")
		}
		return mods.CurseForge, nil
	default:
		return "", fmt.Errorf("未知的模组来源 %q（可选：modrinth / curseforge）", p)
	}
}

func modLoaderFor(app *App, inst *instance.Instance) string {
	if modLoader != "" {
		return strings.ToLower(modLoader)
	}
	if inst != nil {
		if l := inst.LoaderFor(); l != "" {
			return l
		}
	}
	return strings.ToLower(app.Cfg.Mods.Loader)
}

func modGameVersionFor(app *App, inst *instance.Instance) string {
	if modGameVer != "" {
		return modGameVer
	}
	if inst != nil {
		if v := inst.GameVersionFor(); v != "" {
			return v
		}
	}
	return app.Cfg.Mods.GameVersion
}

// modProjectTypeFor 决定搜索 mod 还是 plugin。
func modProjectTypeFor(app *App, inst *instance.Instance) string {
	switch {
	case modPlugin:
		return "plugin"
	case modMod:
		return "mod"
	case modType != "":
		return strings.ToLower(modType)
	}
	if inst != nil && inst.IsPluginServer() {
		return "plugin"
	}
	return "mod"
}

// modDirFor 决定落盘目录。
func modDirFor(app *App, inst *instance.Instance) string {
	if modDir != "" {
		return modDir
	}
	if inst == nil {
		return app.Cfg.Mods.ModsDir
	}
	switch {
	case modPlugin:
		return inst.PluginsDir()
	case modMod:
		return inst.ModsDir()
	}
	if inst.IsPluginServer() {
		return inst.PluginsDir()
	}
	return inst.ModsDir()
}

func modClassIDFor(app *App, inst *instance.Instance) int {
	if modProjectTypeFor(app, inst) == "plugin" {
		return 5 // CurseForge: bukkit plugins
	}
	return 6 // CurseForge: mods
}

// isProjectID 判断参数看起来像项目 ID（Modrinth 用 8 位 base62，CurseForge 用数字）。
func isProjectID(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \t") {
		return false
	}
	digits := true
	for _, r := range s {
		if r < '0' || r > '9' {
			digits = false
			break
		}
	}
	if digits {
		return true
	}
	// Modrinth 的 project id 形如 AANobbMI：无分隔符、长度 8 左右、含大小写
	if len(s) >= 6 && len(s) <= 12 && !strings.Contains(s, "-") {
		hasUpper, hasLower := false, false
		for _, r := range s {
			if r >= 'A' && r <= 'Z' {
				hasUpper = true
			}
			if r >= 'a' && r <= 'z' {
				hasLower = true
			}
		}
		return hasUpper && hasLower
	}
	return false
}

func truncate(s string, n int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= n {
		return string(runes)
	}
	return string(runes[:n]) + "…"
}

func humanCount(n int64) string {
	switch {
	case n >= 100000000:
		return fmt.Sprintf("%.1f亿", float64(n)/100000000)
	case n >= 10000:
		return fmt.Sprintf("%.1f万", float64(n)/10000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func init() {
	modCmd.PersistentFlags().StringVar(&modProvider, "provider", "", "来源：modrinth / curseforge（默认取配置 mods.provider）")
	modCmd.PersistentFlags().BoolVar(&modPlugin, "plugin", false, "按插件处理（放进 plugins/）")
	modCmd.PersistentFlags().BoolVar(&modMod, "mod", false, "按模组处理（放进 mods/）")
	modCmd.PersistentFlags().StringVar(&modDir, "dir", "", "自定义落盘目录")

	modSearchCmd.Flags().IntVarP(&modLimit, "limit", "n", 0, "返回条数（默认取配置 mods.limit）")
	modSearchCmd.Flags().StringVar(&modLoader, "loader", "", "按加载器过滤（fabric/paper/forge…）")
	modSearchCmd.Flags().StringVar(&modGameVer, "version", "", "按 Minecraft 版本过滤")
	modSearchCmd.Flags().StringVar(&modType, "type", "", "项目类型：mod / plugin / datapack")

	modInstallCmd.Flags().StringVar(&modLoader, "loader", "", "按加载器过滤（fabric/paper/forge…）")
	modInstallCmd.Flags().StringVar(&modGameVer, "version", "", "按 Minecraft 版本过滤")
	modInstallCmd.Flags().BoolVarP(&modYes, "yes", "y", false, "跳过确认")
	modInstallCmd.Flags().BoolVar(&modNoBackup, "no-backup", false, "已废弃：同名文件始终会备份为 .bak")

	modRemoveCmd.Flags().BoolVarP(&modYes, "yes", "y", false, "跳过确认")

	modInfoCmd.Flags().StringVar(&modLoader, "loader", "", "按加载器过滤")
	modInfoCmd.Flags().StringVar(&modGameVer, "version", "", "按 Minecraft 版本过滤")

	modCmd.AddCommand(modSearchCmd, modInstallCmd, modListCmd, modRemoveCmd, modInfoCmd)
	rootCmd.AddCommand(modCmd)
}

func daemonRunning(app *App, inst *instance.Instance) bool {
	_ = app
	return daemon.Running(inst)
}

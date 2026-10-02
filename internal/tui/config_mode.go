package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/yuzumikonami/nyatmc/internal/config"
)

// cfgKind 是配置项的编辑方式。
type cfgKind int

const (
	kindText   cfgKind = iota // 自由文本
	kindBool                  // 布尔开关：true/false
	kindInt                   // 整数
	kindPort                  // 端口（1-65535）
	kindMemory                // 内存（形如 2G）
	kindDur                   // 时长（time.ParseDuration 可解析）
	kindSize                  // 容量（形如 20MB）
	kindEnum                  // 枚举
)

// cfgEntry 是一条可编辑的配置项。
type cfgEntry struct {
	section string
	key     string
	path    string
	kind    cfgKind
	options []string
	secret  bool

	get func(*config.Config) string
	set func(*config.Config, string) error
}

// value 返回当前值（供显示与进入编辑时预填）。
func (e *cfgEntry) value(cfg *config.Config) string {
	if e.get == nil {
		return ""
	}
	return e.get(cfg)
}

// display 返回展示用的值（脱敏）。
func (e *cfgEntry) display(cfg *config.Config, width int) string {
	v := e.value(cfg)
	if e.secret && strings.TrimSpace(v) != "" {
		v = strings.Repeat("*", minInt(len(v), 12))
	}
	if strings.TrimSpace(v) == "" {
		v = "（空）"
	}
	return Truncate(v, maxInt(4, width))
}

// apply 用编辑后的文本更新配置中的这一项。
func (e *cfgEntry) apply(cfg *config.Config, text string) error {
	if e.set != nil {
		return e.set(cfg, text)
	}
	return fmt.Errorf("配置项 %s 暂不支持在界面里修改", e.path)
}

// ------------------------------------------------------------------ 校验

// validateCfgValue 按类型校验编辑框里的文本。
func validateCfgValue(kind cfgKind, text string) error {
	return validateCfgValueWith(kind, nil, text)
}

// validateCfgValueWith 在类型校验之外追加枚举取值校验。
func validateCfgValueWith(kind cfgKind, options []string, text string) error {
	text = strings.TrimSpace(text)
	switch kind {
	case kindBool:
		if text != "true" && text != "false" {
			return fmt.Errorf("布尔值只能是 true 或 false（当前 %q）", text)
		}
	case kindInt:
		if _, err := strconv.Atoi(text); err != nil {
			return fmt.Errorf("需要整数（当前 %q）", text)
		}
	case kindPort:
		n, err := strconv.Atoi(text)
		if err != nil {
			return fmt.Errorf("端口需要整数（当前 %q）", text)
		}
		if n < 1 || n > 65535 {
			return fmt.Errorf("端口应在 1-65535 之间（当前 %d）", n)
		}
	case kindMemory:
		if err := validateMemory(text); err != nil {
			return err
		}
	case kindDur:
		if err := validateDuration(text); err != nil {
			return err
		}
	case kindSize:
		if err := validateSize(text); err != nil {
			return err
		}
	case kindEnum:
		if len(options) > 0 {
			// 允许多个枚举值用 / 或 , 分隔（例如一次填多个）
			for _, part := range strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == '/' }) {
				if err := validateEnum(options, strings.TrimSpace(part)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// validateMemory 校验 "2G" / "512M" 这类内存写法。
func validateMemory(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("内存不能为空，示例：2G、512M")
	}
	num := s[:len(s)-1]
	unit := s[len(s)-1]
	switch unit {
	case 'k', 'K', 'm', 'M', 'g', 'G':
	default:
		return fmt.Errorf("内存应形如 2G / 512M（当前 %q）", s)
	}
	if _, err := strconv.ParseFloat(strings.TrimSpace(num), 64); err != nil {
		return fmt.Errorf("内存应形如 2G / 512M（当前 %q）", s)
	}
	return nil
}

// validateDuration 校验时长。
func validateDuration(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("时长不能为空，示例：30s、10m、1h30m、2d")
	}
	var d config.Duration
	if err := d.UnmarshalText([]byte(s)); err != nil {
		return err
	}
	return nil
}

// validateSize 校验容量。
func validateSize(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("容量不能为空，示例：512KB、20MB、1GB")
	}
	var sz config.Size
	if err := sz.UnmarshalText([]byte(s)); err != nil {
		return err
	}
	return nil
}

// ------------------------------------------------------------------ 配置目录

// buildConfigEntries 组装界面里可浏览 / 编辑的配置项。
func buildConfigEntries(res config.Resolved) []cfgEntry {
	entries := []cfgEntry{
		{
			section: "general", key: "默认实例", path: "general.default_instance", kind: kindText,
			get: func(c *config.Config) string { return c.General.DefaultInstance },
			set: func(c *config.Config, v string) error {
				if err := config.ValidateInstanceName(v); err != nil {
					return err
				}
				c.General.DefaultInstance = v
				return nil
			},
		},
		boolEntry("general", "命令行颜色", "general.color",
			func(c *config.Config) bool { return c.General.Color },
			func(c *config.Config, v bool) { c.General.Color = v }),
		boolEntry("general", "守护启动即拉起", "general.auto_start",
			func(c *config.Config) bool { return c.General.AutoStart },
			func(c *config.Config, v bool) { c.General.AutoStart = v }),

		enumEntry("server", "服务端类型", "server.type", []string{"paper", "fabric", "vanilla", "spigot", "purpur", "custom"},
			func(c *config.Config) string { return c.Server.Type },
			func(c *config.Config, v string) { c.Server.Type = v }),
		{
			section: "server", key: "Minecraft 版本", path: "server.minecraft_version", kind: kindText,
			get: func(c *config.Config) string { return c.Server.MinecraftVersion },
			set: func(c *config.Config, v string) error { c.Server.MinecraftVersion = v; return nil },
		},
		{
			section: "server", key: "Java 路径", path: "server.java_path", kind: kindText,
			get: func(c *config.Config) string { return c.Server.JavaPath },
			set: func(c *config.Config, v string) error { c.Server.JavaPath = v; return nil },
		},
		{
			section: "server", key: "最大内存", path: "server.memory", kind: kindMemory,
			get: func(c *config.Config) string { return c.Server.Memory },
			set: func(c *config.Config, v string) error { c.Server.Memory = v; return nil },
		},
		{
			section: "server", key: "最小内存", path: "server.min_memory", kind: kindMemory,
			get: func(c *config.Config) string { return c.Server.MinMemory },
			set: func(c *config.Config, v string) error { c.Server.MinMemory = v; return nil },
		},
		{
			section: "server", key: "监听端口", path: "server.port", kind: kindPort,
			get: func(c *config.Config) string { return strconv.Itoa(c.Server.Port) },
			set: func(c *config.Config, v string) error {
				n, err := strconv.Atoi(v)
				if err != nil {
					return err
				}
				c.Server.Port = n
				return nil
			},
		},
		{
			section: "server", key: "最大玩家数", path: "server.max_players", kind: kindInt,
			get: func(c *config.Config) string { return strconv.Itoa(c.Server.MaxPlayers) },
			set: func(c *config.Config, v string) error {
				n, err := strconv.Atoi(v)
				if err != nil {
					return err
				}
				c.Server.MaxPlayers = n
				return nil
			},
		},
		{
			section: "server", key: "视距", path: "server.view_distance", kind: kindInt,
			get: func(c *config.Config) string { return strconv.Itoa(c.Server.ViewDistance) },
			set: func(c *config.Config, v string) error {
				n, err := strconv.Atoi(v)
				if err != nil {
					return err
				}
				c.Server.ViewDistance = n
				return nil
			},
		},
		{
			section: "server", key: "MOTD", path: "server.motd", kind: kindText,
			get: func(c *config.Config) string { return c.Server.MOTD },
			set: func(c *config.Config, v string) error { c.Server.MOTD = v; return nil },
		},
		boolPtrEntry("server", "同意 EULA", "server.eula",
			func(c *config.Config) *bool { return c.Server.EULA },
			func(c *config.Config, p *bool) { c.Server.EULA = p }),
		boolPtrEntry("server", "正版验证（online-mode）", "server.online_mode",
			func(c *config.Config) *bool { return c.Server.OnlineMode },
			func(c *config.Config, p *bool) { c.Server.OnlineMode = p }),
		boolPtrEntry("server", "启动前自动补齐服务端", "server.auto_download",
			func(c *config.Config) *bool { return c.Server.AutoDownload },
			func(c *config.Config, p *bool) { c.Server.AutoDownload = p }),
		{
			section: "server", key: "主 jar 文件名", path: "server.jar_name", kind: kindText,
			get: func(c *config.Config) string { return c.Server.JarName },
			set: func(c *config.Config, v string) error {
				if v == "" || strings.ContainsAny(v, `/\`) {
					return fmt.Errorf("jar_name 应为不含路径分隔符的文件名")
				}
				c.Server.JarName = v
				return nil
			},
		},
		{
			section: "server", key: "世界目录名", path: "server.world_name", kind: kindText,
			get: func(c *config.Config) string { return c.Server.WorldName },
			set: func(c *config.Config, v string) error { c.Server.WorldName = v; return nil },
		},

		boolPtrEntry("daemon", "看门狗（崩溃自动重启）", "daemon.watchdog",
			func(c *config.Config) *bool { return c.Daemon.Watchdog },
			func(c *config.Config, p *bool) { c.Daemon.Watchdog = p }),
		{
			section: "daemon", key: "最大重启次数", path: "daemon.max_restarts", kind: kindInt,
			get: func(c *config.Config) string { return strconv.Itoa(c.Daemon.MaxRestarts) },
			set: func(c *config.Config, v string) error {
				n, err := strconv.Atoi(v)
				if err != nil {
					return err
				}
				if n < 0 {
					return fmt.Errorf("最大重启次数不能为负")
				}
				c.Daemon.MaxRestarts = n
				return nil
			},
		},
		durEntry("daemon", "重启统计窗口", "daemon.restart_window",
			func(c *config.Config) config.Duration { return c.Daemon.RestartWindow },
			func(c *config.Config, d config.Duration) { c.Daemon.RestartWindow = d }),
		durEntry("daemon", "崩溃后重启延迟", "daemon.restart_delay",
			func(c *config.Config) config.Duration { return c.Daemon.RestartDelay },
			func(c *config.Config, d config.Duration) { c.Daemon.RestartDelay = d }),
		{
			section: "daemon", key: "优雅关闭指令", path: "daemon.stop_command", kind: kindText,
			get: func(c *config.Config) string { return c.Daemon.StopCommand },
			set: func(c *config.Config, v string) error { c.Daemon.StopCommand = v; return nil },
		},
		durEntry("daemon", "优雅关闭超时", "daemon.stop_timeout",
			func(c *config.Config) config.Duration { return c.Daemon.StopTimeout },
			func(c *config.Config, d config.Duration) { c.Daemon.StopTimeout = d }),
		durEntry("daemon", "启动就绪超时", "daemon.startup_timeout",
			func(c *config.Config) config.Duration { return c.Daemon.StartupTimeout },
			func(c *config.Config, d config.Duration) { c.Daemon.StartupTimeout = d }),
		durEntry("daemon", "强杀等待时间", "daemon.kill_timeout",
			func(c *config.Config) config.Duration { return c.Daemon.KillTimeout },
			func(c *config.Config, d config.Duration) { c.Daemon.KillTimeout = d }),
		boolPtrEntry("daemon", "捕获服务端控制台", "daemon.capture_console",
			func(c *config.Config) *bool { return c.Daemon.CaptureConsole },
			func(c *config.Config, p *bool) { c.Daemon.CaptureConsole = p }),
		boolPtrEntry("daemon", "启动前自动备份", "daemon.backup_before_start",
			func(c *config.Config) *bool { return c.Daemon.BackupBeforeStart },
			func(c *config.Config, p *bool) { c.Daemon.BackupBeforeStart = p }),

		{
			section: "backup", key: "备份目录（空=实例目录）", path: "backup.dir", kind: kindText,
			get: func(c *config.Config) string { return c.Backup.Dir },
			set: func(c *config.Config, v string) error { c.Backup.Dir = v; return nil },
		},
		{
			section: "backup", key: "保留份数（0=不限）", path: "backup.keep", kind: kindInt,
			get: func(c *config.Config) string { return strconv.Itoa(c.Backup.Keep) },
			set: func(c *config.Config, v string) error {
				n, err := strconv.Atoi(v)
				if err != nil {
					return err
				}
				if n < 0 {
					return fmt.Errorf("保留份数不能为负")
				}
				c.Backup.Keep = n
				return nil
			},
		},
		durEntry("backup", "最长保留时间", "backup.max_age",
			func(c *config.Config) config.Duration { return c.Backup.MaxAge },
			func(c *config.Config, d config.Duration) { c.Backup.MaxAge = d }),
		enumEntry("backup", "归档格式", "backup.format", []string{"tar.gz", "tar", "zip"},
			func(c *config.Config) string { return c.Backup.Format },
			func(c *config.Config, v string) { c.Backup.Format = v }),
		boolPtrEntry("backup", "回滚前自动备份", "backup.before_restore",
			func(c *config.Config) *bool { return c.Backup.BeforeRestore },
			func(c *config.Config, p *bool) { c.Backup.BeforeRestore = p }),
		boolPtrEntry("backup", "备份前 save-off", "backup.stop_before_backup",
			func(c *config.Config) *bool { return c.Backup.StopBeforeBackup },
			func(c *config.Config, p *bool) { c.Backup.StopBeforeBackup = p }),

		sizeEntry("log", "轮转阈值", "log.rotate_size",
			func(c *config.Config) config.Size { return c.Log.RotateSize },
			func(c *config.Config, s config.Size) { c.Log.RotateSize = s }),
		{
			section: "log", key: "保留份数", path: "log.keep_files", kind: kindInt,
			get: func(c *config.Config) string { return strconv.Itoa(c.Log.KeepFiles) },
			set: func(c *config.Config, v string) error {
				n, err := strconv.Atoi(v)
				if err != nil {
					return err
				}
				if n < 0 {
					return fmt.Errorf("保留份数不能为负")
				}
				c.Log.KeepFiles = n
				return nil
			},
		},
		{
			section: "log", key: "最长保留天数", path: "log.max_age_days", kind: kindInt,
			get: func(c *config.Config) string { return strconv.Itoa(c.Log.MaxAgeDays) },
			set: func(c *config.Config, v string) error {
				n, err := strconv.Atoi(v)
				if err != nil {
					return err
				}
				if n < 0 {
					return fmt.Errorf("保留天数不能为负")
				}
				c.Log.MaxAgeDays = n
				return nil
			},
		},
		boolPtrEntry("log", "归档压缩", "log.compress",
			func(c *config.Config) *bool { return c.Log.Compress },
			func(c *config.Config, p *bool) { c.Log.Compress = p }),
		boolPtrEntry("log", "重启切割日志", "log.rotate_restart",
			func(c *config.Config) *bool { return c.Log.RotateRestart },
			func(c *config.Config, p *bool) { c.Log.RotateRestart = p }),
		durEntry("log", "两次切割最小间隔", "log.min_rotate_interval",
			func(c *config.Config) config.Duration { return c.Log.MinRotateInterval },
			func(c *config.Config, d config.Duration) { c.Log.MinRotateInterval = d }),
		boolPtrEntry("log", "托管 log4j2.xml", "log.manage_log4j2",
			func(c *config.Config) *bool { return c.Log.ManageLog4j2 },
			func(c *config.Config, p *bool) { c.Log.ManageLog4j2 = p }),
		{
			section: "log", key: "控制台环形缓冲行数", path: "log.console_lines", kind: kindInt,
			get: func(c *config.Config) string { return strconv.Itoa(c.Log.ConsoleLines) },
			set: func(c *config.Config, v string) error {
				n, err := strconv.Atoi(v)
				if err != nil {
					return err
				}
				if n <= 0 {
					return fmt.Errorf("行数必须大于 0")
				}
				c.Log.ConsoleLines = n
				return nil
			},
		},

		{
			section: "web", key: "监听地址", path: "web.listen", kind: kindText,
			get: func(c *config.Config) string { return c.Web.Listen },
			set: func(c *config.Config, v string) error {
				if _, _, err := splitHostPort(v); err != nil {
					return fmt.Errorf("监听地址应形如 127.0.0.1:8080：%w", err)
				}
				c.Web.Listen = v
				return nil
			},
		},
		{
			section: "web", key: "访问令牌", path: "web.token", kind: kindText, secret: true,
			get: func(c *config.Config) string { return c.Web.Token },
			set: func(c *config.Config, v string) error { c.Web.Token = v; return nil },
		},
		boolEntry("web", "允许写操作", "web.enable_write",
			func(c *config.Config) bool { return c.Web.EnableWrite },
			func(c *config.Config, v bool) { c.Web.EnableWrite = v }),
		{
			section: "web", key: "前端刷新秒数", path: "web.refresh_seconds", kind: kindInt,
			get: func(c *config.Config) string { return strconv.Itoa(c.Web.RefreshSeconds) },
			set: func(c *config.Config, v string) error {
				n, err := strconv.Atoi(v)
				if err != nil {
					return err
				}
				if n <= 0 {
					return fmt.Errorf("刷新间隔必须大于 0 秒")
				}
				c.Web.RefreshSeconds = n
				return nil
			},
		},
		{
			section: "web", key: "日志接口最大行数", path: "web.max_log_lines", kind: kindInt,
			get: func(c *config.Config) string { return strconv.Itoa(c.Web.MaxLogLines) },
			set: func(c *config.Config, v string) error {
				n, err := strconv.Atoi(v)
				if err != nil {
					return err
				}
				if n <= 0 {
					return fmt.Errorf("行数必须大于 0")
				}
				c.Web.MaxLogLines = n
				return nil
			},
		},

		boolEntry("scheduler", "启用调度器", "scheduler.enabled",
			func(c *config.Config) bool { return c.Scheduler.Enabled },
			func(c *config.Config, v bool) { c.Scheduler.Enabled = v }),
		{
			section: "scheduler", key: "时区（空=本机）", path: "scheduler.timezone", kind: kindText,
			get: func(c *config.Config) string { return c.Scheduler.Timezone },
			set: func(c *config.Config, v string) error { c.Scheduler.Timezone = v; return nil },
		},

		{
			section: "downloader", key: "镜像地址", path: "downloader.mirror", kind: kindText,
			get: func(c *config.Config) string { return c.Downloader.Mirror },
			set: func(c *config.Config, v string) error { c.Downloader.Mirror = v; return nil },
		},
		durEntry("downloader", "下载超时", "downloader.timeout",
			func(c *config.Config) config.Duration { return c.Downloader.Timeout },
			func(c *config.Config, d config.Duration) { c.Downloader.Timeout = d }),
		boolEntry("downloader", "校验哈希", "downloader.verify_checksum",
			func(c *config.Config) bool { return c.Downloader.VerifyChecksum },
			func(c *config.Config, v bool) { c.Downloader.VerifyChecksum = v }),
		{
			section: "downloader", key: "重试次数", path: "downloader.retries", kind: kindInt,
			get: func(c *config.Config) string { return strconv.Itoa(c.Downloader.Retries) },
			set: func(c *config.Config, v string) error {
				n, err := strconv.Atoi(v)
				if err != nil {
					return err
				}
				if n < 0 {
					return fmt.Errorf("重试次数不能为负")
				}
				c.Downloader.Retries = n
				return nil
			},
		},

		enumEntry("mods", "模组来源", "mods.provider", []string{"modrinth", "curseforge"},
			func(c *config.Config) string { return c.Mods.Provider },
			func(c *config.Config, v string) { c.Mods.Provider = v }),
		{
			section: "mods", key: "CurseForge API Key", path: "mods.curseforge_api_key", kind: kindText, secret: true,
			get: func(c *config.Config) string { return c.Mods.CurseForgeAPIKey },
			set: func(c *config.Config, v string) error { c.Mods.CurseForgeAPIKey = v; return nil },
		},
		{
			section: "mods", key: "搜索条数上限", path: "mods.limit", kind: kindInt,
			get: func(c *config.Config) string { return strconv.Itoa(c.Mods.Limit) },
			set: func(c *config.Config, v string) error {
				n, err := strconv.Atoi(v)
				if err != nil {
					return err
				}
				if n <= 0 {
					return fmt.Errorf("条数必须大于 0")
				}
				c.Mods.Limit = n
				return nil
			},
		},
	}

	// 实例级覆盖（[instances.<名字>.xxx]）：只在当前实例确实有覆盖项时才有意义，
	// 但保留在列表里方便用户为当前实例单独调参。
	name := res.Instance
	if name != "" {
		inst := []cfgEntry{
			{
				section: "instances." + name, key: "服务器目录", path: "instances." + name + ".path", kind: kindText,
				get: func(c *config.Config) string { return c.Instances[name].Path },
				set: func(c *config.Config, v string) error {
					return setInstanceField(c, name, func(ic *config.InstanceConfig) { ic.Path = v })
				},
			},
			enumEntry("instances."+name, "实例类型", "instances."+name+".type", []string{"", "paper", "fabric", "vanilla", "spigot", "purpur", "custom"},
				func(c *config.Config) string { return c.Instances[name].Type },
				func(c *config.Config, v string) {
					_ = setInstanceField(c, name, func(ic *config.InstanceConfig) { ic.Type = v })
				}),
			{
				section: "instances." + name, key: "实例版本", path: "instances." + name + ".minecraft_version", kind: kindText,
				get: func(c *config.Config) string { return c.Instances[name].MinecraftVersion },
				set: func(c *config.Config, v string) error {
					return setInstanceField(c, name, func(ic *config.InstanceConfig) { ic.MinecraftVersion = v })
				},
			},
			boolPtrEntry("instances."+name, "启用该实例", "instances."+name+".enabled",
				func(c *config.Config) *bool { return c.Instances[name].Enabled },
				func(c *config.Config, p *bool) {
					_ = setInstanceField(c, name, func(ic *config.InstanceConfig) { ic.Enabled = p })
				}),
		}
		entries = append(entries, inst...)
	}
	return entries
}

// setInstanceField 修改实例覆盖项，必要时创建条目。
func setInstanceField(c *config.Config, name string, fn func(*config.InstanceConfig)) error {
	if c.Instances == nil {
		c.Instances = map[string]config.InstanceConfig{}
	}
	ic, ok := c.Instances[name]
	if !ok {
		if err := config.ValidateInstanceName(name); err != nil {
			return err
		}
	}
	fn(&ic)
	c.Instances[name] = ic
	return nil
}

// boolEntry 构造一个非指针布尔配置项。
func boolEntry(section, key, path string, get func(*config.Config) bool, set func(*config.Config, bool)) cfgEntry {
	return cfgEntry{
		section: section, key: key, path: path, kind: kindBool,
		get: func(c *config.Config) string { return strconv.FormatBool(get(c)) },
		set: func(c *config.Config, v string) error {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return err
			}
			set(c, b)
			return nil
		},
	}
}

// boolPtrEntry 构造一个 *bool 配置项，nil 表示沿用内置默认值。
func boolPtrEntry(section, key, path string, get func(*config.Config) *bool, set func(*config.Config, *bool)) cfgEntry {
	return cfgEntry{
		section: section, key: key, path: path, kind: kindBool,
		get: func(c *config.Config) string {
			p := get(c)
			if p == nil {
				return "（默认）"
			}
			return strconv.FormatBool(*p)
		},
		set: func(c *config.Config, v string) error {
			if v == "" || v == "（默认）" || v == "default" {
				set(c, nil)
				return nil
			}
			b, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("布尔值只能是 true / false（或留空表示默认）")
			}
			set(c, config.BoolPtr(b))
			return nil
		},
	}
}

// durEntry 构造一个时长配置项。
func durEntry(section, key, path string, get func(*config.Config) config.Duration, set func(*config.Config, config.Duration)) cfgEntry {
	return cfgEntry{
		section: section, key: key, path: path, kind: kindDur,
		get: func(c *config.Config) string { return get(c).String() },
		set: func(c *config.Config, v string) error {
			var d config.Duration
			if err := d.UnmarshalText([]byte(v)); err != nil {
				return err
			}
			set(c, d)
			return nil
		},
	}
}

// sizeEntry 构造一个容量配置项。
func sizeEntry(section, key, path string, get func(*config.Config) config.Size, set func(*config.Config, config.Size)) cfgEntry {
	return cfgEntry{
		section: section, key: key, path: path, kind: kindSize,
		get: func(c *config.Config) string { return get(c).String() },
		set: func(c *config.Config, v string) error {
			var s config.Size
			if err := s.UnmarshalText([]byte(v)); err != nil {
				return err
			}
			set(c, s)
			return nil
		},
	}
}

// enumEntry 构造一个枚举配置项（赋值前会校验取值是否在 options 之内）。
func enumEntry(section, key, path string, options []string, get func(*config.Config) string, set func(*config.Config, string)) cfgEntry {
	return cfgEntry{
		section: section, key: key, path: path, kind: kindEnum, options: options,
		get: get,
		set: func(c *config.Config, v string) error {
			if err := validateEnum(options, v); err != nil {
				return err
			}
			set(c, v)
			return nil
		},
	}
}

// validateEnum 校验枚举取值是否在 options 之内。
func validateEnum(options []string, v string) error {
	for _, o := range options {
		if o == v {
			return nil
		}
	}
	shown := make([]string, 0, len(options))
	for _, o := range options {
		if o == "" {
			continue
		}
		shown = append(shown, o)
	}
	return fmt.Errorf("%q 不是合法取值（可选：%s）", v, strings.Join(shown, "/"))
}

// splitHostPort 校验 host:port（避免直接依赖 net 包的错误信息）。
func splitHostPort(s string) (string, string, error) {
	i := strings.LastIndex(s, ":")
	if i <= 0 || i == len(s)-1 {
		return "", "", fmt.Errorf("缺少端口部分")
	}
	host, port := s[:i], s[i+1:]
	if host == "" {
		return "", "", fmt.Errorf("缺少主机部分")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", "", fmt.Errorf("端口 %q 非法", port)
	}
	return host, port, nil
}

// ------------------------------------------------------------------ 编辑器状态

// configState 是配置模式的界面状态。
type configState struct {
	entries  []cfgEntry
	selected int
	offset   int

	editing bool
	buf     string
	cursor  int

	// message 是底部提示（保存结果或校验错误）。
	message string
	// err 表示这条消息是不是错误。
	isErr bool

	// confirmDiscard 在编辑未保存时按 Esc 需要二次确认。
	confirmDiscard bool
	backupPath     string
}

// newConfigState 构造配置模式状态。
func newConfigState(res config.Resolved, cfg *config.Config) *configState {
	cs := &configState{entries: buildConfigEntries(res)}
	// 让列表停在第一条真实存在差异的地方没意义，直接从头开始。
	_ = cfg
	return cs
}

// current 返回当前选中的配置项。
func (cs *configState) current() (cfgEntry, bool) {
	if cs.selected < 0 || cs.selected >= len(cs.entries) {
		return cfgEntry{}, false
	}
	return cs.entries[cs.selected], true
}

// move 上下移动选择。
func (cs *configState) move(delta, visible int) {
	if len(cs.entries) == 0 {
		return
	}
	s := cs.selected + delta
	switch {
	case s < 0:
		s = 0
	case s >= len(cs.entries):
		s = len(cs.entries) - 1
	}
	cs.selected = s
	cs.followCursor(visible)
}

// followCursor 保证选中项在可视范围内。
func (cs *configState) followCursor(visible int) {
	if visible <= 0 {
		return
	}
	if cs.selected < cs.offset {
		cs.offset = cs.selected
	}
	if cs.selected >= cs.offset+visible {
		cs.offset = cs.selected - visible + 1
	}
	if cs.offset < 0 {
		cs.offset = 0
	}
}

// startEdit 开始编辑当前项（布尔项直接在 true/false 之间切换）。
func (cs *configState) startEdit(cfg *config.Config) {
	e, ok := cs.current()
	if !ok {
		return
	}
	cs.editing = true
	cs.confirmDiscard = false
	cs.buf = strings.TrimSpace(e.value(cfg))
	if cs.buf == "（默认）" {
		cs.buf = ""
	}
	cs.cursor = len([]rune(cs.buf))
	cs.message = ""
	cs.isErr = false
	if e.kind == kindBool {
		// 布尔项一键取反，不进入输入行
		cs.editing = false
		cs.toggle(cfg)
	}
	if e.kind == kindEnum {
		cs.cycleEnum(cfg, 1)
		cs.editing = false
	}
}

// toggle 取反布尔项。
func (cs *configState) toggle(cfg *config.Config) {
	e, ok := cs.current()
	if !ok || e.kind != kindBool {
		return
	}
	cur := strings.TrimSpace(e.value(cfg))
	next := "true"
	if cur == "true" {
		next = "false"
	} else if cur == "（默认）" {
		next = "false"
	}
	cs.save(cfg, e, next)
}

// cycleEnum 在枚举值之间轮换。
func (cs *configState) cycleEnum(cfg *config.Config, dir int) {
	e, ok := cs.current()
	if !ok || len(e.options) == 0 {
		return
	}
	cur := strings.TrimSpace(e.value(cfg))
	idx := -1
	for i, o := range e.options {
		if o == cur {
			idx = i
			break
		}
	}
	idx = (idx + dir + len(e.options)) % len(e.options)
	next := e.options[idx]
	if next == "" {
		cs.message = "该选项留空表示沿用实例配置"
		cs.isErr = false
		return
	}
	cs.save(cfg, e, next)
}

// cancelEdit 取消编辑。
func (cs *configState) cancelEdit() {
	cs.editing = false
	cs.buf = ""
	cs.cursor = 0
	cs.confirmDiscard = false
}

// insertRune 在编辑框里插入字符。
func (cs *configState) insertRune(r rune, maxLen int) {
	runes := []rune(cs.buf)
	if cs.cursor < 0 {
		cs.cursor = 0
	}
	if cs.cursor > len(runes) {
		cs.cursor = len(runes)
	}
	if maxLen > 0 && len(runes) >= maxLen {
		return
	}
	runes = append(runes[:cs.cursor], append([]rune{r}, runes[cs.cursor:]...)...)
	cs.buf = string(runes)
	cs.cursor++
}

// backspace 删除光标前一个字符。
func (cs *configState) backspace() {
	runes := []rune(cs.buf)
	if cs.cursor <= 0 || cs.cursor > len(runes) {
		return
	}
	runes = append(runes[:cs.cursor-1], runes[cs.cursor:]...)
	cs.buf = string(runes)
	cs.cursor--
}

// deleteForward 删除光标处字符。
func (cs *configState) deleteForward() {
	runes := []rune(cs.buf)
	if cs.cursor < 0 || cs.cursor >= len(runes) {
		return
	}
	runes = append(runes[:cs.cursor], runes[cs.cursor+1:]...)
	cs.buf = string(runes)
}

// moveCursor 左右移动编辑光标。
func (cs *configState) moveCursor(delta int) {
	cs.cursor += delta
	if cs.cursor < 0 {
		cs.cursor = 0
	}
	if n := len([]rune(cs.buf)); cs.cursor > n {
		cs.cursor = n
	}
}

// commit 校验并保存编辑结果；返回是否成功。
func (cs *configState) commit(cfgPath string) bool {
	e, ok := cs.current()
	if !ok {
		return false
	}
	text := strings.TrimSpace(cs.buf)
	if err := validateCfgValueWith(e.kind, e.options, text); err != nil {
		cs.message = "校验失败：" + err.Error()
		cs.isErr = true
		return false
	}
	if err := cs.saveAndReload(cfgPath, e, text); err != nil {
		cs.message = "保存失败：" + err.Error()
		cs.isErr = true
		return false
	}
	cs.cancelEdit()
	return true
}

// save 直接写入一个新值（用于布尔取反与枚举轮换）。
func (cs *configState) save(cfg *config.Config, e cfgEntry, text string) {
	if err := cs.saveAndReload(configPathOf(cfg), e, text); err != nil {
		cs.message = "保存失败：" + err.Error()
		cs.isErr = true
		return
	}
}

// saveAndReload 是配置保存的核心：
//
//	读盘 → 只改用户编辑的那一项 → 校验 → 原子写回。
//
// 这样并发运行的 supervisor / Web 的热重载一定能拿到一份完整合法的配置。
func (cs *configState) saveAndReload(path string, e cfgEntry, text string) error {
	disk, err := config.Load(path)
	if err != nil {
		return err
	}
	if err := e.apply(disk, text); err != nil {
		return err
	}
	if err := disk.Validate(); err != nil {
		return fmt.Errorf("配置校验不通过：%w", err)
	}
	disk.CleanForSave()
	if err := disk.Save(); err != nil {
		return err
	}
	cs.backupPath = disk.Path + ".bak"
	cs.message = "已保存，运行中的守护进程会自动热重载"
	cs.isErr = false
	return nil
}

// diff 返回本次修改涉及的文件路径（用于界面提示写入位置）。
func (cs *configState) diff(cfgPath string) []string {
	out := []string{cfgPath}
	if cs.backupPath != "" {
		out = append(out, cs.backupPath)
	}
	return out
}

// configPathOf 返回配置文件的落盘路径（仅用于提示）。
func configPathOf(cfg *config.Config) string {
	if cfg != nil && strings.TrimSpace(cfg.Path) != "" {
		return cfg.Path
	}
	return config.ConfigPath()
}

// homePath 返回数据目录（仅用于提示）。
func homePath() string { return config.Home() }

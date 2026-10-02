package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Version 由构建参数注入，用于 User-Agent 与版本展示。
var Version = "dev"

// Default 返回与 nyatmc init 写出的模板保持一致的配置。
func Default() *Config {
	cfg, err := Parse([]byte(DefaultTOML))
	if err != nil {
		// 模板与结构体不一致属于程序缺陷，由单元测试兜底；这里退化为零值 + 补齐。
		cfg = &Config{}
		ApplyDefaults(cfg)
	}
	cfg.Path = ConfigPath()
	return cfg
}

// ApplyDefaults 把零值字段补成默认值。
func ApplyDefaults(c *Config) {
	if strings.TrimSpace(c.General.DefaultInstance) == "" {
		c.General.DefaultInstance = "default"
	}
	if strings.TrimSpace(c.General.Language) == "" {
		c.General.Language = "zh-CN"
	}

	if strings.TrimSpace(c.Server.Type) == "" {
		c.Server.Type = "paper"
	}
	if strings.TrimSpace(c.Server.MinecraftVersion) == "" {
		c.Server.MinecraftVersion = "latest"
	}
	if strings.TrimSpace(c.Server.JavaPath) == "" {
		c.Server.JavaPath = "java"
	}
	if strings.TrimSpace(c.Server.Memory) == "" {
		c.Server.Memory = "2G"
	}
	if strings.TrimSpace(c.Server.MinMemory) == "" {
		c.Server.MinMemory = "512M"
	}
	if c.Server.JVMArgs == nil {
		c.Server.JVMArgs = append([]string(nil), DefaultJVMArgs...)
	}
	if c.Server.ServerArgs == nil {
		c.Server.ServerArgs = []string{"--nogui"}
	}
	if c.Server.Port == 0 {
		c.Server.Port = 25565
	}
	if strings.TrimSpace(c.Server.JarName) == "" {
		c.Server.JarName = "server.jar"
	}
	if strings.TrimSpace(c.Server.WorldName) == "" {
		c.Server.WorldName = "world"
	}
	if c.Server.MaxPlayers == 0 {
		c.Server.MaxPlayers = 20
	}
	if strings.TrimSpace(c.Server.MOTD) == "" {
		c.Server.MOTD = "A NyaTMC powered Minecraft Server"
	}
	if c.Server.ViewDistance == 0 {
		c.Server.ViewDistance = 10
	}
	if c.Server.Properties == nil {
		c.Server.Properties = map[string]string{}
	}

	if strings.TrimSpace(c.Daemon.StopCommand) == "" {
		c.Daemon.StopCommand = "stop"
	}
	if c.Daemon.StopTimeout == 0 {
		c.Daemon.StopTimeout = Duration(60 * time.Second)
	}
	if c.Daemon.StartupTimeout == 0 {
		c.Daemon.StartupTimeout = Duration(5 * time.Minute)
	}
	if c.Daemon.KillTimeout == 0 {
		c.Daemon.KillTimeout = Duration(15 * time.Second)
	}

	if strings.TrimSpace(c.Backup.Format) == "" {
		c.Backup.Format = "tar.gz"
	}
	if c.Backup.Include == nil {
		c.Backup.Include = append([]string(nil), DefaultBackupInclude...)
	}
	if c.Backup.Exclude == nil {
		c.Backup.Exclude = append([]string(nil), DefaultBackupExclude...)
	}

	if c.Log.ConsoleLines == 0 {
		c.Log.ConsoleLines = 500
	}

	if strings.TrimSpace(c.Web.Listen) == "" {
		c.Web.Listen = "127.0.0.1:8080"
	}
	if c.Web.RefreshSeconds == 0 {
		c.Web.RefreshSeconds = 3
	}
	if c.Web.MaxLogLines == 0 {
		c.Web.MaxLogLines = 2000
	}

	if strings.TrimSpace(c.Tunnel.Provider) == "" {
		c.Tunnel.Provider = "cloudflared"
	}
	if strings.TrimSpace(c.Tunnel.Protocol) == "" {
		c.Tunnel.Protocol = "quic"
	}

	if c.Downloader.Timeout == 0 {
		c.Downloader.Timeout = Duration(10 * time.Minute)
	}
	if strings.TrimSpace(c.Downloader.UserAgent) == "" {
		c.Downloader.UserAgent = "nyatmc/" + Version + " (+https://github.com/yuzumikonami/nyatmc)"
	}
	if strings.TrimSpace(c.Downloader.CacheDir) == "" {
		c.Downloader.CacheDir = CacheDir()
	}

	if strings.TrimSpace(c.Mods.Provider) == "" {
		c.Mods.Provider = "modrinth"
	}
	if strings.TrimSpace(c.Mods.ModsDir) == "" {
		c.Mods.ModsDir = "mods"
	}
	if strings.TrimSpace(c.Mods.PluginsDir) == "" {
		c.Mods.PluginsDir = "plugins"
	}
	if c.Mods.Limit == 0 {
		c.Mods.Limit = 20
	}

	if c.Instances == nil {
		c.Instances = map[string]InstanceConfig{}
	}
	// 注意：这里刻意不补齐任何布尔开关。
	// “缺省即 true”的开关（watchdog / compress / online_mode …）用 *bool + 访问器表达，
	// 否则用户把 watchdog 设为 false 会被默认值覆盖回去。
}

// DefaultJVMArgs 是默认的 JVM 调优参数（Aikar 方案的裁剪版）。
var DefaultJVMArgs = []string{
	"-XX:+UseG1GC",
	"-XX:+ParallelRefProcEnabled",
	"-XX:MaxGCPauseMillis=200",
	"-XX:+UnlockExperimentalVMOptions",
	"-XX:+DisableExplicitGC",
	"-XX:+AlwaysPreTouch",
	"-XX:G1NewSizePercent=30",
	"-XX:G1MaxNewSizePercent=40",
	"-XX:G1HeapRegionSize=8M",
	"-XX:G1ReservePercent=20",
	"-XX:G1HeapWastePercent=5",
	"-XX:G1MixedGCCountTarget=4",
	"-XX:InitiatingHeapOccupancyPercent=15",
	"-XX:G1MixedGCLiveThresholdPercent=90",
	"-XX:G1RSetUpdatingPauseTimePercent=5",
	"-XX:SurvivorRatio=32",
	"-XX:+PerfDisableSharedMem",
	"-XX:MaxTenuringThreshold=1",
	"-Dusing.aikars.flags=https://mcflags.emc.gs",
	"-Daikars.new.flags=true",
	"-Dfile.encoding=UTF-8",
}

// DefaultBackupInclude 是默认打包清单。
var DefaultBackupInclude = []string{
	"world",
	"world_nether",
	"world_the_end",
	"server.properties",
	"eula.txt",
	"ops.json",
	"whitelist.json",
	"banned-ips.json",
	"banned-players.json",
	"config",
	"mods",
	"plugins",
	"bukkit.yml",
	"spigot.yml",
	"commands.yml",
	"permissions.yml",
	"paper.yml",
	"paper-global.yml",
	"paper-world-defaults.yml",
	"start.sh",
	"start.bat",
}

// DefaultBackupExclude 是默认排除清单（支持 * 通配）。
var DefaultBackupExclude = []string{
	"logs",
	"cache",
	"backups",
	"libraries",
	"versions",
	".nyatmc",
	"*.tmp",
	"*.log",
	"session.lock",
}

// DefaultTOML 是新配置文件的模板（带中文注释）。
const DefaultTOML = `# ============================================================
#  nyatmc 配置文件
#  路径：~/.nyatmc/config.toml（可用 --home / NYATMC_HOME 覆盖）
#  改完保存即生效：运行中的 supervisor 与 Web 都会热重载。
#  命令行也可以改：nyatmc config set server.memory 4G
# ============================================================

[general]
# 未显式指定实例时使用的实例名
default_instance = "default"
language = "zh-CN"
color = true
# supervisor 启动时是否顺带拉起服务端进程
auto_start = false

[server]
# paper / fabric / vanilla / spigot / purpur / custom
type = "paper"
# "latest" 表示自动取最新正式版
minecraft_version = "latest"
build = ""
loader_version = ""
installer_version = ""
java_path = "java"
memory = "2G"
min_memory = "512M"
port = 25565
max_players = 20
motd = "A NyaTMC powered Minecraft Server"
view_distance = 10
online_mode = true
jar_name = "server.jar"
world_name = "world"
# 是否同意 Minecraft EULA（https://aka.ms/MinecraftEULA），设为 true 才会写入 eula.txt
eula = false
# 启动前自动补齐 / 更新服务端文件
auto_download = true
jvm_args = [
  "-XX:+UseG1GC",
  "-XX:+ParallelRefProcEnabled",
  "-XX:MaxGCPauseMillis=200",
  "-XX:+UnlockExperimentalVMOptions",
  "-XX:+DisableExplicitGC",
  "-XX:+AlwaysPreTouch",
  "-XX:G1NewSizePercent=30",
  "-XX:G1MaxNewSizePercent=40",
  "-XX:G1HeapRegionSize=8M",
  "-XX:G1ReservePercent=20",
  "-XX:G1HeapWastePercent=5",
  "-XX:G1MixedGCCountTarget=4",
  "-XX:InitiatingHeapOccupancyPercent=15",
  "-XX:G1MixedGCLiveThresholdPercent=90",
  "-XX:G1RSetUpdatingPauseTimePercent=5",
  "-XX:SurvivorRatio=32",
  "-XX:+PerfDisableSharedMem",
  "-XX:MaxTenuringThreshold=1",
  "-Dfile.encoding=UTF-8",
]
server_args = ["--nogui"]

# 写入 server.properties 的额外键值（覆盖上面的同名设置）
[server.properties]
# enable-rcon = "true"
# rcon.port = "25575"

[daemon]
# 崩溃自动重启（看门狗）
watchdog = true
# RestartWindow 内最多重启次数，0 表示不限
max_restarts = 5
restart_window = "10m"
restart_delay = "5s"
# 优雅关闭时发往服务端控制台的命令
stop_command = "stop"
stop_timeout = "60s"
startup_timeout = "5m"
kill_timeout = "15s"
# 是否把服务端输出同时写进 nyatmc 控制台日志
capture_console = true
backup_before_start = false

[backup]
# 留空表示 <实例目录>/backups
dir = ""
keep = 10
max_age = "0s"
format = "tar.gz"
before_restore = true
# 备份前先 save-all / save-off，保证世界落盘一致
stop_before_backup = false
include = [
  "world",
  "world_nether",
  "world_the_end",
  "server.properties",
  "eula.txt",
  "ops.json",
  "whitelist.json",
  "banned-ips.json",
  "banned-players.json",
  "config",
  "mods",
  "plugins",
  "bukkit.yml",
  "spigot.yml",
  "commands.yml",
  "permissions.yml",
  "paper.yml",
  "paper-global.yml",
  "paper-world-defaults.yml",
]
exclude = ["logs", "cache", "backups", "libraries", "versions", ".nyatmc", "*.tmp", "*.log", "session.lock"]

[log]
# 超过该大小即归档，并在无人在线时通过优雅重启完成切割
rotate_size = "20MB"
keep_files = 5
max_age_days = 7
compress = true
# 日志超限时是否允许用优雅重启来切割（JVM 持有 latest.log 句柄，无法在线截断）。
# nyatmc 只在“当前没有玩家在线”且距上次切割超过 min_rotate_interval 时才动手，
# 有人在线时会推迟并在控制台提醒，因此不会打断玩家。
rotate_restart = true
min_rotate_interval = "1h"
# 托管服务端 log4j2.xml（写入前会备份原文件，默认关闭）
manage_log4j2 = false
# 控制台环形缓冲行数
console_lines = 500

[scheduler]
enabled = true
timezone = ""
# 内置调度器任务示例（用 nyatmc schedule add 命令添加更省事）
#   nyatmc schedule add daily-backup "0 4 * * *" backup
# [[scheduler.jobs]]
# name = "daily-backup"
# schedule = "0 4 * * *"
# action = "backup"
# instance = "default"
# label = "auto"

[web]
listen = "127.0.0.1:8080"
# 留空且只监听回环地址时免鉴权；对公网暴露请务必设置令牌
token = ""
enable_write = true
refresh_seconds = 3
open = false
max_log_lines = 2000

[tunnel]
provider = "cloudflared"
bin = ""
protocol = "quic"
token = ""
hostname = ""
auto_install = true
auto_start_with_web = false
extra_args = []

[downloader]
# 可选镜像基地址，例如自建 OpenBMCLAPI。
# 注意：公共 BMCLAPI（bmclapi2.bangbang93.com）已不可靠（会跳转到广告域名），
# 留空表示使用官方源；只有你确认可信的镜像才填。
mirror = ""
timeout = "10m"
verify_checksum = true
retries = 3
cache_dir = ""

[mods]
# modrinth（免注册）/ curseforge（需要 API Key）
provider = "modrinth"
curseforge_api_key = ""
mods_dir = "mods"
plugins_dir = "plugins"
game_version = ""
loader = ""
limit = 20

# ============================================================
#  实例覆盖：nyatmc create <名字> 会自动写入这里
# ============================================================
# [instances.survival]
# path = ""
# type = "paper"
# minecraft_version = "1.21.4"
# description = "生存服"
#   [instances.survival.server]
#   port = 25566
#   memory = "4G"
# [instances.creative]
# type = "fabric"
# minecraft_version = "1.21.4"
`

// WriteDefault 按模板写出配置文件。
func WriteDefault(path string) error {
	path = ExpandPath(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建配置目录 %s 失败: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(DefaultTOML), 0o600); err != nil {
		return fmt.Errorf("写入默认配置 %s 失败: %w", path, err)
	}
	return nil
}

// ---------------------------------------------------------------- 校验

var (
	memoryRe   = regexp.MustCompile(`(?i)^\d+(\.\d+)?[kmg]$`)
	validTypes = map[string]bool{
		"paper": true, "fabric": true, "vanilla": true, "spigot": true, "purpur": true, "custom": true,
	}
	validFormats = map[string]bool{"tar.gz": true, "tgz": true, "tar": true, "zip": true}
	validActions = map[string]bool{"backup": true, "restart": true, "stop": true, "start": true, "command": true}
	validModProv = map[string]bool{"modrinth": true, "curseforge": true}
)

// Validate 校验配置，返回聚合后的错误。
func (c *Config) Validate() error {
	var errs []error

	if !validTypes[strings.ToLower(c.Server.Type)] {
		errs = append(errs, fmt.Errorf("server.type = %q 非法，可选：paper/fabric/vanilla/spigot/purpur/custom", c.Server.Type))
	}
	if !memoryRe.MatchString(c.Server.Memory) {
		errs = append(errs, fmt.Errorf("server.memory = %q 非法，示例：2G、2048M", c.Server.Memory))
	}
	if c.Server.MinMemory != "" && !memoryRe.MatchString(c.Server.MinMemory) {
		errs = append(errs, fmt.Errorf("server.min_memory = %q 非法，示例：512M", c.Server.MinMemory))
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		errs = append(errs, fmt.Errorf("server.port = %d 非法，应在 1-65535 之间", c.Server.Port))
	}
	if strings.TrimSpace(c.Server.JarName) == "" || strings.ContainsAny(c.Server.JarName, `/\`) {
		errs = append(errs, fmt.Errorf("server.jar_name = %q 非法，应为不含路径分隔符的文件名", c.Server.JarName))
	}
	if c.Daemon.MaxRestarts < 0 {
		errs = append(errs, fmt.Errorf("daemon.max_restarts = %d 不能为负", c.Daemon.MaxRestarts))
	}
	if c.Daemon.StopTimeout <= 0 {
		errs = append(errs, errors.New("daemon.stop_timeout 必须大于 0"))
	}
	if c.Daemon.StartupTimeout <= 0 {
		errs = append(errs, errors.New("daemon.startup_timeout 必须大于 0"))
	}
	if !validFormats[strings.ToLower(c.Backup.Format)] {
		errs = append(errs, fmt.Errorf("backup.format = %q 非法，可选：tar.gz/tar/zip", c.Backup.Format))
	}
	if c.Backup.Keep < 0 {
		errs = append(errs, fmt.Errorf("backup.keep = %d 不能为负", c.Backup.Keep))
	}
	if c.Log.KeepFiles < 0 {
		errs = append(errs, fmt.Errorf("log.keep_files = %d 不能为负", c.Log.KeepFiles))
	}
	if _, _, err := net.SplitHostPort(c.Web.Listen); err != nil {
		errs = append(errs, fmt.Errorf("web.listen = %q 非法，应形如 127.0.0.1:8080: %w", c.Web.Listen, err))
	}
	if p := strings.ToLower(c.Tunnel.Provider); p != "cloudflared" {
		errs = append(errs, fmt.Errorf("tunnel.provider = %q 暂不支持，目前仅支持 cloudflared", c.Tunnel.Provider))
	}
	if !validModProv[strings.ToLower(c.Mods.Provider)] {
		errs = append(errs, fmt.Errorf("mods.provider = %q 非法，可选：modrinth/curseforge", c.Mods.Provider))
	}
	if c.Downloader.Retries < 0 {
		errs = append(errs, errors.New("downloader.retries 不能为负"))
	}
	if c.Downloader.Timeout <= 0 {
		errs = append(errs, errors.New("downloader.timeout 必须大于 0"))
	}
	if c.Scheduler.Timezone != "" {
		if _, err := time.LoadLocation(c.Scheduler.Timezone); err != nil {
			errs = append(errs, fmt.Errorf("scheduler.timezone = %q 非法: %w", c.Scheduler.Timezone, err))
		}
	}

	seen := map[string]bool{}
	for i, job := range c.Scheduler.Jobs {
		where := fmt.Sprintf("scheduler.jobs[%d]", i)
		name := strings.TrimSpace(job.Name)
		if name == "" {
			errs = append(errs, fmt.Errorf("%s.name 不能为空", where))
		} else if seen[name] {
			errs = append(errs, fmt.Errorf("调度任务名 %q 重复", name))
		}
		seen[name] = true
		if err := ValidateCron(job.Schedule); err != nil {
			errs = append(errs, fmt.Errorf("任务 %s 的 schedule 非法: %w", name, err))
		}
		action := strings.ToLower(strings.TrimSpace(job.Action))
		if !validActions[action] {
			errs = append(errs, fmt.Errorf("任务 %s 的 action = %q 非法，可选：backup/restart/stop/start/command", name, job.Action))
		}
		if action == "command" && strings.TrimSpace(job.Command) == "" {
			errs = append(errs, fmt.Errorf("任务 %s 的 action 为 command 时必须填写 command", name))
		}
		if job.Instance != "" {
			if err := ValidateInstanceName(job.Instance); err != nil {
				errs = append(errs, fmt.Errorf("任务 %s 的 instance 非法: %w", name, err))
			}
		}
	}

	for name, ic := range c.Instances {
		if err := ValidateInstanceName(name); err != nil {
			errs = append(errs, err)
		}
		if ic.Type != "" && !validTypes[strings.ToLower(ic.Type)] {
			errs = append(errs, fmt.Errorf("instances.%s.type = %q 非法", name, ic.Type))
		}
		if ic.Path != "" && strings.TrimSpace(ic.Path) == "" {
			errs = append(errs, fmt.Errorf("instances.%s.path 不能为空白", name))
		}
	}

	return errors.Join(errs...)
}

// Warnings 返回不影响运行但值得提醒的问题。
func (c *Config) Warnings() []string {
	var out []string
	if strings.ToLower(c.Mods.Provider) == "curseforge" && strings.TrimSpace(c.Mods.CurseForgeAPIKey) == "" {
		out = append(out, "mods.provider 为 curseforge 但未配置 curseforge_api_key，模组搜索会失败")
	}
	if !c.Server.EULAEnabled() {
		var unconfirmed []string
		for _, name := range c.InstanceNames() {
			if !c.Resolve(name).Server.EULAEnabled() {
				unconfirmed = append(unconfirmed, name)
			}
		}
		switch {
		case len(unconfirmed) == 0 && len(c.Instances) == 0:
			out = append(out, "尚未同意 Minecraft EULA（server.eula = false），服务端可能拒绝启动；用 --eula 或 nyatmc config set server.eula true 确认")
		case len(unconfirmed) > 0:
			out = append(out, fmt.Sprintf("以下实例尚未同意 Minecraft EULA（服务端会拒绝启动）：%s；可用 nyatmc config set server.eula true <实例> 逐个确认", strings.Join(unconfirmed, "、")))
		}
	}
	if c.Web.Token == "" && !isLoopback(c.Web.Listen) {
		out = append(out, "web.listen 监听非回环地址但未设置 web.token，任何人都能控制你的服务器，强烈建议设置令牌")
	}
	if c.Log.RotateRestartEnabled() {
		out = append(out, "log.rotate_restart = true：日志超限时会通过优雅重启完成切割（JVM 持有 latest.log 句柄）")
	}
	return out
}

func isLoopback(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// cronFieldRe 匹配 cron 字段中允许出现的字符。
var cronFieldRe = regexp.MustCompile(`^[0-9*/,\-?LW#]+$`)

// ValidateCron 校验 5 段（分 时 日 月 周）或 6 段（含秒）cron 表达式。
func ValidateCron(spec string) error {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return errors.New("cron 表达式不能为空（示例：\"0 4 * * *\" 表示每天 4 点）")
	}
	if strings.HasPrefix(spec, "@") {
		switch strings.ToLower(spec) {
		case "@yearly", "@annually", "@monthly", "@weekly", "@daily", "@midnight", "@hourly", "@every":
			return nil
		}
		if strings.HasPrefix(strings.ToLower(spec), "@every ") {
			if _, err := time.ParseDuration(strings.TrimSpace(spec[7:])); err == nil {
				return nil
			}
		}
		return fmt.Errorf("不支持的 cron 宏 %q", spec)
	}
	fields := strings.Fields(spec)
	if len(fields) != 5 && len(fields) != 6 {
		return fmt.Errorf("cron 表达式需要 5 段（分 时 日 月 周），当前 %d 段：%q", len(fields), spec)
	}
	for i, f := range fields {
		if !cronFieldRe.MatchString(f) {
			return fmt.Errorf("cron 第 %d 段 %q 含非法字符", i+1, f)
		}
	}
	return nil
}

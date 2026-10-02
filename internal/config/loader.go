// Package config 提供 nyatmc 的配置定义、加载、保存、实例覆盖合并与热重载能力。
//
// 所有配置集中存放在 ~/.nyatmc/config.toml（可用 NYATMC_HOME / --home / NYATMC_CONFIG 覆盖），
// 全局默认值写在顶层段落中，单个实例的差异通过 [instances.<名字>] 覆盖。
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const (
	// EnvHome 用于覆盖 nyatmc 的数据目录（默认 ~/.nyatmc）。
	EnvHome = "NYATMC_HOME"
	// EnvConfig 用于直接指定配置文件路径。
	EnvConfig = "NYATMC_CONFIG"
	// DefaultDirName 是数据目录的默认名字。
	DefaultDirName = ".nyatmc"
	// FileName 是配置文件名。
	FileName = "config.toml"
)

var homeOverride string

// SetHome 覆盖数据目录（由命令行 --home 参数调用）。
func SetHome(dir string) {
	if strings.TrimSpace(dir) == "" {
		return
	}
	homeOverride = ExpandPath(dir)
}

// Home 返回 nyatmc 的数据目录。
func Home() string {
	if homeOverride != "" {
		return homeOverride
	}
	if v := os.Getenv(EnvHome); strings.TrimSpace(v) != "" {
		return ExpandPath(v)
	}
	hd, err := os.UserHomeDir()
	if err != nil || hd == "" {
		hd = "."
	}
	return filepath.Join(hd, DefaultDirName)
}

// ConfigPath 返回配置文件路径。
func ConfigPath() string {
	if v := os.Getenv(EnvConfig); strings.TrimSpace(v) != "" {
		return ExpandPath(v)
	}
	return filepath.Join(Home(), FileName)
}

// StateDir 返回运行期状态目录（调度器记录、锁文件等）。
func StateDir() string { return filepath.Join(Home(), "state") }

// InstancesDir 返回全部实例目录的父目录。
func InstancesDir() string { return filepath.Join(Home(), "instances") }

// CacheDir 返回下载缓存目录。
func CacheDir() string { return filepath.Join(Home(), "cache") }

// ExpandPath 展开开头的 ~ 并清理路径。
func ExpandPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return p
	}
	if p == "~" {
		if hd, err := os.UserHomeDir(); err == nil && hd != "" {
			return hd
		}
		return p
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if hd, err := os.UserHomeDir(); err == nil && hd != "" {
			return filepath.Join(hd, p[2:])
		}
	}
	return filepath.Clean(p)
}

// ---------------------------------------------------------------- 基础类型

// Duration 是支持 "10s" / "5m" / "1h30m" / 纯秒数 的时长类型。
type Duration time.Duration

// UnmarshalText 实现 encoding.TextUnmarshaler。
func (d *Duration) UnmarshalText(text []byte) error {
	s := strings.TrimSpace(string(text))
	if s == "" {
		*d = 0
		return nil
	}
	if v, err := time.ParseDuration(strings.ToLower(s)); err == nil {
		*d = Duration(v)
		return nil
	}
	// 支持 "2d" 这类天数写法
	if n, ok := strings.CutSuffix(strings.ToLower(s), "d"); ok {
		if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
			*d = Duration(time.Duration(f * float64(24*time.Hour)))
			return nil
		}
	}
	// 支持纯数字（按秒）
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		*d = Duration(time.Duration(f * float64(time.Second)))
		return nil
	}
	return fmt.Errorf("无效的时长 %q（示例：30s、10m、1h30m、2d）", s)
}

// MarshalText 实现 encoding.TextMarshaler。
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// String 返回可读时长。
func (d Duration) String() string {
	if d == 0 {
		return "0s"
	}
	return time.Duration(d).String()
}

// Std 转换为标准库时长。
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Seconds 返回秒数（浮点）。
func (d Duration) Seconds() float64 { return time.Duration(d).Seconds() }

// Size 是支持 "20MB" / "1GiB" / "1048576" 的容量类型。
//
// 单位一律按 1024 进制解释（KB=KiB、MB=MiB、GB=GiB），与 JVM -Xmx2G 的语义一致，
// 避免出现“配置写 20MB、实际只有 19MiB”的困惑。
type Size int64

var sizeRe = regexp.MustCompile(`(?i)^\s*([0-9]*\.?[0-9]+)\s*(b|k|kb|kib|m|mb|mib|g|gb|gib|t|tb|tib)?\s*$`)

// UnmarshalText 实现 encoding.TextUnmarshaler。
func (s *Size) UnmarshalText(text []byte) error {
	raw := strings.TrimSpace(string(text))
	if raw == "" {
		*s = 0
		return nil
	}
	m := sizeRe.FindStringSubmatch(raw)
	if m == nil {
		return fmt.Errorf("无效的容量 %q（示例：512KB、20MB、1GB）", raw)
	}
	f, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return fmt.Errorf("无效的容量 %q: %w", raw, err)
	}
	mult := float64(1)
	switch strings.ToLower(m[2]) {
	case "", "b":
		mult = 1
	case "k", "kb", "kib":
		mult = 1024
	case "m", "mb", "mib":
		mult = 1024 * 1024
	case "g", "gb", "gib":
		mult = 1024 * 1024 * 1024
	case "t", "tb", "tib":
		mult = 1024 * 1024 * 1024 * 1024
	}
	*s = Size(f * mult)
	return nil
}

// MarshalText 实现 encoding.TextMarshaler。
func (s Size) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// Bytes 返回字节数。
func (s Size) Bytes() int64 { return int64(s) }

// String 返回人类可读容量。
func (s Size) String() string {
	n := int64(s)
	switch {
	case n >= 1024*1024*1024 && n%(1024*1024*1024) == 0:
		return fmt.Sprintf("%dGB", n/(1024*1024*1024))
	case n >= 1024*1024 && n%(1024*1024) == 0:
		return fmt.Sprintf("%dMB", n/(1024*1024))
	case n >= 1024 && n%1024 == 0:
		return fmt.Sprintf("%dKB", n/1024)
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// ---------------------------------------------------------------- 配置结构

// Config 是 nyatmc 的完整配置。
type Config struct {
	General    GeneralConfig             `toml:"general" json:"general"`
	Server     ServerConfig              `toml:"server" json:"server"`
	Daemon     DaemonConfig              `toml:"daemon" json:"daemon"`
	Backup     BackupConfig              `toml:"backup" json:"backup"`
	Log        LogConfig                 `toml:"log" json:"log"`
	Scheduler  SchedulerConfig           `toml:"scheduler" json:"scheduler"`
	Web        WebConfig                 `toml:"web" json:"web"`
	Tunnel     TunnelConfig              `toml:"tunnel" json:"tunnel"`
	Downloader DownloaderConfig          `toml:"downloader" json:"downloader"`
	Mods       ModsConfig                `toml:"mods" json:"mods"`
	Instances  map[string]InstanceConfig `toml:"instances" json:"instances"`

	// Path 记录配置来源文件，不写入文件本身。
	Path string `toml:"-" json:"path"`
	// Created 表示本次加载时文件是否由 nyatmc 新建。
	Created bool `toml:"-" json:"created"`
}

// GeneralConfig 是全局行为设置。
type GeneralConfig struct {
	// DefaultInstance 是未显式指定实例时使用的实例名。
	DefaultInstance string `toml:"default_instance" json:"default_instance"`
	// Language 预留的界面语言标记。
	Language string `toml:"language" json:"language"`
	// Color 控制命令行是否使用颜色。
	Color bool `toml:"color" json:"color"`
	// AutoStart 表示 supervisor 启动时是否顺带拉起服务器进程。
	AutoStart bool `toml:"auto_start" json:"auto_start"`
	// AutoUpdateCheck 预留：启动时检查新版本。
	AutoUpdateCheck bool `toml:"auto_update_check" json:"auto_update_check"`
}

// ServerConfig 描述 Minecraft 服务器本体如何运行。
type ServerConfig struct {
	// Type 支持 paper / fabric / vanilla / spigot / purpur / custom。
	Type string `toml:"type" json:"type"`
	// MinecraftVersion 可以是具体版本，也可以是 "latest"。
	MinecraftVersion string `toml:"minecraft_version" json:"minecraft_version"`
	// Build 是 Paper 的构建号，留空则用最新构建。
	Build string `toml:"build" json:"build"`
	// LoaderVersion 是 Fabric loader 版本，留空则用最新。
	LoaderVersion string `toml:"loader_version" json:"loader_version"`
	// InstallerVersion 是 Fabric installer 版本，留空则用最新。
	InstallerVersion string `toml:"installer_version" json:"installer_version"`
	// JavaPath 是 java 可执行文件。
	JavaPath string `toml:"java_path" json:"java_path"`
	// Memory 是最大堆内存，例如 2G。
	Memory string `toml:"memory" json:"memory"`
	// MinMemory 是最小堆内存，例如 512M。
	MinMemory string `toml:"min_memory" json:"min_memory"`
	// JVMArgs 是额外的 JVM 参数。
	JVMArgs []string `toml:"jvm_args" json:"jvm_args"`
	// ServerArgs 是传给服务端的参数。
	ServerArgs []string `toml:"server_args" json:"server_args"`
	// Port 是监听端口，会写入 server.properties。
	Port int `toml:"port" json:"port"`
	// EULA 表示同意 Minecraft 最终用户许可协议（必须为 true 才能启动官方服务端）。
	// 用指针是为了区分“没写”和“显式 false”。
	EULA *bool `toml:"eula" json:"eula"`
	// AutoDownload 表示启动前检查并补齐服务端文件。
	AutoDownload *bool `toml:"auto_download" json:"auto_download"`
	// JarName 是服务端主 jar 的文件名。
	JarName string `toml:"jar_name" json:"jar_name"`
	// WorldName 是世界目录名，用于备份清单。
	WorldName string `toml:"world_name" json:"world_name"`
	// Properties 会覆盖 server.properties 中的同名键。
	Properties map[string]string `toml:"properties" json:"properties"`
	// OnlineMode 对应 server.properties 的 online-mode。
	OnlineMode *bool `toml:"online_mode" json:"online_mode"`
	// MaxPlayers 对应 server.properties 的 max-players。
	MaxPlayers int `toml:"max_players" json:"max_players"`
	// MOTD 对应 server.properties 的 motd。
	MOTD string `toml:"motd" json:"motd"`
	// ViewDistance 对应 server.properties 的 view-distance。
	ViewDistance int `toml:"view_distance" json:"view_distance"`
}

// DaemonConfig 描述进程守护行为。
type DaemonConfig struct {
	// Watchdog 是否在崩溃后自动重启。
	Watchdog *bool `toml:"watchdog" json:"watchdog"`
	// MaxRestarts 是在 RestartWindow 内允许的最大重启次数，0 表示不限制。
	MaxRestarts int `toml:"max_restarts" json:"max_restarts"`
	// RestartWindow 是重启次数统计窗口。
	RestartWindow Duration `toml:"restart_window" json:"restart_window"`
	// RestartDelay 是崩溃后等待多久再重启。
	RestartDelay Duration `toml:"restart_delay" json:"restart_delay"`
	// StopCommand 是优雅关闭时发送到服务端控制台的命令。
	StopCommand string `toml:"stop_command" json:"stop_command"`
	// StopTimeout 是等待服务端退出的最长时间，超时后强杀。
	StopTimeout Duration `toml:"stop_timeout" json:"stop_timeout"`
	// StartupTimeout 是等待服务端就绪的最长时间。
	StartupTimeout Duration `toml:"startup_timeout" json:"startup_timeout"`
	// CaptureConsole 是否把服务端输出同时写入 nyatmc 的控制台日志。
	CaptureConsole *bool `toml:"capture_console" json:"capture_console"`
	// BackupBeforeStart 在启动前自动备份。
	BackupBeforeStart *bool `toml:"backup_before_start" json:"backup_before_start"`
	// KillTimeout 强杀后的等待时间。
	KillTimeout Duration `toml:"kill_timeout" json:"kill_timeout"`
}

// BackupConfig 描述备份与回滚策略。
type BackupConfig struct {
	// Dir 是备份目录，留空则使用实例目录下的 backups。
	Dir string `toml:"dir" json:"dir"`
	// Keep 是保留的备份份数，0 表示不自动清理。
	Keep int `toml:"keep" json:"keep"`
	// MaxAge 是备份最长保留时间，0 表示不按时间清理。
	MaxAge Duration `toml:"max_age" json:"max_age"`
	// Include 是要打包的路径（相对服务器目录）。
	Include []string `toml:"include" json:"include"`
	// Exclude 是要跳过的路径（相对服务器目录，支持 * 通配）。
	Exclude []string `toml:"exclude" json:"exclude"`
	// Format 支持 tar.gz / tar / zip。
	Format string `toml:"format" json:"format"`
	// BeforeRestore 表示回滚前先自动备份当前状态。
	BeforeRestore *bool `toml:"before_restore" json:"before_restore"`
	// StopBeforeBackup 表示备份正在运行的服务器前先执行 save-all 与 save-off。
	StopBeforeBackup *bool `toml:"stop_before_backup" json:"stop_before_backup"`
}

// LogConfig 描述日志轮转策略。
type LogConfig struct {
	// RotateSize 是触发轮转的日志大小。
	RotateSize Size `toml:"rotate_size" json:"rotate_size"`
	// KeepFiles 是保留的历史日志份数。
	KeepFiles int `toml:"keep_files" json:"keep_files"`
	// MaxAgeDays 是历史日志最长保留天数。
	MaxAgeDays int `toml:"max_age_days" json:"max_age_days"`
	// Compress 是否压缩归档日志。
	Compress *bool `toml:"compress" json:"compress"`
	// RotateRestart 表示超限时通过优雅重启完成切割（JVM 持有文件句柄，无法在线截断）。
	RotateRestart *bool `toml:"rotate_restart" json:"rotate_restart"`
	// MinRotateInterval 是两次重启切割之间的最小间隔。
	MinRotateInterval Duration `toml:"min_rotate_interval" json:"min_rotate_interval"`
	// ManageLog4j2 表示托管服务端 log4j2.xml（写入前先备份，默认关闭）。
	ManageLog4j2 *bool `toml:"manage_log4j2" json:"manage_log4j2"`
	// ConsoleLines 是控制台环形缓冲保留的行数。
	ConsoleLines int `toml:"console_lines" json:"console_lines"`
}

// SchedulerConfig 描述内置调度器。
type SchedulerConfig struct {
	// Enabled 是否启用内置调度器。
	Enabled bool `toml:"enabled" json:"enabled"`
	// Timezone 是解释 cron 表达式使用的时区，留空表示本机时区。
	Timezone string `toml:"timezone" json:"timezone"`
	// Jobs 是任务列表，对应 [[scheduler.jobs]]。
	Jobs []Job `toml:"jobs" json:"jobs"`
}

// Job 是一条定时任务。
type Job struct {
	Name string `toml:"name" json:"name"`
	// Schedule 是 5 段 cron 表达式：分 时 日 月 周。
	Schedule string `toml:"schedule" json:"schedule"`
	// Action 支持 backup / restart / stop / start / command。
	Action string `toml:"action" json:"action"`
	// Instance 是目标实例，留空表示所有实例或默认实例。
	Instance string `toml:"instance" json:"instance"`
	// Command 在 Action 为 command 时执行。
	Command string `toml:"command" json:"command"`
	// Enabled 为 nil 表示启用。
	Enabled *bool `toml:"enabled" json:"enabled,omitempty"`
	// Label 会附加到自动备份的文件名上。
	Label string `toml:"label" json:"label"`
}

// IsEnabled 返回任务是否启用。
func (j Job) IsEnabled() bool { return j.Enabled == nil || *j.Enabled }

// WebConfig 描述 Web 仪表盘。
type WebConfig struct {
	// Listen 是监听地址。
	Listen string `toml:"listen" json:"listen"`
	// Token 是访问令牌，留空则只在监听回环地址时免鉴权。
	Token string `toml:"token" json:"token"`
	// EnableWrite 控制是否允许通过 Web 执行写操作（启停、回滚等）。
	EnableWrite bool `toml:"enable_write" json:"enable_write"`
	// RefreshSeconds 是前端自动刷新间隔。
	RefreshSeconds int `toml:"refresh_seconds" json:"refresh_seconds"`
	// Open 表示启动后自动打开浏览器。
	Open bool `toml:"open" json:"open"`
	// MaxLogLines 是单次日志接口返回的最大行数。
	MaxLogLines int `toml:"max_log_lines" json:"max_log_lines"`
}

// TunnelConfig 描述 Cloudflare 内网穿透。
type TunnelConfig struct {
	// Provider 目前仅支持 cloudflared。
	Provider string `toml:"provider" json:"provider"`
	// Bin 是 cloudflared 可执行文件路径，留空表示自动查找或下载。
	Bin string `toml:"bin" json:"bin"`
	// Protocol 是 cloudflared 传输协议。
	Protocol string `toml:"protocol" json:"protocol"`
	// Token 用于命名隧道（cloudflared tunnel run --token），留空表示使用快速隧道。
	Token string `toml:"token" json:"token"`
	// Hostname 是命名隧道对应的域名。
	Hostname string `toml:"hostname" json:"hostname"`
	// AutoInstall 表示缺少 cloudflared 时自动下载。
	AutoInstall bool `toml:"auto_install" json:"auto_install"`
	// AutoStartWithWeb 表示 web serve 启动时一并启动隧道。
	AutoStartWithWeb bool `toml:"auto_start_with_web" json:"auto_start_with_web"`
	// ExtraArgs 是追加给 cloudflared 的参数。
	ExtraArgs []string `toml:"extra_args" json:"extra_args"`
}

// DownloaderConfig 描述下载行为。
type DownloaderConfig struct {
	// Mirror 是可选镜像（BMCLAPI 等），留空表示官方源。
	Mirror string `toml:"mirror" json:"mirror"`
	// Timeout 是单次下载超时。
	Timeout Duration `toml:"timeout" json:"timeout"`
	// VerifyChecksum 是否校验官方提供的哈希。
	VerifyChecksum bool `toml:"verify_checksum" json:"verify_checksum"`
	// Retries 是失败重试次数。
	Retries int `toml:"retries" json:"retries"`
	// UserAgent 是请求头。
	UserAgent string `toml:"user_agent" json:"user_agent"`
	// CacheDir 是下载缓存目录，留空表示 ~/.nyatmc/cache。
	CacheDir string `toml:"cache_dir" json:"cache_dir"`
}

// ModsConfig 描述模组 / 插件下载源。
type ModsConfig struct {
	// Provider 支持 modrinth / curseforge。
	Provider string `toml:"provider" json:"provider"`
	// CurseForgeAPIKey 是 CurseForge 的 API Key（必需）。
	CurseForgeAPIKey string `toml:"curseforge_api_key" json:"curseforge_api_key"`
	// ModsDir 是模组目录（相对服务器目录）。
	ModsDir string `toml:"mods_dir" json:"mods_dir"`
	// PluginsDir 是插件目录（相对服务器目录）。
	PluginsDir string `toml:"plugins_dir" json:"plugins_dir"`
	// GameVersion 留空表示沿用实例的服务端版本。
	GameVersion string `toml:"game_version" json:"game_version"`
	// Loader 留空表示按实例类型推断。
	Loader string `toml:"loader" json:"loader"`
	// Limit 是搜索返回条数。
	Limit int `toml:"limit" json:"limit"`
}

// InstanceConfig 是单个实例对全局默认值的覆盖。
type InstanceConfig struct {
	// Path 是服务器目录，留空表示 ~/.nyatmc/instances/<名字>/server。
	Path string `toml:"path" json:"path"`
	// Enabled 为 false 时调度器与 Web 会跳过该实例。
	Enabled *bool `toml:"enabled" json:"enabled,omitempty"`
	// Type 是实例类型的快捷覆盖。
	Type string `toml:"type" json:"type"`
	// MinecraftVersion 是实例版本的快捷覆盖。
	MinecraftVersion string `toml:"minecraft_version" json:"minecraft_version"`
	// CreatedAt 是实例创建时间。
	CreatedAt string `toml:"created_at" json:"created_at"`
	// Server 是细粒度覆盖。
	Server *ServerConfig `toml:"server" json:"server,omitempty"`
	// Daemon 是细粒度覆盖。
	Daemon *DaemonConfig `toml:"daemon" json:"daemon,omitempty"`
	// Backup 是细粒度覆盖。
	Backup *BackupConfig `toml:"backup" json:"backup,omitempty"`
	// Log 是细粒度覆盖。
	Log *LogConfig `toml:"log" json:"log,omitempty"`
	// Description 是实例备注。
	Description string `toml:"description" json:"description"`
}

// Resolved 是合并实例覆盖后的最终配置，所有路径字段都已经算好。
type Resolved struct {
	Instance  string `json:"instance"`
	Enabled   bool   `json:"enabled"`
	Path      string `json:"path"`
	Root      string `json:"root"`
	MetaDir   string `json:"meta_dir"`
	JarPath   string `json:"jar_path"`
	LogsDir   string `json:"logs_dir"`
	Console   string `json:"console_log"`
	SupLog    string `json:"supervisor_log"`
	Endpoint  string `json:"endpoint"`
	StateFile string `json:"state_file"`
	LockFile  string `json:"lock_file"`
	PIDFile   string `json:"pid_file"`
	BackupDir string `json:"backup_dir"`

	General    GeneralConfig    `json:"general"`
	Server     ServerConfig     `json:"server"`
	Daemon     DaemonConfig     `json:"daemon"`
	Backup     BackupConfig     `json:"backup"`
	Log        LogConfig        `json:"log"`
	Scheduler  SchedulerConfig  `json:"scheduler"`
	Web        WebConfig        `json:"web"`
	Tunnel     TunnelConfig     `json:"tunnel"`
	Downloader DownloaderConfig `json:"downloader"`
	Mods       ModsConfig       `json:"mods"`
}

// ---------------------------------------------------------------- 加载与保存

// 允许任意语言的字母与数字，以及 _ . -（拒绝路径分隔符，避免穿越目录）。
var instanceNameRe = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}_.\-]{0,63}$`)

// ValidateInstanceName 校验实例名是否合法。
func ValidateInstanceName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("实例名不能为空")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("实例名 %q 非法", name)
	}
	if !instanceNameRe.MatchString(name) {
		return fmt.Errorf("实例名 %q 非法：只允许字母、数字、下划线、短横线、点与中文，且不超过 64 个字符", name)
	}
	return nil
}

// Load 读取配置；path 为空时使用 ConfigPath()。
func Load(path string) (*Config, error) {
	if strings.TrimSpace(path) == "" {
		path = ConfigPath()
	}
	path = ExpandPath(path)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置 %s 失败: %w", path, err)
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("解析配置 %s 失败: %w", path, err)
	}
	cfg.Path = path
	return cfg, nil
}

// Parse 解析 TOML 文本并补齐默认值。
func Parse(data []byte) (*Config, error) {
	cfg := &Config{}
	dec := toml.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return nil, err
	}
	ApplyDefaults(cfg)
	return cfg, nil
}

// LoadOrCreate 在配置不存在时按模板创建，然后加载。
func LoadOrCreate(path string) (*Config, error) {
	if strings.TrimSpace(path) == "" {
		path = ConfigPath()
	}
	path = ExpandPath(path)
	if _, err := os.Stat(path); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("访问配置 %s 失败: %w", path, err)
		}
		if err := WriteDefault(path); err != nil {
			return nil, err
		}
		cfg, err := Load(path)
		if err != nil {
			return nil, err
		}
		cfg.Created = true
		return cfg, nil
	}
	return Load(path)
}

// Save 原子写回配置文件（写前备份为 config.toml.bak）。
func (c *Config) Save() error {
	path := c.Path
	if strings.TrimSpace(path) == "" {
		path = ConfigPath()
	}
	return c.SaveTo(path)
}

// SaveTo 原子写入指定路径。
func (c *Config) SaveTo(path string) error {
	path = ExpandPath(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	data, err := toml.Marshal(c)
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	header := "# nyatmc 配置文件（由 nyatmc 自动维护，可手工编辑）\n" +
		"# 修改后运行中的 supervisor / Web 会自动热重载。\n\n"
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append([]byte(header), data...), 0o600); err != nil {
		return fmt.Errorf("写入配置失败: %w", err)
	}
	if old, err := os.ReadFile(path); err == nil && len(old) > 0 {
		_ = os.WriteFile(path+".bak", old, 0o600)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换配置失败: %w", err)
	}
	c.Path = path
	return nil
}

// InstanceNames 返回排序后的实例名列表。
func (c *Config) InstanceNames() []string {
	names := make([]string, 0, len(c.Instances))
	for name := range c.Instances {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// HasInstance 判断实例是否已登记。
func (c *Config) HasInstance(name string) bool {
	_, ok := c.Instances[name]
	return ok
}

// AddInstance 登记一个实例。
func (c *Config) AddInstance(name string, ic InstanceConfig) error {
	if err := ValidateInstanceName(name); err != nil {
		return err
	}
	if c.Instances == nil {
		c.Instances = map[string]InstanceConfig{}
	}
	if _, exists := c.Instances[name]; exists {
		return fmt.Errorf("实例 %q 已存在", name)
	}
	if strings.TrimSpace(ic.CreatedAt) == "" {
		ic.CreatedAt = time.Now().Format(time.RFC3339)
	}
	c.Instances[name] = ic
	return nil
}

// RemoveInstance 删除实例登记（不删除文件）。
func (c *Config) RemoveInstance(name string) error {
	if !c.HasInstance(name) {
		return fmt.Errorf("实例 %q 不存在", name)
	}
	delete(c.Instances, name)
	if c.General.DefaultInstance == name {
		c.General.DefaultInstance = ""
	}
	return nil
}

// InstanceRoot 返回实例的元数据目录（与服务器目录分开）。
func (c *Config) InstanceRoot(name string) string {
	return filepath.Join(InstancesDir(), name)
}

// InstanceMetaDir 返回实例的私有元数据目录。
func (c *Config) InstanceMetaDir(name string) string {
	return filepath.Join(c.InstanceRoot(name), ".nyatmc")
}

// InstanceServerDir 返回实例的服务器目录。
func (c *Config) InstanceServerDir(name string) string {
	if ic, ok := c.Instances[name]; ok && strings.TrimSpace(ic.Path) != "" {
		return ExpandPath(ic.Path)
	}
	return filepath.Join(c.InstanceRoot(name), "server")
}

// Resolve 合并全局默认值与实例覆盖，并计算全部路径。
func (c *Config) Resolve(name string) Resolved {
	if strings.TrimSpace(name) == "" {
		name = c.General.DefaultInstance
	}
	ic := c.Instances[name]

	server := mergeServer(c.Server, ic.Server)
	if ic.Type != "" {
		server.Type = ic.Type
	}
	if ic.MinecraftVersion != "" {
		server.MinecraftVersion = ic.MinecraftVersion
	}
	daemon := mergeDaemon(c.Daemon, ic.Daemon)
	backup := mergeBackup(c.Backup, ic.Backup)
	logc := mergeLog(c.Log, ic.Log)
	dl := c.Downloader
	mods := c.Mods

	root := c.InstanceRoot(name)
	meta := c.InstanceMetaDir(name)
	serverDir := c.InstanceServerDir(name)

	backupDir := strings.TrimSpace(backup.Dir)
	if backupDir == "" {
		backupDir = filepath.Join(root, "backups")
	} else {
		backupDir = ExpandPath(backupDir)
	}
	if strings.TrimSpace(dl.CacheDir) == "" {
		dl.CacheDir = CacheDir()
	} else {
		dl.CacheDir = ExpandPath(dl.CacheDir)
	}

	enabled := true
	if ic.Enabled != nil {
		enabled = *ic.Enabled
	}

	return Resolved{
		Instance:  name,
		Enabled:   enabled,
		Path:      serverDir,
		Root:      root,
		MetaDir:   meta,
		JarPath:   filepath.Join(serverDir, server.JarName),
		LogsDir:   filepath.Join(serverDir, "logs"),
		Console:   filepath.Join(meta, "console.log"),
		SupLog:    filepath.Join(meta, "supervisor.log"),
		Endpoint:  filepath.Join(meta, "endpoint.json"),
		StateFile: filepath.Join(meta, "state.json"),
		LockFile:  filepath.Join(meta, "supervisor.lock"),
		PIDFile:   filepath.Join(meta, "supervisor.pid"),
		BackupDir: backupDir,

		General:    c.General,
		Server:     server,
		Daemon:     daemon,
		Backup:     backup,
		Log:        logc,
		Scheduler:  c.Scheduler,
		Web:        c.Web,
		Tunnel:     c.Tunnel,
		Downloader: dl,
		Mods:       mods,
	}
}

// ---------------------------------------------------------------- 合并辅助

func mergeServer(base ServerConfig, o *ServerConfig) ServerConfig {
	if o == nil {
		return base
	}
	if o.Type != "" {
		base.Type = o.Type
	}
	if o.MinecraftVersion != "" {
		base.MinecraftVersion = o.MinecraftVersion
	}
	if o.Build != "" {
		base.Build = o.Build
	}
	if o.LoaderVersion != "" {
		base.LoaderVersion = o.LoaderVersion
	}
	if o.InstallerVersion != "" {
		base.InstallerVersion = o.InstallerVersion
	}
	if o.JavaPath != "" {
		base.JavaPath = o.JavaPath
	}
	if o.Memory != "" {
		base.Memory = o.Memory
	}
	if o.MinMemory != "" {
		base.MinMemory = o.MinMemory
	}
	if o.JVMArgs != nil {
		base.JVMArgs = append([]string(nil), o.JVMArgs...)
	}
	if o.ServerArgs != nil {
		base.ServerArgs = append([]string(nil), o.ServerArgs...)
	}
	if o.Port != 0 {
		base.Port = o.Port
	}
	if o.EULA != nil {
		base.EULA = o.EULA
	}
	if o.AutoDownload != nil {
		base.AutoDownload = o.AutoDownload
	}
	if o.JarName != "" {
		base.JarName = o.JarName
	}
	if o.WorldName != "" {
		base.WorldName = o.WorldName
	}
	if o.OnlineMode != nil {
		base.OnlineMode = o.OnlineMode
	}
	if o.MaxPlayers != 0 {
		base.MaxPlayers = o.MaxPlayers
	}
	if o.MOTD != "" {
		base.MOTD = o.MOTD
	}
	if o.ViewDistance != 0 {
		base.ViewDistance = o.ViewDistance
	}
	if len(o.Properties) > 0 {
		if base.Properties == nil {
			base.Properties = map[string]string{}
		}
		for k, v := range o.Properties {
			base.Properties[k] = v
		}
	}
	return base
}

func mergeDaemon(base DaemonConfig, o *DaemonConfig) DaemonConfig {
	if o == nil {
		return base
	}
	if o.Watchdog != nil {
		base.Watchdog = o.Watchdog
	}
	if o.MaxRestarts != 0 {
		base.MaxRestarts = o.MaxRestarts
	}
	if o.RestartWindow != 0 {
		base.RestartWindow = o.RestartWindow
	}
	if o.RestartDelay != 0 {
		base.RestartDelay = o.RestartDelay
	}
	if o.StopCommand != "" {
		base.StopCommand = o.StopCommand
	}
	if o.StopTimeout != 0 {
		base.StopTimeout = o.StopTimeout
	}
	if o.StartupTimeout != 0 {
		base.StartupTimeout = o.StartupTimeout
	}
	if o.CaptureConsole != nil {
		base.CaptureConsole = o.CaptureConsole
	}
	if o.BackupBeforeStart != nil {
		base.BackupBeforeStart = o.BackupBeforeStart
	}
	if o.KillTimeout != 0 {
		base.KillTimeout = o.KillTimeout
	}
	return base
}

func mergeBackup(base BackupConfig, o *BackupConfig) BackupConfig {
	if o == nil {
		return base
	}
	if o.Dir != "" {
		base.Dir = o.Dir
	}
	if o.Keep != 0 {
		base.Keep = o.Keep
	}
	if o.MaxAge != 0 {
		base.MaxAge = o.MaxAge
	}
	if o.Include != nil {
		base.Include = append([]string(nil), o.Include...)
	}
	if o.Exclude != nil {
		base.Exclude = append([]string(nil), o.Exclude...)
	}
	if o.Format != "" {
		base.Format = o.Format
	}
	if o.BeforeRestore != nil {
		base.BeforeRestore = o.BeforeRestore
	}
	if o.StopBeforeBackup != nil {
		base.StopBeforeBackup = o.StopBeforeBackup
	}
	return base
}

func mergeLog(base LogConfig, o *LogConfig) LogConfig {
	if o == nil {
		return base
	}
	if o.RotateSize != 0 {
		base.RotateSize = o.RotateSize
	}
	if o.KeepFiles != 0 {
		base.KeepFiles = o.KeepFiles
	}
	if o.MaxAgeDays != 0 {
		base.MaxAgeDays = o.MaxAgeDays
	}
	if o.Compress != nil {
		base.Compress = o.Compress
	}
	if o.RotateRestart != nil {
		base.RotateRestart = o.RotateRestart
	}
	if o.MinRotateInterval != 0 {
		base.MinRotateInterval = o.MinRotateInterval
	}
	if o.ManageLog4j2 != nil {
		base.ManageLog4j2 = o.ManageLog4j2
	}
	if o.ConsoleLines != 0 {
		base.ConsoleLines = o.ConsoleLines
	}
	return base
}

// ---------------------------------------------------------------- 开关访问器
//
// TOML 里这些开关可能缺省，访问器给出缺省语义，避免调用方到处判断 nil。

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// BoolPtr 便于构造 *bool（配置写入、命令行开关）。
func BoolPtr(v bool) *bool { return &v }

// EULAEnabled 是否已同意 Minecraft EULA（缺省 false）。
func (s ServerConfig) EULAEnabled() bool { return boolOr(s.EULA, false) }

// AutoDownloadEnabled 是否在启动前自动补齐服务端文件（缺省 true）。
func (s ServerConfig) AutoDownloadEnabled() bool { return boolOr(s.AutoDownload, true) }

// OnlineModeEnabled 是否开启正版验证（缺省 true）。
func (s ServerConfig) OnlineModeEnabled() bool { return boolOr(s.OnlineMode, true) }

// WatchdogEnabled 是否启用崩溃自动重启（缺省 true）。
func (d DaemonConfig) WatchdogEnabled() bool { return boolOr(d.Watchdog, true) }

// CaptureConsoleEnabled 是否把服务端输出写入 nyatmc 控制台日志（缺省 true）。
func (d DaemonConfig) CaptureConsoleEnabled() bool { return boolOr(d.CaptureConsole, true) }

// BackupBeforeStartEnabled 是否启动前备份（缺省 false）。
func (d DaemonConfig) BackupBeforeStartEnabled() bool { return boolOr(d.BackupBeforeStart, false) }

// BeforeRestoreEnabled 回滚前是否自动备份当前状态（缺省 true）。
func (b BackupConfig) BeforeRestoreEnabled() bool { return boolOr(b.BeforeRestore, true) }

// StopBeforeBackupEnabled 备份前是否先 save-all / save-off（缺省 false）。
func (b BackupConfig) StopBeforeBackupEnabled() bool { return boolOr(b.StopBeforeBackup, false) }

// CompressEnabled 归档日志是否压缩（缺省 true）。
func (l LogConfig) CompressEnabled() bool { return boolOr(l.Compress, true) }

// RotateRestartEnabled 日志超限时是否允许用优雅重启完成切割（缺省 true）。
//
// 真正执行时会额外要求“当前无人在线”，避免打断玩家。
func (l LogConfig) RotateRestartEnabled() bool { return boolOr(l.RotateRestart, true) }

// ManageLog4j2Enabled 是否托管服务端 log4j2.xml（缺省 false）。
func (l LogConfig) ManageLog4j2Enabled() bool { return boolOr(l.ManageLog4j2, false) }

// CleanForSave 在写盘前清掉运行期字段，避免把空 instances 写成 null。
func (c *Config) CleanForSave() {
	if c.Instances == nil {
		c.Instances = map[string]InstanceConfig{}
	}
}

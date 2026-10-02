package instance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yuzumikonami/nyatmc/internal/config"
	"github.com/yuzumikonami/nyatmc/pkg/logger"
)

// CreateOptions 描述创建实例的参数。
type CreateOptions struct {
	Type             string
	MinecraftVersion string
	Build            string
	LoaderVersion    string
	InstallerVersion string
	Memory           string
	Port             int
	JavaPath         string
	Description      string
	// Path 指定服务器目录，留空表示 ~/.nyatmc/instances/<名字>/server。
	Path string
	// EULA 为 true 表示用户已确认同意 Minecraft EULA。
	EULA   bool
	Logger *logger.Logger
}

// Create 登记并初始化一个新实例（不下载服务端文件，下载由 serverjar 包负责）。
func Create(ctx context.Context, cfg *config.Config, name string, opts CreateOptions) (*Instance, error) {
	_ = ctx
	log := opts.Logger
	if log == nil {
		log = logger.Discard()
	}
	if err := config.ValidateInstanceName(name); err != nil {
		return nil, err
	}
	if cfg.HasInstance(name) {
		return nil, fmt.Errorf("实例 %q 已存在；如需重建请先 nyatmc instance delete %s", name, name)
	}

	ic := config.InstanceConfig{
		Description: opts.Description,
		CreatedAt:   time.Now().Format(time.RFC3339),
	}
	if strings.TrimSpace(opts.Path) != "" {
		abs, err := filepath.Abs(config.ExpandPath(opts.Path))
		if err != nil {
			return nil, fmt.Errorf("解析服务器目录失败: %w", err)
		}
		ic.Path = abs
	}
	if opts.Type != "" {
		ic.Type = strings.ToLower(strings.TrimSpace(opts.Type))
	}
	if opts.MinecraftVersion != "" {
		ic.MinecraftVersion = opts.MinecraftVersion
	}

	srv := &config.ServerConfig{}
	filled := false
	if opts.Memory != "" {
		srv.Memory = opts.Memory
		filled = true
	}
	if opts.Port != 0 {
		srv.Port = opts.Port
		filled = true
	}
	if opts.JavaPath != "" {
		srv.JavaPath = opts.JavaPath
		filled = true
	}
	if opts.Build != "" {
		srv.Build = opts.Build
		filled = true
	}
	if opts.LoaderVersion != "" {
		srv.LoaderVersion = opts.LoaderVersion
		filled = true
	}
	if opts.InstallerVersion != "" {
		srv.InstallerVersion = opts.InstallerVersion
		filled = true
	}
	if opts.EULA {
		srv.EULA = config.BoolPtr(true)
		filled = true
	}
	if filled {
		ic.Server = srv
	}

	if err := cfg.AddInstance(name, ic); err != nil {
		return nil, err
	}

	inst, err := New(cfg, name)
	if err != nil {
		return nil, err
	}
	if err := inst.EnsureDirs(); err != nil {
		return nil, err
	}
	if err := inst.ApplyProperties(); err != nil {
		return nil, err
	}
	if err := inst.WriteEULA(opts.EULA); err != nil {
		return nil, err
	}
	if err := inst.WriteStartScripts(); err != nil {
		return nil, err
	}
	if inst.Res.Log.ManageLog4j2Enabled() {
		if err := inst.WriteLog4j2(true); err != nil {
			log.Warnf("写入 log4j2.xml 失败（不影响启动）：%v", err)
		}
	}

	meta := Metadata{
		Name:             name,
		Type:             inst.Res.Server.Type,
		MinecraftVersion: inst.Res.Server.MinecraftVersion,
		Path:             inst.Res.Path,
		Description:      opts.Description,
		CreatedAt:        time.Now(),
		NyatmcVersion:    config.Version,
	}
	if err := inst.SaveMetadata(meta); err != nil {
		return nil, fmt.Errorf("写入实例元数据失败: %w", err)
	}
	if err := cfg.Save(); err != nil {
		return nil, fmt.Errorf("保存配置失败: %w", err)
	}
	log.Infof("实例 %s 已创建，服务器目录：%s", name, inst.Res.Path)
	return inst, nil
}

// DeleteOptions 描述删除实例的行为。
type DeleteOptions struct {
	// Purge 为 true 时连同实例目录一起删除。
	Purge bool
	// KeepBackups 为 true 时把备份移到 ~/.nyatmc/backups-keep/ 下保留。
	KeepBackups bool
}

// Delete 注销实例，可选删除文件。
func Delete(cfg *config.Config, name string, opts DeleteOptions) error {
	if !cfg.HasInstance(name) {
		return fmt.Errorf("实例 %q 未登记", name)
	}
	inst, err := New(cfg, name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(inst.Res.Endpoint); err == nil {
		return fmt.Errorf("实例 %s 正在运行，请先执行 nyatmc stop %s", name, name)
	}

	if opts.Purge {
		if opts.KeepBackups && dirExists(inst.Res.BackupDir) {
			keepDir := filepath.Join(config.Home(), "backups-keep", fmt.Sprintf("%s-%s", name, time.Now().Format("20060102-150405")))
			if err := os.MkdirAll(filepath.Dir(keepDir), 0o755); err != nil {
				return fmt.Errorf("创建备份保留目录失败: %w", err)
			}
			if err := os.Rename(inst.Res.BackupDir, keepDir); err != nil {
				return fmt.Errorf("转移备份目录失败: %w", err)
			}
		}
		if err := safeRemoveAll(inst.Res.Root, name); err != nil {
			return err
		}
		// 自定义服务器目录（例如用户已有的服务器）不自动删除，只提示。
		if !isDefaultServerDir(cfg, name) && dirExists(inst.Res.Path) {
			logger.Warnf("实例 %s 的服务器目录是自定义路径，已保留未删除：%s", name, inst.Res.Path)
		}
	}

	if err := cfg.RemoveInstance(name); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("保存配置失败: %w", err)
	}
	return nil
}

func isDefaultServerDir(cfg *config.Config, name string) bool {
	return filepath.Clean(cfg.InstanceServerDir(name)) == filepath.Clean(filepath.Join(cfg.InstanceRoot(name), "server"))
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// safeRemoveAll 只允许删除实例目录内部的内容，避免误删用户目录。
func safeRemoveAll(target, name string) error {
	abs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("解析路径失败: %w", err)
	}
	base := filepath.Clean(config.InstancesDir())
	if abs == base {
		return fmt.Errorf("拒绝删除实例根目录 %s", abs)
	}
	if !strings.HasPrefix(abs, base+string(os.PathSeparator)) {
		return fmt.Errorf("拒绝删除 %s：它不在实例目录 %s 之内", abs, base)
	}
	if err := os.RemoveAll(abs); err != nil {
		return fmt.Errorf("删除实例目录 %s 失败: %w", abs, err)
	}
	_ = name
	return nil
}

// ---------------------------------------------------------------- server.properties

// defaultProperties 是 nyatmc 为新实例写入的基础配置。
func (i *Instance) desiredProperties() map[string]string {
	s := i.Res.Server
	props := map[string]string{
		"server-port":         strconv.Itoa(s.Port),
		"motd":                s.MOTD,
		"max-players":         strconv.Itoa(s.MaxPlayers),
		"view-distance":       strconv.Itoa(s.ViewDistance),
		"online-mode":         strconv.FormatBool(s.OnlineModeEnabled()),
		"level-name":          s.WorldName,
		"enable-command-block": "true",
		"enable-status":       "true",
		"spawn-protection":    "16",
		"difficulty":          "easy",
		"gamemode":            "survival",
		"allow-flight":        "false",
		"white-list":          "false",
		"enforce-whitelist":   "false",
		"sync-chunk-writes":   "true",
		"max-tick-time":       "-1",
	}
	// 用户自定义的键优先级最高
	for k, v := range s.Properties {
		props[k] = v
	}
	return props
}

// ApplyProperties 把配置里的端口 / MOTD / 人数等写入 server.properties，保留用户已有的其它键。
func (i *Instance) ApplyProperties() error {
	if err := os.MkdirAll(i.Res.Path, 0o755); err != nil {
		return err
	}
	return UpdateProperties(i.PropertiesPath(), i.desiredProperties())
}

// ReadProperties 读取 server.properties 为 map。
func ReadProperties(path string) (map[string]string, error) {
	out := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "!") {
			continue
		}
		idx := strings.Index(trimmed, "=")
		if idx <= 0 {
			continue
		}
		out[strings.TrimSpace(trimmed[:idx])] = strings.TrimSpace(trimmed[idx+1:])
	}
	return out, nil
}

// UpdateProperties 就地更新 server.properties：已有键替换值，新键追加到末尾，注释与顺序保留。
func UpdateProperties(path string, desired map[string]string) error {
	if len(desired) == 0 {
		return nil
	}
	var lines []string
	if data, err := os.ReadFile(path); err == nil {
		lines = strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("读取 %s 失败: %w", path, err)
	}

	seen := map[string]bool{}
	for idx, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "!") {
			continue
		}
		eq := strings.Index(trimmed, "=")
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:eq])
		val, ok := desired[key]
		if !ok {
			continue
		}
		lines[idx] = key + "=" + val
		seen[key] = true
	}

	missing := make([]string, 0, len(desired))
	for k := range desired {
		if !seen[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines, "# 以下键由 nyatmc 追加")
		for _, k := range missing {
			lines = append(lines, k+"="+desired[k])
		}
	}

	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", path, err)
	}
	return nil
}

// ---------------------------------------------------------------- eula / 启动脚本

// WriteEULA 写入 eula.txt；未同意时只写 false 并给出提醒。
func (i *Instance) WriteEULA(accept bool) error {
	value := accept || i.Res.Server.EULAEnabled()
	if err := os.MkdirAll(i.Res.Path, 0o755); err != nil {
		return err
	}
	content := "# 由 nyatmc 生成。\n" +
		"# 设为 true 表示你已阅读并同意 Minecraft 最终用户许可协议：https://aka.ms/MinecraftEULA\n" +
		fmt.Sprintf("eula=%t\n", value)
	if err := os.WriteFile(i.EULAPath(), []byte(content), 0o644); err != nil {
		return fmt.Errorf("写入 eula.txt 失败: %w", err)
	}
	if !value {
		logger.Warnf("实例 %s 尚未同意 EULA，服务端会拒绝启动；用 nyatmc config set server.eula true（或 --eula）确认后重试", i.Name)
	}
	return nil
}

// LaunchCommand 返回启动服务端所用的可执行文件与参数。
func (i *Instance) LaunchCommand() (string, []string) {
	s := i.Res.Server
	java := strings.TrimSpace(s.JavaPath)
	if java == "" {
		java = "java"
	}
	args := make([]string, 0, 8+len(s.JVMArgs)+len(s.ServerArgs))
	if strings.TrimSpace(s.MinMemory) != "" {
		args = append(args, "-Xms"+strings.ToUpper(strings.TrimSpace(s.MinMemory)))
	}
	if strings.TrimSpace(s.Memory) != "" {
		args = append(args, "-Xmx"+strings.ToUpper(strings.TrimSpace(s.Memory)))
	}
	args = append(args, s.JVMArgs...)
	args = append(args, "-jar", filepath.Base(i.Res.JarPath))
	args = append(args, s.ServerArgs...)
	return java, args
}

// WriteStartScripts 生成 start.sh / start.bat，方便脱离 nyatmc 手动启动排查问题。
func (i *Instance) WriteStartScripts() error {
	java, args := i.LaunchCommand()
	quoted := make([]string, 0, len(args))
	for _, a := range args {
		if strings.ContainsAny(a, " \t\"") {
			quoted = append(quoted, strconv.Quote(a))
		} else {
			quoted = append(quoted, a)
		}
	}

	sh := "#!/usr/bin/env bash\n" +
		"# 由 nyatmc 生成；日常请用 nyatmc start / nyatmc tui 管理，本脚本仅用于手动排查。\n" +
		"cd \"$(dirname \"$0\")\" || exit 1\n" +
		"exec " + java + " " + strings.Join(quoted, " ") + "\n"
	if err := os.WriteFile(filepath.Join(i.Res.Path, "start.sh"), []byte(sh), 0o755); err != nil {
		return fmt.Errorf("写入 start.sh 失败: %w", err)
	}

	bat := "@echo off\r\n" +
		"rem 由 nyatmc 生成；日常请用 nyatmc start 管理。\r\n" +
		"cd /d \"%~dp0\"\r\n" +
		java + " " + strings.Join(quoted, " ") + "\r\n"
	if err := os.WriteFile(filepath.Join(i.Res.Path, "start.bat"), []byte(bat), 0o644); err != nil {
		return fmt.Errorf("写入 start.bat 失败: %w", err)
	}
	return nil
}

// WriteLog4j2 写入受管的 log4j2.xml，让服务端自己按大小切割 logs/latest.log。
//
// 这是纯粹的“可选增强”：服务端（Paper/Vanilla 等）默认会生成自己的日志配置，
// 我们只在 log.manage_log4j2 = true 或显式调用时替换，并且先备份原文件。
func (i *Instance) WriteLog4j2(force bool) error {
	if !force && !i.Res.Log.ManageLog4j2Enabled() {
		return nil
	}
	size := i.Res.Log.RotateSize.Bytes()
	if size <= 0 {
		size = 20 * 1024 * 1024
	}
	maxFiles := i.Res.Log.KeepFiles
	if maxFiles <= 0 {
		maxFiles = 5
	}
	path := i.Log4j2Path()
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		if err := os.WriteFile(path+".bak", data, 0o644); err != nil {
			return fmt.Errorf("备份原 log4j2.xml 失败: %w", err)
		}
	}
	xml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!-- 由 nyatmc 生成（log.manage_log4j2 = true）：让服务端按大小自行切割 latest.log。 -->
<Configuration status="WARN" packages="com.mojang.util">
    <Appenders>
        <Console name="SysOut" target="SYSTEM_OUT">
            <PatternLayout pattern="[%%d{HH:mm:ss}] [%%t/%%level]: %%msg%%n" />
        </Console>
        <RollingFile name="File" fileName="logs/latest.log" filePattern="logs/%%d{yyyy-MM-dd}-%%i.log.gz">
            <PatternLayout pattern="[%%d{HH:mm:ss}] [%%t/%%level]: %%msg%%n" />
            <Policies>
                <SizeBasedTriggeringPolicy size="%d"/>
            </Policies>
            <DefaultRolloverStrategy max="%d"/>
        </RollingFile>
    </Appenders>
    <Loggers>
        <Root level="info">
            <AppenderRef ref="SysOut"/>
            <AppenderRef ref="File"/>
        </Root>
    </Loggers>
</Configuration>
`, size, maxFiles)
	if err := os.WriteFile(path, []byte(xml), 0o644); err != nil {
		return fmt.Errorf("写入 log4j2.xml 失败: %w", err)
	}
	return nil
}

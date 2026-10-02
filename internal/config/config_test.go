package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 模板必须能被严格模式解析，否则 nyatmc init 写出的配置会立刻报错。
func TestDefaultTemplateParses(t *testing.T) {
	cfg, err := Parse([]byte(DefaultTOML))
	if err != nil {
		t.Fatalf("默认模板解析失败: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("默认模板校验失败: %v", err)
	}
	if cfg.Server.Type != "paper" {
		t.Errorf("server.type = %q，期望 paper", cfg.Server.Type)
	}
	if cfg.Server.Port != 25565 {
		t.Errorf("server.port = %d，期望 25565", cfg.Server.Port)
	}
	if !cfg.Daemon.WatchdogEnabled() {
		t.Error("默认应启用看门狗")
	}
	if !cfg.Log.RotateRestartEnabled() {
		t.Error("默认应在无人在线时允许用重启切割日志")
	}
	if cfg.Log.RotateSize.Bytes() != 20*1024*1024 {
		t.Errorf("log.rotate_size = %d，期望 20MiB", cfg.Log.RotateSize.Bytes())
	}
	if cfg.Backup.Keep != 10 {
		t.Errorf("backup.keep = %d，期望 10", cfg.Backup.Keep)
	}
	if !cfg.Server.OnlineModeEnabled() {
		t.Error("默认应开启正版验证")
	}
	if cfg.Server.EULAEnabled() {
		t.Error("默认不应自动同意 EULA")
	}
}

// 保存再读取必须保持一致（顺带验证 Duration / Size 的 TOML 编解码）。
func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	cfg := Default()
	cfg.Server.Memory = "4G"
	cfg.Server.MinMemory = "1G"
	cfg.Daemon.RestartDelay = Duration(7 * time.Second)
	cfg.Log.RotateSize = Size(3 * 1024 * 1024)
	cfg.Daemon.Watchdog = BoolPtr(false)
	cfg.Server.OnlineMode = BoolPtr(false)
	cfg.Backup.Include = []string{"world"}
	cfg.Instances["survival"] = InstanceConfig{
		Type:             "fabric",
		MinecraftVersion: "1.21.4",
		Server:           &ServerConfig{Port: 25570, Memory: "8G"},
	}

	if err := cfg.SaveTo(path); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}

	if got.Server.Memory != "4G" || got.Server.MinMemory != "1G" {
		t.Errorf("内存设置丢失: %q / %q", got.Server.Memory, got.Server.MinMemory)
	}
	if got.Daemon.RestartDelay.Std() != 7*time.Second {
		t.Errorf("restart_delay = %s，期望 7s", got.Daemon.RestartDelay)
	}
	if got.Log.RotateSize.Bytes() != 3*1024*1024 {
		t.Errorf("rotate_size = %d，期望 3MiB", got.Log.RotateSize.Bytes())
	}
	if got.Daemon.WatchdogEnabled() {
		t.Error("watchdog = false 丢失（被默认值覆盖）")
	}
	if got.Server.OnlineModeEnabled() {
		t.Error("online_mode = false 丢失（被默认值覆盖）")
	}
	if len(got.Backup.Include) != 1 || got.Backup.Include[0] != "world" {
		t.Errorf("backup.include = %v，期望 [world]", got.Backup.Include)
	}
	if len(got.Instances) != 1 {
		t.Fatalf("实例数量 = %d，期望 1", len(got.Instances))
	}
}

// 实例覆盖必须正确合并到全局默认值上。
func TestResolveInstanceOverride(t *testing.T) {
	cfg := Default()
	cfg.Instances["creative"] = InstanceConfig{
		Description: "创造服",
		Server:      &ServerConfig{Port: 25566, Memory: "8G", EULA: BoolPtr(true)},
		Daemon:      &DaemonConfig{Watchdog: BoolPtr(false)},
		Backup:      &BackupConfig{Keep: 3},
	}

	res := cfg.Resolve("creative")
	if res.Server.Port != 25566 {
		t.Errorf("端口 = %d，期望 25566", res.Server.Port)
	}
	if res.Server.Memory != "8G" {
		t.Errorf("内存 = %q，期望 8G", res.Server.Memory)
	}
	if res.Server.MinMemory != cfg.Server.MinMemory {
		t.Errorf("未覆盖的字段应继承全局值，得到 %q", res.Server.MinMemory)
	}
	if !res.Server.EULAEnabled() {
		t.Error("实例级 eula = true 未生效")
	}
	if res.Daemon.WatchdogEnabled() {
		t.Error("实例级 watchdog = false 未生效")
	}
	if res.Backup.Keep != 3 {
		t.Errorf("实例级 keep = %d，期望 3", res.Backup.Keep)
	}
	if res.JarPath != filepath.Join(res.Path, "server.jar") {
		t.Errorf("JarPath = %q，未按服务器目录推导", res.JarPath)
	}
	if res.BackupDir != filepath.Join(res.Root, "backups") {
		t.Errorf("BackupDir = %q，期望实例目录下的 backups", res.BackupDir)
	}
}

func TestResolveCustomPath(t *testing.T) {
	cfg := Default()
	custom := t.TempDir()
	cfg.Instances["custom"] = InstanceConfig{Path: custom}
	res := cfg.Resolve("custom")
	if res.Path != filepath.Clean(custom) {
		t.Errorf("Path = %q，期望 %q", res.Path, custom)
	}
	if res.MetaDir == res.Path {
		t.Error("元数据目录不应等于服务器目录")
	}
}

func TestLoadOrCreate(t *testing.T) {
	dir := t.TempDir()
	SetHome(dir)
	defer SetHome("")
	path := filepath.Join(dir, "config.toml")

	cfg, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("首次创建失败: %v", err)
	}
	if !cfg.Created {
		t.Error("首次创建应标记 Created")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("配置文件未写出: %v", err)
	}
	again, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("二次加载失败: %v", err)
	}
	if again.Created {
		t.Error("二次加载不应标记 Created")
	}
}

func TestValidateCron(t *testing.T) {
	ok := []string{"0 4 * * *", "*/5 * * * *", "0 0 1 * *", "@daily", "@every 1h30m", "0 0 4 * * *"}
	for _, spec := range ok {
		if err := ValidateCron(spec); err != nil {
			t.Errorf("ValidateCron(%q) 应为合法，得到 %v", spec, err)
		}
	}
	bad := []string{"", "0 4 * *", "0 4 * * * * *", "abc", "@nope"}
	for _, spec := range bad {
		if err := ValidateCron(spec); err == nil {
			t.Errorf("ValidateCron(%q) 应报错", spec)
		}
	}
}

func TestValidateRejectsBroken(t *testing.T) {
	cfg := Default()
	cfg.Server.Type = "forge"
	cfg.Server.Memory = "2GB"
	cfg.Server.Port = 70000
	cfg.Backup.Format = "rar"
	cfg.Scheduler.Jobs = []Job{{Name: "j", Schedule: "bad", Action: "nope"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("非法配置应校验失败")
	}
}

func TestValidateInstanceName(t *testing.T) {
	for _, name := range []string{"default", "survival-2", "我的世界", "a.b"} {
		if err := ValidateInstanceName(name); err != nil {
			t.Errorf("%q 应合法: %v", name, err)
		}
	}
	for _, name := range []string{"", "..", "a/b", "a\\b", "a b", string(make([]byte, 100))} {
		if err := ValidateInstanceName(name); err == nil {
			t.Errorf("%q 应非法", name)
		}
	}
}

// Package instance 负责多实例的磁盘布局、元数据与生命周期（创建 / 删除）。
//
// 布局：
//
//	~/.nyatmc/instances/<名字>/
//	├── .nyatmc/            # nyatmc 私有元数据：endpoint.json、state.json、console.log、instance.json
//	├── backups/            # 备份归档
//	└── server/             # Minecraft 服务器工作目录（jar、world、mods、plugins…）
//
// 服务器目录可以用 [instances.<名字>] path 指到任意位置（例如已有的服务器目录）。
package instance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yuzumikonami/nyatmc/internal/config"
)

// Instance 是一个受管实例。
type Instance struct {
	Name string
	Cfg  *config.Config
	Res  config.Resolved
}

// New 构造实例句柄（不做任何磁盘操作）。
func New(cfg *config.Config, name string) (*Instance, error) {
	if cfg == nil {
		return nil, fmt.Errorf("配置为空")
	}
	if strings.TrimSpace(name) == "" {
		name = cfg.General.DefaultInstance
	}
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("未指定实例名，且配置里没有 general.default_instance")
	}
	if err := config.ValidateInstanceName(name); err != nil {
		return nil, err
	}
	return &Instance{Name: name, Cfg: cfg, Res: cfg.Resolve(name)}, nil
}

// Registered 表示实例已登记在配置文件里。
func (i *Instance) Registered() bool { return i.Cfg.HasInstance(i.Name) }

// String 返回实例名。
func (i *Instance) String() string { return i.Name }

// List 返回配置里登记的全部实例（按名字排序）。
func List(cfg *config.Config) []*Instance {
	names := cfg.InstanceNames()
	out := make([]*Instance, 0, len(names))
	for _, n := range names {
		out = append(out, &Instance{Name: n, Cfg: cfg, Res: cfg.Resolve(n)})
	}
	return out
}

// Discover 同时发现“已登记”和“目录存在但未登记”的实例，便于修复手工拷贝进来的服务器目录。
func Discover(cfg *config.Config) ([]*Instance, []string, error) {
	known := map[string]bool{}
	out := List(cfg)
	for _, inst := range out {
		known[inst.Name] = true
	}

	root := config.InstancesDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil, nil
		}
		return out, nil, fmt.Errorf("读取实例目录 %s 失败: %w", root, err)
	}
	var unregistered []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if known[name] || strings.HasPrefix(name, ".") {
			continue
		}
		// 目录里得有服务器的痕迹才算实例
		if _, err := os.Stat(filepath.Join(root, name, "server")); err != nil {
			if _, err2 := os.Stat(filepath.Join(root, name, ".nyatmc", "instance.json")); err2 != nil {
				continue
			}
		}
		unregistered = append(unregistered, name)
	}
	sort.Strings(unregistered)
	return out, unregistered, nil
}

// EnsureDirs 创建实例需要的全部目录。
func (i *Instance) EnsureDirs() error {
	for _, dir := range []string{i.Res.Root, i.Res.MetaDir, i.Res.Path, i.Res.BackupDir, i.Res.LogsDir} {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建目录 %s 失败: %w", dir, err)
		}
	}
	return nil
}

// Exists 表示服务器目录已经存在。
func (i *Instance) Exists() bool {
	st, err := os.Stat(i.Res.Path)
	return err == nil && st.IsDir()
}

// JarExists 表示服务端主 jar 已经就位。
func (i *Instance) JarExists() bool {
	st, err := os.Stat(i.Res.JarPath)
	return err == nil && st.Mode().IsRegular() && st.Size() > 0
}

// MetadataPath 返回元数据文件路径。
func (i *Instance) MetadataPath() string { return filepath.Join(i.Res.MetaDir, "instance.json") }

// Metadata 是实例的静态信息（创建时间、服务端类型与版本等）。
type Metadata struct {
	Name             string    `json:"name"`
	Type             string    `json:"type"`
	MinecraftVersion string    `json:"minecraft_version"`
	Build            string    `json:"build,omitempty"`
	LoaderVersion    string    `json:"loader_version,omitempty"`
	InstallerVersion string    `json:"installer_version,omitempty"`
	Path             string    `json:"path"`
	Description      string    `json:"description,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at,omitempty"`
	// 已安装的服务端文件信息
	ServerJar     string    `json:"server_jar,omitempty"`
	JarURL        string    `json:"jar_url,omitempty"`
	JarSHA256     string    `json:"jar_sha256,omitempty"`
	JarSize       int64     `json:"jar_size,omitempty"`
	JarInstalled  time.Time `json:"jar_installed,omitempty"`
	NyatmcVersion string    `json:"nyatmc_version,omitempty"`
}

// LoadMetadata 读取实例元数据，文件不存在时返回零值且 error 为 nil。
func (i *Instance) LoadMetadata() (Metadata, error) {
	var m Metadata
	data, err := os.ReadFile(i.MetadataPath())
	if err != nil {
		if os.IsNotExist(err) {
			// 退化到配置里的信息，保证“未登记元数据的旧实例”也能工作
			return Metadata{
				Name:             i.Name,
				Type:             i.Res.Server.Type,
				MinecraftVersion: i.Res.Server.MinecraftVersion,
				Path:             i.Res.Path,
			}, nil
		}
		return m, fmt.Errorf("读取实例元数据失败: %w", err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("实例元数据损坏: %w", err)
	}
	return m, nil
}

// SaveMetadata 写入实例元数据。
func (i *Instance) SaveMetadata(m Metadata) error {
	if err := os.MkdirAll(i.Res.MetaDir, 0o755); err != nil {
		return err
	}
	if m.Name == "" {
		m.Name = i.Name
	}
	m.Path = i.Res.Path
	m.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(i.MetadataPath(), data, 0o644)
}

// StatePath 返回运行状态文件路径。
func (i *Instance) StatePath() string { return i.Res.StateFile }

// EndpointPath 返回控制端点文件路径。
func (i *Instance) EndpointPath() string { return i.Res.Endpoint }

// ConsoleLogPath 返回 nyatmc 捕获的服务端控制台日志。
func (i *Instance) ConsoleLogPath() string { return i.Res.Console }

// MinecraftLogPath 返回服务端自己的 latest.log。
func (i *Instance) MinecraftLogPath() string { return filepath.Join(i.Res.LogsDir, "latest.log") }

// PropertiesPath 返回 server.properties 路径。
func (i *Instance) PropertiesPath() string { return filepath.Join(i.Res.Path, "server.properties") }

// EULAPath 返回 eula.txt 路径。
func (i *Instance) EULAPath() string { return filepath.Join(i.Res.Path, "eula.txt") }

// Log4j2Path 返回 log4j2.xml 路径。
func (i *Instance) Log4j2Path() string { return filepath.Join(i.Res.Path, "log4j2.xml") }

// ModsDir 返回模组目录。
func (i *Instance) ModsDir() string { return filepath.Join(i.Res.Path, i.Res.Mods.ModsDir) }

// PluginsDir 返回插件目录。
func (i *Instance) PluginsDir() string { return filepath.Join(i.Res.Path, i.Res.Mods.PluginsDir) }

// IsPluginServer 判断实例类型是否使用插件（而非模组）。
func (i *Instance) IsPluginServer() bool {
	switch strings.ToLower(i.Res.Server.Type) {
	case "paper", "spigot", "purpur", "bukkit":
		return true
	default:
		return false
	}
}

// LoaderFor 返回适合该实例的模组加载器名（用于 Modrinth / CurseForge 过滤）。
func (i *Instance) LoaderFor() string {
	if i.Res.Mods.Loader != "" {
		return strings.ToLower(i.Res.Mods.Loader)
	}
	switch strings.ToLower(i.Res.Server.Type) {
	case "paper", "spigot", "purpur":
		return "paper"
	case "fabric":
		return "fabric"
	case "vanilla":
		return "vanilla"
	default:
		return strings.ToLower(i.Res.Server.Type)
	}
}

// GameVersionFor 返回用于模组过滤的 Minecraft 版本（配置优先）。
func (i *Instance) GameVersionFor() string {
	if v := strings.TrimSpace(i.Res.Mods.GameVersion); v != "" {
		return v
	}
	v := strings.TrimSpace(i.Res.Server.MinecraftVersion)
	if v == "" || v == "latest" {
		return ""
	}
	return v
}

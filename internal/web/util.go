package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/yuzumikonami/nyatmc/internal/config"
	"github.com/yuzumikonami/nyatmc/internal/daemon"
	"github.com/yuzumikonami/nyatmc/internal/instance"
)

// ---------------------------------------------------------------- 统一响应

// ok 输出成功响应：{"ok":true,"data":...}。
func ok(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": data})
}

// okWith 输出成功响应，并附加额外的顶层字段（例如日志接口的 count）。
func okWith(c *gin.Context, data any, extra gin.H) {
	body := gin.H{"ok": true, "data": data}
	for k, v := range extra {
		body[k] = v
	}
	c.JSON(http.StatusOK, body)
}

// fail 中断请求并输出统一的中文错误：{"ok":false,"error":"..."}。
func fail(c *gin.Context, code int, format string, args ...any) {
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	c.AbortWithStatusJSON(code, gin.H{"ok": false, "error": msg})
}

// bindJSON 宽松解析请求体：空请求体视为默认值，非法 JSON 返回 400。
func bindJSON(c *gin.Context, v any) bool {
	if c.Request == nil || c.Request.Body == nil {
		return true
	}
	data, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		fail(c, http.StatusBadRequest, "读取请求体失败：%v", err)
		return false
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return true
	}
	if err := json.Unmarshal(data, v); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法的 JSON：%v", err)
		return false
	}
	return true
}

func queryInt(c *gin.Context, key string, def int) int {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// humanDuration 输出中文友好的时长。
func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	d = d.Round(time.Second)
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	secs := int(d.Seconds()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%d天%d小时", days, hours)
	case hours > 0:
		return fmt.Sprintf("%d小时%d分", hours, mins)
	case mins > 0:
		return fmt.Sprintf("%d分%d秒", mins, secs)
	default:
		return fmt.Sprintf("%d秒", secs)
	}
}

// ---------------------------------------------------------------- 视图结构

// statusView 是 daemon.Status 的展示视图。
type statusView struct {
	daemon.Status
	Name        string `json:"name"`
	StateLabel  string `json:"state_label"`
	Active      bool   `json:"active"`
	Supervisor  bool   `json:"supervisor_alive"`
	UptimeHuman string `json:"uptime_human"`
	PlayersNow  int    `json:"players_now"`
	Enabled     bool   `json:"enabled"`
	JarReady    bool   `json:"jar_ready"`
	Type        string `json:"type"`
	Directory   string `json:"directory"`
}

func newStatusView(inst *instance.Instance, st daemon.Status) statusView {
	if st.Instance == "" {
		st.Instance = inst.Name
	}
	if st.State == "" {
		st.State = daemon.StateUnknown
	}
	return statusView{
		Status:      st,
		Name:        inst.Name,
		StateLabel:  st.State.Label(),
		Active:      st.State.Active(),
		Supervisor:  !st.Stale,
		UptimeHuman: humanDuration(st.Uptime()),
		PlayersNow:  st.PlayerCount(),
		Enabled:     inst.Res.Enabled,
		JarReady:    inst.JarExists(),
		Type:        inst.Res.Server.Type,
		Directory:   inst.Res.Path,
	}
}

// instanceInfo 是实例列表里的一项（不探测进程，保证接口很快）。
type instanceInfo struct {
	Name             string `json:"name"`
	Type             string `json:"type"`
	MinecraftVersion string `json:"minecraft_version"`
	Path             string `json:"path"`
	Root             string `json:"root"`
	BackupDir        string `json:"backup_dir"`
	ConsoleLog       string `json:"console_log"`
	Enabled          bool   `json:"enabled"`
	Registered       bool   `json:"registered"`
	JarExists        bool   `json:"jar_exists"`
	ServerExists     bool   `json:"server_exists"`
	Description      string `json:"description"`
	Port             int    `json:"port"`
	Memory           string `json:"memory"`
	CreatedAt        string `json:"created_at"`
}

func newInstanceInfo(cfg *config.Config, inst *instance.Instance) instanceInfo {
	ic := cfg.Instances[inst.Name]
	return instanceInfo{
		Name:             inst.Name,
		Type:             inst.Res.Server.Type,
		MinecraftVersion: inst.Res.Server.MinecraftVersion,
		Path:             inst.Res.Path,
		Root:             inst.Res.Root,
		BackupDir:        inst.Res.BackupDir,
		ConsoleLog:       inst.Res.Console,
		Enabled:          inst.Res.Enabled,
		Registered:       inst.Registered(),
		JarExists:        inst.JarExists(),
		ServerExists:     inst.Exists(),
		Description:      ic.Description,
		Port:             inst.Res.Server.Port,
		Memory:           inst.Res.Server.Memory,
		CreatedAt:        ic.CreatedAt,
	}
}

// ---------------------------------------------------------------- 文件小工具

// writeFileAtomic 原子写入文件（先写 .tmp 再改名）。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建目录 %s 失败: %w", dir, err)
	}
	tmp := path + ".nyatmc.tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换 %s 失败: %w", path, err)
	}
	return nil
}

// validRelTarget 校验回滚目标路径：必须是相对路径且不能越出服务器目录。
func validRelTarget(raw string) error {
	t := filepath.ToSlash(strings.TrimSpace(raw))
	if t == "" {
		return fmt.Errorf("回滚目标不能为空")
	}
	if filepath.IsAbs(t) || strings.HasPrefix(t, "/") || strings.Contains(t, ":") {
		return fmt.Errorf("回滚目标 %q 必须是相对服务器目录的路径", raw)
	}
	clean := path.Clean(t)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("回滚目标 %q 不能越出服务器目录", raw)
	}
	return nil
}

// onOff 输出中文开关状态。
func onOff(v bool) string {
	if v {
		return "已开启"
	}
	return "已关闭"
}

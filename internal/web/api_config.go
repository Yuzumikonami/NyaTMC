package web

import (
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/yuzumikonami/nyatmc/internal/config"
)

// handleConfigGet 返回配置文件原文与结构化视图。
func (s *Server) handleConfigGet(c *gin.Context) {
	cfg := s.current()
	path := s.ConfigPath()

	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		fail(c, http.StatusInternalServerError, "读取配置文件 %s 失败：%v", path, err)
		return
	}
	if os.IsNotExist(err) {
		// 文件不在（例如被手工删了），把当前内存里的配置序列化回去给用户看。
		raw = nil
	}

	data := gin.H{
		"path":       path,
		"toml":       string(raw),
		"exists":     err == nil,
		"resolved":   cfg,
		"warnings":   cfg.Warnings(),
		"instances":  cfg.InstanceNames(),
		"default":    cfg.General.DefaultInstance,
	}

	// 指定了实例时额外给出合并后的最终配置。
	if name := strings.TrimSpace(c.Query("instance")); name != "" {
		if verr := config.ValidateInstanceName(name); verr != nil {
			fail(c, http.StatusBadRequest, "实例名不合法：%v", verr)
			return
		}
		if !cfg.HasInstance(name) {
			fail(c, http.StatusNotFound, "实例 %q 不存在", name)
			return
		}
		data["instance"] = name
		data["instance_resolved"] = cfg.Resolve(name)
	}
	ok(c, data)
}

// handleConfigPut 校验并写回配置文件。
//
// 校验顺序：TOML 语法 → 结构体（未知字段也会报错）→ 语义校验，全部通过才落盘。
func (s *Server) handleConfigPut(c *gin.Context) {
	var body struct {
		TOML string `json:"toml"`
	}
	if !bindJSON(c, &body) {
		return
	}
	text := body.TOML
	if strings.TrimSpace(text) == "" {
		fail(c, http.StatusBadRequest, "配置内容不能为空")
		return
	}
	if len(text) > 1<<20 {
		fail(c, http.StatusBadRequest, "配置内容过大（最多 1MB）")
		return
	}

	parsed, err := config.Parse([]byte(text))
	if err != nil {
		fail(c, http.StatusBadRequest, "配置解析失败，未写入磁盘：%v", err)
		return
	}
	if err := parsed.Validate(); err != nil {
		fail(c, http.StatusBadRequest, "配置校验失败，未写入磁盘：%v", err)
		return
	}

	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	path := s.ConfigPath()
	if old, rerr := os.ReadFile(path); rerr == nil && len(old) > 0 {
		if werr := writeFileAtomic(path+".bak", old, 0o600); werr != nil {
			s.log.Warnf("写入配置备份 %s.bak 失败：%v", path, werr)
		}
	}
	if err := writeFileAtomic(path, []byte(text), 0o600); err != nil {
		fail(c, http.StatusInternalServerError, "写入配置失败：%v", err)
		return
	}

	reloaded, err := s.mgr.Reload()
	if err != nil {
		fail(c, http.StatusInternalServerError,
			"配置已写入 %s，但重新加载失败：%v（请检查文件内容后重试）", path, err)
		return
	}
	s.log.Infof("Web：已更新配置文件 %s", path)

	ok(c, gin.H{
		"path":     path,
		"resolved": reloaded,
		"warnings": reloaded.Warnings(),
		"message":  "配置已保存并立即生效",
	})
}

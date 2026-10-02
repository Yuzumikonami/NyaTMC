package web

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/yuzumikonami/nyatmc/internal/tunnel"
)

// tunnelStartRequest 是 POST /api/tunnel/start 的可选请求体。
//
// 全部字段都可以省略：默认把 Web 仪表盘自己监听的地址暴露到公网。
type tunnelStartRequest struct {
	// Port 是要暴露的本地端口（优先于默认目标）。
	Port int `json:"port"`
	// Target 是完整目标地址（优先于 Port），例如 http://127.0.0.1:25565。
	Target string `json:"target"`
	// WaitSeconds 是等待公网地址就绪的上限（默认 30 秒）。
	WaitSeconds int `json:"wait_seconds"`
}

// handleTunnelStatus 返回隧道当前状态。
//
// 状态优先来自跨进程的状态文件（state/tunnel.json），因此 CLI 用
// `nyatmc tunnel start` 启动的隧道在仪表盘里同样可见。
func (s *Server) handleTunnelStatus(c *gin.Context) {
	cfg := s.current()
	resp := gin.H{
		"available":  true,
		"running":    false,
		"provider":   cfg.Tunnel.Provider,
		"protocol":   cfg.Tunnel.Protocol,
		"hostname":   cfg.Tunnel.Hostname,
		"auto_start": cfg.Tunnel.AutoStartWithWeb,
	}
	if st, found := tunnel.Current(); found {
		for k, v := range tunnelView(st) {
			resp[k] = v
		}
	}
	ok(c, resp)
}

// handleTunnelStart 启动（或复用）cloudflared 隧道，尽量等到公网地址就绪。
func (s *Server) handleTunnelStart(c *gin.Context) {
	var req tunnelStartRequest
	if !bindJSON(c, &req) {
		return
	}
	cfg := s.current()
	opts := tunnel.Options{
		Bin:         strings.TrimSpace(cfg.Tunnel.Bin),
		Protocol:    strings.TrimSpace(cfg.Tunnel.Protocol),
		Token:       strings.TrimSpace(cfg.Tunnel.Token),
		Hostname:    strings.TrimSpace(cfg.Tunnel.Hostname),
		ExtraArgs:   append([]string(nil), cfg.Tunnel.ExtraArgs...),
		AutoInstall: cfg.Tunnel.AutoInstall,
		Logger:      s.log,
	}
	switch {
	case strings.TrimSpace(req.Target) != "":
		opts.Target = strings.TrimSpace(req.Target)
	case req.Port > 0:
		if req.Port > 65535 {
			fail(c, http.StatusBadRequest, "端口 %d 非法，应在 1-65535 之间", req.Port)
			return
		}
		opts.Port = req.Port
	default:
		target, err := tunnelTargetFor(s.ListenAddr())
		if err != nil {
			fail(c, http.StatusBadRequest, "无法确定要暴露的本地地址：%v", err)
			return
		}
		opts.Target = target
	}

	wait := time.Duration(req.WaitSeconds) * time.Second
	if wait <= 0 || wait > 5*time.Minute {
		wait = 30 * time.Second
	}
	t, err := opts.Start(c.Request.Context())
	if err != nil {
		fail(c, http.StatusBadGateway, "启动隧道失败：%v", err)
		return
	}
	url, waitErr := t.WaitURL(c.Request.Context(), wait)
	st := t.Status()
	if url != "" {
		st.URL = url
	}
	view := tunnelView(st)
	view["message"] = "隧道已启动"
	if waitErr != nil {
		view["message"] = waitErr.Error()
	}
	ok(c, view)
}

// handleTunnelStop 停止当前隧道；没有运行中的隧道时同样返回成功（幂等）。
func (s *Server) handleTunnelStop(c *gin.Context) {
	err := tunnel.StopAll()
	switch {
	case err == nil:
		ok(c, gin.H{"running": false, "message": "隧道已停止"})
	case errors.Is(err, tunnel.ErrNotRunning):
		ok(c, gin.H{"running": false, "message": "当前没有正在运行的隧道"})
	default:
		fail(c, http.StatusInternalServerError, "停止隧道失败：%v", err)
	}
}

// tunnelView 把 tunnel.Status 转成前端使用的字段。
func tunnelView(st tunnel.Status) gin.H {
	view := gin.H{
		"running":        st.Running,
		"url":            st.URL,
		"target":         st.Target,
		"mode":           st.Mode,
		"pid":            st.PID,
		"uptime_seconds": st.UptimeSeconds,
		"log_path":       st.LogPath,
	}
	if st.Hostname != "" {
		view["hostname"] = st.Hostname
	}
	if st.LastError != "" {
		view["last_error"] = st.LastError
	}
	return view
}

// tunnelTargetFor 把监听地址（例如 0.0.0.0:8080）转换成隧道目标地址。
func tunnelTargetFor(listen string) (string, error) {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return "", errors.New("监听地址为空")
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

// Package web 实现 NyaTMC 的 Web 仪表盘：一组 REST API + 内嵌的单页前端。
//
// 设计要点：
//   - HTTP 框架使用 gin；前端（HTML/CSS/JS）通过 go:embed 打包进二进制，
//     不引用任何 CDN 或外部资源，离线与 Termux 环境都能直接打开；
//   - 默认只监听回环地址；配置了 web.token 时，除 /api/health 外的接口都要求令牌；
//     未配置令牌时只允许来自回环地址的请求；
//   - web.enable_write = false 时所有写操作返回 403；
//   - 所有实例名都经过 config.ValidateInstanceName 校验，不会拼出目录穿越；
//   - 同一个实例的写操作（启停 / 回滚 / 备份）用互斥锁串行化，避免并发点击把状态搞乱。
//
// 统一响应格式：成功 {"ok":true,"data":...}，失败 {"ok":false,"error":"中文原因"}。
package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/yuzumikonami/nyatmc/internal/config"
	"github.com/yuzumikonami/nyatmc/pkg/logger"
)

// Version 是 Web 页面展示的版本号（cmd 层可用 Version 覆盖）。
var Version = "dev"

//go:embed static
var embeddedStatic embed.FS

// Options 是构造 Web 仪表盘服务的参数。
type Options struct {
	// Config 是初始配置（Manager 为空时使用）。
	Config *config.Config
	// Manager 是配置管理器（推荐传入，可热重载并在写操作后立即生效）。
	Manager *config.Manager
	// Logger 是日志输出，为空时用 logger.Default()。
	Logger *logger.Logger
	// Token 覆盖 web.token（仅本次运行有效，不写盘）。
	Token string
	// Listen 覆盖 web.listen，例如 127.0.0.1:8080。
	Listen string
	// EnableWrite 覆盖 web.enable_write。
	EnableWrite *bool
	// Version 覆盖页面展示的版本号。
	Version string
}

// Server 是 Web 仪表盘服务。
type Server struct {
	mgr *config.Manager
	log *logger.Logger

	overrideToken  string
	overrideListen string
	overrideWrite  *bool
	version        string

	assets fs.FS
	index  []byte
	engine *gin.Engine

	locks instanceLocks

	// cfgMu 串行化配置写操作（PUT /api/config、调度任务增删）。
	cfgMu sync.Mutex

	mu  sync.RWMutex
	ln  net.Listener
	srv *http.Server
}

// New 构造 Web 仪表盘服务（不开始监听）。
func New(opts Options) (*Server, error) {
	if opts.Config == nil && opts.Manager == nil {
		return nil, errors.New("web: 必须提供 Config 或 Manager")
	}
	log := opts.Logger
	if log == nil {
		log = logger.Default()
	}

	mgr := opts.Manager
	if mgr == nil {
		cfgPath := strings.TrimSpace(opts.Config.Path)
		if cfgPath == "" {
			cfgPath = config.ConfigPath()
		}
		if _, err := os.Stat(cfgPath); err != nil {
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("访问配置文件 %s 失败: %w", cfgPath, err)
			}
			if err := opts.Config.SaveTo(cfgPath); err != nil {
				return nil, err
			}
		}
		m, err := config.NewManager(cfgPath)
		if err != nil {
			return nil, fmt.Errorf("加载配置失败: %w", err)
		}
		mgr = m
	}

	assets, err := fs.Sub(embeddedStatic, "static")
	if err != nil {
		return nil, fmt.Errorf("读取内嵌前端资源失败: %w", err)
	}
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		return nil, fmt.Errorf("读取内嵌首页失败: %w", err)
	}

	// 没显式设置 GIN_MODE 时用发布模式，避免每行日志都带调试前缀。
	if gin.Mode() == gin.DebugMode && strings.TrimSpace(os.Getenv(gin.EnvGinMode)) == "" {
		gin.SetMode(gin.ReleaseMode)
	}

	version := strings.TrimSpace(opts.Version)
	if version == "" {
		version = Version
	}

	s := &Server{
		mgr:            mgr,
		log:            log,
		overrideToken:  strings.TrimSpace(opts.Token),
		overrideListen: strings.TrimSpace(opts.Listen),
		overrideWrite:  opts.EnableWrite,
		version:        version,
		assets:         assets,
		index:          index,
	}
	s.engine = s.buildEngine()
	return s, nil
}

// ---------------------------------------------------------------- 访问器

// ConfigPath 返回配置文件路径。
func (s *Server) ConfigPath() string { return s.mgr.Path() }

// Version 返回页面展示的版本号。
func (s *Server) versionString() string {
	if s.version == "" {
		return "dev"
	}
	return s.version
}

// current 返回当前生效的配置（只读）。
func (s *Server) current() *config.Config {
	if cfg := s.mgr.Current(); cfg != nil {
		return cfg
	}
	return config.Default()
}

// Token 返回当前生效的访问令牌（空表示未配置）。
func (s *Server) Token() string {
	if s.overrideToken != "" {
		return s.overrideToken
	}
	return strings.TrimSpace(s.current().Web.Token)
}

// Listen 返回当前生效的监听地址。
func (s *Server) ListenAddr() string {
	if s.overrideListen != "" {
		return s.overrideListen
	}
	if a := strings.TrimSpace(s.current().Web.Listen); a != "" {
		return a
	}
	return "127.0.0.1:8080"
}

// WriteEnabled 返回是否允许写操作。
func (s *Server) WriteEnabled() bool {
	if s.overrideWrite != nil {
		return *s.overrideWrite
	}
	return s.current().Web.EnableWrite
}

// Loopback 表示当前监听地址是否只在回环地址上。
func (s *Server) Loopback() bool {
	host, _, err := net.SplitHostPort(s.ListenAddr())
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) refreshSeconds() int {
	n := s.current().Web.RefreshSeconds
	if n <= 0 {
		n = 3
	}
	if n > 60 {
		n = 60
	}
	return n
}

func (s *Server) maxLogLines() int {
	n := s.current().Web.MaxLogLines
	if n <= 0 {
		n = 2000
	}
	if n > 200000 {
		n = 200000
	}
	return n
}

// Engine 返回底层的 gin 引擎（测试用）。
func (s *Server) Engine() *gin.Engine { return s.engine }

// Handler 返回 HTTP 处理器。
func (s *Server) Handler() http.Handler { return s.engine }

// Manager 返回配置管理器。
func (s *Server) Manager() *config.Manager { return s.mgr }

// ---------------------------------------------------------------- 中间件

func (s *Server) securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// 前端只用同源资源，这里用最严格的策略兜底（样式允许内联，图表用 canvas 手绘）。
		h.Set("Content-Security-Policy",
			"default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
				"connect-src 'self'; img-src 'self' data:; font-src 'self'; base-uri 'none'; "+
				"form-action 'none'; frame-ancestors 'none'")
		c.Next()
	}
}

func (s *Server) recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				s.log.Errorf("处理 %s %s 时发生异常：%v", c.Request.Method, c.Request.URL.Path, r)
				if !c.Writer.Written() {
					fail(c, http.StatusInternalServerError, "服务器内部错误：%v", r)
					return
				}
				c.Abort()
			}
		}()
		c.Next()
	}
}

func (s *Server) accessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		status := c.Writer.Status()
		cost := time.Since(start)
		switch {
		case status >= 500:
			s.log.Warnf("Web %s %s → %d（%s）", c.Request.Method, c.Request.URL.Path, status, cost.Round(time.Millisecond))
		case status >= 400:
			s.log.Debugf("Web %s %s → %d（%s）", c.Request.Method, c.Request.URL.Path, status, cost.Round(time.Millisecond))
		default:
			s.log.Debugf("Web %s %s → %d（%s）", c.Request.Method, c.Request.URL.Path, status, cost.Round(time.Millisecond))
		}
	}
}

// auth 校验访问令牌：支持 Authorization: Bearer <token> 与 ?token=<token>。
// 未配置令牌时只允许来自回环地址的请求。
func (s *Server) auth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !s.checkAuth(c) {
			return
		}
		c.Next()
	}
}

// checkAuth 执行一次鉴权；失败时已经写好 401 响应。
func (s *Server) checkAuth(c *gin.Context) bool {
	want := s.Token()
	got := bearerToken(c.GetHeader("Authorization"))
	if got == "" {
		got = strings.TrimSpace(c.Query("token"))
	}
	if want != "" {
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			fail(c, http.StatusUnauthorized,
				"访问令牌无效或缺失：请在请求头带上 Authorization: Bearer <令牌>，或使用 ?token=<令牌>")
			return false
		}
		return true
	}
	if !isLoopbackIP(c.ClientIP()) {
		fail(c, http.StatusUnauthorized,
			"未配置 web.token，Web 仪表盘只允许从本机（回环地址）访问；"+
				"如需远程访问，请在 %s 里设置 [web] token = \"随机字符串\"（或用 nyatmc web serve --token 临时指定）",
			s.ConfigPath())
		return false
	}
	return true
}

// requireWrite 在 web.enable_write = false 时拦下所有写操作。
func (s *Server) requireWrite() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !s.WriteEnabled() {
			fail(c, http.StatusForbidden,
				"配置里 web.enable_write = false，已禁止通过 Web 执行写操作；"+
					"改为 true 并保存配置后即可启用（配置改动会热重载）")
			return
		}
		c.Next()
	}
}

// ---------------------------------------------------------------- 路由

func (s *Server) buildEngine() *gin.Engine {
	r := gin.New()
	// 只信任 RemoteAddr：否则远端可以伪造 X-Forwarded-For: 127.0.0.1 绕过回环判定。
	_ = r.SetTrustedProxies(nil)
	r.Use(s.recovery(), s.securityHeaders(), s.accessLog())

	// 静态前端（不需要令牌，否则浏览器打不开页面；页面本身不含敏感数据）。
	r.GET("/", s.serveIndex)
	r.GET("/index.html", s.serveIndex)
	r.GET("/static/*filepath", s.serveAsset)
	r.GET("/api/health", s.handleHealth)

	api := r.Group("/api", s.auth())
	{
		// 只读接口。
		api.GET("/overview", s.handleOverview)
		api.GET("/instances", s.handleInstanceList)
		api.GET("/instances/:name", s.handleInstanceDetail)
		api.GET("/instances/:name/logs", s.handleLogs)
		api.GET("/instances/:name/logs/stream", s.handleLogStream)
		api.GET("/instances/:name/backups", s.handleBackupList)
		api.GET("/config", s.handleConfigGet)
		api.GET("/scheduler/jobs", s.handleSchedulerList)
		api.GET("/tunnel/status", s.handleTunnelStatus)

		// 写操作接口（web.enable_write = false 时统一 403）。
		w := api.Group("", s.requireWrite())
		w.POST("/instances/:name/start", s.handleStart)
		w.POST("/instances/:name/stop", s.handleStop)
		w.POST("/instances/:name/restart", s.handleRestart)
		w.POST("/instances/:name/command", s.handleCommand)
		w.POST("/instances/:name/backups", s.handleBackupCreate)
		w.POST("/instances/:name/backups/restore", s.handleBackupRestore)
		w.DELETE("/instances/:name/backups", s.handleBackupPrune)
		w.PUT("/config", s.handleConfigPut)
		w.POST("/scheduler/jobs", s.handleSchedulerCreate)
		w.DELETE("/scheduler/jobs/:name", s.handleSchedulerDelete)
		w.POST("/tunnel/start", s.handleTunnelStart)
		w.POST("/tunnel/stop", s.handleTunnelStop)
	}

	r.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api") {
			// 未知接口也走一次鉴权，避免未授权者探测有哪些接口。
			if !s.checkAuth(c) {
				return
			}
			fail(c, http.StatusNotFound, "接口不存在：%s %s", c.Request.Method, c.Request.URL.Path)
			return
		}
		if c.Request.Method != http.MethodGet {
			fail(c, http.StatusNotFound, "路径不存在：%s %s", c.Request.Method, c.Request.URL.Path)
			return
		}
		// 其余 GET 一律回落到单页应用。
		s.serveIndex(c)
	})
	return r
}

// ---------------------------------------------------------------- 静态资源

func (s *Server) serveIndex(c *gin.Context) {
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "text/html; charset=utf-8", s.index)
}

func (s *Server) serveAsset(c *gin.Context) {
	name := strings.TrimPrefix(c.Param("filepath"), "/")
	if name == "" {
		s.serveIndex(c)
		return
	}
	if !validAssetPath(name) {
		fail(c, http.StatusNotFound, "静态资源不存在：%s", name)
		return
	}
	data, err := fs.ReadFile(s.assets, name)
	if err != nil {
		fail(c, http.StatusNotFound, "静态资源不存在：%s", name)
		return
	}
	ctype := mime.TypeByExtension(path.Ext(name))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, ctype, data)
}

// validAssetPath 只接受干净的相对路径，杜绝目录穿越。
func validAssetPath(name string) bool {
	if !fs.ValidPath(name) {
		return false
	}
	if strings.ContainsAny(name, "\\:") || strings.Contains(name, "..") {
		return false
	}
	return true
}

// ---------------------------------------------------------------- 监听与生命周期

// Listen 绑定监听端口，返回实际监听地址。
func (s *Server) Listen() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln != nil {
		return s.ln.Addr().String(), nil
	}
	addr := s.ListenAddr()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("监听 %s 失败：%w（端口可能被占用；用 --listen 换一个地址，例如 127.0.0.1:8081）", addr, err)
	}
	s.ln = ln
	return ln.Addr().String(), nil
}

// URL 返回可供浏览器访问的地址（Listen 之后才有值）。
func (s *Server) URL() string {
	s.mu.RLock()
	ln := s.ln
	s.mu.RUnlock()
	if ln == nil {
		return ""
	}
	host, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		return "http://" + ln.Addr().String()
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// Serve 开始提供服务，直到 ctx 结束（此时优雅退出）。
func (s *Server) Serve(ctx context.Context) error {
	if _, err := s.Listen(); err != nil {
		return err
	}
	s.mu.Lock()
	ln := s.ln
	srv := &http.Server{
		Handler:           s.engine,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		// 不设 WriteTimeout：SSE 日志流是长连接。
	}
	s.srv = srv
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutCtx)
		case <-done:
		}
	}()

	err := srv.Serve(ln)
	close(done)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown 关闭监听。
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.RLock()
	srv := s.srv
	s.mu.RUnlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

// ---------------------------------------------------------------- 实例互斥锁

// instanceLocks 是「按实例名」的互斥锁，保证同一个实例的写操作串行执行。
type instanceLocks struct {
	mu sync.Mutex
	m  map[string]*lockEntry
}

type lockEntry struct {
	mu   sync.Mutex
	refs int
}

// lock 获取指定 key 的锁，返回释放函数。
func (l *instanceLocks) lock(key string) func() {
	l.mu.Lock()
	if l.m == nil {
		l.m = map[string]*lockEntry{}
	}
	e := l.m[key]
	if e == nil {
		e = &lockEntry{}
		l.m[key] = e
	}
	e.refs++
	l.mu.Unlock()

	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		l.mu.Lock()
		e.refs--
		if e.refs <= 0 {
			delete(l.m, key)
		}
		l.mu.Unlock()
	}
}

// ---------------------------------------------------------------- 小工具

func bearerToken(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	if len(header) >= 7 && strings.EqualFold(header[:7], "bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return ""
}

func isLoopbackIP(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

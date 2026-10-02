package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/yuzumikonami/nyatmc/internal/config"
	"github.com/yuzumikonami/nyatmc/pkg/logger"
)

// ---------------------------------------------------------------- 测试脚手架

// newTestServer 在临时目录里搭一套「两个实例 + 一份配置」的 Web 服务。
//
// 测试不依赖任何真实的 Minecraft 服务端：实例目录只是空目录，
// 状态探测会自然退化为「已停止」。
func newTestServer(t *testing.T, mutate func(*config.Config)) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)

	home := t.TempDir()
	config.SetHome(home)
	t.Cleanup(func() { config.SetHome("") })

	cfgPath := filepath.Join(home, "config.toml")
	cfg := config.Default()
	cfg.Path = cfgPath
	cfg.Web.EnableWrite = true
	cfg.Web.Token = ""
	cfg.Instances = map[string]config.InstanceConfig{
		"survival": {Type: "paper", MinecraftVersion: "1.21.4", Description: "生存服", CreatedAt: time.Now().Format(time.RFC3339)},
		"creative": {Type: "fabric", MinecraftVersion: "1.21.4", CreatedAt: time.Now().Format(time.RFC3339)},
	}
	if mutate != nil {
		mutate(cfg)
	}
	for name := range cfg.Instances {
		for _, dir := range []string{
			filepath.Join(home, "instances", name, "server"),
			filepath.Join(home, "instances", name, "backups"),
			filepath.Join(home, "instances", name, ".nyatmc"),
		} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("创建目录 %s 失败：%v", dir, err)
			}
		}
	}
	if err := cfg.SaveTo(cfgPath); err != nil {
		t.Fatalf("写入测试配置失败：%v", err)
	}
	mgr, err := config.NewManager(cfgPath)
	if err != nil {
		t.Fatalf("加载测试配置失败：%v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })

	srv, err := New(Options{Config: cfg, Manager: mgr, Logger: logger.Discard()})
	if err != nil {
		t.Fatalf("构造 Web 服务失败：%v", err)
	}
	return srv
}

type reqOpt func(*http.Request)

func withBearer(token string) reqOpt {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
}

func withRemote(addr string) reqOpt {
	return func(r *http.Request) { r.RemoteAddr = addr }
}

func call(srv *Server, method, target string, body any, opts ...reqOpt) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, o := range opts {
		o(req)
	}
	w := httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)
	return w
}

// decode 解析统一响应体 {"ok":...,"data":...,"error":...}。
func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是合法 JSON：%v\n%s", err, w.Body.String())
	}
	return out
}

func dataOf(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	body := decode(t, w)
	d, _ := body["data"].(map[string]any)
	return d
}

func mustStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("状态码 = %d，期望 %d；响应体：%s", w.Code, want, w.Body.String())
	}
}

// ---------------------------------------------------------------- 健康检查

func TestHealthIsPublic(t *testing.T) {
	srv := newTestServer(t, func(c *config.Config) { c.Web.Token = "s3cret" })

	// 即使配了令牌、即使来自公网地址，健康检查也必须放行。
	w := call(srv, http.MethodGet, "/api/health", nil, withRemote("203.0.113.9:5000"))
	mustStatus(t, w, http.StatusOK)

	body := decode(t, w)
	if body["ok"] != true {
		t.Fatalf("ok 应为 true：%v", body)
	}
	d := dataOf(t, w)
	if d["ok"] != true {
		t.Fatalf("data.ok 应为 true：%v", d)
	}
	if d["version"] == nil || d["version"] == "" {
		t.Fatalf("data.version 不应为空：%v", d)
	}
	if n, _ := d["instances"].(float64); int(n) != 2 {
		t.Fatalf("data.instances = %v，期望 2", d["instances"])
	}
	if d["token_required"] != true {
		t.Fatalf("data.token_required 应为 true：%v", d)
	}
}

// ---------------------------------------------------------------- 鉴权

func TestAuthWithToken(t *testing.T) {
	srv := newTestServer(t, func(c *config.Config) { c.Web.Token = "s3cret" })

	cases := []struct {
		name  string
		opts  []reqOpt
		target string
		want  int
	}{
		{name: "无令牌 401", target: "/api/overview", want: http.StatusUnauthorized},
		{name: "错误令牌 401", target: "/api/overview", opts: []reqOpt{withBearer("nope")}, want: http.StatusUnauthorized},
		{name: "错误 query 令牌 401", target: "/api/overview?token=nope", want: http.StatusUnauthorized},
		{name: "Bearer 通过", target: "/api/overview", opts: []reqOpt{withBearer("s3cret")}, want: http.StatusOK},
		{name: "bearer 大小写不敏感", target: "/api/overview", opts: []reqOpt{withBearer("s3cret")}, want: http.StatusOK},
		{name: "query 令牌通过", target: "/api/overview?token=s3cret", want: http.StatusOK},
		{name: "公网地址也要令牌", target: "/api/overview", opts: []reqOpt{withRemote("198.51.100.4:1234")}, want: http.StatusUnauthorized},
		{name: "未知接口也要鉴权", target: "/api/nope", want: http.StatusUnauthorized},
		{name: "未知接口带令牌 404", target: "/api/nope", opts: []reqOpt{withBearer("s3cret")}, want: http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := call(srv, http.MethodGet, tc.target, nil, tc.opts...)
			mustStatus(t, w, tc.want)
			if tc.want == http.StatusUnauthorized {
				body := decode(t, w)
				if body["ok"] != false {
					t.Fatalf("失败响应 ok 应为 false：%v", body)
				}
				msg, _ := body["error"].(string)
				if strings.TrimSpace(msg) == "" {
					t.Fatalf("失败响应缺少中文 error：%v", body)
				}
			}
		})
	}
}

func TestNoTokenLoopbackOnly(t *testing.T) {
	srv := newTestServer(t, nil)

	// httptest.NewRequest 默认 RemoteAddr 是 192.0.2.1:1234（公网地址）→ 401。
	w := call(srv, http.MethodGet, "/api/overview", nil)
	mustStatus(t, w, http.StatusUnauthorized)

	// 回环地址放行。
	w = call(srv, http.MethodGet, "/api/overview", nil, withRemote("127.0.0.1:54321"))
	mustStatus(t, w, http.StatusOK)

	w = call(srv, http.MethodGet, "/api/overview", nil, withRemote("[::1]:54321"))
	mustStatus(t, w, http.StatusOK)

	// 伪造 X-Forwarded-For 不能绕过回环判定（服务端不信任任何代理）。
	req := httptest.NewRequest(http.MethodGet, "/api/overview", nil)
	req.RemoteAddr = "198.51.100.7:9000"
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	rec := httptest.NewRecorder()
	srv.Engine().ServeHTTP(rec, req)
	mustStatus(t, rec, http.StatusUnauthorized)
}

// ---------------------------------------------------------------- 只读模式

func TestWriteDisabledReturns403(t *testing.T) {
	srv := newTestServer(t, func(c *config.Config) {
		c.Web.Token = "t"
		c.Web.EnableWrite = false
	})
	auth := withBearer("t")

	writes := []struct {
		method string
		target string
		body   any
	}{
		{http.MethodPost, "/api/instances/survival/start", nil},
		{http.MethodPost, "/api/instances/survival/stop", map[string]any{"force": true}},
		{http.MethodPost, "/api/instances/survival/restart", nil},
		{http.MethodPost, "/api/instances/survival/command", map[string]any{"line": "say hi"}},
		{http.MethodPost, "/api/instances/survival/backups", map[string]any{"label": "x"}},
		{http.MethodPost, "/api/instances/survival/backups/restore", map[string]any{"name": "latest"}},
		{http.MethodDelete, "/api/instances/survival/backups", map[string]any{"keep": 1}},
		{http.MethodPut, "/api/config", map[string]any{"toml": "[web]\nlisten = \"127.0.0.1:8080\"\n"}},
		{http.MethodPost, "/api/scheduler/jobs", map[string]any{"name": "a", "schedule": "0 4 * * *", "action": "backup"}},
		{http.MethodDelete, "/api/scheduler/jobs/a", nil},
		{http.MethodPost, "/api/tunnel/start", nil},
		{http.MethodPost, "/api/tunnel/stop", nil},
	}
	for _, tc := range writes {
		w := call(srv, tc.method, tc.target, tc.body, auth)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s %s 状态码 = %d，期望 403；响应体：%s", tc.method, tc.target, w.Code, w.Body.String())
		}
		body := decode(t, w)
		if body["ok"] != false {
			t.Fatalf("%s %s 失败响应 ok 应为 false", tc.method, tc.target)
		}
	}

	// 只读接口不受影响。
	w := call(srv, http.MethodGet, "/api/overview", nil, auth)
	mustStatus(t, w, http.StatusOK)
	if srv.WriteEnabled() {
		t.Fatal("web.enable_write = false 时 WriteEnabled() 应为 false")
	}
}

// ---------------------------------------------------------------- 实例名校验

func TestInstanceNameValidation(t *testing.T) {
	srv := newTestServer(t, nil)
	loop := withRemote("127.0.0.1:1234")

	cases := []struct {
		target string
		want   int
	}{
		{"/api/instances/bad%20name", http.StatusBadRequest},
		{"/api/instances/" + strings.Repeat("a", 80), http.StatusBadRequest},
		{"/api/instances/" + "%2e%2e", http.StatusBadRequest},
		// 含斜杠的路径不会匹配单段 :name，直接 404（同样不可能穿越目录）。
		{"/api/instances/..%2f..%2fetc", http.StatusNotFound},
		{"/api/instances/nosuch", http.StatusNotFound},
		{"/api/instances/survival", http.StatusOK},
		{"/api/instances/生存服", http.StatusNotFound}, // 合法名字但没登记
	}
	for _, tc := range cases {
		w := call(srv, http.MethodGet, tc.target, nil, loop)
		if w.Code != tc.want {
			t.Fatalf("GET %s 状态码 = %d，期望 %d；响应体：%s", tc.target, w.Code, tc.want, w.Body.String())
		}
	}
}

// ---------------------------------------------------------------- 路由表

func TestAllRoutesRegistered(t *testing.T) {
	srv := newTestServer(t, nil)
	registered := map[string]bool{}
	for _, r := range srv.Engine().Routes() {
		registered[r.Method+" "+r.Path] = true
	}
	if len(routeTable()) < 20 {
		t.Fatalf("路由表条目太少：%d", len(routeTable()))
	}
	for _, r := range routeTable() {
		key := r.Method + " " + r.Path
		if !registered[key] {
			t.Errorf("路由未注册：%s（%s）", key, r.Desc)
		}
	}
	// 健康检查必须免鉴权、其余 /api 必须鉴权。
	for _, r := range routeTable() {
		if r.Path == "/api/health" && r.Auth {
			t.Error("/api/health 不应要求鉴权")
		}
		if strings.HasPrefix(r.Path, "/api/") && r.Path != "/api/health" && !r.Auth {
			t.Errorf("%s 应当要求鉴权", r.Path)
		}
	}
}

// ---------------------------------------------------------------- 静态资源

func TestEmbeddedFrontend(t *testing.T) {
	srv := newTestServer(t, nil)
	loop := withRemote("127.0.0.1:1234")

	w := call(srv, http.MethodGet, "/", nil)
	mustStatus(t, w, http.StatusOK)
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("首页 Content-Type = %q", ct)
	}
	html := w.Body.String()
	for _, needle := range []string{"NyaTMC", "仪表盘", "/static/app.js", "/static/style.css"} {
		if !strings.Contains(html, needle) {
			t.Errorf("首页缺少 %q", needle)
		}
	}
	// 不能有任何外链（SVG 命名空间 http://www.w3.org/2000/svg 不是网络请求，先剔除）。
	plain := strings.ReplaceAll(html, "http://www.w3.org/2000/svg", "")
	for _, bad := range []string{"http://", "https://", "//cdn", "unpkg", "jsdelivr", "googleapis", "@import"} {
		if strings.Contains(plain, bad) {
			t.Errorf("首页引用了外部资源 %q", bad)
		}
	}
	if !strings.Contains(html, "data:image/svg+xml") {
		t.Error("首页应当使用内联 data: URI 图标")
	}

	for _, asset := range []string{"/static/app.js", "/static/style.css"} {
		w := call(srv, http.MethodGet, asset, nil)
		mustStatus(t, w, http.StatusOK)
		body := strings.ReplaceAll(w.Body.String(), "http://www.w3.org/2000/svg", "")
		if strings.Contains(body, "http://") || strings.Contains(body, "https://") {
			t.Errorf("%s 里出现了绝对外链", asset)
		}
	}

	// 静态资源不需要令牌（否则浏览器打不开页面）。
	w = call(srv, http.MethodGet, "/static/app.js", nil, withRemote("203.0.113.5:1"))
	mustStatus(t, w, http.StatusOK)

	// 不存在的资源 → JSON 404。
	w = call(srv, http.MethodGet, "/static/nope.js", nil, loop)
	mustStatus(t, w, http.StatusNotFound)

	// 未知前端路由回落到单页应用。
	w = call(srv, http.MethodGet, "/whatever", nil)
	mustStatus(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), "NyaTMC") {
		t.Error("未知前端路由应回落首页")
	}

	// 安全响应头。
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("缺少 X-Content-Type-Options 响应头")
	}
	if w.Header().Get("Content-Security-Policy") == "" {
		t.Error("缺少 Content-Security-Policy 响应头")
	}
}

// TestFrontendConsistency 检查前端页面里引用的资源与元素 id 都真实存在，
// 避免改 HTML/JS 时漏改一处，页面在浏览器里静默失灵。
func TestFrontendConsistency(t *testing.T) {
	srv := newTestServer(t, nil)

	html, err := fs.ReadFile(srv.assets, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	js, err := fs.ReadFile(srv.assets, "app.js")
	if err != nil {
		t.Fatal(err)
	}

	// 1) 页面引用的静态资源必须真的嵌进去了。
	assetRe := regexp.MustCompile(`(?:src|href)="(/static/[^"]+)"`)
	found := 0
	for _, m := range assetRe.FindAllStringSubmatch(string(html), -1) {
		found++
		name := strings.TrimPrefix(m[1], "/static/")
		if _, err := fs.Stat(srv.assets, name); err != nil {
			t.Errorf("页面引用了不存在的内嵌资源 %s：%v", m[1], err)
		}
	}
	if found == 0 {
		t.Error("页面里没有引用任何 /static/ 资源，检查 HTML 是否写错")
	}

	// 2) JS 里 $('xxx') 用到的 id 必须在 HTML 里存在。
	idSet := map[string]bool{}
	for _, m := range regexp.MustCompile(`id="([^"]+)"`).FindAllStringSubmatch(string(html), -1) {
		idSet[m[1]] = true
	}
	used := map[string]bool{}
	for _, m := range regexp.MustCompile(`\$\('([^']+)'\)`).FindAllStringSubmatch(string(js), -1) {
		used[m[1]] = true
	}
	if len(used) < 20 {
		t.Fatalf("app.js 里只引用了 %d 个元素 id，看起来不对", len(used))
	}
	for id := range used {
		if !idSet[id] {
			t.Errorf("app.js 引用了 HTML 里不存在的 id %q", id)
		}
	}

	// 3) 页面不能有内联脚本（CSP 只允许同源脚本）。
	//    Go 的 regexp 不支持负向前瞻（(?!...)），这里逐个检查 script 标签。
	for _, tag := range regexp.MustCompile(`(?i)<script[^>]*>`).FindAllString(string(html), -1) {
		if !strings.Contains(strings.ToLower(tag), "src=") {
			t.Errorf("首页出现了内联 <script>（缺少 src=），会被 CSP 拦下：%s", tag)
		}
	}
	// 4) 页面不能用内联事件处理器（onclick 等）。
	if regexp.MustCompile(`(?i)\son(click|load|change|submit|input)=`).MatchString(string(html)) {
		t.Error("首页出现了内联事件处理器，会被 CSP 拦下")
	}
}

func TestValidAssetPath(t *testing.T) {
	cases := map[string]bool{
		"app.js":          true,
		"style.css":       true,
		"img/icon.svg":    true,
		"../secret":       false,
		"a/../../b":       false,
		"/etc/passwd":     false,
		"a\\b":            false,
		"C:/windows":      false,
		"":                false,
	}
	for in, want := range cases {
		if got := validAssetPath(in); got != want {
			t.Errorf("validAssetPath(%q) = %v，期望 %v", in, got, want)
		}
	}
}

// ---------------------------------------------------------------- 概览与详情

func TestOverviewAndDetail(t *testing.T) {
	srv := newTestServer(t, nil)
	loop := withRemote("127.0.0.1:1234")

	w := call(srv, http.MethodGet, "/api/overview", nil, loop)
	mustStatus(t, w, http.StatusOK)
	d := dataOf(t, w)
	if n, _ := d["count"].(float64); int(n) != 2 {
		t.Fatalf("overview count = %v，期望 2", d["count"])
	}
	list, _ := d["instances"].([]any)
	if len(list) != 2 {
		t.Fatalf("instances 长度 = %d，期望 2", len(list))
	}
	first, _ := list[0].(map[string]any)
	if first["state_label"] == nil || first["state_label"] == "" {
		t.Errorf("缺少 state_label：%v", first)
	}
	if first["state"] != "stopped" {
		t.Errorf("空实例的状态应为 stopped，实际 %v", first["state"])
	}
	if first["supervisor_alive"] != false {
		t.Errorf("实例未运行，supervisor_alive 应为 false：%v", first["supervisor_alive"])
	}

	w = call(srv, http.MethodGet, "/api/instances", nil, loop)
	mustStatus(t, w, http.StatusOK)
	d = dataOf(t, w)
	if n, _ := d["count"].(float64); int(n) != 2 {
		t.Fatalf("instances count = %v，期望 2", d["count"])
	}
	if d["default"] != "default" {
		t.Errorf("default 实例 = %v", d["default"])
	}

	w = call(srv, http.MethodGet, "/api/instances/survival", nil, loop)
	mustStatus(t, w, http.StatusOK)
	d = dataOf(t, w)
	if d["name"] != "survival" {
		t.Errorf("详情 name = %v", d["name"])
	}
	if _, ok := d["status"].(map[string]any); !ok {
		t.Errorf("详情缺少 status 对象：%v", d)
	}
	if _, ok := d["dirs"].(map[string]any); !ok {
		t.Errorf("详情缺少 dirs 对象：%v", d)
	}
	if _, ok := d["metadata"].(map[string]any); !ok {
		t.Errorf("详情缺少 metadata 对象：%v", d)
	}
}

// ---------------------------------------------------------------- 日志

func TestLogsEndpoint(t *testing.T) {
	srv := newTestServer(t, nil)
	loop := withRemote("127.0.0.1:1234")

	w := call(srv, http.MethodGet, "/api/instances/survival/logs?lines=10", nil, loop)
	mustStatus(t, w, http.StatusOK)
	body := decode(t, w)
	if body["ok"] != true {
		t.Fatalf("ok 应为 true：%v", body)
	}
	if _, ok := body["data"].([]any); !ok {
		t.Fatalf("data 应当是文本行数组，实际 %T", body["data"])
	}
	if n, _ := body["count"].(float64); int(n) != 0 {
		t.Fatalf("count = %v，期望 0", body["count"])
	}
}

func TestLogStreamIsSSE(t *testing.T) {
	srv := newTestServer(t, nil)

	ts := httptest.NewServer(srv.Engine())
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/instances/survival/logs/stream?lines=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求 SSE 失败：%v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE 状态码 = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("SSE Content-Type = %q", ct)
	}

	// 读取前几行，应当能看到 hello 事件（readSSE 会自己超时，不会挂住测试）。
	lines, err := readSSE(resp.Body, "event: hello", 5*time.Second)
	if err != nil {
		t.Fatalf("读取 SSE 失败：%v\n已读到：%v", err, lines)
	}
	cancel()
}

// readSSE 一直读到出现 needle（或超时），返回已读到的行。
func readSSE(body io.Reader, needle string, timeout time.Duration) ([]string, error) {
	type result struct {
		lines []string
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		var lines []string
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			lines = append(lines, line)
			if strings.Contains(line, needle) {
				ch <- result{lines: lines}
				return
			}
		}
		ch <- result{lines: lines, err: sc.Err()}
	}()
	select {
	case r := <-ch:
		return r.lines, r.err
	case <-time.After(timeout):
		return nil, context.DeadlineExceeded
	}
}

// ---------------------------------------------------------------- 指令

func TestCommandOnStoppedInstance(t *testing.T) {
	srv := newTestServer(t, nil)
	loop := withRemote("127.0.0.1:1234")

	// 空指令 → 400
	w := call(srv, http.MethodPost, "/api/instances/survival/command", map[string]any{"line": "  "}, loop)
	mustStatus(t, w, http.StatusBadRequest)

	// 换行 → 400
	w = call(srv, http.MethodPost, "/api/instances/survival/command", map[string]any{"line": "say a\nsay b"}, loop)
	mustStatus(t, w, http.StatusBadRequest)

	// 非法 JSON → 400
	req := httptest.NewRequest(http.MethodPost, "/api/instances/survival/command", strings.NewReader("{not json"))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	srv.Engine().ServeHTTP(rec, req)
	mustStatus(t, rec, http.StatusBadRequest)

	// 实例未运行 → 409
	w = call(srv, http.MethodPost, "/api/instances/survival/command", map[string]any{"line": "say hi"}, loop)
	mustStatus(t, w, http.StatusConflict)
}

// ---------------------------------------------------------------- 备份

func TestBackupsListAndPrune(t *testing.T) {
	srv := newTestServer(t, nil)
	loop := withRemote("127.0.0.1:1234")

	w := call(srv, http.MethodGet, "/api/instances/survival/backups", nil, loop)
	mustStatus(t, w, http.StatusOK)
	d := dataOf(t, w)
	if n, _ := d["count"].(float64); int(n) != 0 {
		t.Fatalf("空目录的备份数应为 0：%v", d["count"])
	}
	if _, ok := d["backups"].([]any); !ok {
		t.Fatalf("backups 应当是数组：%T", d["backups"])
	}
	if !strings.Contains(d["dir"].(string), "backups") {
		t.Errorf("备份目录 = %v", d["dir"])
	}

	// 没有保留策略时清理应当报 400（配置里 keep = 0 / max_age = 0）。
	w = call(srv, http.MethodDelete, "/api/instances/survival/backups", map[string]any{"keep": 0}, loop)
	mustStatus(t, w, http.StatusBadRequest)

	// 指定 keep 时可以清理（空目录下删除 0 个）。
	w = call(srv, http.MethodDelete, "/api/instances/survival/backups", map[string]any{"keep": 2}, loop)
	mustStatus(t, w, http.StatusOK)

	// 回滚不存在的备份 → 404。
	w = call(srv, http.MethodPost, "/api/instances/survival/backups/restore", map[string]any{"name": "nope.tar.gz"}, loop)
	mustStatus(t, w, http.StatusNotFound)

	// 回滚目标路径越界 → 400。
	w = call(srv, http.MethodPost, "/api/instances/survival/backups/restore",
		map[string]any{"name": "latest", "targets": []string{"../outside"}}, loop)
	mustStatus(t, w, http.StatusBadRequest)
}

func TestValidRelTarget(t *testing.T) {
	cases := map[string]bool{
		"world":            true,
		"world/level.dat":  true,
		"../etc":           false,
		"/etc/passwd":      false,
		"a/../../b":        false,
		"":                 false,
		"c:/windows":       false,
	}
	for in, want := range cases {
		if got := validRelTarget(in) == nil; got != want {
			t.Errorf("validRelTarget(%q) 合法 = %v，期望 %v", in, got, want)
		}
	}
}

// ---------------------------------------------------------------- 配置读写

func TestConfigGetAndPut(t *testing.T) {
	srv := newTestServer(t, nil)
	loop := withRemote("127.0.0.1:1234")

	w := call(srv, http.MethodGet, "/api/config", nil, loop)
	mustStatus(t, w, http.StatusOK)
	d := dataOf(t, w)
	raw, _ := d["toml"].(string)
	if !strings.Contains(raw, "[server]") || !strings.Contains(raw, "[web]") {
		t.Fatalf("配置原文不完整：%s", raw)
	}
	if d["path"] != srv.ConfigPath() {
		t.Errorf("path = %v，期望 %v", d["path"], srv.ConfigPath())
	}
	if _, ok := d["resolved"].(map[string]any); !ok {
		t.Errorf("缺少 resolved 结构化视图：%v", d)
	}

	// 语法错误 → 400，且不写盘。
	before, _ := os.ReadFile(srv.ConfigPath())
	w = call(srv, http.MethodPut, "/api/config", map[string]any{"toml": "这不是 toml !!!"}, loop)
	mustStatus(t, w, http.StatusBadRequest)
	if msg, _ := decode(t, w)["error"].(string); !strings.Contains(msg, "解析") {
		t.Errorf("错误信息应当说明解析失败：%v", msg)
	}
	after, _ := os.ReadFile(srv.ConfigPath())
	if !bytes.Equal(before, after) {
		t.Fatal("校验失败时不应该写盘")
	}

	// 未知字段 → 400（DisallowUnknownFields）。
	w = call(srv, http.MethodPut, "/api/config", map[string]any{
		"toml": raw + "\n[unknown_section]\nfoo = 1\n",
	}, loop)
	mustStatus(t, w, http.StatusBadRequest)

	// 语义错误（内存写法非法）→ 400。
	memRe := regexp.MustCompile(`memory\s*=\s*['"]2G['"]`)
	bad := memRe.ReplaceAllString(raw, `memory = "两个G"`)
	if bad == raw {
		t.Fatalf("测试前置条件失败：没找到 server.memory，配置如下：\n%s", raw)
	}
	w = call(srv, http.MethodPut, "/api/config", map[string]any{"toml": bad}, loop)
	mustStatus(t, w, http.StatusBadRequest)
	if msg, _ := decode(t, w)["error"].(string); !strings.Contains(msg, "校验") {
		t.Errorf("错误信息应当说明校验失败：%v", msg)
	}

	// 合法修改 → 200，并且立即生效。
	good := memRe.ReplaceAllString(raw, `memory = "4G"`)
	w = call(srv, http.MethodPut, "/api/config", map[string]any{"toml": good}, loop)
	mustStatus(t, w, http.StatusOK)
	if got := srv.Manager().Current().Server.Memory; got != "4G" {
		t.Fatalf("保存后内存配置 = %q，期望 4G", got)
	}
	onDisk, err := os.ReadFile(srv.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`memory\s*=\s*['"]4G['"]`).MatchString(string(onDisk)) {
		t.Fatalf("磁盘上的配置没有更新：%s", string(onDisk))
	}
	if _, err := os.Stat(srv.ConfigPath() + ".bak"); err != nil {
		t.Errorf("应当留下配置备份 .bak：%v", err)
	}

	// 空内容 → 400。
	w = call(srv, http.MethodPut, "/api/config", map[string]any{"toml": "   "}, loop)
	mustStatus(t, w, http.StatusBadRequest)
}

func TestConfigGetWithInstance(t *testing.T) {
	srv := newTestServer(t, nil)
	loop := withRemote("127.0.0.1:1234")

	w := call(srv, http.MethodGet, "/api/config?instance=survival", nil, loop)
	mustStatus(t, w, http.StatusOK)
	d := dataOf(t, w)
	if _, ok := d["instance_resolved"].(map[string]any); !ok {
		t.Fatalf("缺少 instance_resolved：%v", d)
	}

	w = call(srv, http.MethodGet, "/api/config?instance=nosuch", nil, loop)
	mustStatus(t, w, http.StatusNotFound)

	w = call(srv, http.MethodGet, "/api/config?instance=bad%20name", nil, loop)
	mustStatus(t, w, http.StatusBadRequest)
}

// ---------------------------------------------------------------- 调度任务

func TestSchedulerCRUD(t *testing.T) {
	srv := newTestServer(t, nil)
	loop := withRemote("127.0.0.1:1234")

	w := call(srv, http.MethodGet, "/api/scheduler/jobs", nil, loop)
	mustStatus(t, w, http.StatusOK)
	d := dataOf(t, w)
	if n, _ := d["count"].(float64); int(n) != 0 {
		t.Fatalf("初始任务数 = %v，期望 0", d["count"])
	}

	// 非法 cron。
	w = call(srv, http.MethodPost, "/api/scheduler/jobs", map[string]any{
		"name": "bad", "schedule": "不是cron", "action": "backup",
	}, loop)
	mustStatus(t, w, http.StatusBadRequest)

	// 非法动作。
	w = call(srv, http.MethodPost, "/api/scheduler/jobs", map[string]any{
		"name": "bad", "schedule": "0 4 * * *", "action": "explode",
	}, loop)
	mustStatus(t, w, http.StatusBadRequest)

	// command 动作缺少指令。
	w = call(srv, http.MethodPost, "/api/scheduler/jobs", map[string]any{
		"name": "bad", "schedule": "0 4 * * *", "action": "command",
	}, loop)
	mustStatus(t, w, http.StatusBadRequest)

	// 目标实例不存在。
	w = call(srv, http.MethodPost, "/api/scheduler/jobs", map[string]any{
		"name": "bad", "schedule": "0 4 * * *", "action": "backup", "instance": "ghost",
	}, loop)
	mustStatus(t, w, http.StatusBadRequest)

	// 任务名非法。
	w = call(srv, http.MethodPost, "/api/scheduler/jobs", map[string]any{
		"name": "坏 名字", "schedule": "0 4 * * *", "action": "backup",
	}, loop)
	mustStatus(t, w, http.StatusBadRequest)

	// 正常新增。
	w = call(srv, http.MethodPost, "/api/scheduler/jobs", map[string]any{
		"name": "daily-backup", "schedule": "0 4 * * *", "action": "backup",
		"instance": "survival", "label": "auto",
	}, loop)
	mustStatus(t, w, http.StatusOK)

	// 重复新增。
	w = call(srv, http.MethodPost, "/api/scheduler/jobs", map[string]any{
		"name": "daily-backup", "schedule": "0 4 * * *", "action": "backup",
	}, loop)
	mustStatus(t, w, http.StatusBadRequest)

	// 列表里应当带上下次执行时间。
	w = call(srv, http.MethodGet, "/api/scheduler/jobs", nil, loop)
	mustStatus(t, w, http.StatusOK)
	d = dataOf(t, w)
	jobs, _ := d["jobs"].([]any)
	if len(jobs) != 1 {
		t.Fatalf("任务数 = %d，期望 1", len(jobs))
	}
	job, _ := jobs[0].(map[string]any)
	if job["name"] != "daily-backup" {
		t.Errorf("任务名 = %v", job["name"])
	}
	if next, _ := job["next_run"].(string); next == "" {
		t.Errorf("缺少下次执行时间：%v", job)
	}
	if nextHuman, _ := job["next_human"].(string); !strings.HasPrefix(nextHuman, "还有") {
		t.Errorf("下次执行的中文描述异常：%v", job["next_human"])
	}

	// 保存后配置里确实有这条任务。
	cur := srv.Manager().Current()
	if len(cur.Scheduler.Jobs) != 1 || cur.Scheduler.Jobs[0].Name != "daily-backup" {
		t.Fatalf("配置里的任务不对：%+v", cur.Scheduler.Jobs)
	}

	// 删除。
	w = call(srv, http.MethodDelete, "/api/scheduler/jobs/daily-backup", nil, loop)
	mustStatus(t, w, http.StatusOK)
	if len(srv.Manager().Current().Scheduler.Jobs) != 0 {
		t.Fatal("删除后配置里仍有任务")
	}

	// 再删一次 → 404。
	w = call(srv, http.MethodDelete, "/api/scheduler/jobs/daily-backup", nil, loop)
	mustStatus(t, w, http.StatusNotFound)
}

// ---------------------------------------------------------------- 穿透

func TestTunnelAPI(t *testing.T) {
	// 状态：没有隧道运行时 available=true、running=false。
	srv := newTestServer(t, nil)
	loop := withRemote("127.0.0.1:1234")

	w := call(srv, http.MethodGet, "/api/tunnel/status", nil, loop)
	mustStatus(t, w, http.StatusOK)
	d := dataOf(t, w)
	if d["available"] != true {
		t.Fatalf("tunnel.available 应为 true：%v", d)
	}
	if d["running"] != false {
		t.Fatalf("tunnel.running 应为 false：%v", d)
	}

	// stop 幂等：没有运行中的隧道也返回成功。
	w = call(srv, http.MethodPost, "/api/tunnel/stop", nil, loop)
	mustStatus(t, w, http.StatusOK)
	if d := dataOf(t, w); d["running"] != false {
		t.Fatalf("stop 后 running 应为 false：%v", d)
	}

	// start：cloudflared 不存在且禁止自动下载 → 502，并说明原因。
	srv2 := newTestServer(t, func(cfg *config.Config) {
		cfg.Tunnel.Bin = filepath.Join(t.TempDir(), "no-such-cloudflared")
		cfg.Tunnel.AutoInstall = false
	})
	w = call(srv2, http.MethodPost, "/api/tunnel/start", nil, loop)
	mustStatus(t, w, http.StatusBadGateway)
	if msg, _ := decode(t, w)["error"].(string); !strings.Contains(msg, "cloudflared") {
		t.Errorf("502 信息应当提到 cloudflared：%v", msg)
	}
}

// ---------------------------------------------------------------- 其它

func TestNewRequiresConfig(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("没有配置时 New 应当报错")
	}
}

func TestNewCreatesConfigWhenMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	home := t.TempDir()
	config.SetHome(home)
	t.Cleanup(func() { config.SetHome("") })

	cfgPath := filepath.Join(home, "config.toml")
	cfg := config.Default()
	cfg.Path = cfgPath
	srv, err := New(Options{Config: cfg, Logger: logger.Discard()})
	if err != nil {
		t.Fatalf("New 失败：%v", err)
	}
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("配置不存在时应当自动写出：%v", err)
	}
	if srv.ConfigPath() != cfgPath {
		t.Fatalf("ConfigPath = %q，期望 %q", srv.ConfigPath(), cfgPath)
	}
}

func TestListenAndURL(t *testing.T) {
	srv := newTestServer(t, func(c *config.Config) { c.Web.Listen = "127.0.0.1:0" })
	addr, err := srv.Listen()
	if err != nil {
		t.Fatalf("Listen 失败：%v", err)
	}
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("监听地址 = %q", addr)
	}
	url := srv.URL()
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("URL = %q", url)
	}
	if !srv.Loopback() {
		t.Fatal("127.0.0.1:0 应当被判定为回环地址")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()

	// 真实请求一次。
	resp, err := http.Get(url + "/api/health")
	if err != nil {
		cancel()
		t.Fatalf("请求 %s 失败：%v", url, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("健康检查状态码 = %d", resp.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve 退出异常：%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve 没有在 ctx 取消后退出")
	}
}

func TestHumanDuration(t *testing.T) {
	cases := map[time.Duration]string{
		0:                    "-",
		5 * time.Second:      "5秒",
		90 * time.Second:     "1分30秒",
		2 * time.Hour:        "2小时0分",
		50 * time.Hour:       "2天2小时",
	}
	for in, want := range cases {
		if got := humanDuration(in); got != want {
			t.Errorf("humanDuration(%v) = %q，期望 %q", in, got, want)
		}
	}
}

func TestTailFileLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	var sb strings.Builder
	for i := 0; i < 50; i++ {
		sb.WriteString("line-")
		sb.WriteString(strings.Repeat("x", i%7))
		sb.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	got := tailFileLines(path, 5)
	if len(got) != 5 {
		t.Fatalf("tailFileLines 返回 %d 行，期望 5", len(got))
	}
	if tailFileLines(filepath.Join(dir, "missing.log"), 5) != nil {
		t.Fatal("文件不存在时应当返回 nil")
	}
}

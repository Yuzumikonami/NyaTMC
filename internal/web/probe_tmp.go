package web

// 本文件集中登记对外路由表，供测试与文档使用。
//
// 说明：文件名沿用了最初的“依赖探测”临时脚本名（沙箱不允许删除文件），
// 内容已经是真实实现，可以安全地重命名为 routes.go。

// Route 描述一条对外路由。
type Route struct {
	// Method 是 HTTP 方法。
	Method string
	// Path 是 gin 路由模板（:name 表示实例名）。
	Path string
	// Auth 表示是否需要令牌校验（/api/health 免鉴权）。
	Auth bool
	// Write 表示是否是写操作（web.enable_write = false 时返回 403）。
	Write bool
	// Desc 是中文说明。
	Desc string
}

// Routes 返回完整的对外路由表（顺序与注册顺序一致）。
func (s *Server) Routes() []Route { return routeTable() }

// routeTable 是路由表的唯一事实来源：buildEngine 按它注册，测试按它校验。
func routeTable() []Route {
	return []Route{
		{Method: "GET", Path: "/", Auth: false, Write: false, Desc: "内嵌前端首页"},
		{Method: "GET", Path: "/static/*filepath", Auth: false, Write: false, Desc: "内嵌静态资源（无 CDN）"},
		{Method: "GET", Path: "/api/health", Auth: false, Write: false, Desc: "健康检查（免鉴权）"},

		{Method: "GET", Path: "/api/overview", Auth: true, Desc: "所有实例的汇总状态"},
		{Method: "GET", Path: "/api/instances", Auth: true, Desc: "实例列表"},
		{Method: "GET", Path: "/api/instances/:name", Auth: true, Desc: "单实例详情"},
		{Method: "GET", Path: "/api/instances/:name/logs", Auth: true, Desc: "最近 N 行日志"},
		{Method: "GET", Path: "/api/instances/:name/logs/stream", Auth: true, Desc: "SSE 实时日志与状态"},
		{Method: "GET", Path: "/api/instances/:name/backups", Auth: true, Desc: "备份列表"},
		{Method: "GET", Path: "/api/config", Auth: true, Desc: "读取配置原文与结构化视图"},
		{Method: "GET", Path: "/api/scheduler/jobs", Auth: true, Desc: "调度任务列表"},
		{Method: "GET", Path: "/api/tunnel/status", Auth: true, Desc: "穿透状态"},

		{Method: "POST", Path: "/api/instances/:name/start", Auth: true, Write: true, Desc: "启动实例"},
		{Method: "POST", Path: "/api/instances/:name/stop", Auth: true, Write: true, Desc: "停止实例"},
		{Method: "POST", Path: "/api/instances/:name/restart", Auth: true, Write: true, Desc: "重启实例"},
		{Method: "POST", Path: "/api/instances/:name/command", Auth: true, Write: true, Desc: "发送控制台指令"},
		{Method: "POST", Path: "/api/instances/:name/backups", Auth: true, Write: true, Desc: "立即备份"},
		{Method: "POST", Path: "/api/instances/:name/backups/restore", Auth: true, Write: true, Desc: "回滚备份"},
		{Method: "DELETE", Path: "/api/instances/:name/backups", Auth: true, Write: true, Desc: "清理旧备份"},
		{Method: "PUT", Path: "/api/config", Auth: true, Write: true, Desc: "校验并写回配置"},
		{Method: "POST", Path: "/api/scheduler/jobs", Auth: true, Write: true, Desc: "新增调度任务"},
		{Method: "DELETE", Path: "/api/scheduler/jobs/:name", Auth: true, Write: true, Desc: "删除调度任务"},
		{Method: "POST", Path: "/api/tunnel/start", Auth: true, Write: true, Desc: "启动穿透（默认暴露 Web 自身）"},
		{Method: "POST", Path: "/api/tunnel/stop", Auth: true, Write: true, Desc: "停止穿透"},
	}
}

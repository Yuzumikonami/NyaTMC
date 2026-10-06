package version

// ldflags 注入变量 用于版本判断和自动更新
// Use ldflags to inject vars | Usage: Auto Update
var (
	Version    = "dev"
	Date       = "unknown"
	Autoupdate = "nil"
)

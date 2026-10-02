package cmd

import (
	"fmt"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/yuzumikonami/nyatmc/internal/config"
)

// 这些变量可以通过 -ldflags 注入：
//
//	go build -ldflags "-X github.com/yuzumikonami/nyatmc/cmd.Version=1.0.0"
var (
	// Version 是 nyatmc 的版本号。
	Version = "0.1.0-dev"
	// Commit 是构建时的 git 提交。
	Commit = "unknown"
	// BuildDate 是构建时间。
	BuildDate = "unknown"
)

func init() {
	config.Version = Version
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "显示 nyatmc 版本与运行环境",
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		info := map[string]any{
			"version":    Version,
			"commit":     Commit,
			"build_date": BuildDate,
			"go":         runtime.Version(),
			"os":         runtime.GOOS,
			"arch":       runtime.GOARCH,
			"home":       config.Home(),
			"config":     app.Cfg.Path,
		}
		if app.JSON {
			return app.JSONOut(info)
		}
		app.Out("nyatmc %s", Version)
		app.Out("  提交      %s", Commit)
		app.Out("  构建时间  %s", BuildDate)
		app.Out("  运行环境  %s/%s (%s)", runtime.GOOS, runtime.GOARCH, runtime.Version())
		app.Out("  数据目录  %s", config.Home())
		app.Out("  配置文件  %s", app.Cfg.Path)
		return nil
	},
}

// humanTime 供其它命令展示时间戳。
func humanTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02 15:04:05")
}

// percent 计算百分比（0-100），总量为 0 时返回 0。
func percent(done, total int64) float64 {
	if total <= 0 {
		return 0
	}
	p := float64(done) / float64(total) * 100
	if p > 100 {
		p = 100
	}
	return p
}

// fmtPercent 输出百分比文本。
func fmtPercent(done, total int64) string {
	return fmt.Sprintf("%.1f%%", percent(done, total))
}

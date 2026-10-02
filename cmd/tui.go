package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yuzumikonami/nyatmc/internal/daemon"
	"github.com/yuzumikonami/nyatmc/internal/instance"
	"github.com/yuzumikonami/nyatmc/internal/tui"
)

var (
	tuiRefresh     time.Duration
	tuiNoAltScreen bool
	tuiInstances   string
)

var tuiCmd = &cobra.Command{
	Use:   "tui [实例名]",
	Short: "打开终端看板（纯 ANSI，单键启停 / 备份 / 改配置）",
	Long: `打开终端看板：全屏、纯 ANSI 转义码渲染，不需要任何 TUI 库，Termux 上也能跑。

看板会通过 supervisor 的控制通道订阅实时日志与状态；拿不到推送时自动退化为
每秒轮询。所有按键都是单键生效，不需要回车：

  S 启动   E 停止（优雅关闭）   K 强制结束   R 重启   B 立即备份
  C 配置   L 只看日志   P 只看玩家   Tab/数字键 切换实例
  ↑/↓ 滚动一行   PgUp/PgDn 翻页   Home/End 跳最早/回到末尾
  ?/h 帮助   Q 退出

配置模式里可以先浏览 ~/.nyatmc/config.toml 的关键项，按 Enter 就地修改：
保存时先读盘、只改你编辑的那一项、整体校验通过后才原子写回，
运行中的守护进程与 Web 会自动热重载。`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newAppInstance(cmd, args)
		if err != nil {
			return err
		}

		if tuiRefresh <= 0 {
			return fmt.Errorf("--refresh 必须是正数时长，例如 500ms、2s")
		}
		if tuiRefresh < 100*time.Millisecond {
			app.Warn("刷新间隔 %s 太短，已提升到 100ms 以免占满 CPU", tuiRefresh)
			tuiRefresh = 100 * time.Millisecond
		}

		names, err := tuiPickInstances(app, tuiInstances)
		if err != nil {
			return err
		}

		opts := tui.Options{
			Instance:    app.Inst.Name,
			Refresh:     tuiRefresh,
			NoAltScreen: tuiNoAltScreen,
			Instances:   names,
		}
		err = tui.Run(app.Ctx, app.Cfg, opts)
		switch {
		case err == nil:
			app.Out("已退出看板（不会停止服务端）。")
			return nil
		case errors.Is(err, tui.ErrNotTTY):
			// 非交互环境（重定向、cron、日志采集）不喷转义码，改为打印一份表格
			app.Warn("当前不是交互式终端，终端看板需要 TTY；下面用表格展示实例状态。")
			tuiPrintFallback(app, names)
			return nil
		default:
			return err
		}
	},
}

// tuiPickInstances 解析 --instances，并为多实例场景补齐实例名列表。
func tuiPickInstances(app *App, raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '，' }) {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		if !app.Cfg.HasInstance(name) {
			hint := ""
			if others := app.Cfg.InstanceNames(); len(others) > 0 {
				hint = fmt.Sprintf("，可用实例：%s", strings.Join(others, "、"))
			}
			return nil, fmt.Errorf("实例 %q 不存在%s", name, hint)
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--instances 里没有有效的实例名（示例：--instances survival,creative）")
	}
	return out, nil
}

// tuiPrintFallback 在非 TTY 环境下输出状态表格与操作提示。
func tuiPrintFallback(app *App, names []string) {
	if len(names) == 0 {
		names = app.Cfg.InstanceNames()
	}
	if len(names) == 0 {
		app.Out("还没有任何实例：运行 nyatmc init 初始化，或 nyatmc create <名字> 新建实例。")
		return
	}

	rows := make([][]string, 0, len(names))
	for _, name := range names {
		inst, err := instance.New(app.Cfg, name)
		if err != nil {
			rows = append(rows, []string{name, "配置错误", "-", "-", err.Error()})
			continue
		}
		st, _ := daemon.GetStatus(app.Ctx, app.Cfg, inst)
		pid := "-"
		if st.PID > 0 {
			pid = fmt.Sprintf("%d", st.PID)
		}
		mem := "-"
		if st.MemoryUsedMB > 0 && st.MemoryLimit != "" {
			mem = fmt.Sprintf("%d MB / %s", st.MemoryUsedMB, st.MemoryLimit)
		} else if st.MemoryLimit != "" {
			mem = st.MemoryLimit
		}
		rows = append(rows, []string{
			name,
			app.StateLabel(st.State),
			pid,
			HumanDuration(st.Uptime()),
			mem,
		})
	}
	app.Table([]string{"实例", "状态", "PID", "运行时长", "内存"}, rows)
	app.Out("")
	app.Out("在真正的终端里运行 nyatmc tui 可以使用完整看板；")
	app.Out("脚本里请改用：nyatmc status --json / nyatmc logs -f / nyatmc start -f")
	if !stdoutIsTerminal() {
		app.Out("提示：输出被重定向时不会绘制看板。")
	}
}

// stdoutIsTerminal 判断标准输出是否为终端（不依赖 x/term，便于在脚本里判断）。
func stdoutIsTerminal() bool {
	st, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

func init() {
	tuiCmd.Flags().DurationVar(&tuiRefresh, "refresh", time.Second, "刷新间隔（默认 1s）")
	tuiCmd.Flags().BoolVar(&tuiNoAltScreen, "no-alt-screen", false, "不使用备用屏幕缓冲区（调试用：原地刷新）")
	tuiCmd.Flags().StringVar(&tuiInstances, "instances", "", "只监控这些实例，用逗号分隔（例如 a,b）")

	rootCmd.AddCommand(tuiCmd)
}

// Package cmd 实现 nyatmc 的命令行界面。
//
// 每个文件负责一组命令，统一通过 rootCmd.AddCommand 注册；所有命令共享
// root.go 里的全局标志与 App 运行上下文（配置、实例、日志、输出格式）。
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/yuzumikonami/nyatmc/internal/config"
	"github.com/yuzumikonami/nyatmc/internal/daemon"
	"github.com/yuzumikonami/nyatmc/internal/instance"
	"github.com/yuzumikonami/nyatmc/pkg/logger"
)

// 全局标志。
var (
	flagConfig   string
	flagInstance string
	flagHome     string
	flagVerbose  bool
	flagJSON     bool
	flagNoColor  bool
)

var rootCmd = &cobra.Command{
	Use:   "nyatmc",
	Short: "NyaTMC - Minecraft 服务器全能管理工具",
	Long: `NyaTMC 是用 Go 编写的 Minecraft 服务器管理工具，单个二进制文件、零运行时依赖，
在 Termux（Android）、Linux 与 Windows 上开箱即用。

三种交互方式：
  nyatmc start / stop / backup   命令行（适合脚本与系统定时任务）
  nyatmc tui                      纯 ANSI 终端看板，单键操作
  nyatmc web serve                浏览器仪表盘（可配合 Cloudflare 穿透）

配置集中在 ~/.nyatmc/config.toml，修改后运行中的实例会自动热重载。`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return fmt.Errorf("未知命令 %q，运行 nyatmc --help 查看用法", args[0])
		}
		_ = cmd.Help()
		app, err := newApp(cmd)
		if err == nil {
			printOverview(app)
		}
		return nil
	},
}

// Execute 是 main.go 的入口。
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "\n出错了：%v\n", err)
		os.Exit(exitCodeFor(err))
	}
}

// ExitError 携带退出码的命令错误（例如 status 检测到未运行时返回 3）。
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("退出码 %d", e.Code)
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error { return e.Err }

// ExitWith 构造一个带退出码的错误。
func ExitWith(code int, format string, args ...any) error {
	return &ExitError{Code: code, Err: fmt.Errorf(format, args...)}
}

func exitCodeFor(err error) int {
	var ee *ExitError
	if errors.As(err, &ee) && ee.Code != 0 {
		return ee.Code
	}
	return 1
}

func init() {
	rootCmd.PersistentFlags().StringVar(&flagConfig, "config", "", "配置文件路径（默认 ~/.nyatmc/config.toml）")
	rootCmd.PersistentFlags().StringVarP(&flagInstance, "instance", "i", "", "目标实例名（默认取配置里的 general.default_instance）")
	rootCmd.PersistentFlags().StringVar(&flagHome, "home", "", "数据目录（默认 ~/.nyatmc，也可用 NYATMC_HOME）")
	rootCmd.PersistentFlags().BoolVarP(&flagVerbose, "verbose", "v", false, "输出调试日志")
	rootCmd.PersistentFlags().BoolVar(&flagJSON, "json", false, "以 JSON 输出（便于脚本处理）")
	rootCmd.PersistentFlags().BoolVar(&flagNoColor, "no-color", false, "关闭彩色输出")
}

// App 是一次命令执行共享的上下文。
type App struct {
	Ctx  context.Context
	Cfg  *config.Config
	Inst *instance.Instance
	Log  *logger.Logger
	JSON bool
	// Color 控制是否输出 ANSI 颜色。
	Color bool
}

// newApp 加载配置与日志（不做实例解析）。
func newApp(cmd *cobra.Command) (*App, error) {
	config.SetHome(flagHome)
	cfg, err := config.LoadOrCreate(flagConfig)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("配置校验失败：%w\n提示：运行 nyatmc config validate 查看详情", err)
	}

	log := logger.Default()
	if flagVerbose {
		log.SetLevel(logger.LevelDebug)
	}
	color := !flagNoColor && cfg.General.Color && isStdoutTerminal()
	log.SetColor(color)

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	return &App{Ctx: ctx, Cfg: cfg, Log: log, JSON: flagJSON, Color: color}, nil
}

// newAppInstance 加载配置并解析目标实例。
func newAppInstance(cmd *cobra.Command, args []string) (*App, error) {
	app, err := newApp(cmd)
	if err != nil {
		return nil, err
	}
	inst, err := app.resolveInstance(args)
	if err != nil {
		return nil, err
	}
	app.Inst = inst
	return app, nil
}

// resolveInstance 依次按位置参数、--instance、配置默认值确定实例。
func (a *App) resolveInstance(args []string) (*instance.Instance, error) {
	name := strings.TrimSpace(flagInstance)
	if name == "" && len(args) > 0 {
		name = strings.TrimSpace(args[0])
	}
	if name == "" {
		name = a.Cfg.General.DefaultInstance
	}
	if name == "" {
		names := a.Cfg.InstanceNames()
		if len(names) == 1 {
			name = names[0]
		} else {
			return nil, fmt.Errorf("未指定实例，且配置里没有 general.default_instance；用 --instance 或 nyatmc use <名字> 指定")
		}
	}
	inst, err := instance.New(a.Cfg, name)
	if err != nil {
		return nil, err
	}
	if !inst.Registered() {
		hint := ""
		if others := a.Cfg.InstanceNames(); len(others) > 0 {
			hint = fmt.Sprintf("，已有实例：%s", strings.Join(others, "、"))
		}
		return nil, fmt.Errorf("实例 %q 不存在%s；用 nyatmc create %s 新建", name, hint, name)
	}
	return inst, nil
}

// resolveInstances 支持 --all 批量操作。
func (a *App) resolveInstances(args []string, all bool) ([]*instance.Instance, error) {
	if all {
		list := instance.List(a.Cfg)
		if len(list) == 0 {
			return nil, fmt.Errorf("还没有任何实例，先运行 nyatmc init 或 nyatmc create <名字>")
		}
		return list, nil
	}
	inst, err := a.resolveInstance(args)
	if err != nil {
		return nil, err
	}
	return []*instance.Instance{inst}, nil
}

// ---------------------------------------------------------------- 输出

// Out 输出一行普通文本。
func (a *App) Out(format string, args ...any) {
	fmt.Fprintf(os.Stdout, format+"\n", args...)
}

// Errf 输出一行到标准错误。
func (a *App) Errf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// OK 输出成功提示。
func (a *App) OK(format string, args ...any) {
	a.Out("%s %s", a.paint("32", "✓"), fmt.Sprintf(format, args...))
}

// Warn 输出警告。
func (a *App) Warn(format string, args ...any) {
	a.Errf("%s %s", a.paint("33", "!"), fmt.Sprintf(format, args...))
}

// Fail 输出失败。
func (a *App) Fail(format string, args ...any) {
	a.Errf("%s %s", a.paint("31", "✗"), fmt.Sprintf(format, args...))
}

// JSONOut 以 JSON 输出。
func (a *App) JSONOut(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (a *App) paint(code, s string) string {
	if !a.Color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// StateLabel 返回带颜色的状态标签。
func (a *App) StateLabel(state daemon.State) string {
	label := state.Label()
	switch state {
	case daemon.StateRunning:
		return a.paint("32", label)
	case daemon.StateStarting, daemon.StateRestarting:
		return a.paint("36", label)
	case daemon.StateStopping:
		return a.paint("33", label)
	case daemon.StateCrashed:
		return a.paint("31", label)
	default:
		return a.paint("90", label)
	}
}

// Table 输出对齐的表格。
func (a *App) Table(headers []string, rows [][]string) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, strings.Join(headers, "\t"))
	sep := make([]string, len(headers))
	for i := range sep {
		sep[i] = strings.Repeat("-", maxInt(3, len(headers[i])))
	}
	fmt.Fprintln(w, strings.Join(sep, "\t"))
	for _, row := range rows {
		fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	_ = w.Flush()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// HumanDuration 输出中文友好的时长。
func HumanDuration(d time.Duration) string {
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

func isStdoutTerminal() bool {
	st, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

// printOverview 在没有子命令时打印一份简短概览。
func printOverview(app *App) {
	names := app.Cfg.InstanceNames()
	if len(names) == 0 {
		app.Out("\n还没有任何实例：运行 nyatmc init 初始化，或 nyatmc create <名字> 新建实例。")
		return
	}
	sort.Strings(names)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rows := make([][]string, 0, len(names))
	for _, name := range names {
		inst, err := instance.New(app.Cfg, name)
		if err != nil {
			continue
		}
		st, _ := daemon.GetStatus(ctx, app.Cfg, inst)
		players := "-"
		if st.PlayerCount() > 0 {
			players = fmt.Sprintf("%d", st.PlayerCount())
		}
		rows = append(rows, []string{name, app.StateLabel(st.State), fmt.Sprintf("%d", st.PID), HumanDuration(st.Uptime()), players})
	}
	app.Out("\n实例概览：")
	app.Table([]string{"实例", "状态", "PID", "运行时长", "在线"}, rows)
	app.Out("\n常用命令：nyatmc start | stop | status | tui | web serve | backup")
}

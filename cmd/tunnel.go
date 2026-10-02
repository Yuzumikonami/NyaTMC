package cmd

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yuzumikonami/nyatmc/internal/tunnel"
)

var (
	tunnelStartPort      int
	tunnelStartTarget    string
	tunnelStartProtocol  string
	tunnelStartToken     string
	tunnelStartHostname  string
	tunnelStartBin       string
	tunnelStartDir       string
	tunnelStartNoInstall bool
	tunnelStartWait      time.Duration
	tunnelStatusLines    int
	tunnelInstallDir     string
)

var tunnelCmd = &cobra.Command{
	Use:   "tunnel",
	Short: "Cloudflare 内网穿透（把本机端口暴露到公网）",
	Long: `用 cloudflared 把本机端口暴露到公网，无需公网 IP、无需改路由器。

默认使用“快速隧道”：不需要 Cloudflare 账号，启动后会得到一个随机的
https://xxx.trycloudflare.com 地址，适合临时分享给朋友或手机上看 Web 仪表盘。

在 [tunnel] 里填好 token 则使用“命名隧道”（需要自己的域名）：
  [tunnel]
  token = "eyJhIjoi..."
  hostname = "mc.example.com"

隧道进程与 nyatmc 本身分离：nyatmc tunnel start 退出后隧道继续运行，
状态记录在 ~/.nyatmc/state/tunnel.json，用 nyatmc tunnel stop 停止。`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

var tunnelStartCmd = &cobra.Command{
	Use:   "start",
	Short: "启动隧道并醒目地打印公网地址",
	Long: `启动隧道。

默认把 web.listen 指向的地址（例如 127.0.0.1:8080）作为目标；
也可以用 --port 指定本地端口，或用 --target 指定完整地址。`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		opts, err := tunnelOptionsFor(app, cmd)
		if err != nil {
			return err
		}

		t, err := opts.Start(app.Ctx)
		if err != nil {
			return err
		}
		url, waitErr := t.WaitURL(app.Ctx, tunnelStartWait)
		st := t.Status()
		if st.URL == "" {
			st.URL = url
		}

		if app.JSON {
			if waitErr != nil {
				app.Warn("%v", waitErr)
			}
			return app.JSONOut(st)
		}

		app.OK("隧道已启动：模式 %s，目标 %s，pid %d", tunnelModeLabel(st.Mode), st.Target, st.PID)
		if waitErr != nil {
			app.Warn("%v", waitErr)
		}
		if st.URL == "" {
			app.Out("用 nyatmc tunnel status 查看进展，隧道就绪后可用 nyatmc tunnel url 获取地址。")
			return nil
		}
		tunnelBanner(app, st)
		return nil
	},
}

var tunnelStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "停止隧道",
	Long:  "停止由 nyatmc 启动的 cloudflared 隧道，并清理状态记录。",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		err = tunnel.StopAll()
		switch {
		case err == nil:
			app.OK("隧道已停止")
		case errors.Is(err, tunnel.ErrNotRunning):
			app.Warn("当前没有正在运行的隧道")
		default:
			return err
		}
		return nil
	},
}

var tunnelStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "查看隧道运行状态与最近日志",
	Long: `查看隧道状态：是否在运行、公网地址、本地目标、pid、运行时长以及最近的 cloudflared 输出。

退出码：0 = 运行中，3 = 未运行（便于脚本判断）。`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		st, ok := tunnel.Current()
		if app.JSON {
			if !ok {
				return app.JSONOut(map[string]any{"running": false})
			}
			return app.JSONOut(st)
		}
		if !ok {
			return ExitWith(3, "隧道未在运行")
		}
		if !st.Running {
			if st.LastError != "" {
				app.Warn("隧道未在运行：%s", st.LastError)
			}
			return ExitWith(3, "隧道未在运行")
		}
		printTunnelStatus(app, st, tunnelStatusLines)
		return nil
	},
}

var tunnelURLCmd = &cobra.Command{
	Use:   "url",
	Short: "只输出公网地址（便于脚本或扫码）",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		st, ok := tunnel.Current()
		if !ok || !st.Running {
			return ExitWith(3, "隧道未在运行")
		}
		if strings.TrimSpace(st.URL) == "" {
			return ExitWith(3, "隧道还在建立中，暂时没有公网地址；用 nyatmc tunnel status 查看日志")
		}
		if app.JSON {
			return app.JSONOut(map[string]string{"url": st.URL})
		}
		app.Out("%s", st.URL)
		return nil
	},
}

var tunnelInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "下载 cloudflared 并报告版本与路径",
	Long: `下载当前平台对应的 cloudflared 到 ~/.nyatmc/bin（可用 --dir 改目录），
下载后会校验文件非空且可执行，并打印版本。`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		dir := strings.TrimSpace(tunnelInstallDir)
		if dir == "" {
			dir = tunnel.DefaultBinDir()
		}
		path, err := tunnel.EnsureBinary(app.Ctx, dir, app.Log)
		if err != nil {
			return err
		}
		ver, verr := tunnel.Version(app.Ctx, path)
		if app.JSON {
			return app.JSONOut(map[string]string{"path": path, "version": ver})
		}
		app.OK("cloudflared 已就绪")
		app.Out("  路径：%s", path)
		if verr != nil {
			app.Warn("无法读取 cloudflared 版本：%v", verr)
		} else {
			app.Out("  版本：%s", ver)
		}
		if cfgBin := strings.TrimSpace(app.Cfg.Tunnel.Bin); cfgBin == "" && !strings.EqualFold(filepath.Dir(path), tunnel.DefaultBinDir()) {
			app.Out("  提示：该路径不是默认目录，请设置 tunnel.bin = %q 或用 --bin 指定。", path)
		}
		return nil
	},
}

// tunnelOptionsFor 依据命令行与配置组装启动参数。
func tunnelOptionsFor(app *App, cmd *cobra.Command) (tunnel.Options, error) {
	cfgTunnel := app.Cfg.Tunnel
	opts := tunnel.Options{
		Bin:         strings.TrimSpace(tunnelStartBin),
		Protocol:    strings.TrimSpace(tunnelStartProtocol),
		Token:       strings.TrimSpace(tunnelStartToken),
		Hostname:    strings.TrimSpace(tunnelStartHostname),
		Dir:         strings.TrimSpace(tunnelStartDir),
		ExtraArgs:   append([]string(nil), cfgTunnel.ExtraArgs...),
		AutoInstall: cfgTunnel.AutoInstall,
		Logger:      app.Log,
	}
	if opts.Bin == "" {
		opts.Bin = strings.TrimSpace(cfgTunnel.Bin)
	}
	if opts.Protocol == "" {
		opts.Protocol = strings.TrimSpace(cfgTunnel.Protocol)
	}
	if opts.Token == "" {
		opts.Token = strings.TrimSpace(cfgTunnel.Token)
	}
	if opts.Hostname == "" {
		opts.Hostname = strings.TrimSpace(cfgTunnel.Hostname)
	}
	if tunnelStartNoInstall {
		opts.AutoInstall = false
	}

	switch {
	case strings.TrimSpace(tunnelStartTarget) != "":
		opts.Target = strings.TrimSpace(tunnelStartTarget)
	case cmd.Flags().Changed("port"):
		if tunnelStartPort <= 0 || tunnelStartPort > 65535 {
			return opts, fmt.Errorf("--port %d 非法，应在 1-65535 之间", tunnelStartPort)
		}
		opts.Port = tunnelStartPort
	default:
		target, err := webListenTarget(app.Cfg.Web.Listen)
		if err != nil {
			return opts, err
		}
		opts.Target = target
	}
	return opts, nil
}

// webListenTarget 把 web.listen（127.0.0.1:8080）转成隧道目标地址。
func webListenTarget(listen string) (string, error) {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return "", errors.New("配置里没有 web.listen，请用 --port 指定要暴露的本地端口")
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("web.listen = %q 无法解析（%v）；请用 --port 指定要暴露的本地端口", listen, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

// tunnelModeLabel 返回模式的中文名。
func tunnelModeLabel(mode string) string {
	if mode == tunnel.ModeNamed {
		return "命名隧道"
	}
	return "快速隧道"
}

// tunnelBanner 醒目地打印公网地址（供玩家连接 / 手机查看）。
func tunnelBanner(app *App, st tunnel.Status) {
	app.Out("")
	app.Out("  ┌──────────────────────────────────────────────────────────────")
	app.Out("  │  ☁  公网地址（手机 / 玩家可以直接访问）：")
	app.Out("  │")
	app.Out("  │      %s", app.paint("36", st.URL))
	app.Out("  │")
	app.Out("  └──────────────────────────────────────────────────────────────")
	app.Out("     本地目标：%s", st.Target)
	app.Out("     进程 PID：%d    模式：%s", st.PID, tunnelModeLabel(st.Mode))
	app.Out("")
	app.Out("  停止隧道：nyatmc tunnel stop     查看状态：nyatmc tunnel status")
}

// printTunnelStatus 打印详情与最近日志。
func printTunnelStatus(app *App, st tunnel.Status, lines int) {
	mark := func(label, value string) {
		if strings.TrimSpace(value) == "" {
			value = "-"
		}
		app.Out("  %-10s %s", label, value)
	}
	app.Out("隧道状态")
	mark("状态", app.paint("32", "运行中"))
	mark("公网地址", app.paint("36", st.URL))
	mark("本地目标", st.Target)
	mark("模式", tunnelModeLabel(st.Mode))
	mark("PID", fmt.Sprintf("%d", st.PID))
	mark("运行时长", HumanDuration(st.Uptime))
	mark("协议", st.Protocol)
	if st.Hostname != "" {
		mark("域名", st.Hostname)
	}
	mark("二进制", st.BinPath)
	mark("日志", st.LogPath)
	if st.LastError != "" {
		mark("最近问题", st.LastError)
	}
	if lines > 0 && len(st.LogLines) > 0 {
		if lines > len(st.LogLines) {
			lines = len(st.LogLines)
		}
		app.Out("\n最近日志：")
		for _, l := range st.LogLines[len(st.LogLines)-lines:] {
			app.Out("  %s", l)
		}
	}
}

func init() {
	tunnelStartCmd.Flags().IntVar(&tunnelStartPort, "port", 0, "要暴露的本地端口（默认取 web.listen）")
	tunnelStartCmd.Flags().StringVar(&tunnelStartTarget, "target", "", "完整目标地址，例如 http://127.0.0.1:8080")
	tunnelStartCmd.Flags().StringVar(&tunnelStartProtocol, "protocol", "", "传输协议：quic / http2 / auto（默认取配置）")
	tunnelStartCmd.Flags().StringVar(&tunnelStartToken, "token", "", "命名隧道 token（留空使用快速隧道）")
	tunnelStartCmd.Flags().StringVar(&tunnelStartHostname, "hostname", "", "命名隧道域名（仅用于展示）")
	tunnelStartCmd.Flags().StringVar(&tunnelStartBin, "bin", "", "cloudflared 可执行文件路径")
	tunnelStartCmd.Flags().StringVar(&tunnelStartDir, "dir", "", "存放自动下载的 cloudflared 的目录")
	tunnelStartCmd.Flags().BoolVar(&tunnelStartNoInstall, "no-install", false, "缺少 cloudflared 时不自动下载")
	tunnelStartCmd.Flags().DurationVar(&tunnelStartWait, "wait", 60*time.Second, "等待公网地址的时间上限")

	tunnelStatusCmd.Flags().IntVarP(&tunnelStatusLines, "lines", "n", 10, "附带最近 N 行 cloudflared 日志")

	tunnelInstallCmd.Flags().StringVar(&tunnelInstallDir, "dir", "", "下载目录（默认 ~/.nyatmc/bin）")

	tunnelCmd.AddCommand(tunnelStartCmd, tunnelStopCmd, tunnelStatusCmd, tunnelURLCmd, tunnelInstallCmd)
	rootCmd.AddCommand(tunnelCmd)
}

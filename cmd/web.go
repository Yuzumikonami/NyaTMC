package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/yuzumikonami/nyatmc/internal/config"
	"github.com/yuzumikonami/nyatmc/internal/tunnel"
	"github.com/yuzumikonami/nyatmc/internal/web"
	"github.com/yuzumikonami/nyatmc/pkg/socket"
)

// Web 仪表盘的命令行标志。
var (
	webListen    string
	webToken     string
	webOpen      bool
	webTunnel    bool
	webTokenSave bool
)

var webCmd = &cobra.Command{
	Use:   "web",
	Short: "浏览器仪表盘（REST API + 内嵌前端）",
	Long: `通过浏览器管理 nyatmc：状态总览、启停控制、实时日志、性能图表、
备份与回滚、配置编辑与调度任务，全部在一个页面里完成。

前端与接口都打包在单个二进制里，不依赖任何 CDN 或外部资源，
因此离线、内网、Termux 环境都能直接打开。

默认只监听 127.0.0.1；要对外提供服务请设置 web.token。`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return fmt.Errorf("未知子命令 %q，运行 nyatmc web --help 查看用法", args[0])
		}
		return cmd.Help()
	},
}

var webServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "启动 Web 仪表盘",
	Long: `启动 Web 仪表盘并阻塞运行，按 Ctrl+C 优雅退出。

访问方式：
  http://127.0.0.1:8080/                 本机浏览（未配置令牌时只允许本机）
  http://127.0.0.1:8080/?token=<令牌>    带令牌访问（令牌也会记在浏览器本地）

安全提醒：默认只监听回环地址。如果要用 --listen 暴露到局域网或公网，
请务必同时设置 web.token（或使用 --token 临时指定），否则任何能访问该端口的人
都可以启停你的服务器。`,
	Args: cobra.NoArgs,
	RunE: runWebServe,
}

var webTokenCmd = &cobra.Command{
	Use:   "token",
	Short: "生成一个随机访问令牌",
	Long: `生成一个随机访问令牌（32 字节十六进制）。

默认只打印到终端；加 --save 会写入配置文件的 [web] token 字段。
注意：--save 会用 TOML 序列化重写整个配置文件，配置文件里的注释会丢失。`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		token, err := socket.NewToken()
		if err != nil {
			return fmt.Errorf("生成令牌失败：%w", err)
		}
		if app.JSON {
			if err := app.JSONOut(map[string]any{
				"token": token,
				"saved": webTokenSave,
				"path":  app.Cfg.Path,
			}); err != nil {
				return err
			}
			return nil
		}
		if !webTokenSave {
			app.Out("%s", token)
			app.Out("")
			app.Out("用法：nyatmc web serve --token %s", token)
			app.Out("持久化：nyatmc config set web.token %s（或 nyatmc web token --save）", token)
			return nil
		}
		app.Cfg.Web.Token = token
		if err := app.Cfg.Save(); err != nil {
			return fmt.Errorf("写入配置失败：%w", err)
		}
		app.OK("已生成并写入访问令牌")
		app.Out("  配置文件  %s", app.Cfg.Path)
		app.Out("  令牌      %s", token)
		app.Out("")
		app.Out("提示：配置文件已被重新序列化，原有注释会丢失。")
		app.Out("启动：nyatmc web serve")
		return nil
	},
}

// runWebServe 是 nyatmc web serve 的实现。
func runWebServe(cmd *cobra.Command, _ []string) error {
	app, err := newApp(cmd)
	if err != nil {
		return err
	}

	// Ctrl+C / SIGTERM 时取消 ctx，HTTP 服务优雅退出。
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 用配置管理器托管配置：Web 里改配置、加调度任务都会立即生效。
	mgr, err := config.NewManager(app.Cfg.Path)
	if err != nil {
		return fmt.Errorf("加载配置失败：%w", err)
	}
	defer mgr.Close()
	mgr.SetErrorHandler(func(err error) {
		app.Warn("配置文件热重载失败：%v", err)
	})
	if err := mgr.Start(ctx); err != nil {
		app.Warn("配置文件热重载未启用（%v），Web 里保存的配置仍会立即生效", err)
	}

	srv, err := web.New(web.Options{
		Config:  app.Cfg,
		Manager: mgr,
		Logger:  app.Log,
		Token:   webToken,
		Listen:  webListen,
		Version: Version,
	})
	if err != nil {
		return fmt.Errorf("初始化 Web 仪表盘失败：%w", err)
	}

	listenAddr, err := srv.Listen()
	if err != nil {
		return err
	}
	url := srv.URL()
	app.OK("NyaTMC Web 仪表盘已启动")
	app.Out("  监听地址  %s", srv.ListenAddr())
	app.Out("  访问地址  %s", url)
	app.Out("  配置文件  %s", srv.ConfigPath())

	token := srv.Token()
	switch {
	case webToken != "":
		app.Out("  访问令牌  %s（由 --token 指定，仅本次运行有效）", token)
		app.Out("  带令牌的地址：%s/?token=%s", url, token)
	case token != "":
		app.Out("  访问令牌  已使用配置文件里的 web.token")
		app.Out("  带令牌的地址：%s/?token=%s", url, token)
	default:
		app.Warn("未配置 web.token：只允许来自本机（回环地址）的访问")
		app.Out("  生成令牌：nyatmc web token --save")
	}

	if !srv.WriteEnabled() {
		app.Warn("web.enable_write = false：所有写操作接口都会返回 403（页面进入只读模式）")
	}
	if !srv.Loopback() {
		app.Warn("监听地址 %s 不是回环地址，请务必设置 web.token，否则任何人都能控制你的服务器", srv.ListenAddr())
	}

	// --tunnel / tunnel.auto_start_with_web：把仪表盘经 Cloudflare 暴露到公网。
	var ownTunnel *tunnel.Tunnel
	if webTunnel || app.Cfg.Tunnel.AutoStartWithWeb {
		target, terr := webListenTarget(listenAddr)
		if terr != nil {
			app.Warn("无法确定隧道目标地址：%v", terr)
		} else if st, running := tunnel.Current(); running && st.Running && st.Target == target {
			app.Out("")
			app.OK("已有隧道指向 %s，直接复用（nyatmc tunnel stop 可停止）", target)
			if st.URL != "" {
				app.Out("  公网地址  %s", app.paint("36", st.URL))
			}
		} else {
			t, terr := tunnelOptionsFromConfig(app, target).Start(ctx)
			if terr != nil {
				app.Warn("启动 Cloudflare 隧道失败：%v", terr)
				app.Out("  替代方案：单独运行 cloudflared tunnel --url %s", target)
			} else {
				ownTunnel = t
				app.Out("")
				app.OK("Cloudflare 隧道已启动（pid %d，目标 %s）", t.Status().PID, target)
				go func() {
					public, werr := t.WaitURL(ctx, 90*time.Second)
					if werr != nil {
						if ctx.Err() == nil {
							app.Warn("等待隧道公网地址：%v", werr)
						}
						return
					}
					app.Out("")
					app.Out("  ☁  公网地址：%s", app.paint("36", public))
					app.Out("      手机 / 远程浏览器可直接访问；退出 web serve 时隧道一并停止。")
				}()
			}
		}
	}
	if ownTunnel != nil {
		defer func() {
			if serr := ownTunnel.Stop(); serr != nil && !errors.Is(serr, tunnel.ErrNotRunning) {
				app.Warn("停止隧道失败：%v", serr)
			}
		}()
	}

	if webOpen || app.Cfg.Web.Open {
		go func() {
			if err := webOpenBrowser(url); err != nil {
				app.Warn("自动打开浏览器失败：%v（请手动访问 %s）", err, url)
			}
		}()
	}

	app.Out("")
	app.Out("按 Ctrl+C 退出。")
	if err := srv.Serve(ctx); err != nil {
		return fmt.Errorf("Web 服务异常退出：%w", err)
	}
	app.Out("Web 仪表盘已关闭。")
	return nil
}

// webOpenBrowser 尝试用系统默认浏览器打开地址。
func webOpenBrowser(url string) error {
	// Termux（Android）优先用它自带的方式打开。
	if strings.TrimSpace(os.Getenv("TERMUX_VERSION")) != "" || strings.Contains(strings.ToLower(os.Getenv("PREFIX")), "com.termux") {
		if path, err := exec.LookPath("termux-open-url"); err == nil {
			return exec.Command(path, url).Start()
		}
	}
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		for _, bin := range []string{"xdg-open", "sensible-browser", "x-www-browser"} {
			if path, err := exec.LookPath(bin); err == nil {
				return exec.Command(path, url).Start()
			}
		}
		return fmt.Errorf("系统里找不到可用的浏览器打开命令（xdg-open 等）")
	}
}

// tunnelOptionsFromConfig 只依据配置文件组装隧道启动参数（web serve 用，
// 不读取 tunnel start 的命令行标志）。
func tunnelOptionsFromConfig(app *App, target string) tunnel.Options {
	ct := app.Cfg.Tunnel
	return tunnel.Options{
		Bin:         strings.TrimSpace(ct.Bin),
		Target:      target,
		Protocol:    strings.TrimSpace(ct.Protocol),
		Token:       strings.TrimSpace(ct.Token),
		Hostname:    strings.TrimSpace(ct.Hostname),
		ExtraArgs:   append([]string(nil), ct.ExtraArgs...),
		AutoInstall: ct.AutoInstall,
		Logger:      app.Log,
	}
}

func init() {
	webServeCmd.Flags().StringVar(&webListen, "listen", "", "监听地址，覆盖 web.listen（例如 127.0.0.1:8080）")
	webServeCmd.Flags().StringVar(&webToken, "token", "", "本次运行的访问令牌，覆盖 web.token（不写入配置）")
	webServeCmd.Flags().BoolVar(&webOpen, "open", false, "启动后自动打开浏览器")
	webServeCmd.Flags().BoolVar(&webTunnel, "tunnel", false, "同时启动 Cloudflare 穿透，把仪表盘暴露到公网（退出时一并停止）")

	webTokenCmd.Flags().BoolVar(&webTokenSave, "save", false, "把生成的令牌写入配置文件（会重写文件，注释丢失）")

	webCmd.AddCommand(webServeCmd, webTokenCmd)
	rootCmd.AddCommand(webCmd)
}

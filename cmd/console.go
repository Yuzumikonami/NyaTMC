package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yuzumikonami/nyatmc/internal/daemon"
)

var consoleLines int

var consoleCmd = &cobra.Command{
	Use:   "console [实例名]",
	Short: "连接服务端控制台（实时日志 + 直接输入指令）",
	Long: `连接到运行中实例的服务端控制台。

会先回显最近的日志，然后实时推送新日志；你在这里输入的每一行都会原样
发送给服务端（等同于在服务器终端里敲命令）。输入 /exit 退出（不会关闭服务器），
Ctrl+C 同样只是退出连接。

要优雅关闭服务器请用 nyatmc stop，不要在这里输入 stop（那样守护进程不会退出）。`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newAppInstance(cmd, args)
		if err != nil {
			return err
		}
		inst := app.Inst
		if !daemon.Running(inst) {
			return fmt.Errorf("实例 %s 未在运行；先执行 nyatmc start %s", inst.Name, inst.Name)
		}

		if consoleLines > 0 {
			if lines, err := daemon.Logs(app.Ctx, inst, consoleLines); err == nil {
				for _, l := range lines {
					app.Out("%s", l)
				}
			}
		}
		app.Out("已连接到 %s 的控制台（输入 /exit 退出）", inst.Name)

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()

		done := make(chan struct{})
		go func() {
			defer close(done)
			err := daemon.Subscribe(ctx, inst, func(ev daemon.LogEvent) error {
				if ev.Kind == "log" {
					app.Out("%s", ev.Text)
				}
				return nil
			})
			if err != nil && ctx.Err() == nil {
				app.Warn("日志订阅中断：%v", err)
			}
		}()

		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			switch line {
			case "":
				continue
			case "/exit", "/quit", "/q":
				return nil
			}
			if err := daemon.SendCommand(ctx, inst, line); err != nil {
				app.Fail("%v", err)
			}
		}
		<-done
		return nil
	},
}

var commandCmd = &cobra.Command{
	Use:     "command <指令…>",
	Aliases: []string{"cmd", "send"},
	Short:   "向服务端控制台发送一条指令（适合脚本调用）",
	Long: `向运行中的服务端发送一条控制台指令，例如：

  nyatmc command "say 服务器 5 分钟后重启"
  nyatmc command tps -i survival
  nyatmc command "whitelist add Steve"

注意：直接发送 stop 只会让服务端退出，守护进程仍会驻留；要完整停止请用 nyatmc stop。`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newAppInstance(cmd, nil)
		if err != nil {
			return err
		}
		line := strings.Join(args, " ")
		if err := daemon.SendCommand(app.Ctx, app.Inst, line); err != nil {
			return err
		}
		if app.JSON {
			return app.JSONOut(map[string]any{"instance": app.Inst.Name, "sent": line})
		}
		app.OK("已发送：%s", line)
		return nil
	},
}

var sayCmd = &cobra.Command{
	Use:   "say <消息…>",
	Short: "在服务器里广播一条消息",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newAppInstance(cmd, nil)
		if err != nil {
			return err
		}
		line := "say " + strings.Join(args, " ")
		if err := daemon.SendCommand(app.Ctx, app.Inst, line); err != nil {
			return err
		}
		app.OK("已广播：%s", strings.Join(args, " "))
		return nil
	},
}

func init() {
	consoleCmd.Flags().IntVarP(&consoleLines, "lines", "n", 50, "连接时先回显的最近日志行数（0 表示不回显）")
	rootCmd.AddCommand(consoleCmd, commandCmd, sayCmd)
}

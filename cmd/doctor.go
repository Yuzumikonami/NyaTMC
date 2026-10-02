package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yuzumikonami/nyatmc/internal/backup"
	"github.com/yuzumikonami/nyatmc/internal/config"
	"github.com/yuzumikonami/nyatmc/internal/daemon"
	"github.com/yuzumikonami/nyatmc/internal/instance"
	"github.com/yuzumikonami/nyatmc/pkg/logger"
)

// checkResult 是一条体检结果。
type checkResult struct {
	Name    string `json:"name"`
	Level   string `json:"level"` // ok / warn / fail / info
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "环境体检：Java、端口、EULA、目录权限、磁盘占用与依赖工具",
	Long: `对当前环境做一次体检，覆盖：

  - 数据目录是否可写、配置文件是否合法
  - Java 是否存在、版本是否满足要求（1.17+ 需要 Java 17，1.20.5+ 需要 Java 21）
  - 每个实例：服务端文件是否就位、EULA 是否确认、端口是否被占用
  - 体积占用（世界 / 日志 / 备份），帮你发现存储爆炸风险
  - 可选工具：cloudflared（内网穿透）
  - Termux 相关提示（后台被杀、唤醒锁）

有「失败」项时退出码为 1，便于脚本判断。`,
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		var results []checkResult
		add := func(name, level, msg, hint string) {
			results = append(results, checkResult{Name: name, Level: level, Message: msg, Hint: hint})
		}

		// 1. 数据目录
		home := config.Home()
		if err := os.MkdirAll(home, 0o755); err != nil {
			add("数据目录", "fail", fmt.Sprintf("%s 无法创建：%v", home, err), "检查目录权限或换一个目录（--home）")
		} else if err := probeWritable(home); err != nil {
			add("数据目录", "fail", fmt.Sprintf("%s 不可写：%v", home, err), "检查目录权限")
		} else {
			add("数据目录", "ok", fmt.Sprintf("%s 可写", home), "")
		}

		// 2. 配置文件
		if err := app.Cfg.Validate(); err != nil {
			add("配置文件", "fail", fmt.Sprintf("%s 校验失败：%v", app.Cfg.Path, err), "nyatmc config edit")
		} else {
			add("配置文件", "ok", fmt.Sprintf("%s 合法", app.Cfg.Path), "")
		}
		for _, w := range app.Cfg.Warnings() {
			add("配置提醒", "warn", w, "")
		}

		// 3. Java
		javaPath := app.Cfg.Resolve("").Server.JavaPath
		javaVersion, javaErr := javaVersionOf(javaPath)
		switch {
		case javaErr != nil:
			add("Java", "fail", fmt.Sprintf("执行 %s 失败：%v", javaPath, javaErr), "安装 JRE/JDK（Termux: pkg install openjdk-21），或用 nyatmc config set server.java_path /path/to/java")
		default:
			level := "ok"
			hint := ""
			if javaVersion > 0 && javaVersion < 17 {
				level = "fail"
				hint = "1.17 及以上需要 Java 17，1.20.5 及以上需要 Java 21"
			}
			add("Java", level, fmt.Sprintf("%s（Java %d）", javaPath, javaVersion), hint)
		}

		// 4. 可选工具
		if p, err := exec.LookPath("cloudflared"); err == nil {
			add("cloudflared", "ok", fmt.Sprintf("已安装：%s", p), "")
		} else {
			add("cloudflared", "info", "未安装（仅在内网穿透时需要）", "nyatmc tunnel install 可自动下载")
		}

		// 5. 实例逐个体检
		ctx := app.Ctx
		list := instance.List(app.Cfg)
		if len(list) == 0 {
			add("实例", "warn", "还没有任何实例", "nyatmc create <名字>")
		}
		for _, inst := range list {
			res := inst.Res
			if !inst.Exists() {
				add(fmt.Sprintf("实例 %s", inst.Name), "fail", fmt.Sprintf("服务器目录不存在：%s", res.Path), "nyatmc start 会自动创建，或用 nyatmc create 重新创建")
				continue
			}
			if inst.JarExists() {
				st, _ := os.Stat(res.JarPath)
				add(fmt.Sprintf("实例 %s 服务端", inst.Name), "ok", fmt.Sprintf("%s（%s）", filepath.Base(res.JarPath), backup.HumanSize(st.Size())), "")
			} else {
				add(fmt.Sprintf("实例 %s 服务端", inst.Name), "warn", "服务端文件尚未下载", "nyatmc server download 或直接 nyatmc start（auto_download = true 时会自动获取）")
			}
			if res.Server.EULAEnabled() {
				add(fmt.Sprintf("实例 %s EULA", inst.Name), "ok", "已确认", "")
			} else {
				add(fmt.Sprintf("实例 %s EULA", inst.Name), "fail", "尚未同意 Minecraft EULA，服务端会拒绝启动", "nyatmc config set server.eula true")
			}

			running := daemon.Running(inst)
			if running {
				st, _ := daemon.GetStatus(ctx, app.Cfg, inst)
				add(fmt.Sprintf("实例 %s 进程", inst.Name), "ok", fmt.Sprintf("运行中（pid %d，%s）", st.PID, st.State.Label()), "")
			} else {
				if err := probePortFree(res.Server.Port); err != nil {
					add(fmt.Sprintf("实例 %s 端口", inst.Name), "fail", fmt.Sprintf("端口 %d 无法绑定：%v", res.Server.Port, err), "改端口：nyatmc config set server.port <新端口>")
				} else {
					add(fmt.Sprintf("实例 %s 端口", inst.Name), "ok", fmt.Sprintf("%d 可用", res.Server.Port), "")
				}
			}

			if inst.JarExists() {
				for _, sub := range []string{res.Server.WorldName, "logs", "mods", "plugins"} {
					dir := filepath.Join(res.Path, sub)
					if size, err := logger.DirSize(dir); err == nil && size > 0 {
						add(fmt.Sprintf("实例 %s 占用 %s", inst.Name, sub), "info", backup.HumanSize(size), "")
					}
				}
			}
			backups, _ := backup.List(inst)
			if len(backups) > 0 {
				var total int64
				for _, a := range backups {
					total += a.Size
				}
				add(fmt.Sprintf("实例 %s 备份", inst.Name), "info", fmt.Sprintf("%d 份，共 %s", len(backups), backup.HumanSize(total)), "")
			} else {
				add(fmt.Sprintf("实例 %s 备份", inst.Name), "warn", "还没有任何备份", "nyatmc backup")
			}
		}

		// 6. 平台提示
		if isTermux() {
			add("Termux 提示", "info", "建议执行 termux-wake-lock，避免后台被系统杀掉", "pkg install termux-api 后可配合 termux-notification 做提醒")
		}
		if runtime.GOOS == "windows" {
			add("Windows 提示", "info", "后台运行请用 nyatmc start（会脱离终端）；查看状态用 nyatmc status", "")
		}

		// 输出
		if app.JSON {
			fails, warns := countLevels(results)
			if err := app.JSONOut(map[string]any{
				"results": results,
				"summary": map[string]any{"fail": fails, "warn": warns, "total": len(results)},
			}); err != nil {
				return err
			}
			if fails > 0 {
				return ExitWith(1, "体检发现 %d 项失败", fails)
			}
			return nil
		}

		label := map[string]string{"ok": "✓", "warn": "!", "fail": "✗", "info": "·"}
		color := map[string]string{"ok": "32", "warn": "33", "fail": "31", "info": "90"}
		for _, r := range results {
			if r.Level == "info" {
				app.Out("  %s %-24s %s", app.paint(color[r.Level], label[r.Level]), r.Name, r.Message)
				continue
			}
			app.Out("  %s %-24s %s", app.paint(color[r.Level], label[r.Level]), r.Name, r.Message)
			if r.Hint != "" {
				app.Out("      %s", r.Hint)
			}
		}
		fails, warns := countLevels(results)
		app.Out("")
		app.Out("体检完成：%d 项，%d 项警告，%d 项失败", len(results), warns, fails)
		if fails > 0 {
			return ExitWith(1, "体检未通过")
		}
		return nil
	},
}

func countLevels(results []checkResult) (fails, warns int) {
	for _, r := range results {
		switch r.Level {
		case "fail":
			fails++
		case "warn":
			warns++
		}
	}
	return fails, warns
}

func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".nyatmc-write-test-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

var javaVersionRe = regexp.MustCompile(`version "(\d+)(?:\.(\d+))?`)

// javaVersionOf 执行 java -version 并解析主版本号。
func javaVersionOf(javaPath string) (int, error) {
	if strings.TrimSpace(javaPath) == "" {
		javaPath = "java"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*1e9)
	defer cancel()
	out, err := exec.CommandContext(ctx, javaPath, "-version").CombinedOutput()
	if err != nil && len(out) == 0 {
		return 0, err
	}
	text := string(out)
	m := javaVersionRe.FindStringSubmatch(text)
	if m == nil {
		return 0, fmt.Errorf("无法解析 java 版本输出：%s", strings.TrimSpace(firstLine(text)))
	}
	major, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, err
	}
	// Java 8 的版本号是 1.8.0_xxx
	if major == 1 && m[2] != "" {
		if minor, err := strconv.Atoi(m[2]); err == nil {
			major = minor
		}
	}
	return major, nil
}

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	return s
}

func isTermux() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	prefix := os.Getenv("PREFIX")
	if strings.Contains(prefix, "com.termux") {
		return true
	}
	if _, err := os.Stat("/data/data/com.termux/files/usr"); err == nil {
		return true
	}
	return false
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}

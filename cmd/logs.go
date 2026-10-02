package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yuzumikonami/nyatmc/internal/backup"
	"github.com/yuzumikonami/nyatmc/internal/daemon"
	"github.com/yuzumikonami/nyatmc/internal/instance"
	"github.com/yuzumikonami/nyatmc/pkg/logger"
)

var (
	logsLines    int
	logsFollow   bool
	logsFile     string
	logsGrep     string
	logsRotate   bool
	logsPrune    bool
	logsTruncate bool
	logsAll      bool
)

var logsCmd = &cobra.Command{
	Use:   "logs [实例名]",
	Short: "查看服务端日志（支持跟随、过滤、手动切割与清理）",
	Long: `查看日志。

默认优先显示 nyatmc 捕获的控制台日志（logs 目录里服务端自己的 latest.log 也会一并提供）：

  nyatmc logs -n 100                 看最近 100 行
  nyatmc logs -f                     实时跟随（等价于 tail -f）
  nyatmc logs --file latest          看服务端自己的 logs/latest.log
  nyatmc logs --grep ERROR -n 50     只看包含 ERROR 的行
  nyatmc logs --rotate               手动归档一份并（在服务器停止时）清空
  nyatmc logs --prune                按 log.keep_files / log.max_age_days 清理历史日志`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app, err := newApp(cmd)
		if err != nil {
			return err
		}
		instances, err := app.resolveInstances(args, logsAll)
		if err != nil {
			return err
		}

		for _, inst := range instances {
			if logsRotate {
				if err := rotateLogs(app, inst); err != nil {
					return err
				}
			}
			if logsPrune {
				if err := pruneLogs(app, inst); err != nil {
					return err
				}
			}
			if logsRotate || logsPrune {
				if !logsFollow {
					continue
				}
			}
			path := pickLogFile(inst, logsFile)
			if path == "" {
				app.Warn("实例 %s 还没有日志文件", inst.Name)
				continue
			}
			if len(instances) > 1 && !app.JSON {
				app.Out("=== 实例 %s（%s） ===", inst.Name, path)
			}
			if logsFollow {
				if err := followLogs(app, inst, path); err != nil {
					return err
				}
				continue
			}
			lines, err := readTailFile(path, logsLines)
			if err != nil {
				return fmt.Errorf("读取日志 %s 失败：%w", path, err)
			}
			shown := 0
			for _, l := range lines {
				if !matchGrep(l, logsGrep) {
					continue
				}
				app.Out("%s", l)
				shown++
			}
			if shown == 0 && !app.JSON {
				if logsGrep != "" {
					app.Out("（最近 %d 行里没有匹配 %q 的内容）", len(lines), logsGrep)
				} else {
					app.Out("（日志为空）")
				}
			}
		}
		return nil
	},
}

func matchGrep(line, kw string) bool {
	if strings.TrimSpace(kw) == "" {
		return true
	}
	return strings.Contains(strings.ToLower(line), strings.ToLower(kw))
}

// pickLogFile 决定要看的日志文件。
func pickLogFile(inst *instance.Instance, which string) string {
	switch strings.ToLower(strings.TrimSpace(which)) {
	case "console", "nyatmc":
		return inst.ConsoleLogPath()
	case "latest", "server", "mc":
		return inst.MinecraftLogPath()
	case "":
		// 默认：优先服务端 latest.log，其次 nyatmc 捕获的控制台日志
		if st, err := os.Stat(inst.MinecraftLogPath()); err == nil && st.Size() > 0 {
			return inst.MinecraftLogPath()
		}
		if st, err := os.Stat(inst.ConsoleLogPath()); err == nil && st.Size() > 0 {
			return inst.ConsoleLogPath()
		}
		if _, err := os.Stat(inst.MinecraftLogPath()); err == nil {
			return inst.MinecraftLogPath()
		}
		return inst.ConsoleLogPath()
	default:
		return which
	}
}

// followLogs 实时跟随日志：实例在运行时走控制通道，否则轮询文件增量。
func followLogs(app *App, inst *instance.Instance, path string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	app.Out("正在跟随 %s（Ctrl+C 退出）…", path)
	if daemon.Running(inst) && path != inst.ConsoleLogPath() {
		// 服务端自己的 latest.log 无法通过控制通道获得，统一走文件轮询
		app.Log.Debugf("实例在运行，latest.log 走文件轮询")
	}
	if daemon.Running(inst) && path == inst.ConsoleLogPath() {
		// 先补齐已落盘的内容，再订阅增量，避免中间丢行
		if lines, err := readTailFile(path, 20); err == nil {
			for _, l := range lines {
				if matchGrep(l, logsGrep) {
					app.Out("%s", l)
				}
			}
		}
		err := daemon.Subscribe(ctx, inst, func(ev daemon.LogEvent) error {
			if ev.Kind != "log" {
				return nil
			}
			if matchGrep(ev.Text, logsGrep) {
				app.Out("%s", ev.Text)
			}
			return nil
		})
		if err == nil || ctx.Err() != nil {
			return nil
		}
		app.Warn("控制通道不可用，改用文件轮询：%v", err)
	}
	return followFile(ctx, path, logsGrep, func(line string) { app.Out("%s", line) })
}

// followFile 轮询文件增量（支持被切割/清空后从头继续）。
func followFile(ctx context.Context, path, grep string, emit func(string)) error {
	var offset int64
	if st, err := os.Stat(path); err == nil {
		offset = st.Size()
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		st, err := f.Stat()
		if err != nil {
			_ = f.Close()
			continue
		}
		if st.Size() < offset {
			offset = 0 // 文件被切割或清空
		}
		if st.Size() == offset {
			_ = f.Close()
			continue
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			_ = f.Close()
			continue
		}
		data, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			continue
		}
		offset = st.Size()
		text := strings.TrimRight(string(data), "\n")
		for _, line := range strings.Split(text, "\n") {
			if line == "" {
				continue
			}
			if matchGrep(line, grep) {
				emit(line)
			}
		}
	}
}

// rotateLogs 手动归档服务端日志。
func rotateLogs(app *App, inst *instance.Instance) error {
	res := inst.Res
	limit := res.Log.RotateSize.Bytes()
	if limit <= 0 {
		limit = 20 * 1024 * 1024
	}
	latest := inst.MinecraftLogPath()
	st, err := os.Stat(latest)
	if err != nil {
		// 服务端还没产生 latest.log，尝试归档 nyatmc 自己的控制台日志
		console := inst.ConsoleLogPath()
		if _, cerr := os.Stat(console); cerr != nil {
			app.Warn("实例 %s 还没有可归档的日志", inst.Name)
			return nil
		}
		latest = console
		st, err = os.Stat(console)
		if err != nil {
			return nil
		}
	}
	if st.Size() == 0 {
		app.Warn("实例 %s 的日志是空的，无需切割", inst.Name)
		return nil
	}

	archiveDir := filepath.Join(res.LogsDir, "nyatmc-archive")
	dst, err := logger.ArchiveFile(latest, archiveDir, res.Log.CompressEnabled())
	if err != nil {
		return err
	}
	app.OK("已归档 %s（%s）→ %s", filepath.Base(latest), backup.HumanSize(st.Size()), dst)

	if logsTruncate || !daemon.Running(inst) {
		if latest == inst.MinecraftLogPath() {
			if err := os.Truncate(latest, 0); err != nil {
				app.Warn("清空 %s 失败：%v", latest, err)
			} else {
				app.Out("已清空 %s（服务端未运行，可以安全截断）", filepath.Base(latest))
			}
		}
	} else {
		app.Warn("实例正在运行，JVM 仍持有该文件句柄，无法在线截断；已保留归档，切割会在下次重启时完成")
		app.Out("  想要自动切割：保持 log.rotate_restart = true（nyatmc 会在无玩家在线时优雅重启完成切割）")
	}
	return nil
}

// pruneLogs 清理日志归档与历史日志。
func pruneLogs(app *App, inst *instance.Instance) error {
	res := inst.Res
	archiveDir := filepath.Join(res.LogsDir, "nyatmc-archive")
	maxAge := time.Duration(res.Log.MaxAgeDays) * 24 * time.Hour

	removed, err := logger.PruneDir(archiveDir, res.Log.KeepFiles, maxAge)
	if err != nil {
		return err
	}
	total := len(removed)

	// 服务端自己的历史日志（latest.log 除外）
	entries, err := os.ReadDir(res.LogsDir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() || e.Name() == "latest.log" {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, ".log") && !strings.HasSuffix(name, ".log.gz") {
				continue
			}
			info, ierr := e.Info()
			if ierr != nil {
				continue
			}
			byAge := maxAge > 0 && time.Since(info.ModTime()) > maxAge
			if !byAge {
				continue
			}
			if rerr := os.Remove(filepath.Join(res.LogsDir, name)); rerr == nil {
				total++
			}
		}
	}

	if app.JSON {
		return app.JSONOut(map[string]any{"instance": inst.Name, "removed": total})
	}
	app.OK("实例 %s：清理了 %d 个历史日志文件", inst.Name, total)
	return nil
}

func init() {
	logsCmd.Flags().IntVarP(&logsLines, "lines", "n", 200, "显示最后 N 行")
	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "实时跟随输出")
	logsCmd.Flags().StringVar(&logsFile, "file", "", "指定日志文件：latest（服务端）或 console（nyatmc 捕获）")
	logsCmd.Flags().StringVar(&logsGrep, "grep", "", "只显示包含关键词的行")
	logsCmd.Flags().BoolVar(&logsRotate, "rotate", false, "手动归档当前日志")
	logsCmd.Flags().BoolVar(&logsTruncate, "truncate", false, "配合 --rotate：归档后清空（仅服务端已停止时有效）")
	logsCmd.Flags().BoolVar(&logsPrune, "prune", false, "按配置清理历史日志")
	logsCmd.Flags().BoolVar(&logsAll, "all", false, "对所有实例执行")
	rootCmd.AddCommand(logsCmd)
}

// logFileExists 判断日志文件是否存在且有内容。
func logFileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Size() > 0
}

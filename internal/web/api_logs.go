package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/yuzumikonami/nyatmc/internal/daemon"
	"github.com/yuzumikonami/nyatmc/internal/logwatch"
)

// handleLogs 返回最近 N 行控制台日志。
//
// 响应里 data 是文本行数组，另附 count 便于前端显示。
func (s *Server) handleLogs(c *gin.Context) {
	inst, fine := s.resolveInstance(c)
	if !fine {
		return
	}
	max := s.maxLogLines()
	lines := queryInt(c, "lines", 200)
	if lines <= 0 {
		lines = 200
	}
	lines = clampInt(lines, 1, max)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	out, err := daemon.Logs(ctx, inst, lines)
	if err != nil {
		fail(c, http.StatusInternalServerError, "读取日志失败：%v", err)
		return
	}
	source := "console"
	if len(out) == 0 {
		// 没有 nyatmc 捕获的控制台日志时，退回到服务端自己的 latest.log。
		if tail := tailFileLines(inst.MinecraftLogPath(), lines); len(tail) > 0 {
			out = tail
			source = "minecraft"
		}
	}
	if out == nil {
		out = []string{}
	}
	okWith(c, out, gin.H{
		"count":    len(out),
		"source":   source,
		"instance": inst.Name,
		"running":  daemon.Running(inst),
	})
}

// tailFileLines 读取文件末尾的 n 行（只读最后 512KB，照顾 Termux 的内存）。
func tailFileLines(path string, n int) []string {
	if n <= 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	const maxBytes = 512 * 1024
	start := int64(0)
	if st.Size() > maxBytes {
		start = st.Size() - maxBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	text := string(data)
	if start > 0 {
		if idx := strings.IndexByte(text, '\n'); idx >= 0 {
			text = text[idx+1:]
		}
	}
	return logwatch.TailLines(text, n)
}

// ---------------------------------------------------------------- SSE 实时流

// handleLogStream 通过 SSE 推送实时日志与状态事件。
//
// 事件类型：hello（连接建立）、log（一行日志）、status（状态快照）、
// notice（提示，例如实例未运行）；另外每 15 秒发送一条注释行心跳防止代理超时。
func (s *Server) handleLogStream(c *gin.Context) {
	inst, fine := s.resolveInstance(c)
	if !fine {
		return
	}

	lines := queryInt(c, "lines", 200)
	if lines <= 0 {
		lines = 200
	}
	lines = clampInt(lines, 1, s.maxLogLines())

	h := c.Writer.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)
	c.Writer.Flush()

	var werr error
	write := func(event string, payload any) bool {
		if werr != nil {
			return false
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		var b strings.Builder
		if event != "" {
			b.WriteString("event: ")
			b.WriteString(event)
			b.WriteString("\n")
		}
		b.WriteString("data: ")
		b.Write(data)
		b.WriteString("\n\n")
		if _, err := c.Writer.WriteString(b.String()); err != nil {
			werr = err
			return false
		}
		c.Writer.Flush()
		return true
	}
	// 立刻推一次注释行，让浏览器知道连接已建立。
	_, _ = c.Writer.WriteString(": connected\n\n")
	c.Writer.Flush()

	cfg := s.current()
	ctx := c.Request.Context()

	write("hello", gin.H{
		"instance":        inst.Name,
		"version":         s.versionString(),
		"time":            time.Now().Format(time.RFC3339),
		"refresh_seconds": s.refreshSeconds(),
		"max_log_lines":   s.maxLogLines(),
	})

	// 先补上历史日志。
	histCtx, histCancel := context.WithTimeout(ctx, 20*time.Second)
	history, _ := daemon.Logs(histCtx, inst, lines)
	if len(history) == 0 {
		history = tailFileLines(inst.MinecraftLogPath(), lines)
	}
	histCancel()
	for _, line := range history {
		if !write("log", gin.H{"text": line, "time": time.Now().Format(time.RFC3339), "history": true}) {
			return
		}
	}

	// 订阅 supervisor 的事件流（阻塞式，放到 goroutine 里再通过 channel 送回）。
	events := make(chan daemon.LogEvent, 512)
	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	go func() {
		defer close(events)
		err := daemon.Subscribe(subCtx, inst, func(ev daemon.LogEvent) error {
			select {
			case events <- ev:
				return nil
			case <-subCtx.Done():
				return subCtx.Err()
			}
		})
		if err != nil && subCtx.Err() == nil {
			select {
			case events <- daemon.LogEvent{Kind: "notice", Text: subscribeNotice(err)}:
			default:
			}
		}
	}()

	refresh := time.Duration(s.refreshSeconds()) * time.Second
	statusTicker := time.NewTicker(refresh)
	defer statusTicker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if werr != nil {
				return
			}
			if _, err := c.Writer.WriteString(": ping\n\n"); err != nil {
				return
			}
			c.Writer.Flush()
		case <-statusTicker.C:
			stCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			st, err := daemon.GetStatus(stCtx, cfg, inst)
			cancel()
			if err != nil {
				st = daemon.Status{Instance: inst.Name, State: daemon.StateUnknown, LastError: err.Error()}
			}
			if !write("status", newStatusView(inst, st)) {
				return
			}
		case ev, open := <-events:
			if !open {
				write("notice", gin.H{"text": "实时通道已断开（实例可能未运行），浏览器会自动重连"})
				return
			}
			switch ev.Kind {
			case "status":
				if ev.Status != nil {
					if !write("status", newStatusView(inst, *ev.Status)) {
						return
					}
				}
			case "exit":
				if !write("exit", gin.H{"text": ev.Text, "time": ev.Time.Format(time.RFC3339)}) {
					return
				}
			case "notice":
				if !write("notice", gin.H{"text": ev.Text}) {
					return
				}
			default:
				if !write("log", gin.H{"text": ev.Text, "time": ev.Time.Format(time.RFC3339)}) {
					return
				}
			}
		}
	}
}

func subscribeNotice(err error) string {
	if err == nil {
		return "实时通道已结束"
	}
	if errors.Is(err, daemon.ErrNotRunning) {
		return "实例当前未运行，实时日志不可用；启动后浏览器会自动重连"
	}
	msg := err.Error()
	if strings.Contains(msg, "未在运行") || strings.Contains(msg, "找不到控制端点") {
		return "实例当前未运行，实时日志不可用；启动后浏览器会自动重连"
	}
	return fmt.Sprintf("实时通道异常：%s（浏览器会自动重连）", msg)
}

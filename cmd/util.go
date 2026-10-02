package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/yuzumikonami/nyatmc/internal/logwatch"
)

// readTailFile 读取文件末尾并返回最后 n 行。
//
// 只读最后 512KB，避免把很大的日志文件整个读进内存（Termux 上内存很宝贵）。
func readTailFile(path string, n int) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	const maxBytes = 512 * 1024
	start := int64(0)
	if st.Size() > maxBytes {
		start = st.Size() - maxBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	text := string(data)
	if start > 0 {
		if idx := strings.IndexByte(text, '\n'); idx >= 0 {
			text = text[idx+1:]
		}
	}
	return logwatch.TailLines(text, n), nil
}

// confirm 在终端上询问一次，只有回答 y/yes/是 才返回 true。
//
// 非交互环境（stdin 不是终端）一律返回 false，避免脚本里误删数据。
func confirm(app *App, prompt string) bool {
	if !isStdinTerminal() {
		app.Warn("当前不是交互终端，请加 --yes 明确确认")
		return false
	}
	fmt.Fprintf(os.Stderr, "%s [y/N]: ", prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "是", "确认", "好":
		return true
	default:
		return false
	}
}

func isStdinTerminal() bool {
	st, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

// probePortFree 尝试绑定端口，用于判断端口是否可用。
func probePortFree(port int) error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	return ln.Close()
}

// contextWithTimeout 在父 context 上叠加超时（父为 nil 时退化为 Background）。
func contextWithTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if d <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, d)
}

// firstNonEmpty 返回第一个非空字符串（去空白）。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

package daemon

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"github.com/yuzumikonami/nyatmc/internal/logwatch"
)

// probePort 尝试绑定端口，用于“启动前提醒端口占用”。
func probePort(port int) error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	return ln.Close()
}

// readTailLines 读取文件末尾最多 maxBytes 字节并返回最后 n 行。
//
// 用于 supervisor 不在时仍能看日志，避免把整个大日志读进内存。
func readTailLines(path string, n int, maxBytes int64) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	start := int64(0)
	if maxBytes > 0 && st.Size() > maxBytes {
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
		// 从中间截断时首行可能不完整，丢掉它
		if idx := strings.IndexByte(text, '\n'); idx >= 0 {
			text = text[idx+1:]
		}
	}
	return logwatch.TailLines(text, n), nil
}

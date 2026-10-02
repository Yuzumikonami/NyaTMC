package daemon

import (
	"os"
	"os/signal"
	"syscall"
)

// shutdownSignals 订阅中断信号（Ctrl+C / SIGTERM）。
func shutdownSignals() chan os.Signal {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	return ch
}

// releaseSignals 取消订阅。
func releaseSignals(ch chan os.Signal) {
	signal.Stop(ch)
	close(ch)
}

// writePIDFile 写入 supervisor 的 pid 文件。
func writePIDFile(path string, pid int) error {
	return os.WriteFile(path, []byte(itoa(pid)+"\n"), 0o644)
}

// readPIDFile 读取 pid 文件，不存在或非法时返回 0。
func readPIDFile(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := 0
	for _, r := range string(data) {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

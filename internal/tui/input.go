package tui

import (
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

// terminal 封装输入/输出通道与终端状态。
//
// 只依赖 golang.org/x/term（raw 模式与尺寸），不引入任何 TUI 库。
type terminal struct {
	in       *os.File
	out      *os.File
	oldState *term.State
	raw      bool
	tty      bool

	keys chan []byte

	resize chan os.Signal
	stop   chan struct{}
}

// openTerminal 打开终端并进入 raw 模式。
func openTerminal() (*terminal, error) {
	t := &terminal{
		in:     os.Stdin,
		out:    os.Stdout,
		keys:   make(chan []byte, 256),
		resize: make(chan os.Signal, 4),
		stop:   make(chan struct{}),
	}
	inFd := int(os.Stdin.Fd())
	outFd := int(os.Stdout.Fd())
	t.tty = term.IsTerminal(inFd) && term.IsTerminal(outFd)
	if !t.tty {
		return t, nil
	}

	// 进入 raw 模式：关闭行缓冲、回显与信号生成（Ctrl+C 由我们自己处理）
	state, err := term.MakeRaw(inFd)
	if err != nil {
		return t, err
	}
	t.oldState = state
	t.raw = true
	go t.readLoop()

	// Unix 下监听窗口尺寸变化；容器/Windows 不支持时忽略即可（主循环会轮询尺寸）。
	watchResize(t.resize)
	return t, nil
}

// readLoop 持续把标准输入的数据送进 keys 通道；输入结束时送一个 nil。
func (t *terminal) readLoop() {
	buf := make([]byte, 1024)
	for {
		n, err := t.in.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			select {
			case t.keys <- chunk:
			case <-t.stop:
				return
			}
		}
		if err != nil {
			// 读取出错（Ctrl+D、终端消失等）时通知主循环退出，避免界面卡住
			select {
			case t.keys <- nil:
			case <-t.stop:
			}
			return
		}
	}
}

// close 恢复终端原始状态。可以安全地重复调用。
func (t *terminal) close() {
	if t == nil {
		return
	}
	select {
	case <-t.stop:
		// 已经关闭过
	default:
		close(t.stop)
	}
	if t.raw && t.oldState != nil {
		_ = term.Restore(int(t.in.Fd()), t.oldState)
		t.raw = false
	}
	stopResize(t.resize)
}

// size 返回终端尺寸（列、行）；失败时退回环境变量或 80x24。
func (t *terminal) size() (int, int) {
	if t != nil && t.tty {
		if w, h, err := term.GetSize(int(t.out.Fd())); err == nil && w > 0 && h > 0 {
			return w, h
		}
	}
	if w, h, ok := sizeFromEnv(); ok {
		return w, h
	}
	return 80, 24
}

// sizeFromEnv 在拿不到 ioctl 时用环境变量兜底（例如某些 Termux 会话）。
func sizeFromEnv() (int, int, bool) {
	cols, ok1 := atoiEnv("COLUMNS")
	rows, ok2 := atoiEnv("LINES")
	if ok1 && ok2 && cols > 0 && rows > 0 {
		return cols, rows, true
	}
	return 0, 0, false
}

func atoiEnv(name string) (int, bool) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return 0, false
	}
	n := 0
	for _, r := range v {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
		if n > 10000 {
			return 0, false
		}
	}
	if n == 0 {
		return 0, false
	}
	return n, true
}

// ------------------------------------------------------------------ 按键事件

// KeyKind 是按键类别。
type KeyKind string

// 按键类别取值。
const (
	KeyRune    KeyKind = "rune"    // 可打印字符
	KeyEnter   KeyKind = "enter"   // 回车
	KeyEsc     KeyKind = "esc"     // Esc（单独按下）
	KeyTab     KeyKind = "tab"     // Tab
	KeyBacktab KeyKind = "backtab" // Shift+Tab
	KeyBack    KeyKind = "back"    // 退格
	KeyDelete  KeyKind = "delete"  // Delete
	KeyUp      KeyKind = "up"
	KeyDown    KeyKind = "down"
	KeyLeft    KeyKind = "left"
	KeyRight   KeyKind = "right"
	KeyPgUp    KeyKind = "pgup"
	KeyPgDn    KeyKind = "pgdn"
	KeyHome    KeyKind = "home"
	KeyEnd     KeyKind = "end"
	KeyCtrlC   KeyKind = "ctrl-c"
	KeyCtrlL   KeyKind = "ctrl-l"
	KeyEOF     KeyKind = "eof"
	KeyUnknown KeyKind = "unknown"
)

// Key 是一次按键解析结果。
type Key struct {
	Kind KeyKind
	Rune rune
}

// keyParser 把输入字节流切成按键：天然处理转义序列被拆包的情况。
type keyParser struct {
	pending []byte
}

// Next 解析出下一个按键。buf 为空表示没有更多输入了。
func (p *keyParser) Next(buf []byte, atEOF bool) (Key, bool) {
	p.pending = append(p.pending, buf...)
	if len(p.pending) == 0 {
		return Key{}, false
	}
	for {
		if len(p.pending) == 0 {
			return Key{}, false
		}
		k, n, ok := parseKey(p.pending, atEOF)
		if !ok {
			return Key{}, false
		}
		p.pending = p.pending[n:]
		return k, true
	}
}

// Flush 在输入结束时吐出仍然悬着的按键（例如单独一个 Esc）。
func (p *keyParser) Flush() (Key, bool) {
	if len(p.pending) == 0 {
		return Key{}, false
	}
	k, n, ok := parseKey(p.pending, true)
	if !ok {
		k = Key{Kind: KeyUnknown}
		n = len(p.pending)
	}
	p.pending = p.pending[n:]
	return k, true
}

// parseKey 从 b 里解析一个按键，返回按键与消耗的字节数。
//
// atEOF 为 true 表示不会再收到更多字节：此时单独的 ESC 会被当作 Esc 键，
// 不完整的转义序列也会被强制解析掉，避免挂住。
func parseKey(b []byte, atEOF bool) (Key, int, bool) {
	if len(b) == 0 {
		return Key{}, 0, false
	}
	switch b[0] {
	case 0x1b:
		// EOF 时的流式特殊标记
		if len(b) == 1 {
			if atEOF {
				return Key{Kind: KeyEsc}, 1, true
			}
			return Key{}, 0, false
		}
		if b[1] == '[' || b[1] == 'O' {
			if k, n, ok := parseCSI(b, atEOF); ok {
				return k, n, true
			}
			if atEOF {
				return Key{Kind: KeyUnknown}, len(b), true
			}
			return Key{}, 0, false
		}
		if b[1] == 0x1b {
			// 连按两次 Esc：先返回一个 Esc
			return Key{Kind: KeyEsc}, 1, true
		}
		// Alt+字符：按普通字符处理，忽略 Alt
		r, size := decodeRune(b[1:])
		return Key{Kind: KeyRune, Rune: r}, 1 + size, true

	case '\r', '\n':
		return Key{Kind: KeyEnter}, 1, true
	case '\t':
		return Key{Kind: KeyTab}, 1, true
	case 0x7f, 0x08:
		return Key{Kind: KeyBack}, 1, true
	case 0x03:
		return Key{Kind: KeyCtrlC}, 1, true
	case 0x0c:
		return Key{Kind: KeyCtrlL}, 1, true
	case 0x00:
		return Key{Kind: KeyCtrlC}, 1, true
	}
	if b[0] < 0x20 {
		return Key{Kind: KeyUnknown}, 1, true
	}
	r, size := decodeRune(b)
	if r == 0 && size == 0 {
		return Key{}, 0, false
	}
	return Key{Kind: KeyRune, Rune: r}, size, true
}

// parseCSI 解析 CSI / SS3 序列。
func parseCSI(b []byte, atEOF bool) (Key, int, bool) {
	if len(b) < 3 {
		if atEOF {
			return Key{Kind: KeyUnknown}, len(b), true
		}
		return Key{}, 0, false
	}
	head := b[1]
	body := b[2:]
	// SS3：ESC O A/B/C/D/H/F
	if head == 'O' {
		switch body[0] {
		case 'A':
			return Key{Kind: KeyUp}, 3, true
		case 'B':
			return Key{Kind: KeyDown}, 3, true
		case 'C':
			return Key{Kind: KeyRight}, 3, true
		case 'D':
			return Key{Kind: KeyLeft}, 3, true
		case 'H':
			return Key{Kind: KeyHome}, 3, true
		case 'F':
			return Key{Kind: KeyEnd}, 3, true
		}
		return Key{Kind: KeyUnknown}, 3, true
	}

	// CSI：找终结符
	final := -1
	for i := 2; i < len(b); i++ {
		c := b[i]
		if c >= 0x40 && c <= 0x7e {
			final = i
			break
		}
	}
	if final < 0 {
		if atEOF {
			return Key{Kind: KeyUnknown}, len(b), true
		}
		return Key{}, 0, false
	}
	params := string(b[2:final])
	term := b[final]
	n := final + 1
	// 带修饰键的 CSI（如 ESC [ 1 ; 5 A）只看最后一位
	if i := strings.LastIndexByte(params, ';'); i >= 0 {
		params = params[i+1:]
	}
	switch term {
	case 'A':
		return Key{Kind: KeyUp}, n, true
	case 'B':
		return Key{Kind: KeyDown}, n, true
	case 'C':
		return Key{Kind: KeyRight}, n, true
	case 'D':
		return Key{Kind: KeyLeft}, n, true
	case 'H':
		return Key{Kind: KeyHome}, n, true
	case 'F':
		return Key{Kind: KeyEnd}, n, true
	case 'Z':
		return Key{Kind: KeyBacktab}, n, true
	case '~':
		switch params {
		case "1", "7":
			return Key{Kind: KeyHome}, n, true
		case "2":
			return Key{Kind: KeyUnknown}, n, true // Insert，不处理
		case "3":
			return Key{Kind: KeyDelete}, n, true
		case "4", "8":
			return Key{Kind: KeyEnd}, n, true
		case "5":
			return Key{Kind: KeyPgUp}, n, true
		case "6":
			return Key{Kind: KeyPgDn}, n, true
		case "200":
			return Key{Kind: KeyUnknown}, n, true // 粘贴开始
		case "201":
			return Key{Kind: KeyUnknown}, n, true // 粘贴结束
		}
		return Key{Kind: KeyUnknown}, n, true
	}
	return Key{Kind: KeyUnknown}, n, true
}

// decodeRune 解码一个 UTF-8 字符，非法/不完整时返回 (0, 0) 表示需要更多字节。
func decodeRune(b []byte) (rune, int) {
	if len(b) == 0 {
		return 0, 0
	}
	c := b[0]
	switch {
	case c < 0x80:
		return rune(c), 1
	case c&0xE0 == 0xC0:
		if len(b) < 2 {
			return 0, 0
		}
		return rune(c&0x1F)<<6 | rune(b[1]&0x3F), 2
	case c&0xF0 == 0xE0:
		if len(b) < 3 {
			return 0, 0
		}
		return rune(c&0x0F)<<12 | rune(b[1]&0x3F)<<6 | rune(b[2]&0x3F), 3
	case c&0xF8 == 0xF0:
		if len(b) < 4 {
			return 0, 0
		}
		return rune(c&0x07)<<18 | rune(b[1]&0x3F)<<12 | rune(b[2]&0x3F)<<6 | rune(b[3]&0x3F), 4
	}
	return rune(c), 1
}

// ------------------------------------------------------------------ 尺寸变化

// watchResize 在 Unix 上订阅 SIGWINCH；Windows 没有该信号，主循环改用轮询。
func watchResize(ch chan os.Signal) {
	notifyWinch(ch)
}

// stopResize 取消尺寸变化的订阅。
func stopResize(ch chan os.Signal) {
	ignoreWinch(ch)
}

// resizeTick 返回轮询尺寸的间隔：Unix 靠信号，其它平台靠这个兜底。
func resizeTick() time.Duration { return 500 * time.Millisecond }

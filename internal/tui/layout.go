// Package tui 实现 nyatmc 的纯 ANSI 终端看板。
//
// 设计约束（见项目说明）：
//   - 不引入任何 TUI 库，全部转义码自己写；
//   - 唯一的外部依赖是 golang.org/x/term（raw 模式与终端尺寸），失败时退化为轮询；
//   - 进入备用屏幕缓冲区，退出时（含 panic）一定恢复光标与终端原始状态；
//   - 内存受限（Termux），日志只保留末尾若干行，绝不缓存整份日志。
//
// 文件划分：
//
//	tui.go          主循环、模型与单键操作
//	render.go       布局与绘制（纯 ANSI）
//	input.go        raw 模式、按键解析、终端尺寸
//	refresh.go      数据采集（daemon.Subscribe + 轮询退化）
//	config_mode.go  界面内配置浏览与编辑
//	layout.go       纯逻辑：宽度计算、裁剪、日志环形缓冲、转义码解析
package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ------------------------------------------------------------------ 显示宽度

// runeWidth 返回字符在终端里占用的列数（CJK 等全角字符算 2 列）。
func runeWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case r == '\t':
		return 0 // 制表符单独处理，不占固定宽度
	case r < 32 || r == 0x7f:
		return 0 // 控制字符不占位
	case r < 0x1100:
		return 1
	case isZeroWidth(r):
		return 0
	case isWide(r):
		return 2
	default:
		return 1
	}
}

// isZeroWidth 判断组合字符等零宽字符。
func isZeroWidth(r rune) bool {
	return r == 0x200b || r == 0x200c || r == 0x200d || r == 0xfeff ||
		unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf)
}

// wideRanges 是常见的宽字符区间（East Asian Wide / Fullwidth）。
var wideRanges = [][2]rune{
	{0x1100, 0x115F}, // 韩文字母
	{0x2E80, 0x303E}, // 中日韩部首、标点
	{0x3041, 0x33FF}, // 平假名、片假名、注音、中日韩兼容
	{0x3400, 0x4DBF}, // 中日韩扩展 A
	{0x4E00, 0x9FFF}, // 中日韩统一表意文字
	{0xA000, 0xA4CF}, // 彝文
	{0xAC00, 0xD7A3}, // 韩文音节
	{0xF900, 0xFAFF}, // 中日韩兼容表意文字
	{0xFE10, 0xFE19}, // 竖排标点
	{0xFE30, 0xFE6F}, // 中日韩兼容形式
	{0xFF00, 0xFF60}, // 全角形式
	{0xFFE0, 0xFFE6}, // 全角符号
	{0x1F300, 0x1F64F},
	{0x1F900, 0x1F9FF},
	{0x20000, 0x3FFFD}, // 中日韩扩展 B 及以后
}

// isWide 判断字符是否占两列。
func isWide(r rune) bool {
	if r < 0x1100 {
		return false
	}
	for _, rg := range wideRanges {
		if r >= rg[0] && r <= rg[1] {
			return true
		}
	}
	return false
}

// Measure 返回字符串在终端里占用的列数（忽略 ANSI 转义序列）。
func Measure(s string) int {
	return measure(s, 0)
}

// measure 返回宽度；当 limit > 0 时达到上限即停止（最多多算一个字符）。
func measure(s string, limit int) int {
	w := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			if n := escapeLen(s[i:]); n > 0 {
				i += n
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size <= 1 {
			i++
			w++
		} else {
			i += size
			w += runeWidth(r)
		}
		if limit > 0 && w > limit {
			return w
		}
	}
	return w
}

// escapeLen 返回 s 开头一个 ANSI 转义序列的长度（s 必须以 ESC 开头）。
func escapeLen(s string) int {
	if len(s) < 2 || s[0] != 0x1b {
		return 0
	}
	switch s[1] {
	case '[': // CSI：ESC [ 参数 终结符
		for i := 2; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
		}
		return len(s)
	case ']': // OSC：ESC ] ... BEL 或 ESC \
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
		return len(s)
	default:
		return 2
	}
}

// StripANSI 去掉字符串里的 ANSI 转义序列。
func StripANSI(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			if n := escapeLen(s[i:]); n > 0 {
				i += n
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// Sanitize 把日志文本里不可见的控制字符去掉，避免破坏版面。
func Sanitize(s string) string {
	s = StripANSI(s)
	if !strings.ContainsFunc(s, func(r rune) bool {
		return r < 32 || r == 0x7f
	}) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 32 || r == 0x7f {
			// 控制字符（含 \t）一律去掉：服务端日志里它们会让版面错位
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Truncate 按显示宽度截断字符串，放不下时在末尾补省略号（能补才补）。
//
// 中文等宽字符占两列：当前缀没占满宽度、被裁掉的又恰好只剩最后一个字符时，
// 省略号挤进去反而损失信息，此时保持前缀原样——
// 所以 "运行中" 在 5 列下是 "运行"（2+2 之后只剩“中”），在 4 列下是 "运…"。
func Truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if Measure(s) <= width {
		return s
	}
	if width <= 1 {
		return "…"
	}
	// 先尽量多放字符
	w := 0
	end := len(s)
	for i, r := range s {
		rw := runeWidth(r)
		if w+rw > width {
			end = i
			break
		}
		w += rw
		end = i + len(string(r))
	}
	if w == 0 {
		return "…"
	}
	// 前缀没占满宽度、且被裁掉的只剩最后一个字符：保持前缀原样。
	if w < width && utf8.RuneCountInString(s[end:]) == 1 {
		return s[:end]
	}
	// 其余情况统一按「内容占 width-1 列 + 省略号」处理。
	return tailFrom(s, width)
}

// tailFrom 返回末尾（含省略号）仍能放进 width 列的前缀。
func tailFrom(s string, width int) string {
	w := 0
	end := 0
	for i, r := range s {
		rw := runeWidth(r)
		if w+rw > width-1 {
			end = i
			break
		}
		w += rw
		end = i + len(string(r))
	}
	if w == 0 {
		return "…"
	}
	return s[:end] + "…"
}

// Pad 在右侧补空格直到占满 width 列（超出则原样返回）。
func Pad(s string, width int) string {
	n := width - Measure(s)
	if n <= 0 {
		return s
	}
	return s + strings.Repeat(" ", n)
}

// PadLeft 在左侧补空格。
func PadLeft(s string, width int) string {
	n := width - Measure(s)
	if n <= 0 {
		return s
	}
	return strings.Repeat(" ", n) + s
}

// Tail 返回字符串末尾至多 width 列的内容（用于把长日志裁掉开头）。
func Tail(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if Measure(s) <= width {
		return s
	}
	runes := []rune(s)
	w := 0
	for i := len(runes) - 1; i >= 0; i-- {
		w += runeWidth(runes[i])
		if w > width {
			return string(runes[i+1:])
		}
	}
	return s
}

// Fill 返回 n 个重复字符（n <= 0 时返回空串）。
func Fill(ch string, n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(ch, n)
}

// ------------------------------------------------------------------ 日志环形缓冲

// LogRing 是一个固定容量的行缓冲：内存占用恒定，只保留末尾若干行。
type LogRing struct {
	lines []string
	max   int
	start int
	n     int
}

// NewLogRing 创建最多保存 max 行的环形缓冲。
func NewLogRing(max int) *LogRing {
	if max < 1 {
		max = 1
	}
	return &LogRing{lines: make([]string, max), max: max}
}

// Append 追加一行；超过容量时覆盖最旧的一行。
func (r *LogRing) Append(line string) {
	r.lines[(r.start+r.n)%r.max] = line
	if r.n == r.max {
		r.start = (r.start + 1) % r.max
		return
	}
	r.n++
}

// AppendAll 批量追加。
func (r *LogRing) AppendAll(lines []string) {
	for _, l := range lines {
		r.Append(l)
	}
}

// Len 返回当前行数。
func (r *LogRing) Len() int { return r.n }

// Cap 返回容量。
func (r *LogRing) Cap() int { return r.max }

// At 返回第 i 行（0 为最旧一行）。
func (r *LogRing) At(i int) string {
	if i < 0 || i >= r.n {
		return ""
	}
	return r.lines[(r.start+i)%r.max]
}

// Reset 清空缓冲。
func (r *LogRing) Reset() {
	r.start = 0
	r.n = 0
}

// Window 返回从 offset 开始的 count 行（offset 从底部计数，0 表示最后一行）。
// 返回值中最后一个是"最新"的一行；不足 count 行时只返回实际拥有的行。
func (r *LogRing) Window(count, offset int) []string {
	if count <= 0 || r.n == 0 {
		return nil
	}
	if count > r.n {
		count = r.n
	}
	end := r.n - offset // 不含
	if end < 0 {
		end = 0
	}
	start := end - count
	if start < 0 {
		start = 0
	}
	out := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		out = append(out, r.At(i))
	}
	return out
}

// Scroll 根据当前滚动偏移算出新的偏移。
//
// lines 是总行数；delta > 0 表示向下（看得更晚，0 即回到最新输出）。
// 返回值已经裁剪到 [0, lines-1]。
func Scroll(offset, delta, lines int) int {
	if lines <= 0 {
		return 0
	}
	offset += delta
	switch {
	case offset < 0:
		return 0
	case offset > lines-1:
		return lines - 1
	}
	return offset
}

// ScrollHome 跳到最开头。
func ScrollHome(lines int) int {
	if lines <= 1 {
		return 0
	}
	return lines - 1
}

package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/yuzumikonami/nyatmc/internal/config"
	"github.com/yuzumikonami/nyatmc/internal/daemon"
	"github.com/yuzumikonami/nyatmc/internal/instance"
)

// ------------------------------------------------------------------ 样式

// 前景色 SGR 参数。
const (
	cReset  = "0"
	cDim    = "90"
	cRed    = "31"
	cGreen  = "32"
	cYellow = "33"
	cBlue   = "34"
	cPurple = "35"
	cCyan   = "36"
	cWhite  = "97"
)

// style 是一段文本的显示样式。
type style struct {
	fg   string
	bold bool
	dim  bool
	rev  bool
}

// 常用样式。
var (
	styPlain  = style{fg: cWhite}
	styMuted  = style{fg: cDim}
	styTitle  = style{fg: cCyan, bold: true}
	styBorder = style{fg: cDim}
	styGreen  = style{fg: cGreen}
	styCyan   = style{fg: cCyan}
	styYellow = style{fg: cYellow}
	styRed    = style{fg: cRed}
	styRev    = style{rev: true}
)

// enabled 判断该样式是否真的需要转义序列。
func (s style) enabled() bool {
	return s.fg != "" && s.fg != cWhite || s.bold || s.dim || s.rev
}

// seq 返回进入该样式的 SGR 序列。
func (s style) seq() string {
	if !s.enabled() {
		return ""
	}
	parts := make([]string, 0, 3)
	if s.bold {
		parts = append(parts, "1")
	}
	if s.dim {
		parts = append(parts, "2")
	}
	if s.rev {
		parts = append(parts, "7")
	}
	if s.fg != "" {
		parts = append(parts, s.fg)
	}
	return "\x1b[" + strings.Join(parts, ";") + "m"
}

// stateStyle 返回状态对应的颜色样式。
func stateStyle(st daemon.State) style {
	switch st {
	case daemon.StateRunning:
		return styGreen
	case daemon.StateStarting, daemon.StateRestarting:
		return style{fg: cCyan}
	case daemon.StateStopping:
		return styYellow
	case daemon.StateCrashed:
		return styRed
	default:
		return styMuted
	}
}

// stateDot 返回状态指示灯。
func stateDot(st daemon.State) string {
	if st.Active() {
		return "●"
	}
	switch st {
	case daemon.StateCrashed:
		return "◆"
	default:
		return "○"
	}
}

// ------------------------------------------------------------------ 行缓冲

// lineWriter 按行拼接内容，自动跟踪显示宽度并补齐整行。
type lineWriter struct {
	width int
	buf   strings.Builder
	used  int
}

func newLineWriter(width int) *lineWriter {
	return &lineWriter{width: maxInt(0, width)}
}

// Text 追加带样式的文本；超出宽度时按显示宽度截断。
func (l *lineWriter) Text(text string, st style) {
	if text == "" {
		return
	}
	text = sanitizeCell(text)
	if remaining := l.width - l.used; Measure(text) > remaining {
		text = Truncate(text, remaining)
	}
	if text == "" {
		return
	}
	if s := st.seq(); s != "" {
		l.buf.WriteString(s)
		l.buf.WriteString(text)
		l.buf.WriteString("\x1b[0m")
	} else {
		l.buf.WriteString(text)
	}
	l.used += Measure(text)
}

// Repeat 追加 n 个重复字符。
func (l *lineWriter) Repeat(ch string, n int, st style) {
	if n <= 0 {
		return
	}
	l.Text(strings.Repeat(ch, n), st)
}

// Span 用指定字符填满整行（常用于分隔线）。
func (l *lineWriter) Span(ch string, st style) {
	l.Repeat(ch, l.width-l.used, st)
}

// TextAt 把光标移到第 col 列（0 基）后追加文本，必要时补空格。
func (l *lineWriter) TextAt(col int, text string, st style) {
	if col < 0 {
		col = 0
	}
	if col > l.used {
		l.Repeat(" ", col-l.used, styPlain)
	}
	l.Text(text, st)
}

// FillTo 用空格补齐到第 col 列。
func (l *lineWriter) FillTo(col int) {
	if col > l.used {
		l.Repeat(" ", col-l.used, styPlain)
	}
}

// Rest 返回本行剩余可用列数。
func (l *lineWriter) Rest() int { return l.width - l.used }

// Width 返回整行宽度。
func (l *lineWriter) Width() int { return l.width }

// Used 返回已经占用的列数。
func (l *lineWriter) Used() int { return l.used }

// String 返回补齐后的整行内容。
func (l *lineWriter) String() string {
	s := l.buf.String()
	if pad := l.width - l.used; pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// sanitizeCell 去掉文本里的换行与控制字符，保证一格内容不破坏版面。
func sanitizeCell(s string) string {
	s = Sanitize(s)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return s
}

// ------------------------------------------------------------------ 视图模型

// viewMode 是当前界面模式。
type viewMode int

const (
	modeDashboard viewMode = iota
	modeConfig
	modeHelp
)

// view 是渲染一帧所需的全部数据（由主循环填好后交给渲染器）。
type view struct {
	mode   viewMode
	width  int
	height int

	inst      *instance.Instance
	status    daemon.Status
	statusErr string

	logs   *LogRing
	offset int

	instances []string
	instIndex int

	opLabel string
	toast   string
	toastAt time.Time

	hideLogs    bool
	hidePlayers bool

	cfg *configState
}

// ------------------------------------------------------------------ 渲染器

// renderer 负责把 view 画成一整帧（自带 ANSI 转义码）。
//
// 为了兼容 Windows 传统控制台，边框只用 ASCII 字符。
type renderer struct {
	cfg *config.Config
}

// rowSep 是不带光标定位时的行分隔符（离线预览与测试用）。
const rowSep = "\n"

// draw 生成整帧输出。frame 为 true 时在每行前加上光标定位。
func (r *renderer) draw(v *view, cursor *cursorPos, frame bool) string {
	if v.width < 2 || v.height < 2 {
		return ""
	}
	var b strings.Builder
	b.Grow(v.width*v.height + 256)

	writeRow := func(id int, content string) {
		if frame {
			fmt.Fprintf(&b, "\x1b[%d;1H", id+1)
		}
		b.WriteString(content)
		if !frame {
			b.WriteString(rowSep)
		}
	}

	switch v.mode {
	case modeHelp:
		r.drawHelp(v, writeRow)
		return b.String()
	case modeConfig:
		r.drawConfig(v, writeRow, frame, cursor)
		return b.String()
	}
	r.drawDashboard(v, writeRow)
	return b.String()
}

// renderLines 生成不带光标定位的整帧（每行一个字符串），供测试与离线预览使用。
func (r *renderer) renderLines(v *view) []string {
	out := r.draw(v, nil, false)
	lines := strings.Split(out, rowSep)
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	for len(lines) < v.height {
		lines = append(lines, "")
	}
	if len(lines) > v.height {
		lines = lines[:v.height]
	}
	return lines
}

// drawDashboard 绘制看板主体的各行。
func (r *renderer) drawDashboard(v *view, writeRow func(int, string)) {
	// 行分配：标题 2 行、指标 2~3 行，底部按键提示 1 行，其余给日志/玩家。
	used := r.drawHeader(v, writeRow, 1)
	used += r.drawMetrics(v, writeRow)
	if v.height-used >= 5 {
		r.drawMainPanel(v, writeRow, used, v.height-used-1)
	} else {
		r.drawTooSmall(v, writeRow, used, v.height-used)
	}
	if t := v.toast; t != "" && v.height >= 2 {
		r.drawToast(v, writeRow, v.height-2, t)
	}
	r.drawKeybar(v, writeRow, v.height-1)
}

// drawTooSmall 在小窗口里给出中文提示而不是乱喷转义码。
func (r *renderer) drawTooSmall(v *view, writeRow func(int, string), row, rows int) {
	for i := 0; i < rows; i++ {
		lw := newLineWriter(v.width)
		if i == 0 {
			lw.Text("窗口太小，无法显示看板内容。", styYellow)
		} else if i == 1 {
			lw.Text("请把终端调整到至少 80x24（当前 ", styPlain)
			lw.Text(fmt.Sprintf("%dx%d", v.width, v.height), styCyan)
			lw.Text("），或按 Q 退出。", styPlain)
		}
		writeRow(row+i, lw.String())
	}
}

// ------------------------------------------------------------------ 标题栏

// drawHeader 绘制顶部标题栏，返回占用的行数。
func (r *renderer) drawHeader(v *view, writeRow func(int, string), maxRows int) int {
	st := v.status
	lines := make([]*lineWriter, 0, 2)

	// 第一行：品牌、实例名、状态与关键数字
	lw := newLineWriter(v.width)
	lw.Text(" nyatmc ", styTitle)
	if v.inst != nil {
		lw.Text("实例 ", styMuted)
		lw.Text(truncateName(v.inst.Name, v.width), stBold())
	}
	ss := stateStyle(st.State)
	lw.Text("  ", styPlain)
	lw.Text(stateDot(st.State), ss)
	lw.Text(" "+st.State.Label(), ss)
	if st.State == daemon.StateRunning && !st.Ready {
		lw.Text("（未就绪）", styYellow)
	}
	if st.Stale {
		lw.Text(" 状态可能滞后", styYellow)
	}
	if v.opLabel != "" {
		lw.Text("  ["+v.opLabel+"]", styYellow)
	}
	if v.instIndex >= 0 && len(v.instances) > 1 {
		lw.Text(fmt.Sprintf("  [%d/%d 实例]", v.instIndex+1, len(v.instances)), styMuted)
	}

	// 第二行：PID / 运行时长 / 版本 / 内存
	lw2 := newLineWriter(v.width)
	lw2.Text(" PID ", styMuted)
	lw2.Text(pidText(st), styPlain)
	lw2.Text("  运行 ", styMuted)
	lw2.Text(uptimeText(st), styPlain)
	lw2.Text("  版本 ", styMuted)
	lw2.Text(versionText(st), styPlain)
	lw2.Text("  内存 ", styMuted)
	lw2.Text(memoryText(st), memStyle(st))
	if v.statusErr != "" {
		lw2.Text("  ", styPlain)
		lw2.Text(Truncate(v.statusErr, maxInt(8, lw2.Rest()-1)), styRed)
	}

	lines = append(lines, lw, lw2)
	if maxRows < len(lines) {
		lines = lines[:maxRows]
	}
	for i, l := range lines {
		writeRow(i, l.String())
	}
	return len(lines)
}

func stBold() style { return style{fg: cWhite, bold: true} }

func truncateName(name string, width int) string {
	return Truncate(name, maxInt(4, width/3))
}

// pidText 返回 PID 展示文本。
func pidText(st daemon.Status) string {
	switch {
	case st.PID > 0 && st.SupervisorPID > 0 && st.PID != st.SupervisorPID:
		return fmt.Sprintf("%d（守护 %d）", st.PID, st.SupervisorPID)
	case st.PID > 0:
		return fmt.Sprintf("%d", st.PID)
	case st.SupervisorPID > 0:
		return fmt.Sprintf("-（守护 %d）", st.SupervisorPID)
	default:
		return "-"
	}
}

// uptimeText 返回运行时长文本。
func uptimeText(st daemon.Status) string {
	if !st.State.Active() {
		return "-"
	}
	return humanDuration(st.Uptime())
}

// versionText 返回版本文本。
func versionText(st daemon.Status) string {
	v := st.Version
	if v == "" {
		v = st.ServerType
	} else if st.ServerType != "" {
		v = v + " (" + st.ServerType + ")"
	}
	if v == "" {
		return "-"
	}
	return v
}

// memoryText 返回内存占用文本。
func memoryText(st daemon.Status) string {
	limit := strings.TrimSpace(st.MemoryLimit)
	switch {
	case st.MemoryUsedMB > 0 && limit != "":
		return fmt.Sprintf("%d MB / %s", st.MemoryUsedMB, limit)
	case st.MemoryUsedMB > 0:
		return fmt.Sprintf("%d MB", st.MemoryUsedMB)
	case limit != "":
		return limit
	default:
		return "-"
	}
}

func memStyle(st daemon.Status) style {
	used, limit := st.MemoryUsedMB, parseMemoryMB(st.MemoryLimit)
	if used > 0 && limit > 0 {
		ratio := float64(used) / float64(limit)
		switch {
		case ratio >= 0.92:
			return styRed
		case ratio >= 0.8:
			return styYellow
		}
	}
	return styPlain
}

// parseMemoryMB 把 "2G" / "512M" 解析成 MB。
func parseMemoryMB(s string) int64 {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0
	}
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "G"):
		mult, s = 1024, strings.TrimSuffix(s, "G")
	case strings.HasSuffix(s, "M"):
		mult, s = 1, strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "K"):
		mult, s = 0, strings.TrimSuffix(s, "K")
	case strings.HasSuffix(s, "T"):
		mult, s = 1024*1024, strings.TrimSuffix(s, "T")
	default:
		return 0
	}
	var f float64
	if _, err := fmt.Sscanf(s, "%g", &f); err != nil {
		return 0
	}
	if mult == 0 {
		return int64(f / 1024)
	}
	return int64(f * float64(mult))
}

// ------------------------------------------------------------------ 指标区

// metric 是一个指标项。
type metric struct {
	key string
	val string
	sty style
}

// drawMetrics 绘制指标区（TPS / 在线 / 看门狗 / 重启 / 警告错误 / 最近错误）。
func (r *renderer) drawMetrics(v *view, writeRow func(int, string)) int {
	st := v.status
	items := []metric{
		tpsMetric(st),
		{key: "在线", val: fmt.Sprintf("%d/%d", st.PlayerCount(), maxPlayersOf(v)), sty: styPlain},
		{key: "看门狗", val: onOffText(st.Watchdog), sty: boolStyle(st.Watchdog)},
		{key: "重启", val: fmt.Sprintf("%d 次", st.Restarts), sty: restartStyle(st)},
		{key: "警告/错误", val: fmt.Sprintf("%d/%d", st.Warnings, st.Errors), sty: warnStyle(st)},
	}
	if st.LastExitCode != 0 {
		items = append(items, metric{key: "退出码", val: fmt.Sprintf("%d", st.LastExitCode), sty: styRed})
	}
	if st.TickBehindMS > 0 {
		items = append(items, metric{key: "落后", val: fmt.Sprintf("%dms", st.TickBehindMS), sty: styYellow})
	}
	errText := strings.TrimSpace(st.LastError)
	if errText == "" {
		errText = strings.TrimSpace(st.Note)
	}

	rows := 2
	if v.width < 72 {
		rows = 3
	}
	if v.height < 16 {
		rows = 2
	}
	written := 0

	if rows <= 2 {
		lw := newLineWriter(v.width)
		lw.Text(" ", styPlain)
		for _, m := range items {
			seg := fmt.Sprintf("%s %s", m.key, m.val)
			need := Measure(seg) + 3
			if lw.Rest() < need {
				break
			}
			lw.Text(m.key+" ", styMuted)
			lw.Text(m.val+"  ", m.sty)
		}
		writeRow(2, lw.String())
		written++
		lw2 := newLineWriter(v.width)
		lw2.Text(" ", styPlain)
		lw2.Text("最近错误 ", styMuted)
		if errText == "" {
			lw2.Text("无", styGreen)
		} else {
			lw2.Text(Truncate(errText, maxInt(8, lw2.Rest()-1)), styRed)
		}
		writeRow(3, lw2.String())
		written++
	} else {
		// 两行指标 + 一行最近错误
		half := (len(items) + 1) / 2
		for i := 0; i < 2; i++ {
			part := items
			if i == 0 {
				part = items[:minInt(half, len(items))]
			} else {
				part = items[minInt(half, len(items)):]
			}
			lw := newLineWriter(v.width)
			lw.Text(" ", styPlain)
			for _, m := range part {
				if lw.Rest() < Measure(m.key)+Measure(m.val)+4 {
					break
				}
				lw.Text(m.key+" ", styMuted)
				lw.Text(m.val+"  ", m.sty)
			}
			writeRow(2+i, lw.String())
			written++
		}
		lw := newLineWriter(v.width)
		lw.Text(" 最近错误 ", styMuted)
		if errText == "" {
			lw.Text("无", styGreen)
		} else {
			lw.Text(Truncate(errText, maxInt(8, lw.Rest()-1)), styRed)
		}
		writeRow(2+2, lw.String())
		written++
	}
	return written
}

func maxPlayersOf(v *view) int {
	if v.status.MaxPlayers > 0 {
		return v.status.MaxPlayers
	}
	if v.inst != nil {
		return v.inst.Res.Server.MaxPlayers
	}
	return 0
}

// tpsMetric 返回 TPS 指标；未知时明确解释原因。
func tpsMetric(st daemon.Status) metric {
	if st.TPSKnown {
		sty := styGreen
		switch {
		case st.TPS < 15:
			sty = styRed
		case st.TPS < 19:
			sty = styYellow
		}
		return metric{key: "TPS", val: fmt.Sprintf("%.2f", st.TPS), sty: sty}
	}
	return metric{key: "TPS", val: "未知（需服务端输出 /tps，或装 Spark）", sty: styMuted}
}

func onOffText(v bool) string {
	if v {
		return "开"
	}
	return "关"
}

func boolStyle(v bool) style {
	if v {
		return styGreen
	}
	return styMuted
}

func restartStyle(st daemon.Status) style {
	if st.Restarts > 0 {
		return styYellow
	}
	return styPlain
}

func warnStyle(st daemon.Status) style {
	if st.Errors > 0 {
		return styRed
	}
	if st.Warnings > 0 {
		return styYellow
	}
	return styGreen
}

// ------------------------------------------------------------------ 日志 / 玩家

// drawMainPanel 绘制主体区：默认是「日志 + 玩家」，按 L/P 可以只留其中一个。
func (r *renderer) drawMainPanel(v *view, writeRow func(int, string), row, rows int) int {
	if rows <= 0 {
		return 0
	}
	playersOnly := v.hideLogs && !v.hidePlayers
	logsOnly := v.hidePlayers || (!v.hideLogs && len(r.playerLines(v)) == 0)

	switch {
	case playersOnly:
		writeRow(row, r.panelTop(v, "在线玩家", fmt.Sprintf("%d/%d ", v.status.PlayerCount(), maxPlayersOf(v))))
		writeRow(row+rows-1, r.panelBottom(v))
		lines := r.playerLines(v)
		for i := 0; i < rows-2; i++ {
			lw := newLineWriter(v.width)
			lw.Text("| ", styBorder)
			if i < len(lines) {
				lw.Text(Truncate(lines[i], v.width-3), styPlain)
			}
			writeRow(row+1+i, lw.String())
		}
		return rows
	case logsOnly:
		writeRow(row, r.panelTop(v, "日志", r.logTitleRight(v)))
		writeRow(row+rows-1, r.panelBottom(v))
		r.drawLogRows(v, writeRow, row+1, rows-2)
		return rows
	}

	// 日志 + 玩家：玩家区最多占总高度的一半
	playerLines := r.playerLines(v)
	inner := rows - 2
	playerBlock := len(playerLines)
	if maxPlayer := maxInt(2, inner/3); playerBlock > maxPlayer {
		playerBlock = maxPlayer
	}
	logInner := inner - playerBlock - 2
	if logInner < 2 {
		logInner = maxInt(0, inner-playerBlock)
		playerBlock = inner - logInner
	}
	writeRow(row, r.panelTop(v, "日志", r.logTitleRight(v)))
	r.drawLogRows(v, writeRow, row+1, logInner)
	base := row + 1 + logInner
	writeRow(base, r.panelTop(v, "在线玩家", fmt.Sprintf("%d/%d ", v.status.PlayerCount(), maxPlayersOf(v))))
	for i := 0; i < playerBlock; i++ {
		lw := newLineWriter(v.width)
		lw.Text("| ", styBorder)
		if i < len(playerLines) {
			lw.Text(Truncate(playerLines[i], v.width-3), styPlain)
		}
		writeRow(base+1+i, lw.String())
	}
	writeRow(base+playerBlock+1, r.panelBottom(v))
	return rows
}

// drawLogRows 绘制日志区内部若干行（含滚动提示）。
func (r *renderer) drawLogRows(v *view, writeRow func(int, string), row, rows int) {
	if rows <= 0 {
		return
	}
	content := r.logWindow(v, rows)
	for i := 0; i < rows; i++ {
		lw := newLineWriter(v.width)
		lw.Text("| ", styBorder)
		line := ""
		if i < len(content) {
			line = content[i]
		}
		r.drawLogLine(lw, line, v.width-3)
		writeRow(row+i, lw.String())
	}
}

// logWindow 按滚动偏移取出要显示的日志行，并在底部给出偏移提示。
func (r *renderer) logWindow(v *view, rows int) []string {
	out := make([]string, 0, rows)
	total := v.logs.Len()
	if total == 0 {
		if v.inst != nil && !v.status.State.Active() {
			out = append(out, "（实例未在运行，暂无日志；按 S 启动）")
		} else {
			out = append(out, "（暂无日志输出）")
		}
	} else {
		hint := 0
		if v.offset > 0 {
			hint = 1
		}
		out = append(out, v.logs.Window(rows-hint, v.offset)...)
		if hint == 1 {
			out = append(out, fmt.Sprintf("── 已向上滚动 %d 行（End 回到末尾）", v.offset))
		}
	}
	for len(out) < rows {
		out = append([]string{""}, out...)
	}
	if len(out) > rows {
		out = out[len(out)-rows:]
	}
	return out
}

// logTitleRight 返回日志区标题右侧的提示（行数、跟随状态）。
func (r *renderer) logTitleRight(v *view) string {
	total := v.logs.Len()
	if v.offset > 0 {
		return fmt.Sprintf("%d 行 已暂停跟随 ", total)
	}
	return fmt.Sprintf("%d 行 自动跟随 ", total)
}

// drawLogLine 绘制一行日志，错误/警告行会着色。
func (r *renderer) drawLogLine(lw *lineWriter, line string, width int) {
	text := Truncate(line, width)
	if text == "" {
		return
	}
	switch {
	case containsAnyFold(text, "error", "错误", "exception", "severe", "致命"):
		lw.Text(text, styRed)
	case containsAnyFold(text, "warn", "警告", "caution"):
		lw.Text(text, styYellow)
	case strings.Contains(text, "Done (") || strings.Contains(text, "完成"):
		lw.Text(text, styGreen)
	default:
		lw.Text(text, styPlain)
	}
}

// playerLines 返回玩家区分行内容。
func (r *renderer) playerLines(v *view) []string {
	players := v.status.Players
	if len(players) == 0 {
		return []string{"暂无玩家在线"}
	}
	return wrapList(players, v.width-4)
}

// wrapList 把名字列表按宽度折行（用中文顿号分隔）。
func wrapList(items []string, width int) []string {
	if width < 4 {
		width = 4
	}
	var out []string
	cur := ""
	for _, it := range items {
		piece := it
		if cur != "" {
			piece = "、" + it
		}
		if Measure(cur+piece) > width && cur != "" {
			out = append(out, cur)
			cur = it
			continue
		}
		cur += piece
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func (r *renderer) panelTop(v *view, title, right string) string {
	lw := newLineWriter(v.width)
	inner := maxInt(0, v.width-2)
	head := "+- " + title + " "
	rightPart := ""
	if right != "" {
		rightPart = " " + strings.TrimRight(right, " ") + " "
	}
	pad := inner - Measure(head) - Measure(rightPart)
	if pad < 1 {
		pad = 1
		head = Truncate(head, maxInt(0, inner-Measure(rightPart)-1))
		pad = maxInt(1, inner-Measure(head)-Measure(rightPart))
	}
	lw.Text("+", styBorder)
	lw.Text(head, styTitle)
	lw.Text(strings.Repeat("-", pad), styBorder)
	if rightPart != "" {
		lw.Text(rightPart, styMuted)
	}
	lw.Text("+", styBorder)
	return lw.String()
}

func (r *renderer) panelBottom(v *view) string {
	lw := newLineWriter(v.width)
	lw.Text("+", styBorder)
	lw.Repeat("-", maxInt(0, v.width-2), styBorder)
	lw.Text("+", styBorder)
	return lw.String()
}

// ------------------------------------------------------------------ 底部栏

// drawToast 绘制一闪而过的操作结果提示。
func (r *renderer) drawToast(v *view, writeRow func(int, string), row int, text string) {
	if row < 0 {
		return
	}
	lw := newLineWriter(v.width)
	sty := styGreen
	if strings.HasPrefix(text, "失败") || strings.HasPrefix(text, "错误") {
		sty = styRed
	} else if strings.HasPrefix(text, "提示") || strings.HasPrefix(text, "警告") {
		sty = styYellow
	}
	lw.Text(" "+Truncate(text, v.width-2), style{fg: sty.fg, bold: true})
	writeRow(row, lw.String())
}

// keyHint 是一个按键提示。
type keyHint struct {
	key  string
	desc string
}

// keyHintSets 是按键提示栏的三档方案（从全到精简），窄屏自动降级。
var keyHintSets = [][]keyHint{
	{
		{"S", "启动"}, {"E", "停止"}, {"R", "重启"}, {"B", "备份"}, {"C", "配置"},
		{"L", "日志"}, {"P", "玩家"}, {"K", "强杀"}, {"Tab", "切换"}, {"?", "帮助"}, {"Q", "退出"},
	},
	{
		{"S", "启动"}, {"E", "停止"}, {"R", "重启"}, {"B", "备份"}, {"C", "配置"},
		{"K", "强杀"}, {"?", "帮助"}, {"Q", "退出"},
	},
	{
		{"S", "启"}, {"E", "停"}, {"R", "重"}, {"C", "配"}, {"Q", "退"},
	},
	{
		{"S", "启"}, {"E", "停"}, {"Q", "退"},
	},
}

// hintsWidth 计算一组提示占用的列数。
func hintsWidth(hints []keyHint) int {
	w := 0
	for _, h := range hints {
		w += Measure(h.key) + Measure(h.desc) + 4
	}
	return w
}

// pickHints 选出第一组能放下的提示；都放不下时用最精简的一组。
func pickHints(width int) []keyHint {
	for _, set := range keyHintSets {
		if hintsWidth(set) <= width {
			return set
		}
	}
	return keyHintSets[len(keyHintSets)-1]
}

// drawKeybar 绘制底部按键提示栏（窄屏只保留最常用的键）。
func (r *renderer) drawKeybar(v *view, writeRow func(int, string), row int) {
	lw := newLineWriter(v.width)
	lw.Text(" ", styPlain)
	for _, h := range pickHints(v.width) {
		if lw.Rest() < Measure(h.key)+Measure(h.desc)+3 {
			break
		}
		lw.Text(h.key, style{fg: cCyan, bold: true})
		lw.Text(" "+h.desc+"  ", styMuted)
	}
	writeRow(row, lw.String())
}

// ------------------------------------------------------------------ 浮层

// helpText 是帮助浮层的内容。
var helpText = []string{
	"nyatmc 终端看板 —— 帮助",
	"",
	"单键操作（无需回车）：",
	"  S            启动实例（后台守护 + 崩溃自动重启）",
	"  E            优雅停止（发送 stop 指令，超时后强杀）",
	"  K            强制结束（立刻杀进程，慎用）",
	"  R            重启实例",
	"  B            立即备份（完成后自动按配置清理）",
	"  C            配置模式：↑/↓ 选择，Enter 编辑",
	"  Q / Ctrl+C   退出看板（不影响服务端）",
	"",
	"浏览：",
	"  ↑ / ↓        日志上下滚动一行",
	"  PgUp / PgDn  日志翻页",
	"  Home / End   跳到最早 / 回到末尾（自动跟随）",
	"  L            只看日志（隐藏玩家区）",
	"  P            只看玩家（隐藏日志区）",
	"  Tab / 数字键  在多个实例之间切换",
	"  ?  / h       打开 / 关闭本帮助",
	"",
	"说明：",
	"  * 日志只保留末尾若干行，内存占用恒定（Termux 友好）。",
	"  * TPS 显示「未知」表示服务端还没输出过 /tps 结果，",
	"    可以在服务端控制台执行 /tps，或安装 Spark 之类的插件。",
	"  * 状态与日志通过 supervisor 的控制通道实时推送；",
	"    拿不到推送时自动退化为每秒轮询。",
	"  * 窗口尺寸变化会自动重排，最小支持 80x24。",
	"",
	"按任意键返回看板。",
}

func (r *renderer) drawHelp(v *view, writeRow func(int, string)) {
	// 半透明感：帮助浮层直接铺满窗口，保持简洁
	rows := v.height
	start := maxInt(0, (rows-len(helpText))/2)
	for i := 0; i < rows; i++ {
		lw := newLineWriter(v.width)
		idx := i - start
		if idx >= 0 && idx < len(helpText) {
			line := helpText[idx]
			sty := styPlain
			switch {
			case idx == 0:
				sty = styTitle
			case strings.HasSuffix(strings.TrimSpace(line), "：") && !strings.HasPrefix(line, "  "):
				sty = styCyan
			case strings.HasPrefix(line, "  ") && !strings.Contains(line, "  "):
				sty = styMuted
			}
			pad := maxInt(0, (v.width-Measure(line))/2)
			lw.Text(strings.Repeat(" ", pad), styPlain)
			lw.Text(Truncate(line, maxInt(4, v.width-pad)), sty)
		}
		writeRow(i, lw.String())
	}
}

// ------------------------------------------------------------------ 配置模式

// cursorPos 描述终端光标应该停在哪里（配置模式编辑时用）。
type cursorPos struct {
	row int
	col int
}

// cfgRow 是配置列表里一行的绘制信息（纯逻辑，方便单测）。
type cfgRow struct {
	section string // 与上一行不同的段落名，空串表示延续
	key     string
	value   string
}

// buildCfgRows 把配置项与选中项整理成列表行。
func buildCfgRows(entries []cfgEntry, cfg *config.Config, width int) []cfgRow {
	rows := make([]cfgRow, 0, len(entries))
	lastSection := ""
	for i := range entries {
		e := &entries[i]
		section := ""
		if e.section != lastSection {
			section = e.section
			lastSection = e.section
		}
		rows = append(rows, cfgRow{
			section: section,
			key:     Truncate(e.key, maxInt(6, width/2)),
			value:   e.display(cfg, maxInt(4, width/2-4)),
		})
	}
	return rows
}

// drawConfig 绘制配置模式界面。
func (r *renderer) drawConfig(v *view, writeRow func(int, string), frame bool, cursor *cursorPos) {
	cs := v.cfg
	cfg := r.cfg

	if cs == nil {
		writeRow(0, newLineWriter(v.width).String())
		return
	}

	// 第一行：标题
	head := newLineWriter(v.width)
	head.Text(" nyatmc ", styTitle)
	head.Text("配置模式 ", stBold())
	head.Text(" "+configPathOf(cfg), styMuted)
	writeRow(0, head.String())

	// 第二行：数据目录提示
	sub := newLineWriter(v.width)
	sub.Text(" ↑/↓ 选择  Enter 编辑  Tab/[ ] 切换枚举  空格 取反布尔  Esc 返回看板  Q 退出", styMuted)
	writeRow(1, sub.String())

	// 最后两行：编辑行与消息行
	editRow := v.height - 2
	msgRow := v.height - 1
	listTop := 2
	listRows := maxInt(1, editRow-listTop)

	rows := buildCfgRows(cs.entries, cfg, v.width)
	for i := 0; i < listRows; i++ {
		idx := cs.offset + i
		lw := newLineWriter(v.width)
		if idx >= len(rows) {
			writeRow(listTop+i, lw.String())
			continue
		}
		row := rows[idx]
		selected := idx == cs.selected
		if selected {
			lw.Text(">", styCyan)
		} else {
			lw.Text(" ", styPlain)
		}
		if row.section != "" {
			lw.Text("["+row.section+"]", styMuted)
			lw.Text(" ", styPlain)
		} else {
			lw.Text(" ", styPlain)
		}
		keySty := styPlain
		if selected {
			keySty = style{fg: cCyan, bold: true}
		}
		lw.Text(Pad(row.key, maxInt(8, v.width/2)), keySty)
		valSty := styPlain
		if selected && cs.editing {
			valSty = styYellow
		}
		lw.Text("= ", styMuted)
		lw.Text(Truncate(row.value, maxInt(4, lw.Rest()-1)), valSty)
		writeRow(listTop+i, lw.String())
	}

	// 编辑行
	el := newLineWriter(v.width)
	if cs.editing {
		e, _ := cs.current()
		el.Text(" 编辑 ", styYellow)
		el.Text(e.path, styMuted)
		el.Text(" = ", styMuted)
		prefixWidth := el.Used()
		buf := cs.buf
		cursorDrawing := sanitizeCell(buf) + " "
		el.Text(cursorDrawing, style{rev: true})
		el.Text("   Enter 保存  Esc 取消", styMuted)
		if cursor != nil && frame {
			cursor.row = editRow
			cursor.col = prefixWidth + cursorColOffset(buf, cs.cursor)
		}
	} else {
		e, ok := cs.current()
		if ok {
			el.Text(" 选中 ", styMuted)
			el.Text(e.path, styCyan)
			if e.kind == kindBool {
				el.Text("  （布尔：按空格 / Enter 直接切换）", styMuted)
			} else if e.kind == kindEnum {
				el.Text("  （枚举：按 Tab 或 [ ] 轮换）", styMuted)
			}
		}
	}
	writeRow(editRow, el.String())

	// 消息行
	ml := newLineWriter(v.width)
	if cs.message != "" {
		sty := styGreen
		if cs.isErr {
			sty = styRed
		}
		ml.Text(" "+Truncate(cs.message, v.width-2), sty)
	} else {
		ml.Text(" 提示：保存时先读盘、只改这一项，再整体校验写回；运行中的守护进程会自动热重载。", styMuted)
	}
	writeRow(msgRow, ml.String())
}

// cursorColOffset 返回编辑框内光标所在的列偏移（按显示宽度计算）。
func cursorColOffset(buf string, cursor int) int {
	runes := []rune(buf)
	if cursor > len(runes) {
		cursor = len(runes)
	}
	if cursor < 0 {
		cursor = 0
	}
	return Measure(string(runes[:cursor]))
}

// ------------------------------------------------------------------ 小工具

func containsAnyFold(s string, subs ...string) bool {
	low := strings.ToLower(s)
	for _, sub := range subs {
		if strings.Contains(low, strings.ToLower(sub)) {
			return true
		}
	}
	return false
}

// humanDuration 输出中文友好的时长。
func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	d = d.Round(time.Second)
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	secs := int(d.Seconds()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%d天%d小时", days, hours)
	case hours > 0:
		return fmt.Sprintf("%d小时%d分", hours, mins)
	case mins > 0:
		return fmt.Sprintf("%d分%d秒", mins, secs)
	default:
		return fmt.Sprintf("%d秒", secs)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

package tui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yuzumikonami/nyatmc/internal/config"
	"github.com/yuzumikonami/nyatmc/internal/daemon"
	"github.com/yuzumikonami/nyatmc/internal/instance"
)

// TestMain 把数据目录指到临时目录，保证测试绝不碰用户真实的 ~/.nyatmc。
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "nyatmc-tui-test-")
	if err == nil {
		_ = os.Setenv("NYATMC_HOME", dir)
		defer os.RemoveAll(dir)
	}
	os.Exit(m.Run())
}

// ------------------------------------------------------------------ 显示宽度与裁剪

func TestMeasure(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"运行中", 6},                                  // 中文按 2 列
		{"TPS 未知", 3 + 1 + 4},                       // 混排
		{"\x1b[32m运行中\x1b[0m", 6},                  // 忽略 ANSI
		{"a\tb", 2},                                  // 制表符不占位
		{"\x1b[38;5;196m红\x1b[0m", 2},               // 256 色序列
		{"\x1b]0;标题\x07结束", 4},                     // OSC 序列
		{"✓ 完成", 1 + 1 + 4},                        // 符号 + 空格 + 中文
		{"\u200b宽", 2},                               // 零宽字符
	}
	for _, c := range cases {
		if got := Measure(c.in); got != c.want {
			t.Errorf("Measure(%q) = %d，期望 %d", c.in, got, c.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in    string
		width int
		want  string
	}{
		{"abcdef", 10, "abcdef"},
		{"abcdef", 6, "abcdef"},
		{"abcdef", 5, "abcd…"},
		{"abcdef", 4, "abc…"},
		{"运行中", 6, "运行中"},
		{"运行中", 5, "运行"}, // 2+2 正好占满，放不下省略号
		{"运行中", 4, "运…"},
		{"运行中", 3, "运…"},
		{"运行中", 2, "…"},
		{"运行中", 1, "…"},
		{"运行中", 0, ""},
		{"ab", 2, "ab"},
		{"ab", 1, "…"},
	}
	for _, c := range cases {
		if got := Truncate(c.in, c.width); got != c.want {
			t.Errorf("Truncate(%q, %d) = %q，期望 %q", c.in, c.width, got, c.want)
		}
		if w := Measure(Truncate(c.in, c.width)); w > c.width {
			t.Errorf("Truncate(%q, %d) 结果宽度 %d 超过上限", c.in, c.width, w)
		}
	}
}

func TestPadAndTail(t *testing.T) {
	if got := Pad("abc", 5); got != "abc  " {
		t.Errorf("Pad = %q", got)
	}
	if got := Pad("abcd", 3); got != "abcd" {
		t.Errorf("Pad 不应截断，得到 %q", got)
	}
	if got := Pad("运行", 5); got != "运行 " {
		t.Errorf("Pad 中文 = %q", got)
	}
	if got := PadLeft("ab", 4); got != "  ab" {
		t.Errorf("PadLeft = %q", got)
	}
	if got := Tail("0123456789", 4); got != "6789" {
		t.Errorf("Tail = %q", got)
	}
	if got := Tail("一二三四五", 5); got != "四五" {
		t.Errorf("Tail 中文 = %q", got)
	}
	if got := Tail("短", 5); got != "短" {
		t.Errorf("Tail 短串 = %q", got)
	}
}

func TestSanitizeAndStripANSI(t *testing.T) {
	if got := StripANSI("\x1b[31m红\x1b[0m"); got != "红" {
		t.Errorf("StripANSI = %q", got)
	}
	if got := Sanitize("a\x00b\x1b[32mc"); got != "abc" {
		t.Errorf("Sanitize = %q", got)
	}
	if got := Sanitize("一\t二\n三"); got != "一二三" {
		t.Errorf("Sanitize 控制字符 = %q", got)
	}
}

// ------------------------------------------------------------------ 日志环形缓冲

func TestLogRingWrap(t *testing.T) {
	r := NewLogRing(3)
	if r.Len() != 0 || r.Cap() != 3 {
		t.Fatalf("初始状态错误：len=%d cap=%d", r.Len(), r.Cap())
	}
	r.AppendAll([]string{"a", "b", "c"})
	if got := strings.Join(r.Window(3, 0), ","); got != "a,b,c" {
		t.Errorf("Window = %q", got)
	}
	r.Append("d")
	if got := strings.Join(r.Window(3, 0), ","); got != "b,c,d" {
		t.Errorf("覆盖后 Window = %q", got)
	}
	if got := r.At(0); got != "b" {
		t.Errorf("At(0) = %q，期望 b", got)
	}
	if got := strings.Join(r.Window(2, 1), ","); got != "b,c" {
		t.Errorf("带偏移 Window = %q", got)
	}
	if got := r.Window(99, 0); len(got) != 3 {
		t.Errorf("超量请求应裁剪，得到 %d 行", len(got))
	}
	r.Reset()
	if r.Len() != 0 {
		t.Errorf("Reset 后应清空")
	}
}

func TestLogRingManyAppends(t *testing.T) {
	r := NewLogRing(5)
	for i := 0; i < 100; i++ {
		r.Append("line-" + strconv.Itoa(i))
	}
	if r.Len() != 5 {
		t.Fatalf("Len = %d，期望 5", r.Len())
	}
	got := r.Window(5, 0)
	want := []string{"line-95", "line-96", "line-97", "line-98", "line-99"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Window[%d] = %q，期望 %q", i, got[i], want[i])
		}
	}
}

func TestScrolling(t *testing.T) {
	cases := []struct {
		offset, delta, lines, want int
	}{
		{0, 1, 100, 1},
		{0, -1, 100, 0},
		{5, -5, 100, 0},
		{5, -6, 100, 0},
		{90, 20, 100, 99},
		{0, 0, 0, 0},
		{3, 1, 0, 0},
	}
	for _, c := range cases {
		if got := Scroll(c.offset, c.delta, c.lines); got != c.want {
			t.Errorf("Scroll(%d, %d, %d) = %d，期望 %d", c.offset, c.delta, c.lines, got, c.want)
		}
	}
	if got := ScrollHome(100); got != 99 {
		t.Errorf("ScrollHome(100) = %d", got)
	}
	if got := ScrollHome(0); got != 0 {
		t.Errorf("ScrollHome(0) = %d", got)
	}
}

// ------------------------------------------------------------------ 按键解析

func keysOf(t *testing.T, in string) []Key {
	t.Helper()
	p := &keyParser{}
	out := []Key{}
	for {
		k, ok := p.Next([]byte(in), false)
		if !ok {
			break
		}
		out = append(out, k)
		in = ""
	}
	return out
}

func TestParseKeys(t *testing.T) {
	cases := []struct {
		in   string
		want KeyKind
		r    rune
	}{
		{"s", KeyRune, 's'},
		{"S", KeyRune, 'S'},
		{"?", KeyRune, '?'},
		{"中", KeyRune, '中'},
		{"\r", KeyEnter, 0},
		{"\n", KeyEnter, 0},
		{"\t", KeyTab, 0},
		{"\x7f", KeyBack, 0},
		{"\x08", KeyBack, 0},
		{"\x03", KeyCtrlC, 0},
		{"\x0c", KeyCtrlL, 0},
		{"\x1b[A", KeyUp, 0},
		{"\x1b[B", KeyDown, 0},
		{"\x1b[C", KeyRight, 0},
		{"\x1b[D", KeyLeft, 0},
		{"\x1b[5~", KeyPgUp, 0},
		{"\x1b[6~", KeyPgDn, 0},
		{"\x1b[H", KeyHome, 0},
		{"\x1b[F", KeyEnd, 0},
		{"\x1b[1~", KeyHome, 0},
		{"\x1b[4~", KeyEnd, 0},
		{"\x1b[3~", KeyDelete, 0},
		{"\x1b[Z", KeyBacktab, 0},
		{"\x1bOA", KeyUp, 0},
		{"\x1b[1;5A", KeyUp, 0}, // 带修饰键
	}
	for _, c := range cases {
		got := keysOf(t, c.in)
		if len(got) != 1 {
			t.Errorf("parseKey(%q) 得到 %d 个按键，期望 1 个", c.in, len(got))
			continue
		}
		if got[0].Kind != c.want {
			t.Errorf("parseKey(%q) = %v，期望 %v", c.in, got[0].Kind, c.want)
		}
		if c.r != 0 && got[0].Rune != c.r {
			t.Errorf("parseKey(%q) 字符 = %q，期望 %q", c.in, got[0].Rune, c.r)
		}
	}
}

func TestParseKeysMultiple(t *testing.T) {
	keys := keysOf(t, "se\x1b[BQ")
	want := []KeyKind{KeyRune, KeyRune, KeyDown, KeyRune}
	if len(keys) != len(want) {
		t.Fatalf("解析出 %d 个按键，期望 %d 个", len(keys), len(want))
	}
	for i := range want {
		if keys[i].Kind != want[i] {
			t.Errorf("第 %d 个按键 = %v，期望 %v", i, keys[i].Kind, want[i])
		}
	}
}

func TestParseKeysSplitEscape(t *testing.T) {
	// 转义序列被拆成多个读包时不能误判
	p := &keyParser{}
	if k, ok := p.Next([]byte("\x1b"), false); ok {
		t.Fatalf("不完整的转义序列不应产出按键，得到 %v", k)
	}
	if k, ok := p.Next([]byte("[B"), false); !ok || k.Kind != KeyDown {
		t.Fatalf("补齐后应为 Down，得到 %v", k)
	}

	// 用户单独按 Esc：等一小段时间后由主循环用 atEOF 把它冲刷出来
	p2 := &keyParser{}
	if _, ok := p2.Next([]byte("\x1b"), false); ok {
		t.Fatalf("流式状态下单独的 Esc 会等待后续字节")
	}
	if k, ok := p2.Next(nil, true); !ok || k.Kind != KeyEsc {
		t.Fatalf("超时冲刷后应得到 Esc，得到 %v ok=%v", k, ok)
	}
}

// ------------------------------------------------------------------ 配置模式

func TestValidateCfgValue(t *testing.T) {
	good := []struct {
		kind cfgKind
		text string
	}{
		{kindBool, "true"},
		{kindBool, "false"},
		{kindInt, "42"},
		{kindInt, "-3"},
		{kindPort, "25565"},
		{kindPort, "1"},
		{kindPort, "65535"},
		{kindMemory, "2G"},
		{kindMemory, "512M"},
		{kindMemory, "1g"},
		{kindDur, "30s"},
		{kindDur, "1h30m"},
		{kindDur, "2d"},
		{kindSize, "20MB"},
		{kindSize, "1GiB"},
		{kindText, "随便什么"},
	}
	for _, c := range good {
		if err := validateCfgValue(c.kind, c.text); err != nil {
			t.Errorf("validateCfgValue(%v, %q) 报错：%v", c.kind, c.text, err)
		}
	}

	bad := []struct {
		kind cfgKind
		text string
	}{
		{kindBool, "yes"},
		{kindBool, "1"},
		{kindBool, "TRUE "},
		{kindInt, "abc"},
		{kindPort, "0"},
		{kindPort, "65536"},
		{kindPort, "80s"},
		{kindMemory, "2GB"},
		{kindMemory, "G"},
		{kindMemory, ""},
		{kindDur, "10 分钟"},
		{kindSize, "20 XB"},
	}
	for _, c := range bad {
		if err := validateCfgValue(c.kind, c.text); err == nil {
			t.Errorf("validateCfgValue(%v, %q) 应该报错但没有", c.kind, c.text)
		}
	}

	// 枚举只在给了 options 时校验
	if err := validateCfgValueWith(kindEnum, []string{"paper", "fabric"}, "spigot"); err == nil {
		t.Errorf("枚举非法值应该报错")
	}
	if err := validateCfgValueWith(kindEnum, []string{"paper", "fabric"}, "fabric"); err != nil {
		t.Errorf("枚举合法值不应报错：%v", err)
	}
}

func TestConfigEntriesCoverage(t *testing.T) {
	cfg := config.Default()
	res := cfg.Resolve("default")
	entries := buildConfigEntries(res)
	sections := map[string]int{}
	for i := range entries {
		e := &entries[i]
		sections[e.section]++
		if e.path == "" || e.key == "" {
			t.Errorf("配置项缺少 path/key：%+v", e)
		}
	}
	for _, want := range []string{"general", "server", "daemon", "backup", "log", "web", "scheduler", "downloader", "mods"} {
		if sections[want] == 0 {
			t.Errorf("配置项没有覆盖 %s.* 段落", want)
		}
	}
	// 每一项都应该能读出一个值（不能 panic，且路径可解析）
	for i := range entries {
		e := &entries[i]
		_ = e.value(cfg)
	}
}

func TestConfigSaveOnlyChangesEditedField(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NYATMC_HOME", dir)
	path := filepath.Join(dir, "config.toml")
	if err := config.WriteDefault(path); err != nil {
		t.Fatalf("写出默认配置失败：%v", err)
	}

	// 模拟用户手工改了一项，界面保存时不能把它覆盖掉
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("加载配置失败：%v", err)
	}
	cfg.Server.MOTD = "手工改过的欢迎语"
	if err := cfg.Save(); err != nil {
		t.Fatalf("保存配置失败：%v", err)
	}

	cs := newConfigState(cfg.Resolve("default"), cfg)
	var target cfgEntry
	for i := range cs.entries {
		if cs.entries[i].path == "server.port" {
			target = cs.entries[i]
		}
	}
	if target.path == "" {
		t.Fatal("找不到 server.port 配置项")
	}
	if err := cs.saveAndReload(path, target, "25599"); err != nil {
		t.Fatalf("保存 server.port 失败：%v", err)
	}
	if cs.isErr {
		t.Fatalf("保存后不应标记为错误：%s", cs.message)
	}
	if !strings.Contains(cs.message, "已保存") {
		t.Errorf("保存提示不符：%q", cs.message)
	}

	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("重新加载失败：%v", err)
	}
	if reloaded.Server.Port != 25599 {
		t.Errorf("端口没有写入：%d", reloaded.Server.Port)
	}
	if reloaded.Server.MOTD != "手工改过的欢迎语" {
		t.Errorf("保存时覆盖了用户手工修改的字段：%q", reloaded.Server.MOTD)
	}
	// 保存会写出 .bak 备份
	if _, err := config.Load(path + ".bak"); err != nil {
		t.Errorf("应该存在 .bak 备份：%v", err)
	}
}

func TestConfigSaveRejectsInvalidValue(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NYATMC_HOME", dir)
	path := filepath.Join(dir, "config.toml")
	if err := config.WriteDefault(path); err != nil {
		t.Fatalf("写出默认配置失败：%v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("加载配置失败：%v", err)
	}
	before, err := config.Load(path)
	if err != nil {
		t.Fatalf("加载配置失败：%v", err)
	}

	cs := newConfigState(cfg.Resolve("default"), cfg)
	var target cfgEntry
	for i := range cs.entries {
		if cs.entries[i].path == "server.port" {
			target = cs.entries[i]
		}
	}
	if err := cs.saveAndReload(path, target, "70000"); err == nil {
		t.Fatal("非法端口应该被拒绝")
	}
	after, err := config.Load(path)
	if err != nil {
		t.Fatalf("加载配置失败：%v", err)
	}
	if after.Server.Port != before.Server.Port {
		t.Errorf("非法输入不该写盘：%d -> %d", before.Server.Port, after.Server.Port)
	}
}

func TestConfigEditorBuffer(t *testing.T) {
	cs := &configState{}
	cs.insertRune('a', 10)
	cs.insertRune('中', 10)
	cs.insertRune('c', 10)
	if cs.buf != "a中c" {
		t.Fatalf("插入后 buf = %q", cs.buf)
	}
	if cs.cursor != 3 {
		t.Fatalf("游标 = %d，期望 3", cs.cursor)
	}
	cs.moveCursor(-1)
	cs.backspace()
	if cs.buf != "ac" {
		t.Errorf("退格后 buf = %q，期望 ac", cs.buf)
	}
	cs.deleteForward()
	if cs.buf != "a" {
		t.Errorf("删除后 buf = %q，期望 a", cs.buf)
	}
	if got := cursorColOffset("a中", 2); got != 3 {
		t.Errorf("cursorColOffset = %d，期望 3（中文按 2 列）", got)
	}
}

func TestConfigToggleBoolUsesPtrSemantics(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NYATMC_HOME", dir)
	path := filepath.Join(dir, "config.toml")
	if err := config.WriteDefault(path); err != nil {
		t.Fatalf("写出默认配置失败：%v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("加载配置失败：%v", err)
	}

	cs := newConfigState(cfg.Resolve("default"), cfg)
	idx := -1
	for i := range cs.entries {
		if cs.entries[i].path == "daemon.watchdog" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("找不到 daemon.watchdog 配置项")
	}
	cs.selected = idx
	cs.toggle(cfg)
	if cs.isErr {
		t.Fatalf("取反看门狗失败：%s", cs.message)
	}

	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("重新加载失败：%v", err)
	}
	if reloaded.Daemon.Watchdog == nil {
		t.Fatal("看门狗应该被写成显式的 *bool，而不是 nil")
	}
	if *reloaded.Daemon.Watchdog {
		t.Errorf("默认 true 取反后应为 false，得到 true")
	}
	// 显式的 false 必须能被访问器读出来（不能被默认值覆盖）
	if reloaded.Daemon.WatchdogEnabled() {
		t.Errorf("WatchdogEnabled() 应为 false")
	}
}

// ------------------------------------------------------------------ 渲染布局

// testRenderer 构造一个不依赖真实终端的 view。
func testView(width, height int, logs []string) *view {
	cfg := config.Default()
	inst, err := instance.New(cfg, "default")
	if err != nil {
		panic(err)
	}
	ring := NewLogRing(200)
	ring.AppendAll(logs)
	return &view{
		mode:      modeDashboard,
		width:     width,
		height:    height,
		inst:      inst,
		status:    testStatus(),
		logs:      ring,
		instances: []string{"default"},
		instIndex: 0,
	}
}

func testStatus() daemon.Status {
	return daemon.Status{
		Instance:      "default",
		State:         daemon.StateRunning,
		Ready:         true,
		PID:           12345,
		SupervisorPID: 12300,
		StartedAt:     time.Now().Add(-3 * time.Hour),
		UptimeSeconds: 3*3600 + 25*60,
		Restarts:      2,
		Players:       []string{"Nya", "Steve"},
		MaxPlayers:    20,
		TPS:           19.94,
		TPSKnown:      true,
		Version:       "1.21.4",
		ServerType:    "paper",
		MemoryLimit:   "2G",
		MemoryUsedMB:  860,
		Warnings:      3,
		Errors:        1,
		Watchdog:      true,
		LastError:     "第 128 行：Unknown item 'foo'",
	}
}

func checkFrame(t *testing.T, name string, v *view) []string {
	t.Helper()
	r := &renderer{cfg: config.Default()}
	lines := r.renderLines(v)
	if len(lines) != v.height {
		t.Fatalf("%s：渲染行数 %d，期望 %d", name, len(lines), v.height)
	}
	for i, l := range lines {
		if got := Measure(l); got > v.width {
			t.Errorf("%s：第 %d 行宽度 %d 超过 %d：%q", name, i, got, v.width, StripANSI(l))
		}
	}
	return lines
}

func TestRenderDashboardSizes(t *testing.T) {
	logs := make([]string, 0, 50)
	for i := 0; i < 50; i++ {
		logs = append(logs, "[12:00:0"+strconv.Itoa(i%10)+"] 服务端运行中 tick="+strconv.Itoa(i))
	}
	for _, size := range [][2]int{{80, 24}, {120, 40}, {60, 20}, {40, 12}, {100, 30}} {
		v := testView(size[0], size[1], logs)
		lines := checkFrame(t, "dashboard", v)
		joined := StripANSI(strings.Join(lines, "\n"))
		if !strings.Contains(joined, "nyatmc") {
			t.Errorf("%dx%d：缺少品牌字样", size[0], size[1])
		}
		if !strings.Contains(joined, "运行中") && size[0] >= 60 {
			t.Errorf("%dx%d：缺少状态标签", size[0], size[1])
		}
	}
}

func TestRenderDashboardNarrowDegrades(t *testing.T) {
	v := testView(40, 12, nil)
	lines := checkFrame(t, "narrow", v)
	joined := StripANSI(strings.Join(lines, "\n"))
	// 底部必须有按键提示（窄屏用精简版）
	if !strings.Contains(joined, "Q") {
		t.Errorf("窄屏应该保留退出提示，得到：\n%s", joined)
	}
}

func TestRenderPlayerSection(t *testing.T) {
	logs := make([]string, 0, 30)
	for i := 0; i < 30; i++ {
		logs = append(logs, "日志行 "+strconv.Itoa(i))
	}
	v := testView(100, 30, logs)
	lines := checkFrame(t, "players", v)
	joined := StripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "在线玩家") {
		t.Errorf("应该显示在线玩家区：\n%s", joined)
	}
	if !strings.Contains(joined, "Nya") {
		t.Errorf("应该显示玩家名：\n%s", joined)
	}

	// 没有玩家时的提示
	v2 := testView(100, 30, logs)
	v2.status.Players = nil
	lines2 := checkFrame(t, "noplayers", v2)
	if !strings.Contains(StripANSI(strings.Join(lines2, "\n")), "暂无玩家在线") {
		t.Errorf("无玩家时应给出提示")
	}
}

func TestRenderTPSUnknownExplained(t *testing.T) {
	v := testView(120, 40, []string{"启动完成"})
	v.status.TPSKnown = false
	v.status.TPS = 0
	joined := StripANSI(strings.Join(checkFrame(t, "tps", v), "\n"))
	if !strings.Contains(joined, "TPS") || !strings.Contains(joined, "未知") {
		t.Errorf("TPS 未知时应有说明：\n%s", joined)
	}
	if !strings.Contains(joined, "/tps") {
		t.Errorf("应该提示需要服务端输出 /tps：\n%s", joined)
	}
}

func TestRenderLogModes(t *testing.T) {
	logs := []string{"a", "b", "c", "d", "e"}
	for _, mode := range []runMode{dashDefault, dashLogsOnly, dashPlayersOnly} {
		v := testView(90, 24, logs)
		v.hideLogs = mode == dashPlayersOnly
		v.hidePlayers = mode == dashLogsOnly
		lines := checkFrame(t, "mode", v)
		joined := StripANSI(strings.Join(lines, "\n"))
		switch mode {
		case dashPlayersOnly:
			if strings.Contains(joined, "日志") {
				t.Errorf("只看玩家时不应有日志区：\n%s", joined)
			}
			if !strings.Contains(joined, "在线玩家") {
				t.Errorf("只看玩家时应有玩家区")
			}
		case dashLogsOnly:
			if strings.Contains(joined, "在线玩家") {
				t.Errorf("只看日志时不应有玩家区：\n%s", joined)
			}
		default:
			if !strings.Contains(joined, "在线玩家") {
				t.Errorf("默认布局应有玩家区")
			}
		}
	}
}

func TestRenderScrollIndicator(t *testing.T) {
	logs := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		logs = append(logs, "line "+strconv.Itoa(i))
	}
	v := testView(100, 30, logs)
	v.offset = 20
	joined := StripANSI(strings.Join(checkFrame(t, "scroll", v), "\n"))
	if !strings.Contains(joined, "已向上滚动 20 行") {
		t.Errorf("滚动时应给出提示：\n%s", joined)
	}
	if !strings.Contains(joined, "已暂停跟随") {
		t.Errorf("滚动时应提示暂停跟随")
	}
}

func TestRenderToast(t *testing.T) {
	v := testView(80, 24, []string{"x"})
	v.toast = "备份完成：default-manual-20260101-040000.tar.gz（12.3MB）"
	lines := checkFrame(t, "toast", v)
	joined := StripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "备份完成") {
		t.Errorf("应该显示操作结果提示：\n%s", joined)
	}
	// 失败提示带「失败：」前缀
	v.toast = "失败：端口被占用"
	joined = StripANSI(strings.Join(checkFrame(t, "toastfail", v), "\n"))
	if !strings.Contains(joined, "失败：") {
		t.Errorf("失败提示应带前缀：\n%s", joined)
	}
}

func TestRenderHelpOverlay(t *testing.T) {
	v := testView(100, 30, nil)
	v.mode = modeHelp
	lines := checkFrame(t, "help", v)
	joined := StripANSI(strings.Join(lines, "\n"))
	for _, want := range []string{"帮助", "启动实例", "强制结束", "配置模式", "Tab", "退出看板", "/tps"} {
		if !strings.Contains(joined, want) {
			t.Errorf("帮助里缺少 %q：\n%s", want, joined)
		}
	}
}

func TestRenderConfigOverlay(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NYATMC_HOME", dir)
	cfg := config.Default()
	res := cfg.Resolve("default")
	v := testView(100, 30, nil)
	v.mode = modeConfig
	v.cfg = newConfigState(res, cfg)
	// 选中内存项（编辑行会显示它的配置路径）
	cs := v.cfg
	for i := range cs.entries {
		if cs.entries[i].path == "server.memory" {
			cs.selected = i
		}
	}
	lines := checkFrame(t, "config", v)
	joined := StripANSI(strings.Join(lines, "\n"))
	for _, want := range []string{"配置模式", "[server]", "最大内存", "server.memory", "Esc 返回看板", "选中"} {
		if !strings.Contains(joined, want) {
			t.Errorf("配置界面缺少 %q：\n%s", want, joined)
		}
	}

	// 进入编辑态
	cs.startEdit(cfg)
	lines = checkFrame(t, "config-edit", v)
	joined = StripANSI(strings.Join(lines, "\n"))
	for _, want := range []string{"编辑", "server.memory", "Enter 保存", "Esc 取消"} {
		if !strings.Contains(joined, want) {
			t.Errorf("编辑态界面缺少 %q：\n%s", want, joined)
		}
	}
}

func TestBuildCfgRows(t *testing.T) {
	cfg := config.Default()
	res := cfg.Resolve("default")
	entries := buildConfigEntries(res)
	rows := buildCfgRows(entries, cfg, 100)
	if len(rows) != len(entries) {
		t.Fatalf("行数 %d != 配置项数 %d", len(rows), len(entries))
	}
	// 段落名只出现在该段第一行
	seen := map[string]bool{}
	for _, r := range rows {
		if r.section == "" {
			continue
		}
		if seen[r.section] {
			t.Errorf("段落 %s 重复出现", r.section)
		}
		seen[r.section] = true
	}
	// 行宽不能超过给定宽度（相对宽松的上限）
	for _, r := range rows {
		if Measure(r.key) > 50 || Measure(r.value) > 50 {
			t.Errorf("行内容过宽：%+v", r)
		}
	}
}

func TestParseMemoryMB(t *testing.T) {
	cases := map[string]int64{
		"2G":   2048,
		"512M": 512,
		"1T":   1024 * 1024,
		"":     0,
		"abc":  0,
	}
	for in, want := range cases {
		if got := parseMemoryMB(in); got != want {
			t.Errorf("parseMemoryMB(%q) = %d，期望 %d", in, got, want)
		}
	}
}

func TestHumanDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "-"},
		{45 * time.Second, "45秒"},
		{90 * time.Second, "1分30秒"},
		{3 * time.Hour, "3小时0分"},
		{50 * time.Hour, "2天2小时"},
	}
	for _, c := range cases {
		if got := humanDuration(c.in); got != c.want {
			t.Errorf("humanDuration(%v) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestWrapList(t *testing.T) {
	got := wrapList([]string{"a", "b", "c"}, 100)
	if len(got) != 1 || got[0] != "a、b、c" {
		t.Errorf("wrapList = %v", got)
	}
	got = wrapList([]string{"aaaa", "bbbb", "cccc"}, 8)
	if len(got) < 2 {
		t.Errorf("窄宽度应折行，得到 %v", got)
	}
	for _, l := range got {
		if Measure(l) > 8 {
			t.Errorf("折行后仍超宽：%q", l)
		}
	}
}

func TestPickHintsDegrades(t *testing.T) {
	full := pickHints(200)
	if len(full) < 8 {
		t.Errorf("宽屏应给出完整提示，得到 %d 个", len(full))
	}
	narrow := pickHints(30)
	if hintsWidth(narrow) > 30 {
		t.Errorf("窄屏提示仍超宽：%d", hintsWidth(narrow))
	}
	tiny := pickHints(6)
	if len(tiny) == 0 {
		t.Errorf("极窄屏也要保留最少提示")
	}
}

func TestParseKeyEOFAndFlush(t *testing.T) {
	// EOF 时残留的不完整序列也要被消费掉，不能挂住主循环
	p := &keyParser{}
	if k, ok := p.Next([]byte("\x1b["), true); !ok {
		t.Fatalf("EOF 时残包也应产出按键，得到 %v", k)
	}
	if k, ok := p.Next(nil, true); ok {
		t.Fatalf("消费后不应再有按键，得到 %v", k)
	}

	// 流式状态下残留的序列由 Flush 吐出
	p2 := &keyParser{}
	if _, ok := p2.Next([]byte("\x1b["), false); ok {
		t.Fatal("不完整序列不应立即产出按键")
	}
	if k, ok := p2.Flush(); !ok || k.Kind != KeyUnknown {
		t.Fatalf("Flush 应吐出残留按键，得到 %v ok=%v", k, ok)
	}
	if _, ok := p2.Flush(); ok {
		t.Fatal("Flush 之后不应再残留按键")
	}
}

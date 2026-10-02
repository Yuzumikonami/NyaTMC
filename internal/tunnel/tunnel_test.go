package tunnel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuzumikonami/nyatmc/internal/config"
)

// quickTunnelOutput 是一段真实的 cloudflared 快速隧道输出（节选）。
const quickTunnelOutput = `2024-05-01T10:00:00Z INF Thank you for trying Cloudflare Tunnel. Doing so, without a Cloudflare account, is a quick way to experiment and try it out.
2024-05-01T10:00:00Z INF Requesting new quick Tunnel on trycloudflare.com...
2024-05-01T10:00:01Z INF +--------------------------------------------------------------------------------------------+
2024-05-01T10:00:01Z INF |  Your quick Tunnel has been created! Visit it at (it may take some time to be reachable):  |
2024-05-01T10:00:01Z INF |  https://random-words-here.trycloudflare.com                                               |
2024-05-01T10:00:01Z INF +--------------------------------------------------------------------------------------------+
2024-05-01T10:00:01Z INF Cannot determine default configuration path. No file [config.yml config.yaml] in [~/.cloudflared ~/.cloudflare-warp ~/cloudflare-warp /etc/cloudflared /usr/local/etc/cloudflared]
2024-05-01T10:00:01Z INF Version 2024.4.1
2024-05-01T10:00:01Z INF GOOS: linux, GOVersion: go1.22.2, GoArch: amd64
2024-05-01T10:00:01Z INF Settings: map[protocol:quic url:http://127.0.0.1:8080]
2024-05-01T10:00:01Z INF Generated Connector ID: 4f8a5c3b-0000-1111-2222-333344445555
2024-05-01T10:00:01Z INF Initial protocol quic
2024-05-01T10:00:01Z INF Starting metrics server on 127.0.0.1:20241/metrics
2024-05-01T10:00:02Z INF Registered tunnel connection connIndex=0 connection=abc event=0 ip=198.41.200.13 location=hkg01 protocol=quic
`

// useTempHome 把 nyatmc 数据目录指到临时目录，避免污染真实 ~/.nyatmc。
func useTempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	config.SetHome(home)
	return home
}

func TestExtractURLFromRealOutput(t *testing.T) {
	got := ExtractURL(quickTunnelOutput)
	want := "https://random-words-here.trycloudflare.com"
	if got != want {
		t.Fatalf("提取公网地址失败：得到 %q，期望 %q", got, want)
	}
}

func TestExtractURLEdgeCases(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"单行", "INF |  https://a-b-c.trycloudflare.com  |", "https://a-b-c.trycloudflare.com"},
		{"无地址", "INF Starting metrics server on 127.0.0.1:20241/metrics", ""},
		{"非 trycloudflare", "https://mc.example.com", ""},
		{"空字符串", "", ""},
		{"多个只取第一个", "https://one.trycloudflare.com https://two.trycloudflare.com", "https://one.trycloudflare.com"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ExtractURL(c.in); got != c.want {
				t.Fatalf("ExtractURL(%q) = %q，期望 %q", c.in, got, c.want)
			}
		})
	}
}

func TestResolveTarget(t *testing.T) {
	cases := []struct {
		name    string
		opts    Options
		want    string
		wantErr string
	}{
		{"按端口", Options{Port: 8080}, "http://127.0.0.1:8080", ""},
		{"补全协议", Options{Target: "127.0.0.1:9000"}, "http://127.0.0.1:9000", ""},
		{"原样保留", Options{Target: "https://mc.example.com"}, "https://mc.example.com", ""},
		{"Target 优先于 Port", Options{Target: "http://127.0.0.1:1234", Port: 8080}, "http://127.0.0.1:1234", ""},
		{"缺目标", Options{}, "", "缺少要暴露的目标"},
		{"端口越界", Options{Port: 70000}, "", "非法"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.opts.resolveTarget()
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("期望错误包含 %q，实际 err=%v", c.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("意外错误：%v", err)
			}
			if got != c.want {
				t.Fatalf("resolveTarget() = %q，期望 %q", got, c.want)
			}
		})
	}
}

func TestCommandArgs(t *testing.T) {
	quick := Options{Protocol: "http2"}
	got := quick.commandArgs("http://127.0.0.1:8080", ModeQuick)
	want := []string{"tunnel", "--no-autoupdate", "--url", "http://127.0.0.1:8080", "--protocol", "http2"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("快速隧道参数 = %v，期望 %v", got, want)
	}

	named := Options{Token: "tok", ExtraArgs: []string{"--logfile", "", "extra"}}
	got = named.commandArgs("http://127.0.0.1:8080", ModeNamed)
	want = []string{"tunnel", "--no-autoupdate", "run", "--token", "tok", "--logfile", "extra"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("命名隧道参数 = %v，期望 %v", got, want)
	}

	if p := (Options{}).protocol(); p != DefaultProtocol {
		t.Fatalf("默认协议 = %q，期望 %q", p, DefaultProtocol)
	}
}

func TestInitialURL(t *testing.T) {
	if got := (Options{Hostname: "mc.example.com"}).initialURL(ModeNamed); got != "https://mc.example.com" {
		t.Fatalf("命名隧道地址 = %q", got)
	}
	if got := (Options{Hostname: "http://mc.example.com"}).initialURL(ModeNamed); got != "http://mc.example.com" {
		t.Fatalf("带协议的域名 = %q", got)
	}
	if got := (Options{Hostname: "mc.example.com"}).initialURL(ModeQuick); got != "" {
		t.Fatalf("快速隧道不该有初始地址，得到 %q", got)
	}
}

func TestTailLines(t *testing.T) {
	home := useTempHome(t)
	path := filepath.Join(home, "tunnel.log")
	var b strings.Builder
	for i := 1; i <= 300; i++ {
		b.WriteString("line-")
		b.WriteString(strings.Repeat("x", i%5))
		b.WriteString("-")
		b.WriteString(itoa(i))
		b.WriteString("\r\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	all := tailLines(path, maxLogLines)
	if len(all) != maxLogLines {
		t.Fatalf("环形缓冲应保留 %d 行，实际 %d 行", maxLogLines, len(all))
	}
	if !strings.HasSuffix(all[len(all)-1], "-300") {
		t.Fatalf("最后一行应是 line-300，实际 %q", all[len(all)-1])
	}
	if strings.Contains(all[0], "\r") {
		t.Fatalf("行尾的 \\r 应被清理：%q", all[0])
	}

	few := tailLines(path, 3)
	if len(few) != 3 || !strings.HasSuffix(few[2], "-300") {
		t.Fatalf("取最后 3 行失败：%v", few)
	}

	if got := tailLines(filepath.Join(home, "不存在.log"), 5); got != nil {
		t.Fatalf("不存在的文件应返回 nil，实际 %v", got)
	}
}

func TestStateFileRoundTrip(t *testing.T) {
	useTempHome(t)
	want := State{
		Running:   true,
		URL:       "https://abc.trycloudflare.com",
		PID:       4242,
		StartedAt: time.Now().Round(time.Second),
		Target:    "http://127.0.0.1:8080",
		Mode:      ModeQuick,
		Protocol:  "quic",
		BinPath:   "/usr/local/bin/cloudflared",
		LogPath:   LogPath(),
	}
	if err := writeState(want); err != nil {
		t.Fatalf("写入状态失败：%v", err)
	}
	if _, err := os.Stat(StatePath()); err != nil {
		t.Fatalf("状态文件不存在：%v", err)
	}
	got, err := loadState()
	if err != nil {
		t.Fatalf("读取状态失败：%v", err)
	}
	if got.URL != want.URL || got.PID != want.PID || got.Target != want.Target || got.Mode != want.Mode {
		t.Fatalf("状态往返不一致：%+v", got)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt 应被自动填充")
	}

	if err := writeStoppedState(want.Target, "手动停止"); err != nil {
		t.Fatalf("写入停止状态失败：%v", err)
	}
	got, _ = loadState()
	if got.Running || got.PID != 0 || got.Target != want.Target {
		t.Fatalf("停止状态不正确：%+v", got)
	}

	if err := ClearState(); err != nil {
		t.Fatalf("清理状态失败：%v", err)
	}
	if _, err := loadState(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("清理后应返回 ErrNotExist，实际 %v", err)
	}
}

func TestStatusFromState(t *testing.T) {
	useTempHome(t)

	if _, ok := StatusFromState(); ok {
		t.Fatal("没有状态文件时不应返回状态")
	}
	if _, ok := Current(); ok {
		t.Fatal("没有状态文件时 Current 不应返回状态")
	}
	if got := Running(); len(got) != 0 {
		t.Fatalf("没有隧道运行时 Running() 应为空，实际 %v", got)
	}

	// 进程不存在的“运行中”记录：应当被判定为已退出。
	dead := State{
		Running:   true,
		PID:       4111111,
		StartedAt: time.Now().Add(-time.Minute),
		Target:    "http://127.0.0.1:8080",
		Mode:      ModeQuick,
		LogPath:   LogPath(),
	}
	if err := writeState(dead); err != nil {
		t.Fatal(err)
	}
	st, ok := StatusFromState()
	if !ok {
		t.Fatal("应能从状态文件读回状态")
	}
	if st.Running {
		t.Fatalf("pid %d 不存在，不应判定为运行中", dead.PID)
	}
	if !strings.Contains(st.LastError, "已退出") {
		t.Fatalf("应给出“已退出”提示，实际 %q", st.LastError)
	}
	if got := Running(); len(got) != 0 {
		t.Fatalf("进程已退出时 Running() 应为空，实际 %v", got)
	}
}

func TestStatusFromStateReadsLogTail(t *testing.T) {
	useTempHome(t)
	logPath := LogPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte(quickTunnelOutput), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeState(State{
		Running:   true,
		PID:       os.Getpid(), // 用当前进程伪造“活着”，只需验证状态解析
		StartedAt: time.Now().Add(-2 * time.Second),
		Target:    "http://127.0.0.1:8080",
		Mode:      ModeQuick,
		LogPath:   logPath,
	}); err != nil {
		t.Fatal(err)
	}

	st, ok := StatusFromState()
	if !ok {
		t.Fatal("应能读到状态")
	}
	if !st.Running {
		t.Fatal("pid 为当前进程，应判定为运行中")
	}
	if st.URL != "https://random-words-here.trycloudflare.com" {
		t.Fatalf("应从日志里解析出公网地址，实际 %q", st.URL)
	}
	if len(st.LogLines) == 0 {
		t.Fatal("应返回最近日志")
	}
	if st.UptimeSeconds < 1 {
		t.Fatalf("运行时长应大于 1 秒，实际 %d", st.UptimeSeconds)
	}
	if list := Running(); len(list) != 1 || !list[0].Running {
		t.Fatalf("Running() 应返回 1 条运行中的隧道，实际 %v", list)
	}
}

func TestFindBinaryNotFound(t *testing.T) {
	useTempHome(t)
	t.Setenv("PATH", "")

	got, err := FindBinary("绝对不存在的路径/cloudflared")
	if err == nil {
		t.Fatalf("应返回错误，实际找到 %q", got)
	}
	if !strings.Contains(err.Error(), "找不到 cloudflared") {
		t.Fatalf("错误信息应为中文提示，实际 %q", err.Error())
	}
	if !strings.Contains(err.Error(), "tunnel install") {
		t.Fatalf("错误信息应提示 tunnel install，实际 %q", err.Error())
	}
}

func TestFindBinaryPrefersExplicitPath(t *testing.T) {
	home := useTempHome(t)
	dir := filepath.Join(home, "custom")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, localBinaryName())
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := FindBinary(bin)
	if err != nil {
		t.Fatalf("应能找到显式指定的 cloudflared：%v", err)
	}
	if !strings.EqualFold(got, bin) {
		t.Fatalf("FindBinary() = %q，期望 %q", got, bin)
	}
}

func TestKillGracefulOnDeadPID(t *testing.T) {
	if err := killGraceful(0); err != nil {
		t.Fatalf("pid 0 应直接返回 nil：%v", err)
	}
	if err := killGraceful(4111111); err != nil {
		t.Fatalf("不存在的 pid 应直接返回 nil：%v", err)
	}
}

func TestStopWithoutPID(t *testing.T) {
	useTempHome(t)
	tn := &Tunnel{target: "http://127.0.0.1:8080"}
	if err := tn.Stop(); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("未运行的隧道应返回 ErrNotRunning，实际 %v", err)
	}
	if err := StopAll(); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("没有隧道时 StopAll 应返回 ErrNotRunning，实际 %v", err)
	}
}

// itoa 避免在测试里引入 strconv 之外的依赖，保持用例可读。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

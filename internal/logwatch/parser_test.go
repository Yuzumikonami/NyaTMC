package logwatch

import (
	"strings"
	"testing"
)

const sampleLog = `[12:00:00] [main/INFO]: Starting minecraft server version 1.21.4
[12:00:01] [Server thread/INFO]: Preparing level "world"
[12:00:05] [Server thread/INFO]: Done (4.321s)! For help, type "help"
[12:01:00] [Server thread/INFO]: Steve joined the game
[12:01:30] [Server thread/INFO]: Alex[/127.0.0.1:51234] logged in with entity id 123 at (0.0, 64.0, 0.0)
[12:02:00] [Server thread/INFO]: TPS from last 1m, 5m, 15m: 19.98, 20.0, 20.0
[12:03:00] [Server thread/WARN]: Can't keep up! Is the server overloaded? Running 5000ms or 100 ticks behind
[12:04:00] [Server thread/INFO]: Steve left the game
[12:05:00] [Server thread/INFO]: There are 1 of a max of 20 players online: Alex
[12:06:00] [Server thread/ERROR]: Something exploded
`

func TestParserBasics(t *testing.T) {
	p := New()
	for _, line := range strings.Split(strings.TrimRight(sampleLog, "\n"), "\n") {
		p.Feed(line)
	}
	s := p.Snapshot()

	if s.Version != "1.21.4" {
		t.Errorf("版本 = %q，期望 1.21.4", s.Version)
	}
	if !s.Ready {
		t.Error("应识别出服务端已就绪")
	}
	if !s.TPSKnown || s.TPS != 19.98 {
		t.Errorf("TPS = %v（known=%v），期望 19.98", s.TPS, s.TPSKnown)
	}
	if s.TickBehindMS != 5000 {
		t.Errorf("落后毫秒 = %d，期望 5000", s.TickBehindMS)
	}
	if s.MaxPlayers != 20 {
		t.Errorf("最大人数 = %d，期望 20", s.MaxPlayers)
	}
	// list 输出是权威信息：Steve 已离开，只应剩 Alex
	if len(s.Players) != 1 || s.Players[0] != "Alex" {
		t.Errorf("在线玩家 = %v，期望 [Alex]", s.Players)
	}
	if s.Errors == 0 {
		t.Error("应统计到错误条数")
	}
}

func TestParserPlayerJoinLeave(t *testing.T) {
	p := New()
	p.Feed("[12:00:00] [Server thread/INFO]: Done (1.0s)! For help, type \"help\"")
	p.Feed("[12:00:01] [Server thread/INFO]: Steve joined the game")
	p.Feed("[12:00:02] [Server thread/INFO]: Alex joined the game")
	if got := p.Snapshot().Players; len(got) != 2 || got[0] != "Steve" || got[1] != "Alex" {
		t.Fatalf("在线玩家 = %v，期望 [Steve Alex]", got)
	}
	p.Feed("[12:00:03] [Server thread/INFO]: Steve lost connection: Disconnected")
	if got := p.Snapshot().Players; len(got) != 1 || got[0] != "Alex" {
		t.Fatalf("在线玩家 = %v，期望 [Alex]", got)
	}
	// 重复加入不应重复计数
	p.Feed("[12:00:04] [Server thread/INFO]: Alex joined the game")
	if got := p.Snapshot().Players; len(got) != 1 {
		t.Fatalf("重复加入被计成 %v", got)
	}
}

func TestParserEULAAndBindHints(t *testing.T) {
	p := New()
	p.Feed("[12:00:00] [main/ERROR]: You need to agree to the EULA in order to run the server")
	if !strings.Contains(p.Snapshot().LastError, "EULA") {
		t.Errorf("未识别 EULA 提示：%q", p.Snapshot().LastError)
	}

	p2 := New()
	p2.Feed("[12:00:00] [Server thread/ERROR]: **** FAILED TO BIND TO PORT!")
	if !strings.Contains(p2.Snapshot().LastError, "端口") {
		t.Errorf("未识别绑定失败：%q", p2.Snapshot().LastError)
	}
}

func TestParserReset(t *testing.T) {
	p := New()
	p.Feed("[12:00:00] [Server thread/INFO]: Steve joined the game")
	p.Feed("[12:00:01] [Server thread/INFO]: Done (1.0s)! For help, type \"help\"")
	p.Reset()
	s := p.Snapshot()
	if s.Ready || len(s.Players) != 0 {
		t.Errorf("Reset 之后应清空：ready=%v players=%v", s.Ready, s.Players)
	}
}

func TestTailLines(t *testing.T) {
	text := "a\nb\nc\nd\n"
	got := TailLines(text, 2)
	if len(got) != 2 || got[0] != "c" || got[1] != "d" {
		t.Errorf("TailLines = %v，期望 [c d]", got)
	}
	if got := TailLines(text, 100); len(got) != 4 {
		t.Errorf("行数不足时应全返回，得到 %v", got)
	}
	if got := TailLines(text, 0); got != nil {
		t.Errorf("n=0 应返回 nil，得到 %v", got)
	}
}

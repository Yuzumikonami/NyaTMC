// Package logwatch 解析 Minecraft 服务端控制台输出，提取状态信息。
//
// 解析出的信息用于 `nyatmc status`、TUI 看板与 Web 仪表盘：就绪状态、在线玩家、
// TPS、版本号、错误与警告计数等。全部基于日志文本，不需要服务端装任何插件。
package logwatch

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Snapshot 是某一时刻解析出的服务端状态。
type Snapshot struct {
	// Ready 表示服务端已完成启动（打印过 Done）。
	Ready bool
	// Players 是在线玩家名（按加入顺序）。
	Players []string
	// MaxPlayers 来自 list 命令输出，未知为 0。
	MaxPlayers int
	// TPS 是最近一次日志里出现的 TPS，TPSKnown 表示是否可用。
	TPS   float64
	TPSKnown bool
	// TickBehindMS 是最近一次“服务器跟不上”事件的落后毫秒数。
	TickBehindMS int64
	// Version 是服务端自报的 Minecraft 版本。
	Version string
	// Warnings / Errors 是累计的警告与错误条数。
	Warnings int
	Errors   int
	// LastError 是最近一条值得展示的错误文本。
	LastError string
	// LastLine 是最近一行输出。
	LastLine string
	// StartedAt 是最近一次启动时刻（看到 Starting minecraft server version 时刷新）。
	StartedAt time.Time
	// UpdatedAt 是最后一次状态变化时间。
	UpdatedAt time.Time
}

// Parser 增量解析服务端输出。
type Parser struct {
	mu      sync.Mutex
	snap    Snapshot
	order   []string
	players map[string]bool
}

// New 创建解析器。
func New() *Parser {
	return &Parser{players: map[string]bool{}}
}

var (
	reDone       = regexp.MustCompile(`Done \(([0-9.]+)s\)! For help, type "help"`)
	reStarting   = regexp.MustCompile(`Starting minecraft server version (\S+)`)
	reJoined     = regexp.MustCompile(`^\s*([^\s\[]+)(?:\[[^\]]*\])?\s+joined the game`)
	reLeft       = regexp.MustCompile(`^\s*([^\s\[]+)(?:\[[^\]]*\])?\s+left the game`)
	reLoggedIn   = regexp.MustCompile(`^\s*([^\s\[]+)(?:\[[^\]]*\])?\s+logged in with entity id`)
	reLostConn   = regexp.MustCompile(`^\s*([^\s\[]+)(?:\[[^\]]*\])?\s+lost connection`)
	reDisconnect = regexp.MustCompile(`Disconnecting ([^\s\[]+)`)
	reTPS        = regexp.MustCompile(`TPS from last 1m, 5m, 15m:\s*([0-9.]+)`)
	reTickBehind = regexp.MustCompile(`Can't keep up! Is the server overloaded\? Running (\d+)ms or (\d+) ticks behind`)
	reListNew    = regexp.MustCompile(`There are (\d+) of a max of (\d+) players online:?\s*(.*)$`)
	reListOld    = regexp.MustCompile(`There are (\d+)/(\d+) players online:?\s*(.*)$`)
	reEULA       = regexp.MustCompile(`(?i)you need to agree to the eula`)
	reBindFail   = regexp.MustCompile(`(?i)failed to bind to port|Address already in use`)
	reOOM        = regexp.MustCompile(`OutOfMemoryError`)
	reLevelWarn  = regexp.MustCompile(`/(WARN|WARNING|ERROR|FATAL|SEVERE)\]`)
)

// Feed 喂入一行控制台输出，返回快照是否有变化。
func (p *Parser) Feed(line string) bool {
	// 日志级别必须从原始行里判断：stripTimestamp 会把 `[Server thread/ERROR]` 前缀一起剥掉。
	level := detectLevel(line)
	body := stripTimestamp(line)
	if strings.TrimSpace(body) == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	changed := false
	p.snap.LastLine = line
	p.touch(&changed)

	if m := reStarting.FindStringSubmatch(body); m != nil {
		p.snap.Version = m[1]
		p.snap.Ready = false
		p.snap.StartedAt = time.Now()
		p.snap.TickBehindMS = 0
		p.players = map[string]bool{}
		p.order = nil
		p.snap.Players = nil
		changed = true
	}
	if reDone.MatchString(body) {
		if !p.snap.Ready {
			p.snap.Ready = true
			changed = true
		}
	}
	if m := reJoined.FindStringSubmatch(body); m != nil {
		p.addPlayer(m[1], &changed)
	}
	if m := reLoggedIn.FindStringSubmatch(body); m != nil {
		p.addPlayer(m[1], &changed)
	}
	if m := reLeft.FindStringSubmatch(body); m != nil {
		p.removePlayer(m[1], &changed)
	}
	if m := reLostConn.FindStringSubmatch(body); m != nil {
		p.removePlayer(m[1], &changed)
	}
	if m := reDisconnect.FindStringSubmatch(body); m != nil {
		p.removePlayer(m[1], &changed)
	}
	if m := reTPS.FindStringSubmatch(body); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			p.snap.TPS = v
			p.snap.TPSKnown = true
			changed = true
		}
	}
	if m := reTickBehind.FindStringSubmatch(body); m != nil {
		if v, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			p.snap.TickBehindMS = v
			changed = true
		}
	}
	if m := reListNew.FindStringSubmatch(body); m != nil {
		p.applyList(m[2], m[3], &changed)
	} else if m := reListOld.FindStringSubmatch(body); m != nil {
		p.applyList(m[2], m[3], &changed)
	}
	if reEULA.MatchString(body) {
		p.noteError("服务端要求先同意 Minecraft EULA（见 eula.txt / server.eula）", &changed)
	}
	if reBindFail.MatchString(body) {
		p.noteError("端口被占用或无法绑定，请检查 server.port 是否冲突", &changed)
	}
	if reOOM.MatchString(body) {
		p.noteError("服务端内存不足（OutOfMemoryError），请调大 server.memory", &changed)
	}
	if level != "" {
		switch level {
		case "WARN":
			p.snap.Warnings++
		case "ERROR", "FATAL":
			p.snap.Errors++
			if p.snap.LastError == "" || p.snap.Errors%10 == 1 {
				p.snap.LastError = strings.TrimSpace(body)
			}
		}
		changed = true
	}

	p.snap.UpdatedAt = time.Now()
	return changed
}

// Snapshot 返回当前快照的副本。
func (p *Parser) Snapshot() Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.snap
	s.Players = append([]string(nil), p.snap.Players...)
	return s
}

// Reset 清空解析状态（例如服务端进程已退出）。
func (p *Parser) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.snap.Ready = false
	p.snap.Players = nil
	p.snap.TPSKnown = false
	p.snap.TPS = 0
	p.snap.TickBehindMS = 0
	p.players = map[string]bool{}
	p.order = nil
}

// SetReady 由调用方直接设置就绪状态（例如进程退出时置为 false）。
func (p *Parser) SetReady(ready bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.snap.Ready = ready
}

func (p *Parser) touch(changed *bool) {
	if !*changed {
		// LastLine 也属于快照内容，统一在 Feed 末尾刷新 UpdatedAt
	}
}

func (p *Parser) addPlayer(name string, changed *bool) {
	name = strings.TrimSpace(name)
	if name == "" || p.players[name] {
		return
	}
	p.players[name] = true
	p.order = append(p.order, name)
	p.snap.Players = append([]string(nil), p.order...)
	*changed = true
}

func (p *Parser) removePlayer(name string, changed *bool) {
	name = strings.TrimSpace(name)
	if name == "" || !p.players[name] {
		return
	}
	delete(p.players, name)
	for idx, n := range p.order {
		if n == name {
			p.order = append(p.order[:idx], p.order[idx+1:]...)
			break
		}
	}
	p.snap.Players = append([]string(nil), p.order...)
	*changed = true
}

func (p *Parser) applyList(maxStr, namesStr string, changed *bool) {
	if max, err := strconv.Atoi(strings.TrimSpace(maxStr)); err == nil && max > 0 {
		if p.snap.MaxPlayers != max {
			p.snap.MaxPlayers = max
			*changed = true
		}
	}
	// list 命令的输出是权威信息，用它重置在线列表
	namesStr = strings.TrimSpace(namesStr)
	fresh := map[string]bool{}
	var order []string
	if namesStr != "" {
		for _, n := range strings.Split(namesStr, ",") {
			n = strings.TrimSpace(n)
			if n == "" {
				continue
			}
			if !fresh[n] {
				fresh[n] = true
				order = append(order, n)
			}
		}
	}
	p.players = fresh
	p.order = order
	p.snap.Players = append([]string(nil), order...)
	*changed = true
}

func (p *Parser) noteError(msg string, changed *bool) {
	if p.snap.LastError != msg {
		p.snap.LastError = msg
		*changed = true
	}
	p.snap.Errors++
}

// detectLevel 从原始日志行里判断级别，返回 WARN / ERROR / FATAL 或空字符串。
func detectLevel(line string) string {
	m := reLevelWarn.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	switch m[1] {
	case "WARN", "WARNING":
		return "WARN"
	case "SEVERE":
		return "ERROR"
	default:
		return m[1]
	}
}

// stripTimestamp 去掉 `[12:34:56] [Server thread/INFO]: ` 这类前缀，同时把分隔用的冒号一起吃掉。
func stripTimestamp(line string) string {
	s := strings.TrimSpace(line)
	for {
		if !strings.HasPrefix(s, "[") {
			break
		}
		end := strings.Index(s, "]")
		if end < 0 {
			break
		}
		head := s[1:end]
		// 只剥掉形如 12:34:56 或 Server thread/INFO 的前缀，其余内容原样保留
		if !strings.Contains(head, ":") && !strings.Contains(head, "/") {
			break
		}
		s = strings.TrimSpace(s[end+1:])
		s = strings.TrimSpace(strings.TrimPrefix(s, ":"))
	}
	return s
}

// TailLines 从文本里取最后 n 行。
func TailLines(text string, n int) []string {
	if n <= 0 {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
}

// SortPlayers 返回排序后的玩家名（展示用）。
func SortPlayers(players []string) []string {
	out := append([]string(nil), players...)
	sort.Strings(out)
	return out
}

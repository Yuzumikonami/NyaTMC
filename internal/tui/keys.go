package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/yuzumikonami/nyatmc/internal/backup"
	"github.com/yuzumikonami/nyatmc/internal/daemon"
)

// onKey 处理一个按键；返回 true 表示退出看板。
func (c *controller) onKey(k Key) bool {
	if k.Kind == KeyEOF || k.Kind == KeyCtrlC {
		return true
	}
	switch c.overlay {
	case modeHelp:
		if k.Kind == KeyRune && (k.Rune == 'q' || k.Rune == 'Q') {
			return true
		}
		c.overlay = modeDashboard
		c.dirty = true
		return false
	case modeConfig:
		return c.onConfigKey(k)
	}
	return c.onDashKey(k)
}

// onDashKey 处理看板模式下的按键（全部单键生效，无需回车）。
func (c *controller) onDashKey(k Key) bool {
	switch k.Kind {
	case KeyUp:
		c.scrollLog(+1)
	case KeyDown:
		c.scrollLog(-1)
	case KeyPgUp:
		c.scrollLog(+c.pageRows())
	case KeyPgDn:
		c.scrollLog(-c.pageRows())
	case KeyHome:
		c.offset = ScrollHome(c.logs.Len())
		c.dirty = true
	case KeyEnd:
		c.offset = 0
		c.dirty = true
	case KeyTab:
		c.switchInstance(1)
	case KeyBacktab:
		c.switchInstance(-1)
	case KeyRune:
		return c.onDashRune(k.Rune)
	}
	return false
}

// onDashRune 处理看板模式下的字符键。
func (c *controller) onDashRune(r rune) bool {
	switch r {
	case 'q', 'Q':
		return true
	case 's', 'S':
		c.runOp("启动中", c.opStart)
	case 'e', 'E':
		c.runOp("优雅停止中", c.opStop)
	case 'k', 'K':
		c.runOp("强制结束中", c.opKill)
	case 'r', 'R':
		c.runOp("重启中", c.opRestart)
	case 'b', 'B':
		c.runOp("备份中", c.opBackup)
	case 'c', 'C':
		c.cfs = newConfigState(c.inst.Res, c.cfg)
		c.overlay = modeConfig
		c.dirty = true
	case '?', 'h', 'H':
		c.overlay = modeHelp
		c.dirty = true
	case 'l', 'L':
		c.cycleMode(dashLogsOnly)
	case 'p', 'P':
		c.cycleMode(dashPlayersOnly)
	case 'f', 'F':
		c.offset = 0
		c.dirty = true
	case ' ':
		c.offset = 0
		c.dirty = true
	case '1', '2', '3', '4', '5', '6', '7', '8', '9':
		c.switchTo(int(r - '1'))
	}
	return false
}

// cycleMode 在「只看日志」「只看玩家」与默认布局之间切换。
func (c *controller) cycleMode(want runMode) {
	if c.mode == want {
		c.mode = dashDefault
		c.flash("已恢复完整布局", false)
	} else {
		c.mode = want
		if want == dashLogsOnly {
			c.flash("只看日志（再按一次恢复）", false)
		} else {
			c.flash("只看玩家（再按一次恢复）", false)
		}
	}
	c.dirty = true
}

// scrollLog 调整日志滚动偏移：delta > 0 表示向上翻（看更早的内容）。
func (c *controller) scrollLog(delta int) {
	c.offset = Scroll(c.offset, delta, c.logs.Len())
	c.dirty = true
}

// pageRows 返回翻页步长（约等于日志区高度）。
func (c *controller) pageRows() int {
	h := c.height
	if h <= 6 {
		return 1
	}
	return maxInt(1, h/2)
}

// switchInstance 在实例列表里前后切换。
func (c *controller) switchInstance(delta int) {
	if len(c.instances) <= 1 {
		c.flash("提示：当前只监控一个实例（可用 nyatmc tui --instances a,b 监控多个）", false)
		return
	}
	n := len(c.instances)
	c.idx = ((c.idx+delta)%n + n) % n
	c.attach(c.instances[c.idx])
	c.flash(fmt.Sprintf("已切换到实例 %s", c.inst.Name), false)
	c.dirty = true
}

// switchTo 按序号切换实例（数字键）。
func (c *controller) switchTo(i int) {
	if i < 0 || i >= len(c.instances) || i == c.idx {
		return
	}
	c.idx = i
	c.attach(c.instances[c.idx])
	c.flash(fmt.Sprintf("已切换到实例 %s", c.inst.Name), false)
	c.dirty = true
}

// ------------------------------------------------------------------ 单键操作

// runOp 在后台 goroutine 里执行一次操作，避免阻塞界面。
func (c *controller) runOp(label string, fn func(context.Context) (string, error)) {
	if c.opBusy {
		c.flash("上一个操作还在进行中，请稍候", true)
		c.dirty = true
		return
	}
	c.opBusy = true
	c.opLabel = label
	c.dirty = true
	c.opSeq++
	seq := c.opSeq
	go func() {
		msg, err := fn(c.ctx)
		res := opResult{tag: seq, msg: msg}
		if err != nil {
			res.fail = true
			res.msg = err.Error()
		}
		select {
		case c.ops <- res:
		case <-c.ctx.Done():
		}
	}()
}

// opStart 启动实例（后台守护 + 看门狗，不阻塞界面）。
func (c *controller) opStart(ctx context.Context) (string, error) {
	inst := c.inst
	if daemon.Running(inst) {
		return fmt.Sprintf("实例 %s 已经在运行", inst.Name), nil
	}
	st, err := daemon.Start(ctx, c.cfg, inst, daemon.StartOptions{})
	if err != nil {
		return "", err
	}
	switch {
	case st.State == daemon.StateRunning:
		return fmt.Sprintf("实例 %s 已启动（pid %d），正在等待就绪", inst.Name, st.PID), nil
	default:
		return fmt.Sprintf("已请求启动实例 %s（当前状态 %s）", inst.Name, st.State.Label()), nil
	}
}

// opStop 优雅停止实例。
func (c *controller) opStop(ctx context.Context) (string, error) {
	inst := c.inst
	if err := daemon.Stop(ctx, c.cfg, inst, daemon.StopOptions{Timeout: 90 * time.Second}); err != nil {
		if errorsIs(err, daemon.ErrNotRunning) {
			return fmt.Sprintf("实例 %s 本来就没有在运行", inst.Name), nil
		}
		return "", err
	}
	return fmt.Sprintf("实例 %s 已优雅停止", inst.Name), nil
}

// opKill 强制结束实例（危险操作）。
func (c *controller) opKill(ctx context.Context) (string, error) {
	inst := c.inst
	if err := daemon.Stop(ctx, c.cfg, inst, daemon.StopOptions{Force: true, Timeout: 20 * time.Second}); err != nil {
		if errorsIs(err, daemon.ErrNotRunning) {
			return fmt.Sprintf("实例 %s 本来就没有在运行", inst.Name), nil
		}
		return "", err
	}
	return fmt.Sprintf("已强制结束实例 %s（世界数据可能未完整落盘）", inst.Name), nil
}

// opRestart 重启实例。
func (c *controller) opRestart(ctx context.Context) (string, error) {
	inst := c.inst
	st, err := daemon.Restart(ctx, c.cfg, inst, daemon.RestartOptions{})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("实例 %s 正在重启（当前状态 %s）", inst.Name, st.State.Label()), nil
}

// opBackup 立即备份一次。
func (c *controller) opBackup(ctx context.Context) (string, error) {
	inst := c.inst
	ar, removed, err := backup.CreateAndPrune(inst, "manual", c.log)
	if err != nil {
		return "", err
	}
	msg := fmt.Sprintf("备份完成：%s（%s）", ar.Name, ar.HumanSize())
	if len(removed) > 0 {
		msg += fmt.Sprintf("，清理了 %d 个旧备份", len(removed))
	}
	return msg, nil
}

// errorsIs 是 errors.Is 的薄封装，语义与 errors.Is 一致。
func errorsIs(err, target error) bool { return errors.Is(err, target) }

// ------------------------------------------------------------------ 配置模式按键

func (c *controller) onConfigKey(k Key) bool {
	cs := c.cfs
	if cs == nil {
		c.overlay = modeDashboard
		return false
	}
	if k.Kind == KeyRune && (k.Rune == 'q' || k.Rune == 'Q') && !cs.editing {
		c.overlay = modeDashboard
		c.dirty = true
		return false
	}

	if cs.editing {
		switch k.Kind {
		case KeyEnter:
			if cs.commit(configPathOf(c.cfg)) {
				c.flash(cs.message, false)
				c.dirty = true
			} else {
				c.flash(cs.message, true)
				c.dirty = true
			}
		case KeyEsc:
			cs.cancelEdit()
			c.dirty = true
		case KeyBack:
			cs.backspace()
			c.dirty = true
		case KeyDelete:
			cs.deleteForward()
			c.dirty = true
		case KeyLeft:
			cs.moveCursor(-1)
			c.dirty = true
		case KeyRight:
			cs.moveCursor(1)
			c.dirty = true
		case KeyHome:
			cs.cursor = 0
			c.dirty = true
		case KeyEnd:
			cs.cursor = len([]rune(cs.buf))
			c.dirty = true
		case KeyRune:
			if k.Rune >= 32 {
				cs.insertRune(k.Rune, 512)
				c.dirty = true
			}
		}
		return false
	}

	switch k.Kind {
	case KeyUp:
		cs.move(-1, c.cfsVisible())
		c.dirty = true
	case KeyDown:
		cs.move(1, c.cfsVisible())
		c.dirty = true
	case KeyPgUp:
		cs.move(-c.cfsVisible(), c.cfsVisible())
		c.dirty = true
	case KeyPgDn:
		cs.move(c.cfsVisible(), c.cfsVisible())
		c.dirty = true
	case KeyHome:
		cs.selected = 0
		cs.followCursor(c.cfsVisible())
		c.dirty = true
	case KeyEnd:
		cs.selected = maxInt(0, len(cs.entries)-1)
		cs.followCursor(c.cfsVisible())
		c.dirty = true
	case KeyEnter:
		cs.startEdit(c.cfg)
		c.dirty = true
	case KeyEsc:
		c.overlay = modeDashboard
		c.dirty = true
	case KeyBacktab:
		cs.cycleEnum(c.cfg, -1)
		c.dirty = true
	case KeyTab:
		cs.cycleEnum(c.cfg, 1)
		c.dirty = true
	case KeyRune:
		switch k.Rune {
		case ' ', 'x', 'X':
			cs.toggle(c.cfg)
			c.dirty = true
		case '[':
			cs.cycleEnum(c.cfg, -1)
			c.dirty = true
		case ']':
			cs.cycleEnum(c.cfg, 1)
			c.dirty = true
		case 'e', 'E':
			cs.startEdit(c.cfg)
			c.dirty = true
		case '?', 'h', 'H':
			c.overlay = modeHelp
			c.dirty = true
		}
	}
	return false
}

// cfsVisible 返回配置列表当前可见的行数。
func (c *controller) cfsVisible() int {
	h := c.height
	if h < 12 {
		h = 12
	}
	return maxInt(4, h-8)
}

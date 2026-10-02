package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/yuzumikonami/nyatmc/internal/config"
	"github.com/yuzumikonami/nyatmc/internal/daemon"
	"github.com/yuzumikonami/nyatmc/internal/instance"
)

// ---------------------------------------------------------------- 健康检查

// handleHealth 是免鉴权的健康检查。
func (s *Server) handleHealth(c *gin.Context) {
	cfg := s.current()
	ok(c, gin.H{
		"ok":            true,
		"version":       s.versionString(),
		"instances":     len(cfg.InstanceNames()),
		"write_enabled": s.WriteEnabled(),
		"loopback_only": s.Loopback(),
		"token_required": s.Token() != "",
		"time":          time.Now().Format(time.RFC3339),
	})
}

// ---------------------------------------------------------------- 概览

// handleOverview 汇总所有实例的运行状态。
func (s *Server) handleOverview(c *gin.Context) {
	cfg := s.current()
	insts := instance.List(cfg)
	views := s.statusOfAll(c.Request.Context(), cfg, insts)
	ok(c, gin.H{
		"count":     len(views),
		"instances": views,
		"time":      time.Now().Format(time.RFC3339),
	})
}

// statusOfAll 并发探测多个实例的状态（单个实例探测失败不影响其它实例）。
func (s *Server) statusOfAll(ctx context.Context, cfg *config.Config, insts []*instance.Instance) []statusView {
	out := make([]statusView, len(insts))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, inst := range insts {
		wg.Add(1)
		go func(i int, inst *instance.Instance) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			st, err := daemon.GetStatus(ctx, cfg, inst)
			if err != nil {
				st = daemon.Status{Instance: inst.Name, State: daemon.StateUnknown, LastError: err.Error()}
			}
			out[i] = newStatusView(inst, st)
		}(i, inst)
	}
	wg.Wait()
	return out
}

// ---------------------------------------------------------------- 实例列表与详情

// handleInstanceList 返回实例列表（只读静态信息，不做进程探测）。
func (s *Server) handleInstanceList(c *gin.Context) {
	cfg := s.current()
	insts := instance.List(cfg)
	items := make([]instanceInfo, 0, len(insts))
	for _, inst := range insts {
		items = append(items, newInstanceInfo(cfg, inst))
	}
	ok(c, gin.H{
		"count":     len(items),
		"instances": items,
		"default":   cfg.General.DefaultInstance,
	})
}

// handleInstanceDetail 返回单实例的完整信息。
func (s *Server) handleInstanceDetail(c *gin.Context) {
	inst, fine := s.resolveInstance(c)
	if !fine {
		return
	}
	cfg := s.current()
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()

	st, err := daemon.GetStatus(ctx, cfg, inst)
	if err != nil {
		st = daemon.Status{Instance: inst.Name, State: daemon.StateUnknown, LastError: err.Error()}
	}
	meta, metaErr := inst.LoadMetadata()

	detail := gin.H{
		"name":     inst.Name,
		"status":   newStatusView(inst, st),
		"metadata": meta,
		"info":     newInstanceInfo(cfg, inst),
		"dirs": gin.H{
			"root":       inst.Res.Root,
			"server":     inst.Res.Path,
			"meta":       inst.Res.MetaDir,
			"jar":        inst.Res.JarPath,
			"logs":       inst.Res.LogsDir,
			"console":    inst.Res.Console,
			"supervisor": inst.Res.SupLog,
			"backups":    inst.Res.BackupDir,
			"state":      inst.Res.StateFile,
			"endpoint":   inst.Res.Endpoint,
		},
		"settings": gin.H{
			"type":              inst.Res.Server.Type,
			"minecraft_version": inst.Res.Server.MinecraftVersion,
			"memory":            inst.Res.Server.Memory,
			"min_memory":        inst.Res.Server.MinMemory,
			"java_path":         inst.Res.Server.JavaPath,
			"port":              inst.Res.Server.Port,
			"motd":              inst.Res.Server.MOTD,
			"max_players":       inst.Res.Server.MaxPlayers,
			"view_distance":     inst.Res.Server.ViewDistance,
			"online_mode":       inst.Res.Server.OnlineModeEnabled(),
			"eula":              inst.Res.Server.EULAEnabled(),
			"auto_download":     inst.Res.Server.AutoDownloadEnabled(),
			"watchdog":          inst.Res.Daemon.WatchdogEnabled(),
			"stop_timeout":      inst.Res.Daemon.StopTimeout.String(),
			"startup_timeout":   inst.Res.Daemon.StartupTimeout.String(),
			"backup_format":     inst.Res.Backup.Format,
			"backup_keep":       inst.Res.Backup.Keep,
			"backup_before_restore": inst.Res.Backup.BeforeRestoreEnabled(),
			"external_path":     strings.TrimSpace(cfg.Instances[inst.Name].Path) != "",
		},
		"notes": []string{
			"EULA：" + onOff(inst.Res.Server.EULAEnabled()),
			"看门狗：" + onOff(inst.Res.Daemon.WatchdogEnabled()),
		},
	}
	if metaErr != nil {
		detail["metadata_error"] = metaErr.Error()
	}
	ok(c, detail)
}

// resolveInstance 校验 URL 里的实例名并构造实例句柄；失败时已经写好响应。
func (s *Server) resolveInstance(c *gin.Context) (*instance.Instance, bool) {
	name := strings.TrimSpace(c.Param("name"))
	if err := config.ValidateInstanceName(name); err != nil {
		fail(c, http.StatusBadRequest, "实例名不合法：%v", err)
		return nil, false
	}
	cfg := s.current()
	if !cfg.HasInstance(name) {
		names := cfg.InstanceNames()
		hint := "（当前没有任何实例，先用 nyatmc create <名字> 新建）"
		if len(names) > 0 {
			hint = "，已有实例：" + strings.Join(names, "、")
		}
		fail(c, http.StatusNotFound, "实例 %q 不存在%s", name, hint)
		return nil, false
	}
	inst, err := instance.New(cfg, name)
	if err != nil {
		fail(c, http.StatusBadRequest, "构造实例 %q 失败：%v", name, err)
		return nil, false
	}
	return inst, true
}

// ---------------------------------------------------------------- 启停控制

// handleStart 启动实例。body 可空，或 {"wait":true}。
func (s *Server) handleStart(c *gin.Context) {
	inst, fine := s.resolveInstance(c)
	if !fine {
		return
	}
	var body struct {
		Wait *bool `json:"wait"`
	}
	if !bindJSON(c, &body) {
		return
	}
	wait := body.Wait != nil && *body.Wait

	unlock := s.locks.lock(inst.Name)
	defer unlock()

	cfg := s.current()
	if daemon.Running(inst) {
		st, _ := daemon.GetStatus(c.Request.Context(), cfg, inst)
		fail(c, http.StatusConflict, "实例 %s 已经在运行（状态 %s，pid %d）", inst.Name, st.State.Label(), st.PID)
		return
	}

	waitTimeout := inst.Res.Daemon.StartupTimeout.Std()
	if waitTimeout <= 0 || waitTimeout > 3*time.Minute {
		waitTimeout = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), waitTimeout+20*time.Second)
	defer cancel()

	st, err := daemon.Start(ctx, cfg, inst, daemon.StartOptions{
		Wait:        wait,
		WaitTimeout: waitTimeout,
		ConfigPath:  s.ConfigPath(),
		Logger:      s.log,
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "启动实例 %s 失败：%v", inst.Name, err)
		return
	}
	s.log.Infof("Web：已启动实例 %s（状态 %s）", inst.Name, st.State.Label())
	ok(c, gin.H{"status": newStatusView(inst, st), "message": fmt.Sprintf("实例 %s 已提交启动（状态 %s）", inst.Name, st.State.Label())})
}

// handleStop 停止实例。body：{"force":bool,"timeout":秒数,"keep_supervisor":bool}。
func (s *Server) handleStop(c *gin.Context) {
	inst, fine := s.resolveInstance(c)
	if !fine {
		return
	}
	var body struct {
		Force          bool     `json:"force"`
		Timeout        *float64 `json:"timeout"`
		KeepSupervisor bool     `json:"keep_supervisor"`
	}
	if !bindJSON(c, &body) {
		return
	}

	unlock := s.locks.lock(inst.Name)
	defer unlock()

	timeout := time.Duration(0)
	if body.Timeout != nil {
		if *body.Timeout < 0 {
			fail(c, http.StatusBadRequest, "timeout 不能为负数")
			return
		}
		timeout = time.Duration(*body.Timeout * float64(time.Second))
	}
	if timeout <= 0 {
		timeout = inst.Res.Daemon.StopTimeout.Std()
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout+2*time.Minute)
	defer cancel()

	err := daemon.Stop(ctx, s.current(), inst, daemon.StopOptions{
		Force:          body.Force,
		Timeout:        timeout,
		KeepSupervisor: body.KeepSupervisor,
	})
	switch {
	case err == nil:
	case errors.Is(err, daemon.ErrNotRunning):
		fail(c, http.StatusConflict, "实例 %s 本来就没有在运行", inst.Name)
		return
	default:
		fail(c, http.StatusInternalServerError, "停止实例 %s 失败：%v", inst.Name, err)
		return
	}
	s.log.Infof("Web：已停止实例 %s（force=%v）", inst.Name, body.Force)

	st, _ := daemon.GetStatus(c.Request.Context(), s.current(), inst)
	msg := fmt.Sprintf("实例 %s 已停止", inst.Name)
	if body.KeepSupervisor {
		msg = fmt.Sprintf("实例 %s 的服务端已停止（守护进程保留）", inst.Name)
	}
	ok(c, gin.H{"status": newStatusView(inst, st), "message": msg})
}

// handleRestart 重启实例。body：{"force":bool,"delay":秒数,"wait":bool}。
func (s *Server) handleRestart(c *gin.Context) {
	inst, fine := s.resolveInstance(c)
	if !fine {
		return
	}
	var body struct {
		Force bool     `json:"force"`
		Delay *float64 `json:"delay"`
		Wait  *bool    `json:"wait"`
	}
	if !bindJSON(c, &body) {
		return
	}
	delay := time.Duration(0)
	if body.Delay != nil {
		if *body.Delay < 0 {
			fail(c, http.StatusBadRequest, "delay 不能为负数")
			return
		}
		delay = time.Duration(*body.Delay * float64(time.Second))
	}

	unlock := s.locks.lock(inst.Name)
	defer unlock()

	cfg := s.current()
	wait := body.Wait != nil && *body.Wait
	waitTimeout := inst.Res.Daemon.StartupTimeout.Std()
	if waitTimeout <= 0 || waitTimeout > 3*time.Minute {
		waitTimeout = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), waitTimeout+2*time.Minute)
	defer cancel()

	st, err := daemon.Restart(ctx, cfg, inst, daemon.RestartOptions{
		Force:       body.Force,
		Delay:       delay,
		Wait:        wait,
		WaitTimeout: waitTimeout,
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "重启实例 %s 失败：%v", inst.Name, err)
		return
	}
	s.log.Infof("Web：已重启实例 %s（force=%v）", inst.Name, body.Force)
	ok(c, gin.H{"status": newStatusView(inst, st), "message": fmt.Sprintf("实例 %s 已提交重启", inst.Name)})
}

// handleCommand 往服务端控制台发送一行指令。
func (s *Server) handleCommand(c *gin.Context) {
	inst, fine := s.resolveInstance(c)
	if !fine {
		return
	}
	var body struct {
		Line string `json:"line"`
	}
	if !bindJSON(c, &body) {
		return
	}
	line := strings.TrimSpace(body.Line)
	if line == "" {
		fail(c, http.StatusBadRequest, "指令内容不能为空")
		return
	}
	if len(line) > 1024 {
		fail(c, http.StatusBadRequest, "指令过长（最多 1024 字节）")
		return
	}
	if strings.ContainsAny(line, "\r\n") {
		fail(c, http.StatusBadRequest, "指令不能包含换行符（一次只能发送一行）")
		return
	}

	unlock := s.locks.lock(inst.Name)
	defer unlock()

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	if !daemon.Running(inst) {
		fail(c, http.StatusConflict, "实例 %s 未在运行，无法发送指令", inst.Name)
		return
	}
	if err := daemon.SendCommand(ctx, inst, line); err != nil {
		if errors.Is(err, daemon.ErrNotRunning) {
			fail(c, http.StatusConflict, "实例 %s 未在运行，无法发送指令", inst.Name)
			return
		}
		fail(c, http.StatusInternalServerError, "发送指令失败：%v", err)
		return
	}
	ok(c, gin.H{"line": line, "message": "指令已发送：" + line})
}

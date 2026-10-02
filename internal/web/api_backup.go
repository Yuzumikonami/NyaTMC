package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/yuzumikonami/nyatmc/internal/backup"
	"github.com/yuzumikonami/nyatmc/internal/daemon"
)

// backupView 是备份文件的展示视图。
type backupView struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Label      string `json:"label"`
	Format     string `json:"format"`
	Size       int64  `json:"size"`
	SizeHuman  string `json:"size_human"`
	CreatedAt  string `json:"created_at"`
	AgeHuman   string `json:"age_human"`
	Instance   string `json:"instance"`
}

func newBackupView(a backup.Archive) backupView {
	age := "-"
	if !a.CreatedAt.IsZero() {
		age = humanDuration(time.Since(a.CreatedAt))
	}
	return backupView{
		Name:      a.Name,
		Path:      a.Path,
		Label:     a.Label,
		Format:    a.Format,
		Size:      a.Size,
		SizeHuman: a.HumanSize(),
		CreatedAt: a.CreatedAt.Format("2006-01-02 15:04:05"),
		AgeHuman:  age,
		Instance:  a.Instance,
	}
}

// handleBackupList 列出实例的全部备份。
func (s *Server) handleBackupList(c *gin.Context) {
	inst, fine := s.resolveInstance(c)
	if !fine {
		return
	}
	list, err := backup.List(inst)
	if err != nil {
		fail(c, http.StatusInternalServerError, "读取备份列表失败：%v", err)
		return
	}
	items := make([]backupView, 0, len(list))
	for _, a := range list {
		items = append(items, newBackupView(a))
	}
	bc := inst.Res.Backup
	ok(c, gin.H{
		"count":          len(items),
		"backups":        items,
		"dir":            inst.Res.BackupDir,
		"keep":           bc.Keep,
		"max_age":        bc.MaxAge.String(),
		"format":         bc.Format,
		"before_restore": bc.BeforeRestoreEnabled(),
	})
}

// handleBackupCreate 立即执行一次备份，并按配置清理旧备份。
func (s *Server) handleBackupCreate(c *gin.Context) {
	inst, fine := s.resolveInstance(c)
	if !fine {
		return
	}
	var body struct {
		Label string `json:"label"`
	}
	if !bindJSON(c, &body) {
		return
	}
	label := strings.TrimSpace(body.Label)
	if len(label) > 32 {
		fail(c, http.StatusBadRequest, "备份标签过长（最多 32 个字符）")
		return
	}

	unlock := s.locks.lock(inst.Name)
	defer unlock()

	s.log.Infof("Web：开始备份实例 %s（标签 %q）", inst.Name, label)
	archive, removed, err := backup.CreateAndPrune(inst, label, s.log)
	if err != nil {
		fail(c, http.StatusInternalServerError, "备份实例 %s 失败：%v", inst.Name, err)
		return
	}
	ok(c, gin.H{
		"backup":  newBackupView(archive),
		"removed": len(removed),
		"message": "备份完成：" + archive.Name,
	})
}

// handleBackupRestore 回滚到指定备份。
//
// body：{"name":"latest","targets":["world"],"backup_current":true,"force":false}
func (s *Server) handleBackupRestore(c *gin.Context) {
	inst, fine := s.resolveInstance(c)
	if !fine {
		return
	}
	var body struct {
		Name          string   `json:"name"`
		Targets       []string `json:"targets"`
		BackupCurrent *bool    `json:"backup_current"`
		Force         bool     `json:"force"`
	}
	if !bindJSON(c, &body) {
		return
	}
	for _, t := range body.Targets {
		if err := validRelTarget(t); err != nil {
			fail(c, http.StatusBadRequest, "%v", err)
			return
		}
	}

	unlock := s.locks.lock(inst.Name)
	defer unlock()

	archive, err := backup.Find(inst, body.Name)
	if err != nil {
		fail(c, http.StatusNotFound, "找不到备份：%v", err)
		return
	}

	if daemon.Running(inst) && !body.Force {
		fail(c, http.StatusConflict,
			"实例 %s 正在运行，回滚会覆盖正在被占用的世界文件；请先停止实例，或在请求里带上 force=true 强制继续",
			inst.Name)
		return
	}

	before := inst.Res.Backup.BeforeRestoreEnabled()
	if body.BackupCurrent != nil {
		before = *body.BackupCurrent
	}

	s.log.Infof("Web：回滚实例 %s 到备份 %s（回滚前备份=%v）", inst.Name, archive.Name, before)
	res, err := backup.Restore(inst, archive.Path, backup.RestoreOptions{
		BeforeBackup: before,
		Targets:      body.Targets,
		Logger:       s.log,
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "回滚失败：%v", err)
		return
	}
	msg := "回滚完成，共同步 " + strconv.Itoa(res.Files) + " 个文件"
	if res.Backup != nil {
		msg += "；回滚前的状态已备份为 " + res.Backup.Name
	}
	if daemon.Running(inst) {
		msg += "；实例仍在运行，建议重启后世界才会完全生效"
	}
	ok(c, gin.H{
		"backup":     newBackupView(archive),
		"files":      res.Files,
		"bytes":      res.Bytes,
		"size_human": backup.HumanSize(res.Bytes),
		"made_backup": func() string {
			if res.Backup != nil {
				return res.Backup.Name
			}
			return ""
		}(),
		"message": msg,
	})
}

// handleBackupPrune 清理旧备份。body：{"keep":3,"max_age":86400}。
func (s *Server) handleBackupPrune(c *gin.Context) {
	inst, fine := s.resolveInstance(c)
	if !fine {
		return
	}
	var body struct {
		Keep   *int     `json:"keep"`
		MaxAge *float64 `json:"max_age"`
	}
	if !bindJSON(c, &body) {
		return
	}
	keep := inst.Res.Backup.Keep
	if body.Keep != nil {
		if *body.Keep < 0 {
			fail(c, http.StatusBadRequest, "keep 不能为负数")
			return
		}
		keep = *body.Keep
	}
	maxAge := inst.Res.Backup.MaxAge.Std()
	if body.MaxAge != nil {
		if *body.MaxAge < 0 {
			fail(c, http.StatusBadRequest, "max_age 不能为负数")
			return
		}
		maxAge = time.Duration(*body.MaxAge * float64(time.Second))
	}
	if keep <= 0 && maxAge <= 0 {
		fail(c, http.StatusBadRequest,
			"没有可用的清理策略：配置里 backup.keep = 0 且 backup.max_age = 0；请在请求体里指定 keep（保留份数）或 max_age（秒）")
		return
	}

	unlock := s.locks.lock(inst.Name)
	defer unlock()

	removed, err := backup.Prune(inst, keep, maxAge, s.log)
	if err != nil {
		fail(c, http.StatusInternalServerError, "清理备份失败：%v", err)
		return
	}
	names := make([]string, 0, len(removed))
	for _, p := range removed {
		names = append(names, lastPathSegment(p))
	}
	ok(c, gin.H{
		"removed": names,
		"count":   len(names),
		"message": "已清理 " + strconv.Itoa(len(names)) + " 个旧备份",
	})
}

// ---------------------------------------------------------------- 小工具

func lastPathSegment(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if idx := strings.LastIndex(p, "/"); idx >= 0 {
		return p[idx+1:]
	}
	return p
}

package web

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/robfig/cron/v3"

	"github.com/yuzumikonami/nyatmc/internal/config"
)

// errJobNotFound 用于把「任务不存在」映射成 404。
var errJobNotFound = errors.New("调度任务不存在")

// cronParser 支持 5 段（分 时 日 月 周）与 6 段（含秒）表达式，以及 @daily 之类的宏。
// 说明：internal/scheduler 包尚未提供，这里只做「只读展示 + 按配置增删」，
// 真正执行定时任务仍由 nyatmc 的调度器负责。
var cronParser = cron.NewParser(
	cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// jobNameRe 限制任务名可用的字符，避免写进 TOML 时出问题。
var jobNameRe = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}_.\-]{0,63}$`)

var validJobActions = map[string]bool{
	"backup": true, "restart": true, "stop": true, "start": true, "command": true,
}

// jobView 是一条调度任务的展示视图。
type jobView struct {
	Name       string `json:"name"`
	Schedule   string `json:"schedule"`
	Action     string `json:"action"`
	Instance   string `json:"instance"`
	Command    string `json:"command,omitempty"`
	Label      string `json:"label,omitempty"`
	Enabled    bool   `json:"enabled"`
	NextRun    string `json:"next_run,omitempty"`
	NextHuman  string `json:"next_human,omitempty"`
	ParseError string `json:"parse_error,omitempty"`
}

// handleSchedulerList 列出调度任务与下次执行时间。
func (s *Server) handleSchedulerList(c *gin.Context) {
	cfg := s.current()
	sched := cfg.Scheduler

	loc := time.Local
	if strings.TrimSpace(sched.Timezone) != "" {
		if l, err := time.LoadLocation(sched.Timezone); err == nil {
			loc = l
		}
	}
	now := time.Now().In(loc)

	jobs := make([]jobView, 0, len(sched.Jobs))
	for _, j := range sched.Jobs {
		view := jobView{
			Name:     j.Name,
			Schedule: j.Schedule,
			Action:   j.Action,
			Instance: j.Instance,
			Command:  j.Command,
			Label:    j.Label,
			Enabled:  j.IsEnabled(),
		}
		spec, err := cronParser.Parse(strings.TrimSpace(j.Schedule))
		switch {
		case err != nil:
			view.ParseError = "cron 表达式无法解析：" + err.Error()
		default:
			next := spec.Next(now)
			if !next.IsZero() {
				view.NextRun = next.Format(time.RFC3339)
				view.NextHuman = humanUntil(now, next)
			}
		}
		jobs = append(jobs, view)
	}

	ok(c, gin.H{
		"enabled":         sched.Enabled,
		"timezone":        sched.Timezone,
		"effective_zone":  loc.String(),
		"count":           len(jobs),
		"jobs":            jobs,
		"available":       true,
		"note":            "调度任务的执行由 nyatmc 的调度器负责；Web 只负责读取与增删配置。",
	})
}

// handleSchedulerCreate 新增一条调度任务并写回配置。
func (s *Server) handleSchedulerCreate(c *gin.Context) {
	var body struct {
		Name     string `json:"name"`
		Schedule string `json:"schedule"`
		Action   string `json:"action"`
		Instance string `json:"instance"`
		Command  string `json:"command"`
		Label    string `json:"label"`
		Enabled  *bool  `json:"enabled"`
	}
	if !bindJSON(c, &body) {
		return
	}

	name := strings.TrimSpace(body.Name)
	if !jobNameRe.MatchString(name) {
		fail(c, http.StatusBadRequest,
			"任务名 %q 不合法：只允许字母、数字、下划线、短横线、点与中文，且不超过 64 个字符", body.Name)
		return
	}
	schedule := strings.TrimSpace(body.Schedule)
	if err := config.ValidateCron(schedule); err != nil {
		fail(c, http.StatusBadRequest, "cron 表达式非法：%v", err)
		return
	}
	action := strings.ToLower(strings.TrimSpace(body.Action))
	if !validJobActions[action] {
		fail(c, http.StatusBadRequest, "动作 %q 非法，可选：backup / restart / stop / start / command", body.Action)
		return
	}
	command := strings.TrimSpace(body.Command)
	if action == "command" && command == "" {
		fail(c, http.StatusBadRequest, "动作为 command 时必须填写要发送的控制台指令")
		return
	}
	instanceName := strings.TrimSpace(body.Instance)
	if instanceName != "" {
		if err := config.ValidateInstanceName(instanceName); err != nil {
			fail(c, http.StatusBadRequest, "目标实例名不合法：%v", err)
			return
		}
		if !s.current().HasInstance(instanceName) {
			fail(c, http.StatusBadRequest, "目标实例 %q 不存在", instanceName)
			return
		}
	}

	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	_, err := s.mgr.Update(func(cfg *config.Config) error {
		for _, j := range cfg.Scheduler.Jobs {
			if j.Name == name {
				return fmt.Errorf("调度任务 %q 已存在", name)
			}
		}
		cfg.Scheduler.Jobs = append(cfg.Scheduler.Jobs, config.Job{
			Name:     name,
			Schedule: schedule,
			Action:   action,
			Instance: instanceName,
			Command:  command,
			Label:    strings.TrimSpace(body.Label),
			Enabled:  body.Enabled,
		})
		return cfg.Validate()
	})
	if err != nil {
		fail(c, http.StatusBadRequest, "新增调度任务失败：%v", err)
		return
	}
	s.log.Infof("Web：已新增调度任务 %s（%s → %s）", name, schedule, action)
	ok(c, gin.H{"name": name, "message": "调度任务已保存：" + name})
}

// handleSchedulerDelete 删除一条调度任务。
func (s *Server) handleSchedulerDelete(c *gin.Context) {
	name := strings.TrimSpace(c.Param("name"))
	if !jobNameRe.MatchString(name) {
		fail(c, http.StatusBadRequest, "任务名 %q 不合法", name)
		return
	}

	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	_, err := s.mgr.Update(func(cfg *config.Config) error {
		idx := -1
		for i, j := range cfg.Scheduler.Jobs {
			if j.Name == name {
				idx = i
				break
			}
		}
		if idx < 0 {
			return errJobNotFound
		}
		jobs := make([]config.Job, 0, len(cfg.Scheduler.Jobs)-1)
		jobs = append(jobs, cfg.Scheduler.Jobs[:idx]...)
		jobs = append(jobs, cfg.Scheduler.Jobs[idx+1:]...)
		cfg.Scheduler.Jobs = jobs
		return cfg.Validate()
	})
	switch {
	case err == nil:
	case errors.Is(err, errJobNotFound):
		fail(c, http.StatusNotFound, "调度任务 %q 不存在", name)
		return
	default:
		fail(c, http.StatusBadRequest, "删除调度任务失败：%v", err)
		return
	}
	s.log.Infof("Web：已删除调度任务 %s", name)
	ok(c, gin.H{"name": name, "message": "调度任务已删除：" + name})
}

// humanUntil 输出「还有多久」的中文描述。
func humanUntil(now, next time.Time) string {
	d := next.Sub(now)
	if d < 0 {
		return "已过期"
	}
	return "还有 " + humanDuration(d)
}

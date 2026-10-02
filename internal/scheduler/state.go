package scheduler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yuzumikonami/nyatmc/internal/config"
)

// FileState 是持久化到 state/scheduler.json 的调度器状态。
//
// 它同时承担两件事：记录每个任务的执行历史（供 CLI / Web 展示），
// 以及记录调度器守护进程的 pid（供 `nyatmc schedule status` 判断是否在运行）。
type FileState struct {
	PID       int         `json:"pid"`
	StartedAt time.Time   `json:"started_at"`
	UpdatedAt time.Time   `json:"updated_at"`
	Jobs      []JobStatus `json:"jobs"`
}

// StatePath 返回调度器状态文件路径（~/.nyatmc/state/scheduler.json）。
func StatePath() string { return filepath.Join(config.StateDir(), "scheduler.json") }

// LogPath 返回调度器守护进程的输出日志路径。
func LogPath() string { return filepath.Join(config.StateDir(), "scheduler.log") }

// WriteState 原子写入状态文件。
func WriteState(st FileState) error {
	path := StatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建状态目录 %s 失败: %w", filepath.Dir(path), err)
	}
	st.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化调度器状态失败: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写入调度器状态 %s 失败: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换调度器状态 %s 失败: %w", path, err)
	}
	return nil
}

// ReadState 读取状态文件；文件不存在时返回零值且 error 为 nil。
func ReadState() (FileState, error) {
	var st FileState
	data, err := os.ReadFile(StatePath())
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return st, fmt.Errorf("读取调度器状态失败: %w", err)
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("调度器状态文件损坏: %w", err)
	}
	return st, nil
}

// RunningPID 返回正在运行的调度器进程号（0 表示未运行）。
func RunningPID() int {
	st, err := ReadState()
	if err != nil || st.PID <= 0 {
		return 0
	}
	if !processAlive(st.PID) {
		return 0
	}
	return st.PID
}

// Running 判断调度器守护进程是否在运行。
func Running() bool { return RunningPID() > 0 }

// readStateJobs 读取状态文件里的任务执行历史（best-effort，出错返回 nil）。
func readStateJobs() map[string]JobStatus {
	st, err := ReadState()
	if err != nil || len(st.Jobs) == 0 {
		return nil
	}
	out := make(map[string]JobStatus, len(st.Jobs))
	for _, j := range st.Jobs {
		name := strings.TrimSpace(j.Name)
		if name == "" {
			continue
		}
		out[name] = j
	}
	return out
}

// RecordRun 把一次手动执行的结果并入状态文件。
//
// 与 Runner 内部记账的区别是：它不会丢掉其它任务的历史记录（守护进程与此命令
// 可能同时在写同一份文件）。
func RecordRun(job config.Job, err error) {
	name := strings.TrimSpace(job.Name)
	if name == "" {
		return
	}
	st, rerr := ReadState()
	if rerr != nil {
		st = FileState{}
	}
	now := time.Now()
	updated := false
	for i := range st.Jobs {
		if strings.TrimSpace(st.Jobs[i].Name) != name {
			continue
		}
		st.Jobs[i].Job = job
		st.Jobs[i].LastRun = now
		st.Jobs[i].Runs++
		st.Jobs[i].Running = false
		st.Jobs[i].LastError = ""
		if err != nil {
			st.Jobs[i].LastError = err.Error()
		}
		updated = true
		break
	}
	if !updated {
		item := JobStatus{Job: job, LastRun: now, Runs: 1}
		if err != nil {
			item.LastError = err.Error()
		}
		st.Jobs = append(st.Jobs, item)
	}
	_ = WriteState(st)
}

// StateJob 在状态文件里按名字查找某个任务的执行历史。
func StateJob(name string) (JobStatus, bool) {
	jobs := readStateJobs()
	if jobs == nil {
		return JobStatus{}, false
	}
	st, ok := jobs[strings.TrimSpace(name)]
	return st, ok
}

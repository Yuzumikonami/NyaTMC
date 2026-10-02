package daemon

import (
	"strings"
	"sync"
)

// ringBuffer 是固定容量的行环形缓冲，用于 `nyatmc logs` 与 TUI/Web 的即时回看。
type ringBuffer struct {
	mu    sync.RWMutex
	buf   []string
	size  int
	start int
	count int
}

func newRingBuffer(size int) *ringBuffer {
	if size <= 0 {
		size = 500
	}
	return &ringBuffer{buf: make([]string, size), size: size}
}

// Add 追加一行。
func (r *ringBuffer) Add(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	idx := (r.start + r.count) % r.size
	if r.count == r.size {
		r.buf[r.start] = line
		r.start = (r.start + 1) % r.size
		return
	}
	r.buf[idx] = line
	r.count++
}

// Lines 返回最后 n 行（n<=0 表示全部）。
func (r *ringBuffer) Lines(n int) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.count == 0 {
		return nil
	}
	if n <= 0 || n > r.count {
		n = r.count
	}
	out := make([]string, 0, n)
	from := r.count - n
	for i := from; i < r.count; i++ {
		out = append(out, r.buf[(r.start+i)%r.size])
	}
	return out
}

// Len 返回已缓存行数。
func (r *ringBuffer) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.count
}

// Search 返回包含关键词的最后 n 行。
func (r *ringBuffer) Search(keyword string, n int) []string {
	all := r.Lines(0)
	kw := strings.ToLower(keyword)
	var out []string
	for i := len(all) - 1; i >= 0 && (n <= 0 || len(out) < n); i-- {
		if strings.Contains(strings.ToLower(all[i]), kw) {
			out = append(out, all[i])
		}
	}
	// 反转回时间顺序
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

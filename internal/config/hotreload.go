package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/pelletier/go-toml/v2"
)

// Manager 持有当前配置，并在配置文件变化时热重载、通知订阅者。
//
// 约定：Current() 返回的 *Config 只读，任何修改都必须通过 Update() 或 Save()。
type Manager struct {
	path string

	mu     sync.RWMutex
	cfg    *Config
	subs   map[int]func(*Config)
	nextID int

	onErr   func(error)
	lastSav time.Time

	w    *fsnotify.Watcher
	done chan struct{}
	once sync.Once
}

// NewManager 加载配置并创建管理器（配置不存在时按模板创建）。
func NewManager(path string) (*Manager, error) {
	if strings.TrimSpace(path) == "" {
		path = ConfigPath()
	}
	path = ExpandPath(path)
	cfg, err := LoadOrCreate(path)
	if err != nil {
		return nil, err
	}
	return &Manager{path: path, cfg: cfg, subs: map[int]func(*Config){}, done: make(chan struct{})}, nil
}

// Path 返回配置文件路径。
func (m *Manager) Path() string { return m.path }

// Current 返回当前配置（只读）。
func (m *Manager) Current() *Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

// Resolve 是 Current().Resolve() 的快捷方式。
func (m *Manager) Resolve(instance string) Resolved { return m.Current().Resolve(instance) }

// SetErrorHandler 注册热重载错误回调（例如配置文件被写坏）。
func (m *Manager) SetErrorHandler(fn func(error)) {
	m.mu.Lock()
	m.onErr = fn
	m.mu.Unlock()
}

// Subscribe 注册配置变化回调，返回取消订阅函数。
func (m *Manager) Subscribe(fn func(*Config)) func() {
	if fn == nil {
		return func() {}
	}
	m.mu.Lock()
	id := m.nextID
	m.nextID++
	m.subs[id] = fn
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		delete(m.subs, id)
		m.mu.Unlock()
	}
}

// Reload 重新读盘。
func (m *Manager) Reload() (*Config, error) {
	cfg, err := Load(m.path)
	if err != nil {
		return nil, err
	}
	return m.apply(cfg), nil
}

func (m *Manager) apply(cfg *Config) *Config {
	m.mu.Lock()
	m.cfg = cfg
	subs := make([]func(*Config), 0, len(m.subs))
	for _, fn := range m.subs {
		subs = append(subs, fn)
	}
	m.mu.Unlock()
	for _, fn := range subs {
		func() {
			defer func() { recover() }() // 单个订阅者 panic 不影响其它订阅者
			fn(cfg)
		}()
	}
	return cfg
}

// Save 写盘并立即生效（不需要等文件监听）。
func (m *Manager) Save(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("配置为空")
	}
	m.mu.Lock()
	m.lastSav = time.Now()
	m.mu.Unlock()
	if err := cfg.SaveTo(m.path); err != nil {
		return err
	}
	m.apply(cfg)
	return nil
}

// Update 以读写锁保护的方式修改配置并写盘。
func (m *Manager) Update(fn func(*Config) error) (*Config, error) {
	if fn == nil {
		return m.Current(), nil
	}
	cur := m.Current()
	// 复制一份再修改，避免修改失败时污染内存中的配置
	clone, err := cloneConfig(cur)
	if err != nil {
		return nil, err
	}
	if err := fn(clone); err != nil {
		return nil, err
	}
	if err := m.Save(clone); err != nil {
		return nil, err
	}
	return m.Current(), nil
}

func cloneConfig(c *Config) (*Config, error) {
	data, err := toml.Marshal(c)
	if err != nil {
		return nil, err
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, err
	}
	cfg.Path = c.Path
	return cfg, nil
}

// Start 开始监听文件变化，ctx 结束时停止。
func (m *Manager) Start(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("创建文件监听失败: %w", err)
	}
	m.w = w
	// 监听目录而不是文件本身：很多编辑器会“写临时文件 + 改名”替换配置。
	if err := w.Add(filepath.Dir(m.path)); err != nil {
		_ = w.Close()
		return fmt.Errorf("监听配置目录失败: %w", err)
	}

	base := filepath.Base(m.path)
	go func() {
		var timer *time.Timer
		trigger := func() {
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(250*time.Millisecond, func() {
				m.mu.RLock()
				recent := time.Since(m.lastSav) < 700*time.Millisecond
				m.mu.RUnlock()
				if recent {
					return // 自己刚写盘，跳过
				}
				if _, err := m.Reload(); err != nil {
					m.mu.RLock()
					fn := m.onErr
					m.mu.RUnlock()
					if fn != nil {
						fn(err)
					}
				}
			})
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-m.done:
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				name := filepath.Base(ev.Name)
				if name == base || strings.HasPrefix(name, base+".") {
					trigger()
				}
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				m.mu.RLock()
				fn := m.onErr
				m.mu.RUnlock()
				if fn != nil && err != nil {
					fn(err)
				}
			}
		}
	}()
	return nil
}

// Close 停止监听。
func (m *Manager) Close() error {
	var err error
	m.once.Do(func() {
		close(m.done)
		if m.w != nil {
			err = m.w.Close()
		}
	})
	return err
}

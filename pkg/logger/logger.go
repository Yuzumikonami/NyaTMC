package logger

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Level 是日志级别。
type Level int

// 日志级别定义。
const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
	LevelOff
)

// String 返回级别名。
func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "OFF"
	}
}

// ParseLevel 解析级别名。
func ParseLevel(s string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug", "d", "verbose":
		return LevelDebug, nil
	case "info", "i", "":
		return LevelInfo, nil
	case "warn", "warning", "w":
		return LevelWarn, nil
	case "error", "err", "e":
		return LevelError, nil
	case "off", "none", "silent":
		return LevelOff, nil
	}
	return LevelInfo, fmt.Errorf("未知日志级别 %q（可选 debug/info/warn/error/off）", s)
}

// Logger 是一个带级别与可选颜色的简单日志器。
type Logger struct {
	mu     sync.Mutex
	w      io.Writer
	level  Level
	prefix string
	color  bool
	closer io.Closer
}

// New 创建日志器。
func New(w io.Writer, level Level) *Logger {
	if w == nil {
		w = io.Discard
	}
	return &Logger{w: w, level: level, color: isTerminal(w)}
}

// NewFile 创建按大小轮转的文件日志器，返回日志器与底层 writer（Close 时一并关闭）。
func NewFile(path string, level Level, opts RotateOptions) (*Logger, *RotatingWriter, error) {
	opts.Path = path
	w, err := NewRotatingWriter(opts)
	if err != nil {
		return nil, nil, err
	}
	l := &Logger{w: w, level: level, closer: w}
	return l, w, nil
}

// Discard 返回一个什么都不写的日志器。
func Discard() *Logger { return &Logger{w: io.Discard, level: LevelOff} }

// SetLevel 调整级别。
func (l *Logger) SetLevel(level Level) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.level = level
	l.mu.Unlock()
}

// Level 返回当前级别。
func (l *Logger) Level() Level {
	if l == nil {
		return LevelOff
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.level
}

// SetPrefix 设置每条日志的前缀。
func (l *Logger) SetPrefix(prefix string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.prefix = prefix
	l.mu.Unlock()
}

// SetColor 开关颜色。
func (l *Logger) SetColor(on bool) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.color = on
	l.mu.Unlock()
}

// Writer 返回底层 writer（可用于接管标准库 log 输出）。
func (l *Logger) Writer() io.Writer { return l.w }

// Close 关闭底层文件。
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closer != nil {
		err := l.closer.Close()
		l.closer = nil
		return err
	}
	return nil
}

const (
	ansiReset  = "\x1b[0m"
	ansiRed    = "\x1b[31m"
	ansiYellow = "\x1b[33m"
	ansiCyan   = "\x1b[36m"
	ansiGray   = "\x1b[90m"
)

func (l *Logger) logf(level Level, format string, args ...any) {
	if l == nil {
		return
	}
	l.mu.Lock()
	if level < l.level || l.level == LevelOff {
		l.mu.Unlock()
		return
	}
	w := l.w
	prefix := l.prefix
	color := l.color
	l.mu.Unlock()

	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	stamp := time.Now().Format("2006-01-02 15:04:05")
	head := fmt.Sprintf("%s %-5s", stamp, level.String())
	if prefix != "" {
		head = fmt.Sprintf("%s [%s] %-5s", stamp, prefix, level.String())
	}
	if color {
		c := ansiGray
		switch level {
		case LevelWarn:
			c = ansiYellow
		case LevelError:
			c = ansiRed
		case LevelInfo:
			c = ansiCyan
		}
		head = c + head + ansiReset
	}
	_, _ = fmt.Fprintf(w, "%s %s\n", head, msg)
}

// Debugf 输出 debug 日志。
func (l *Logger) Debugf(format string, args ...any) { l.logf(LevelDebug, format, args...) }

// Infof 输出 info 日志。
func (l *Logger) Infof(format string, args ...any) { l.logf(LevelInfo, format, args...) }

// Warnf 输出 warn 日志。
func (l *Logger) Warnf(format string, args ...any) { l.logf(LevelWarn, format, args...) }

// Errorf 输出 error 日志。
func (l *Logger) Errorf(format string, args ...any) { l.logf(LevelError, format, args...) }

// Debug 输出 debug 日志（非格式化）。
func (l *Logger) Debug(args ...any) { l.logf(LevelDebug, "%s", fmt.Sprint(args...)) }

// Info 输出 info 日志（非格式化）。
func (l *Logger) Info(args ...any) { l.logf(LevelInfo, "%s", fmt.Sprint(args...)) }

// Warn 输出 warn 日志（非格式化）。
func (l *Logger) Warn(args ...any) { l.logf(LevelWarn, "%s", fmt.Sprint(args...)) }

// Error 输出 error 日志（非格式化）。
func (l *Logger) Error(args ...any) { l.logf(LevelError, "%s", fmt.Sprint(args...)) }

// ---------------------------------------------------------------- 全局默认

var (
	defaultMu  sync.RWMutex
	defaultLog = New(os.Stderr, LevelInfo)
)

// SetDefault 设置全局日志器。
func SetDefault(l *Logger) {
	if l == nil {
		return
	}
	defaultMu.Lock()
	defaultLog = l
	defaultMu.Unlock()
}

// Default 返回全局日志器。
func Default() *Logger {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultLog
}

// Infof 使用全局日志器输出。
func Infof(format string, args ...any) { Default().Infof(format, args...) }

// Warnf 使用全局日志器输出。
func Warnf(format string, args ...any) { Default().Warnf(format, args...) }

// Errorf 使用全局日志器输出。
func Errorf(format string, args ...any) { Default().Errorf(format, args...) }

// Debugf 使用全局日志器输出。
func Debugf(format string, args ...any) { Default().Debugf(format, args...) }

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

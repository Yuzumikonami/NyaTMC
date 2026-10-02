// Package logger 提供 nyatmc 自己的日志输出与日志轮转能力。
//
// 轮转策略（rotate.go）：
//   - nyatmc 自己写出的日志（控制台捕获、supervisor 日志）由 RotatingWriter 在线切割，随时可控；
//   - Minecraft 服务端的 logs/latest.log 由 JVM 持有文件句柄，无法在线截断，
//     因此采用“归档压缩 + 保留策略 + 可选优雅重启切割”的方案（见 ArchiveFile / PruneDir）。
package logger

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// RotateOptions 描述轮转规则，零值字段表示不启用对应限制。
type RotateOptions struct {
	// Path 是当前写入的文件。
	Path string
	// MaxSize 是单文件最大字节数，0 表示不按大小轮转。
	MaxSize int64
	// MaxBackups 是保留的历史文件份数，0 表示不按份数清理。
	MaxBackups int
	// MaxAge 是历史文件最长保留时间，0 表示不按时间清理。
	MaxAge time.Duration
	// Compress 是否压缩历史文件（.gz）。
	Compress bool
	// SuffixTimeFormat 是历史文件时间戳格式。
	SuffixTimeFormat string
}

// RotatingWriter 是一个按大小自动轮转的 io.WriteCloser。
type RotatingWriter struct {
	mu   sync.Mutex
	opts RotateOptions
	file *os.File
	size int64
}

// NewRotatingWriter 打开（或创建）目标文件。
func NewRotatingWriter(opts RotateOptions) (*RotatingWriter, error) {
	if strings.TrimSpace(opts.Path) == "" {
		return nil, fmt.Errorf("日志路径不能为空")
	}
	if opts.SuffixTimeFormat == "" {
		opts.SuffixTimeFormat = "20060102-150405"
	}
	w := &RotatingWriter{opts: opts}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *RotatingWriter) open() error {
	if err := os.MkdirAll(filepath.Dir(w.opts.Path), 0o755); err != nil {
		return fmt.Errorf("创建日志目录失败: %w", err)
	}
	f, err := os.OpenFile(w.opts.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("打开日志文件 %s 失败: %w", w.opts.Path, err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("读取日志文件信息失败: %w", err)
	}
	w.file = f
	w.size = st.Size()
	return nil
}

// Write 实现 io.Writer。
func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	if w.opts.MaxSize > 0 && w.size+int64(len(p)) > w.opts.MaxSize && w.size > 0 {
		if err := w.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// Size 返回当前文件大小。
func (w *RotatingWriter) Size() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.size
}

// Rotate 立即归档当前文件并重新开始写入。
func (w *RotatingWriter) Rotate() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.rotateLocked()
}

func (w *RotatingWriter) rotateLocked() error {
	if w.file != nil {
		_ = w.file.Sync()
		_ = w.file.Close()
		w.file = nil
	}
	if st, err := os.Stat(w.opts.Path); err == nil && st.Size() > 0 {
		archive := w.archiveName()
		if err := os.Rename(w.opts.Path, archive); err != nil {
			// 重命名失败时退回复制，尽量不丢日志
			if cerr := copyFile(w.opts.Path, archive); cerr != nil {
				return fmt.Errorf("归档日志失败: %w", err)
			}
			_ = os.Remove(w.opts.Path)
		}
		if w.opts.Compress {
			if err := CompressFile(archive, true); err != nil {
				return err
			}
		}
	}
	if err := w.open(); err != nil {
		return err
	}
	return nil
}

func (w *RotatingWriter) archiveName() string {
	ext := filepath.Ext(w.opts.Path)
	base := strings.TrimSuffix(w.opts.Path, ext)
	stamp := time.Now().Format(w.opts.SuffixTimeFormat)
	return fmt.Sprintf("%s-%s%s", base, stamp, ext)
}

// Close 关闭文件并按保留策略清理历史文件。
func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	_ = PruneFiles(w.opts.Path, w.opts.MaxBackups, w.opts.MaxAge)
	return err
}

// PruneFiles 按“保留份数 + 最长保留时间”清理 file 对应的历史归档。
//
// 历史文件是 file 的同目录下、以 file 主名加时间戳后缀的兄弟文件（.gz 也算）。
func PruneFiles(file string, maxBackups int, maxAge time.Duration) error {
	ext := filepath.Ext(file)
	base := strings.TrimSuffix(file, ext)
	dir := filepath.Dir(file)
	prefix := filepath.Base(base) + "-"

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	type item struct {
		path string
		mod  time.Time
	}
	var items []item
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		if !strings.HasSuffix(name, ext) && !strings.HasSuffix(name, ext+".gz") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, item{path: filepath.Join(dir, name), mod: info.ModTime()})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.After(items[j].mod) })

	now := time.Now()
	for i, it := range items {
		byCount := maxBackups > 0 && i >= maxBackups
		byAge := maxAge > 0 && now.Sub(it.mod) > maxAge
		if byCount || byAge {
			if err := os.Remove(it.path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("删除历史日志 %s 失败: %w", it.path, err)
			}
		}
	}
	return nil
}

// CompressFile 把 path 压缩为 path.gz，成功后可删除原文件。
func CompressFile(path string, removeOriginal bool) error {
	in, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("打开待压缩文件 %s 失败: %w", path, err)
	}
	defer in.Close()

	outPath := path + ".gz"
	tmp := outPath + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("创建压缩文件 %s 失败: %w", outPath, err)
	}
	zw, err := gzip.NewWriterLevel(out, gzip.BestSpeed)
	if err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if _, err := io.Copy(zw, in); err != nil {
		_ = zw.Close()
		_ = out.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("压缩 %s 失败: %w", path, err)
	}
	if err := zw.Close(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = in.Close()
	if err := os.Rename(tmp, outPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("写出压缩文件失败: %w", err)
	}
	if removeOriginal {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("删除原始日志 %s 失败: %w", path, err)
		}
	}
	return nil
}

// ArchiveFile 把任意文件归档为 <archiveDir>/<名字>-<时间戳>.log(.gz)。
//
// 用于服务端的 logs/latest.log：JVM 持有句柄，无法在线清空，
// 因此这里只做“压缩留存 + 记录进度”，真正的切割由优雅重启或服务端自身完成。
func ArchiveFile(path, archiveDir string, compress bool) (string, error) {
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("待归档文件 %s 不存在: %w", path, err)
	}
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return "", fmt.Errorf("创建归档目录失败: %w", err)
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(filepath.Base(path), ext)
	stamp := time.Now().Format("20060102-150405")
	dst := filepath.Join(archiveDir, fmt.Sprintf("%s-%s%s", base, stamp, ext))
	if err := copyFile(path, dst); err != nil {
		return "", err
	}
	if compress {
		if err := CompressFile(dst, true); err != nil {
			return dst, err
		}
		dst += ".gz"
	}
	return dst, nil
}

// PruneDir 按份数与时间清理目录下的归档文件，返回删除的文件列表。
func PruneDir(dir string, maxFiles int, maxAge time.Duration) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	type item struct {
		path string
		mod  time.Time
	}
	var items []item
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, item{path: filepath.Join(dir, e.Name()), mod: info.ModTime()})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.After(items[j].mod) })

	var removed []string
	now := time.Now()
	for i, it := range items {
		byCount := maxFiles > 0 && i >= maxFiles
		byAge := maxAge > 0 && now.Sub(it.mod) > maxAge
		if !byCount && !byAge {
			continue
		}
		if err := os.Remove(it.path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return removed, fmt.Errorf("删除 %s 失败: %w", it.path, err)
		}
		removed = append(removed, it.path)
	}
	return removed, nil
}

// DirSize 返回目录总字节数（用于容量提示）。
func DirSize(dir string) (int64, error) {
	var total int64
	err := filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	if err != nil && os.IsNotExist(err) {
		return 0, nil
	}
	return total, err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("打开 %s 失败: %w", src, err)
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("创建 %s 失败: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("复制到 %s 失败: %w", dst, err)
	}
	if err := out.Close(); err != nil {
		return err
	}
	return nil
}

// Package backup 实现世界存档的压缩备份、回滚与保留策略。
//
// 备份文件默认放在 <实例目录>/backups/ 下，命名形如
//
//	survival-auto-20260101-040000.tar.gz
//
// 打包内容来自配置的 backup.include，排除 backup.exclude（支持 * 通配）。
// 为了安全，所有路径都会被限制在服务器目录内，符号链接一律跳过。
package backup

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yuzumikonami/nyatmc/internal/instance"
	"github.com/yuzumikonami/nyatmc/pkg/logger"
)

// Archive 描述一个备份文件。
type Archive struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
	Instance  string    `json:"instance"`
	Label     string    `json:"label"`
	Format    string    `json:"format"`
}

// HumanSize 返回可读大小。
func (a Archive) HumanSize() string { return HumanSize(a.Size) }

// HumanSize 把字节数格式化为可读字符串（整数倍时不带小数，例如 1GB / 3.5MB）。
func HumanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v := float64(n)
	for _, u := range units {
		v /= unit
		if v < unit {
			if v == math.Trunc(v) {
				return fmt.Sprintf("%.0f%s", v, u)
			}
			return fmt.Sprintf("%.1f%s", v, u)
		}
	}
	return fmt.Sprintf("%.1fPB", v/unit)
}

// Options 描述一次备份。
type Options struct {
	Include []string
	Exclude []string
	// Format 支持 tar.gz / tar / zip。
	Format string
	// Label 会写进文件名，便于区分 auto / manual 等来源。
	Label  string
	Logger *logger.Logger
	// Now 便于测试注入时间。
	Now func() time.Time
}

// CreateDefault 用实例配置里的备份设置执行一次备份。
func CreateDefault(inst *instance.Instance, label string, log *logger.Logger) (Archive, error) {
	cfg := inst.Res.Backup
	return Create(inst, Options{
		Include: cfg.Include,
		Exclude: cfg.Exclude,
		Format:  cfg.Format,
		Label:   label,
		Logger:  log,
	})
}

// CreateAndPrune 备份后按配置清理历史备份。
func CreateAndPrune(inst *instance.Instance, label string, log *logger.Logger) (Archive, []string, error) {
	ar, err := CreateDefault(inst, label, log)
	if err != nil {
		return Archive{}, nil, err
	}
	removed, err := Prune(inst, inst.Res.Backup.Keep, inst.Res.Backup.MaxAge.Std(), log)
	if err != nil {
		// 清理失败不影响备份本身
		if log != nil {
			log.Warnf("清理历史备份失败：%v", err)
		}
	}
	return ar, removed, nil
}

// Create 执行一次备份。
func Create(inst *instance.Instance, opts Options) (Archive, error) {
	log := opts.Logger
	if log == nil {
		log = logger.Discard()
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	format := normalizeFormat(opts.Format)
	dir := inst.Res.BackupDir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Archive{}, fmt.Errorf("创建备份目录 %s 失败: %w", dir, err)
	}

	targets, err := collectTargets(inst.Res.Path, opts.Include, opts.Exclude)
	if err != nil {
		return Archive{}, err
	}
	if len(targets) == 0 {
		return Archive{}, fmt.Errorf("没有可备份的内容：请检查 backup.include（服务器目录 %s）", inst.Res.Path)
	}

	label := sanitizeLabel(opts.Label)
	name := fmt.Sprintf("%s-%s-%s.%s", inst.Name, label, now().Format("20060102-150405"), format)
	dest := filepath.Join(dir, name)
	tmp := dest + ".part"

	var written int64
	var files int
	switch format {
	case "zip":
		files, written, err = createZip(tmp, inst.Res.Path, targets)
	default:
		files, written, err = createTar(tmp, inst.Res.Path, targets, format == "tar.gz")
	}
	if err != nil {
		_ = os.Remove(tmp)
		return Archive{}, err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return Archive{}, fmt.Errorf("写出备份文件失败: %w", err)
	}

	ar := Archive{
		Name:      name,
		Path:      dest,
		Size:      written,
		CreatedAt: now(),
		Instance:  inst.Name,
		Label:     label,
		Format:    format,
	}
	log.Infof("备份完成：%s（%d 个文件，%s）", name, files, HumanSize(written))
	return ar, nil
}

func normalizeFormat(f string) string {
	switch strings.ToLower(strings.TrimSpace(f)) {
	case "zip":
		return "zip"
	case "tar":
		return "tar"
	default:
		return "tar.gz"
	}
}

func sanitizeLabel(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return "manual"
	}
	var b strings.Builder
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "manual"
	}
	return out
}

// collectTargets 把 include 展开成实际存在的文件/目录列表（相对路径）。
func collectTargets(serverDir string, include, exclude []string) ([]string, error) {
	if len(include) == 0 {
		include = []string{"."}
	}
	seen := map[string]bool{}
	var out []string
	for _, raw := range include {
		rel, err := safeRel(serverDir, raw)
		if err != nil {
			return nil, err
		}
		abs := filepath.Join(serverDir, rel)
		st, err := os.Lstat(abs)
		if err != nil {
			// 不存在的条目直接跳过（不同服务端类型目录结构不同）
			continue
		}
		if matchesAny(rel, exclude) {
			continue
		}
		if st.IsDir() {
			err := filepath.Walk(abs, func(path string, info os.FileInfo, werr error) error {
				if werr != nil {
					return nil
				}
				childRel, rerr := filepath.Rel(serverDir, path)
				if rerr != nil {
					return nil
				}
				childRel = filepath.ToSlash(childRel)
				if childRel == "." {
					return nil
				}
				if info.IsDir() {
					if matchesAny(childRel, exclude) {
						return filepath.SkipDir
					}
					return nil
				}
				if info.Mode()&os.ModeSymlink != 0 {
					return nil
				}
				if matchesAny(childRel, exclude) {
					return nil
				}
				if !seen[childRel] {
					seen[childRel] = true
					out = append(out, childRel)
				}
				return nil
			})
			if err != nil {
				return nil, fmt.Errorf("遍历 %s 失败: %w", abs, err)
			}
			continue
		}
		if st.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if !seen[rel] {
			seen[rel] = true
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out, nil
}

// safeRel 校验相对路径，拒绝绝对路径与向上穿越。
func safeRel(base, rel string) (string, error) {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	if rel == "" {
		return "", fmt.Errorf("备份路径不能为空")
	}
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("备份路径 %q 必须是相对服务器目录的路径", rel)
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("备份路径 %q 不能越出服务器目录", rel)
	}
	return clean, nil
}

// matchesAny 用 * 通配匹配相对路径、基名或目录前缀。
func matchesAny(rel string, patterns []string) bool {
	rel = filepath.ToSlash(rel)
	base := filepath.Base(rel)
	for _, p := range patterns {
		p = filepath.ToSlash(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if p == rel || p == base {
			return true
		}
		// 目录前缀：排除 logs 时，logs/latest.log 也要命中
		if strings.HasSuffix(p, "/") && strings.HasPrefix(rel, p) {
			return true
		}
		if strings.HasPrefix(rel, p+"/") {
			return true
		}
		if ok, _ := filepath.Match(p, rel); ok {
			return true
		}
		if ok, _ := filepath.Match(p, base); ok {
			return true
		}
	}
	return false
}

func createTar(dest, serverDir string, targets []string, gzipIt bool) (int, int64, error) {
	f, err := os.Create(dest)
	if err != nil {
		return 0, 0, fmt.Errorf("创建备份文件失败: %w", err)
	}
	var w io.Writer = f
	var gz *gzip.Writer
	if gzipIt {
		gz, err = gzip.NewWriterLevel(f, gzip.BestSpeed)
		if err != nil {
			_ = f.Close()
			return 0, 0, err
		}
		w = gz
	}
	tw := tar.NewWriter(w)

	files := 0
	for _, rel := range targets {
		abs := filepath.Join(serverDir, rel)
		info, err := os.Lstat(abs)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			continue
		}
		hdr.Name = filepath.ToSlash(rel)
		hdr.ModTime = info.ModTime()
		if err := tw.WriteHeader(hdr); err != nil {
			_ = tw.Close()
			_ = f.Close()
			return files, 0, fmt.Errorf("写入备份头失败: %w", err)
		}
		if info.IsDir() {
			files++
			continue
		}
		in, err := os.Open(abs)
		if err != nil {
			continue
		}
		if _, err := io.Copy(tw, in); err != nil {
			_ = in.Close()
			_ = tw.Close()
			_ = f.Close()
			return files, 0, fmt.Errorf("写入 %s 失败: %w", rel, err)
		}
		_ = in.Close()
		files++
	}

	if err := tw.Close(); err != nil {
		_ = f.Close()
		return files, 0, err
	}
	if gz != nil {
		if err := gz.Close(); err != nil {
			_ = f.Close()
			return files, 0, err
		}
	}
	if err := f.Close(); err != nil {
		return files, 0, err
	}
	st, err := os.Stat(dest)
	if err != nil {
		return files, 0, err
	}
	return files, st.Size(), nil
}

func createZip(dest, serverDir string, targets []string) (int, int64, error) {
	f, err := os.Create(dest)
	if err != nil {
		return 0, 0, fmt.Errorf("创建备份文件失败: %w", err)
	}
	zw := zip.NewWriter(f)
	files := 0
	for _, rel := range targets {
		abs := filepath.Join(serverDir, rel)
		info, err := os.Lstat(abs)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || info.IsDir() {
			continue
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			continue
		}
		hdr.Name = filepath.ToSlash(rel)
		hdr.Method = zip.Deflate
		entry, err := zw.CreateHeader(hdr)
		if err != nil {
			_ = zw.Close()
			_ = f.Close()
			return files, 0, err
		}
		in, err := os.Open(abs)
		if err != nil {
			continue
		}
		if _, err := io.Copy(entry, in); err != nil {
			_ = in.Close()
			_ = zw.Close()
			_ = f.Close()
			return files, 0, fmt.Errorf("写入 %s 失败: %w", rel, err)
		}
		_ = in.Close()
		files++
	}
	if err := zw.Close(); err != nil {
		_ = f.Close()
		return files, 0, err
	}
	if err := f.Close(); err != nil {
		return files, 0, err
	}
	st, err := os.Stat(dest)
	if err != nil {
		return files, 0, err
	}
	return files, st.Size(), nil
}

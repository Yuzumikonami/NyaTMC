package backup

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yuzumikonami/nyatmc/internal/instance"
	"github.com/yuzumikonami/nyatmc/pkg/logger"
)

// List 返回实例的全部备份，按时间从新到旧。
func List(inst *instance.Instance) ([]Archive, error) {
	dir := inst.Res.BackupDir
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取备份目录 %s 失败: %w", dir, err)
	}
	var out []Archive
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		format := ""
		switch {
		case strings.HasSuffix(name, ".tar.gz"):
			format = "tar.gz"
		case strings.HasSuffix(name, ".tar"):
			format = "tar"
		case strings.HasSuffix(name, ".zip"):
			format = "zip"
		default:
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Archive{
			Name:      name,
			Path:      filepath.Join(dir, name),
			Size:      info.Size(),
			CreatedAt: info.ModTime(),
			Instance:  inst.Name,
			Label:     parseLabel(name, inst.Name, format),
			Format:    format,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// parseLabel 从 `<实例>-<标签>-<时间戳>.<格式>` 里取出标签。
func parseLabel(name, instName, format string) string {
	base := strings.TrimSuffix(strings.TrimSuffix(name, ".gz"), "."+format)
	base = strings.TrimSuffix(base, ".tar")
	base = strings.TrimSuffix(base, ".zip")
	prefix := instName + "-"
	if !strings.HasPrefix(base, prefix) {
		return "unknown"
	}
	rest := strings.TrimPrefix(base, prefix)
	// 去掉结尾的 -YYYYMMDD-HHMMSS
	if len(rest) > 16 {
		tail := rest[len(rest)-15:]
		if _, err := time.Parse("20060102-150405", tail); err == nil {
			return strings.TrimSuffix(rest[:len(rest)-16], "-")
		}
	}
	return rest
}

// Find 按名字、路径或 "latest" 定位一个备份。
func Find(inst *instance.Instance, ref string) (Archive, error) {
	ref = strings.TrimSpace(ref)
	if ref != "" && ref != "latest" {
		if st, err := os.Stat(ref); err == nil && st.Mode().IsRegular() {
			return Archive{
				Name:      filepath.Base(ref),
				Path:      ref,
				Size:      st.Size(),
				CreatedAt: st.ModTime(),
				Instance:  inst.Name,
				Label:     parseLabel(filepath.Base(ref), inst.Name, extFormat(ref)),
				Format:    extFormat(ref),
			}, nil
		}
	}
	list, err := List(inst)
	if err != nil {
		return Archive{}, err
	}
	if len(list) == 0 {
		return Archive{}, fmt.Errorf("实例 %s 还没有任何备份", inst.Name)
	}
	if ref == "" || ref == "latest" {
		return list[0], nil
	}
	for _, a := range list {
		if a.Name == ref || strings.TrimSuffix(a.Name, filepath.Ext(a.Name)) == ref {
			return a, nil
		}
	}
	return Archive{}, fmt.Errorf("找不到备份 %q（用 nyatmc backup list 查看可用备份）", ref)
}

func extFormat(path string) string {
	switch {
	case strings.HasSuffix(path, ".tar.gz"):
		return "tar.gz"
	case strings.HasSuffix(path, ".zip"):
		return "zip"
	default:
		return "tar"
	}
}

// Prune 按保留份数与最长保留时间清理备份，返回被删除的文件路径。
func Prune(inst *instance.Instance, keep int, maxAge time.Duration, log *logger.Logger) ([]string, error) {
	if keep <= 0 && maxAge <= 0 {
		return nil, nil
	}
	list, err := List(inst)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var removed []string
	for i, a := range list {
		byCount := keep > 0 && i >= keep
		byAge := maxAge > 0 && now.Sub(a.CreatedAt) > maxAge
		if !byCount && !byAge {
			continue
		}
		if err := os.Remove(a.Path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return removed, fmt.Errorf("删除旧备份 %s 失败: %w", a.Path, err)
		}
		removed = append(removed, a.Path)
		if log != nil {
			log.Infof("已清理旧备份：%s", a.Name)
		}
	}
	return removed, nil
}

// RestoreOptions 描述回滚行为。
type RestoreOptions struct {
	// BeforeBackup 回滚前先备份当前状态。
	BeforeBackup bool
	// Targets 只回滚这些相对路径，为空表示整包回滚。
	Targets []string
	Logger  *logger.Logger
}

// RestoreResult 是回滚结果。
type RestoreResult struct {
	Files  int      `json:"files"`
	Bytes  int64    `json:"bytes"`
	Paths  []string `json:"paths"`
	Backup *Archive `json:"backup,omitempty"`
}

// Restore 把备份解包回服务器目录。
func Restore(inst *instance.Instance, archivePath string, opts RestoreOptions) (RestoreResult, error) {
	log := opts.Logger
	if log == nil {
		log = logger.Discard()
	}
	res := RestoreResult{}
	if _, err := os.Stat(archivePath); err != nil {
		return res, fmt.Errorf("备份文件不存在：%s", archivePath)
	}

	if opts.BeforeBackup {
		ar, err := CreateDefault(inst, "before-restore", log)
		if err != nil {
			return res, fmt.Errorf("回滚前自动备份失败（已中止，避免丢失当前状态）：%w", err)
		}
		res.Backup = &ar
	}

	format := extFormat(archivePath)
	var files int
	var bytes int64
	var err error
	switch format {
	case "zip":
		files, bytes, err = restoreZip(archivePath, inst.Res.Path, opts.Targets)
	default:
		files, bytes, err = restoreTar(archivePath, inst.Res.Path, opts.Targets, format == "tar.gz")
	}
	if err != nil {
		return res, err
	}
	res.Files = files
	res.Bytes = bytes
	log.Infof("回滚完成：%s 个文件，%s", fmt.Sprint(files), HumanSize(bytes))
	return res, nil
}

func restoreTar(archivePath, destDir string, targets []string, gzipIt bool) (int, int64, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return 0, 0, fmt.Errorf("打开备份失败: %w", err)
	}
	defer f.Close()

	var r io.Reader = f
	if gzipIt {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return 0, 0, fmt.Errorf("备份不是有效的 gzip：%w", err)
		}
		defer gz.Close()
		r = gz
	}

	tr := tar.NewReader(r)
	files := 0
	var bytes int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return files, bytes, fmt.Errorf("读取备份失败: %w", err)
		}
		target, skip, err := resolveEntry(destDir, hdr.Name, targets)
		if err != nil {
			return files, bytes, err
		}
		if skip {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return files, bytes, fmt.Errorf("创建目录 %s 失败: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return files, bytes, fmt.Errorf("创建目录失败: %w", err)
			}
			n, err := writeFile(target, tr, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return files, bytes, err
			}
			files++
			bytes += n
		default:
			// 符号链接等类型一律跳过，避免把备份里的链接写到服务器目录外
			continue
		}
	}
	return files, bytes, nil
}

func restoreZip(archivePath, destDir string, targets []string) (int, int64, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return 0, 0, fmt.Errorf("打开备份失败: %w", err)
	}
	defer zr.Close()

	files := 0
	var bytes int64
	for _, zf := range zr.File {
		target, skip, err := resolveEntry(destDir, zf.Name, targets)
		if err != nil {
			return files, bytes, err
		}
		if skip {
			continue
		}
		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return files, bytes, err
			}
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return files, bytes, fmt.Errorf("读取 %s 失败: %w", zf.Name, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			_ = rc.Close()
			return files, bytes, err
		}
		n, err := writeFile(target, rc, zf.Mode()&0o777)
		_ = rc.Close()
		if err != nil {
			return files, bytes, err
		}
		files++
		bytes += n
	}
	return files, bytes, nil
}

func writeFile(target string, src io.Reader, mode os.FileMode) (int64, error) {
	if mode == 0 {
		mode = 0o644
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return 0, fmt.Errorf("写入 %s 失败: %w", target, err)
	}
	n, err := io.Copy(out, src)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return n, fmt.Errorf("写入 %s 失败: %w", target, err)
	}
	return n, nil
}

// resolveEntry 校验备份内的路径，返回落地绝对路径与是否跳过。
func resolveEntry(destDir, name string, targets []string) (string, bool, error) {
	clean := filepath.Clean(filepath.ToSlash(name))
	if clean == "." || clean == "" {
		return "", true, nil
	}
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", false, fmt.Errorf("备份内的路径 %q 非法，已中止回滚", name)
	}
	if len(targets) > 0 && !matchesAny(clean, targets) && !matchesAnyPrefix(clean, targets) {
		return "", true, nil
	}
	target := filepath.Join(destDir, filepath.FromSlash(clean))
	absDest, err := filepath.Abs(destDir)
	if err != nil {
		return "", false, err
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", false, err
	}
	if absTarget != absDest && !strings.HasPrefix(absTarget, absDest+string(os.PathSeparator)) {
		return "", false, fmt.Errorf("备份内的路径 %q 会写到服务器目录之外，已中止回滚", name)
	}
	return absTarget, false, nil
}

// matchesAnyPrefix 判断路径是否位于目标目录之内（回滚部分目录时用）。
func matchesAnyPrefix(rel string, targets []string) bool {
	rel = filepath.ToSlash(rel)
	for _, t := range targets {
		t = filepath.ToSlash(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		t = strings.TrimSuffix(t, "/")
		if rel == t || strings.HasPrefix(rel, t+"/") {
			return true
		}
	}
	return false
}

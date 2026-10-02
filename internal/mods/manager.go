package mods

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yuzumikonami/nyatmc/internal/downloader"
	"github.com/yuzumikonami/nyatmc/pkg/logger"
)

// 默认值。
const (
	// defaultTimeout 是单次请求超时。
	defaultTimeout = 60 * time.Second
	// defaultRetries 是默认重试次数。
	defaultRetries = 3
	// defaultUserAgent 是默认 User-Agent（Modrinth 要求带可识别的 UA）。
	defaultUserAgent = "NyaTMC/mods (+https://github.com/yuzumikonami/nyatmc)"
	// defaultLimit 是搜索默认返回条数。
	defaultLimit = 10
	// maxLimit 是搜索允许的最大条数。
	maxLimit = 100
	// maxFileNameLen 是落盘文件名长度上限。
	maxFileNameLen = 180
)

// Provider 是模组来源。
type Provider string

// 支持的来源。
const (
	Modrinth   Provider = "modrinth"
	CurseForge Provider = "curseforge"
)

// Project 是搜索结果里的一个项目。
type Project struct {
	ID           string
	Slug         string
	Title        string
	Description  string
	Author       string
	Provider     Provider
	URL          string
	Downloads    int64
	Categories   []string
	GameVersions []string
	ProjectType  string // mod / plugin / datapack / resourcepack
}

// Version 是项目的某个具体文件版本。
type Version struct {
	ID            string
	Name          string
	VersionNumber string
	FileName      string
	URL           string
	GameVersions  []string
	Loaders       []string
	Downloads     int64
	PublishedAt   time.Time
	// SHA256/SHA1 可能为空（CurseForge 只给 sha1，Modrinth 给 sha512/sha1）。
	SHA256 string
	SHA1   string
	Size   int64
}

// InstalledFile 描述落盘结果。
type InstalledFile struct {
	Path      string
	FileName  string
	ProjectID string
	Title     string
	Version   string
	URL       string
	SHA256    string
	Size      int64
}

// FileInfo 描述目录里的一个文件。
type FileInfo struct {
	Name    string
	Size    int64
	ModTime time.Time
	Path    string
}

// Installer 负责搜索 / 解析 / 下载到指定目录。
type Installer struct {
	Provider    Provider
	APIKey      string // CurseForge 必需
	UserAgent   string
	Timeout     time.Duration
	Retries     int
	GameVersion string // 空表示不按版本过滤
	Loader      string // 例如 paper / fabric / forge / neoforge / quilt
	Dir         string // 落盘目录（磁盘上的绝对路径）
	ClassID     int    // CurseForge 分类：6=mods 5=plugins；0 表示按 Loader 推断
	Logger      *logger.Logger
}

// ProgressFunc 是下载进度回调，可为 nil。
type ProgressFunc func(done, total int64)

// Search 搜索项目。
func (i *Installer) Search(ctx context.Context, query string, limit int) ([]Project, error) {
	if err := i.validate(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, fmt.Errorf("搜索关键词为空")
	}
	ctx = ctxOr(ctx)
	switch i.Provider {
	case Modrinth:
		return i.searchModrinth(ctx, q, limit)
	case CurseForge:
		return i.searchCurseForge(ctx, q, limit)
	default:
		return nil, fmt.Errorf("未知的模组来源 %q（可选 modrinth / curseforge）", i.Provider)
	}
}

// Resolve 解析某个项目在当前 版本/加载器 条件下应下载的文件。
func (i *Installer) Resolve(ctx context.Context, projectID string) (Version, error) {
	if err := i.validate(); err != nil {
		return Version{}, err
	}
	ctx = ctxOr(ctx)
	switch i.Provider {
	case Modrinth:
		return i.resolveModrinth(ctx, projectID)
	case CurseForge:
		return i.resolveCurseForge(ctx, projectID)
	default:
		return Version{}, fmt.Errorf("未知的模组来源 %q（可选 modrinth / curseforge）", i.Provider)
	}
}

// Install 下载并落盘到 i.Dir，返回安装结果。
func (i *Installer) Install(ctx context.Context, projectID string, progress ProgressFunc) (InstalledFile, error) {
	if err := i.validate(); err != nil {
		return InstalledFile{}, err
	}
	if _, err := i.cleanDir(); err != nil {
		return InstalledFile{}, err
	}
	ctx = ctxOr(ctx)
	version, err := i.Resolve(ctx, projectID)
	if err != nil {
		return InstalledFile{}, err
	}
	return i.installVersion(ctx, projectID, version, progress)
}

// SearchAndInstall 按关键词搜索取第一个结果并安装（用于 `nyatmc mod install <名字>`）。
func (i *Installer) SearchAndInstall(ctx context.Context, query string, progress ProgressFunc) (InstalledFile, error) {
	if err := i.validate(); err != nil {
		return InstalledFile{}, err
	}
	if _, err := i.cleanDir(); err != nil {
		return InstalledFile{}, err
	}
	ctx = ctxOr(ctx)
	limit := defaultLimit
	projects, err := i.Search(ctx, query, limit)
	if err != nil {
		return InstalledFile{}, err
	}
	if len(projects) == 0 {
		return InstalledFile{}, fmt.Errorf("没有搜索到匹配 %q 的项目（来源 %s）", query, i.Provider)
	}
	best := projects[0]
	i.logf().Infof("搜索 %q 命中 %d 个结果，安装第一个：%s（%s）", query, len(projects), best.Title, best.ID)
	version, err := i.Resolve(ctx, best.ID)
	if err != nil {
		return InstalledFile{}, err
	}
	return i.installVersion(ctx, best.ID, version, progress)
}

// installVersion 把解析出的版本落盘。
func (i *Installer) installVersion(ctx context.Context, projectID string, version Version, progress ProgressFunc) (InstalledFile, error) {
	name, err := sanitizeModFileName(version.FileName)
	if err != nil {
		return InstalledFile{}, fmt.Errorf("项目 %s 的文件名 %q 不可用 : %w", projectID, version.FileName, err)
	}
	dir, err := i.cleanDir()
	if err != nil {
		return InstalledFile{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return InstalledFile{}, fmt.Errorf("创建模组目录 %s 失败 : %w", dir, err)
	}
	dest := filepath.Join(dir, name)
	// 双保险：确认目标仍在目录内。
	if filepath.Dir(dest) != dir {
		return InstalledFile{}, fmt.Errorf("目标路径 %s 越出目录 %s，已拒绝", dest, dir)
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		backup := dest + ".bak"
		if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
			return InstalledFile{}, fmt.Errorf("清理旧备份 %s 失败 : %w", backup, err)
		}
		if err := os.Rename(dest, backup); err != nil {
			return InstalledFile{}, fmt.Errorf("备份已存在的 %s 失败 : %w", dest, err)
		}
		i.logf().Warnf("已存在同名文件，已备份为 %s", filepath.Base(backup))
	}

	if _, err := downloader.DownloadTo(ctx, version.URL, dest, downloader.DownloadOptions{
		Timeout:      i.Timeout,
		Retries:      i.retries(),
		UserAgent:    i.userAgent(),
		ExpectedSize: version.Size,
	}, func(p downloader.Progress) {
		if progress != nil {
			progress(p.Done, p.Total)
		}
	}); err != nil {
		return InstalledFile{}, fmt.Errorf("下载 %s 失败 : %w", name, err)
	}

	// 校验：优先用远端提供的 sha256，否则用 sha1（两家都给 sha1）。
	// 注意不能用“由 sha512 派生”的值来校验实际文件内容——那只是摘要的摘要，
	// 派生值只是确定性的占位，不能代替内容哈希。
	if err := downloader.VerifyFile(dest, version.SHA256, version.SHA1); err != nil {
		_ = os.Remove(dest)
		return InstalledFile{}, fmt.Errorf("校验失败，已删除下载文件 : %w", err)
	}
	if version.SHA256 == "" && version.SHA1 == "" {
		i.logf().Warnf("%s 没有可用的哈希，已跳过校验", name)
	}

	// 落盘后计算真实哈希，供调用方记录 / 展示。
	got256, got1, err := downloader.HashFile(dest)
	if err != nil {
		return InstalledFile{}, err
	}
	if version.SHA256 == "" {
		version.SHA256 = got256
		version.SHA1 = got1
	}

	st, err := os.Stat(dest)
	if err != nil {
		return InstalledFile{}, fmt.Errorf("检查落盘文件 %s 失败 : %w", dest, err)
	}
	i.logf().Infof("已安装 %s（%d 字节，sha256=%s）", dest, st.Size(), version.SHA256)
	return InstalledFile{
		Path:      dest,
		FileName:  name,
		ProjectID: projectID,
		Title:     orDefaultStr(version.Name, projectID),
		Version:   orDefaultStr(version.VersionNumber, version.ID),
		URL:       version.URL,
		SHA256:    version.SHA256,
		Size:      st.Size(),
	}, nil
}

// ListDir 列出 i.Dir 下的 jar 文件。
func (i *Installer) ListDir() ([]FileInfo, error) {
	dir, err := i.cleanDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取模组目录 %s 失败 : %w", dir, err)
	}
	out := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.EqualFold(filepath.Ext(e.Name()), ".jar") {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		out = append(out, FileInfo{
			Name:    e.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
			Path:    filepath.Join(dir, e.Name()),
		})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out, nil
}

// Remove 删除 i.Dir 下指定文件名的文件（只允许该目录内的简单文件名）。
func (i *Installer) Remove(name string) error {
	dir, err := i.cleanDir()
	if err != nil {
		return err
	}
	clean, err := sanitizeModFileName(name)
	if err != nil {
		return err
	}
	target := filepath.Join(dir, clean)
	if filepath.Dir(target) != dir {
		return fmt.Errorf("目标路径 %s 越出目录 %s，已拒绝", target, dir)
	}
	if err := os.Remove(target); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("文件 %s 不存在", clean)
		}
		return fmt.Errorf("删除 %s 失败 : %w", target, err)
	}
	// 顺带清掉它可能留下的备份。
	_ = os.Remove(target + ".bak")
	i.logf().Infof("已删除 %s", target)
	return nil
}

// cleanDir 返回清洗后的落盘目录绝对路径。
func (i *Installer) cleanDir() (string, error) {
	if strings.TrimSpace(i.Dir) == "" {
		return "", fmt.Errorf("未指定模组目录（请设置 mods_dir / plugins_dir）")
	}
	abs, err := filepath.Abs(i.Dir)
	if err != nil {
		return "", fmt.Errorf("解析模组目录 %s 失败 : %w", i.Dir, err)
	}
	return filepath.Clean(abs), nil
}

// validate 校验安装器的基本配置。
func (i *Installer) validate() error {
	switch i.Provider {
	case Modrinth:
		return nil
	case CurseForge:
		_, err := i.requireAPIKey()
		return err
	case "":
		return fmt.Errorf("未指定模组来源（可选 modrinth / curseforge）")
	default:
		return fmt.Errorf("未知的模组来源 %q（可选 modrinth / curseforge）", i.Provider)
	}
}

// sanitizeModFileName 清洗远端给出的文件名。
//
// 规则：只取 filepath.Base、拒绝空名与 . / ..、拒绝路径分隔符、限制长度。
func sanitizeModFileName(name string) (string, error) {
	raw := strings.TrimSpace(name)
	if raw == "" {
		return "", fmt.Errorf("远端未提供文件名")
	}
	if strings.ContainsRune(raw, 0) {
		return "", fmt.Errorf("文件名含空字符，已拒绝")
	}
	// 先看原始串：含任何路径分隔符一律拒绝（远端不应该给路径）。
	base := filepath.Base(raw)
	if strings.ContainsAny(base, `/\`) {
		return "", fmt.Errorf("文件名 %q 含路径分隔符，已拒绝", name)
	}
	if base != raw && strings.ContainsAny(raw, `/\`) {
		return "", fmt.Errorf("文件名 %q 是路径而非文件名，已拒绝", name)
	}
	base = strings.TrimSpace(base)
	if base == "" || base == "." || base == ".." {
		return "", fmt.Errorf("文件名 %q 非法", name)
	}
	if base == string(filepath.Separator) {
		return "", fmt.Errorf("文件名 %q 非法", name)
	}
	if len(base) > maxFileNameLen {
		ext := filepath.Ext(base)
		if len(ext) > 20 {
			ext = ""
		}
		keep := maxFileNameLen - len(ext)
		if keep < 1 {
			keep = 1
		}
		base = base[:keep] + ext
	}
	if base == "" || base == "." || base == ".." {
		return "", fmt.Errorf("文件名 %q 清洗后为空", name)
	}
	return base, nil
}

// ctxOr 在 ctx 为 nil 时补一个后台 context。
func ctxOr(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// ---------------------------------------------------------------- HTTP

// getJSONWithHeaders 发起带额外请求头的 GET 并按重试策略解析 JSON。
func getJSONWithHeaders(ctx context.Context, cli *http.Client, rawURL string, retries int, userAgent string, log *logger.Logger, headers map[string]string, out any) error {
	if cli == nil {
		cli = &http.Client{Timeout: defaultTimeout}
	}
	if retries <= 0 {
		retries = defaultRetries
	}
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			if log != nil {
				log.Debugf("第 %d 次重试 %s", attempt, rawURL)
			}
			if err := sleepCtx(ctx, backoff(attempt-1)); err != nil {
				return fmt.Errorf("请求 %s 已取消 : %w", rawURL, err)
			}
		}
		lastErr = func() error {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
			if err != nil {
				return &fatalError{fmt.Errorf("构造请求 %q 失败 : %w", rawURL, err)}
			}
			if strings.TrimSpace(userAgent) == "" {
				userAgent = defaultUserAgent
			}
			req.Header.Set("User-Agent", userAgent)
			req.Header.Set("Accept", "application/json")
			for k, v := range headers {
				req.Header.Set(k, v)
			}
			resp, err := cli.Do(req)
			if err != nil {
				return err
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
				snippet := strings.TrimSpace(string(body))
				if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
					return &fatalError{fmt.Errorf("请求 %s 被拒绝（HTTP %d）：API Key 缺失或无效，请检查 mods.curseforge_api_key；远端响应：%s",
						rawURL, resp.StatusCode, snippet)}
				}
				se := &statusError{Code: resp.StatusCode, Status: resp.Status, URL: rawURL, Snippet: snippet}
				if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
					return se
				}
				return &fatalError{se}
			}
			dec := json.NewDecoder(io.LimitReader(resp.Body, 32<<20))
			if err := dec.Decode(out); err != nil {
				return &fatalError{fmt.Errorf("解析 %s 的 JSON 响应失败 : %w", rawURL, err)}
			}
			return nil
		}()
		if lastErr == nil {
			return nil
		}
		var fe *fatalError
		if errors.As(lastErr, &fe) {
			return fe.err
		}
		if !transient(lastErr) && !retryableStatusErr(lastErr) {
			return lastErr
		}
	}
	return fmt.Errorf("请求 %s 重试 %d 次后仍失败 : %w", rawURL, retries, lastErr)
}

// statusError 表示非 200 的 HTTP 响应。
type statusError struct {
	Code    int
	Status  string
	URL     string
	Snippet string
}

// Error 实现 error。
func (e *statusError) Error() string {
	msg := fmt.Sprintf("请求 %s 失败：HTTP %s", e.URL, e.Status)
	if e.Snippet != "" {
		msg += "，响应：" + e.Snippet
	}
	return msg
}

// retryableStatusErr 判断错误是否为可重试的状态码（429 / 5xx）。
func retryableStatusErr(err error) bool {
	var se *statusError
	if !errors.As(err, &se) {
		return false
	}
	return se.Code == http.StatusTooManyRequests || se.Code >= 500
}

// fatalError 包装不应重试的错误。
type fatalError struct{ err error }

// Error 实现 error。
func (f *fatalError) Error() string { return f.err.Error() }

// Unwrap 支持 errors.Is / errors.As。
func (f *fatalError) Unwrap() error { return f.err }

// transient 判断是否值得重试（网络层错误）。
func transient(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	var oe *net.OpError
	return errors.As(err, &oe)
}

// backoff 返回第 attempt 次重试前的等待时间（attempt 从 0 开始）。
func backoff(attempt int) time.Duration {
	if attempt > 4 {
		attempt = 4
	}
	return time.Duration(200*(1<<uint(attempt))) * time.Millisecond
}

// sleepCtx 等待 d，ctx 取消时提前返回。
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

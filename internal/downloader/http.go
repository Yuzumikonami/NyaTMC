package downloader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yuzumikonami/nyatmc/pkg/logger"
)

// 默认值。调用方未显式指定超时 / 重试 / User-Agent 时使用。
const (
	// DefaultTimeout 是单次 HTTP 请求的默认超时。
	DefaultTimeout = 60 * time.Second
	// DefaultRetries 是默认重试次数。
	DefaultRetries = 3
	// DefaultUserAgent 是默认 User-Agent。
	DefaultUserAgent = "NyaTMC/downloader (+https://github.com/yuzumikonami/nyatmc)"
	// progressStep 是进度回调的最小步进，避免回调过于频繁。
	progressStep = 512 * 1024
)

// Progress 是下载进度。
type Progress struct {
	// Phase 例如 "解析版本" / "下载中" / "校验中"。
	Phase string
	// Done 是已完成字节数。
	Done int64
	// Total 是总字节数，未知时为 0。
	Total int64
	// Percent 是完成百分比（0-100），总大小未知时为 0。
	Percent float64
}

// ProgressFunc 是进度回调，可为 nil。
type ProgressFunc func(Progress)

// DownloadOptions 控制下载行为。
type DownloadOptions struct {
	// Timeout 是单次请求超时，为 0 时使用 DefaultTimeout。
	Timeout time.Duration
	// Retries 是失败重试次数，为 0 时使用 DefaultRetries。
	Retries int
	// UserAgent 是请求头，为空时使用 DefaultUserAgent。
	UserAgent string
	// ExpectedSize 大于 0 时会校验最终大小。
	ExpectedSize int64
}

// HTTPClient 构造带超时与 User-Agent 的客户端。
//
// 注意：http.Client 本身不保存 User-Agent，它由 newRequest 在每次请求时附加。
func HTTPClient(timeout time.Duration, userAgent string) *http.Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &http.Client{Timeout: timeout}
}

// HTTPStatusError 表示 HTTP 响应状态异常。
type HTTPStatusError struct {
	// Status 是完整状态行，例如 "410 Gone"。
	Status string
	// Code 是状态码。
	Code int
	// URL 是请求地址。
	URL string
	// Snippet 是响应体开头的一小段内容，便于排查。
	Snippet string
}

// Error 实现 error。
func (e *HTTPStatusError) Error() string {
	msg := fmt.Sprintf("请求 %s 失败：HTTP %s", e.URL, e.Status)
	if e.Snippet != "" {
		msg += "，响应：" + e.Snippet
	}
	return msg
}

// IsHTTPStatus 判断 err 链上是否存在指定状态码的 HTTPStatusError。
func IsHTTPStatus(err error, code int) bool {
	var se *HTTPStatusError
	if errors.As(err, &se) {
		return se.Code == code
	}
	return false
}

// retryableStatus 判断状态码是否值得重试。
func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests ||
		code == http.StatusRequestTimeout ||
		code >= 500
}

// newRequest 构造带 User-Agent 与 Accept 的 GET 请求。
func newRequest(ctx context.Context, rawURL, userAgent string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求 %q 失败 : %w", rawURL, err)
	}
	if strings.TrimSpace(userAgent) == "" {
		userAgent = DefaultUserAgent
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, */*")
	return req, nil
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

// backoff 返回第 attempt 次重试前的等待时间（attempt 从 0 开始）。
func backoff(attempt int) time.Duration {
	if attempt > 4 {
		attempt = 4
	}
	return time.Duration(200*(1<<uint(attempt))) * time.Millisecond
}

// transientErr 判断是否为可重试的传输层错误。
func transientErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// 由调用方决定的超时交给上层，不盲目重试；但连接级别的超时值得重试。
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return true
		}
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	return false
}

// getRaw 执行 GET 并把响应体交给 handle；对临时性错误按 retries 重试。
//
// handle 在每次尝试中都会被调用一次（包括最后一次失败的尝试）；它返回的 error 会决定
// 是否继续重试，因此 handle 内部应通过 transient 返回值表达“这次失败可重试”。
func getRaw(ctx context.Context, cli *http.Client, rawURL string, retries int, userAgent string, log *logger.Logger, handle func(*http.Response) error) error {
	if cli == nil {
		cli = HTTPClient(DefaultTimeout, userAgent)
	}
	if retries <= 0 {
		retries = DefaultRetries
	}
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, backoff(attempt-1)); err != nil {
				return fmt.Errorf("请求 %s 已取消 : %w", rawURL, err)
			}
			if log != nil {
				log.Debugf("第 %d 次重试 %s", attempt, rawURL)
			}
		}
		lastErr = func() error {
			req, err := newRequest(ctx, rawURL, userAgent)
			if err != nil {
				return err
			}
			resp, err := cli.Do(req)
			if err != nil {
				return err
			}
			defer func() { _ = resp.Body.Close() }()
			return handle(resp)
		}()
		if lastErr == nil {
			return nil
		}
		if !transientErr(lastErr) && !isRetryableError(lastErr) {
			return lastErr
		}
	}
	return fmt.Errorf("请求 %s 重试 %d 次后仍失败 : %w", rawURL, retries, lastErr)
}

// getJSON 发起 GET 并把 JSON 解析到 out。JSON 结构异常不会重试。
func getJSON(ctx context.Context, cli *http.Client, rawURL string, retries int, userAgent string, log *logger.Logger, out any) error {
	return getRaw(ctx, cli, rawURL, retries, userAgent, log, func(resp *http.Response) error {
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			se := &HTTPStatusError{
				Status:  resp.Status,
				Code:    resp.StatusCode,
				URL:     rawURL,
				Snippet: strings.TrimSpace(string(body)),
			}
			if retryableStatus(resp.StatusCode) {
				return fmt.Errorf("%w", se)
			}
			return &nonRetryable{se}
		}
		lim := io.LimitReader(resp.Body, 64<<20)
		dec := json.NewDecoder(lim)
		if err := dec.Decode(out); err != nil {
			return &nonRetryable{fmt.Errorf("解析 %s 的 JSON 响应失败 : %w", rawURL, err)}
		}
		return nil
	})
}

// nonRetryable 包装不应重试的错误。
type nonRetryable struct{ err error }

// Error 实现 error。
func (n *nonRetryable) Error() string { return n.err.Error() }

// Unwrap 支持 errors.Is / errors.As。
func (n *nonRetryable) Unwrap() error { return n.err }

// DownloadTo 带进度、断点容错地把 url 下载到 dest（先写 .part 再改名）。
//
// 返回落盘的实际字节数。
func DownloadTo(ctx context.Context, url, dest string, opts DownloadOptions, progress ProgressFunc) (int64, error) {
	if strings.TrimSpace(url) == "" {
		return 0, fmt.Errorf("下载地址为空")
	}
	if strings.TrimSpace(dest) == "" {
		return 0, fmt.Errorf("下载目标路径为空")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	retries := opts.Retries
	if retries <= 0 {
		retries = DefaultRetries
	}
	userAgent := opts.UserAgent
	if strings.TrimSpace(userAgent) == "" {
		userAgent = DefaultUserAgent
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return 0, fmt.Errorf("创建下载目录 %s 失败 : %w", filepath.Dir(dest), err)
	}
	part := dest + ".part"
	if err := os.Remove(part); err != nil && !os.IsNotExist(err) {
		return 0, fmt.Errorf("清理残留临时文件 %s 失败 : %w", part, err)
	}

	cli := HTTPClient(timeout, userAgent)
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, backoff(attempt-1)); err != nil {
				return 0, fmt.Errorf("下载 %s 已取消 : %w", url, err)
			}
			_ = os.Remove(part)
		}
		n, err := downloadOnce(ctx, cli, url, part, userAgent, opts.ExpectedSize, progress)
		if err == nil {
			if rerr := os.Rename(part, dest); rerr != nil {
				_ = os.Remove(part)
				return 0, fmt.Errorf("重命名 %s 为 %s 失败 : %w", part, dest, rerr)
			}
			return n, nil
		}
		lastErr = err
		_ = os.Remove(part)
		if !transientErr(err) && !isRetryableError(err) {
			return 0, err
		}
	}
	return 0, fmt.Errorf("下载 %s 重试 %d 次后仍失败 : %w", url, retries, lastErr)
}

// isRetryableError 判断包装后的错误是否可重试。
func isRetryableError(err error) bool {
	var se *HTTPStatusError
	if errors.As(err, &se) {
		return retryableStatus(se.Code)
	}
	var nr *nonRetryable
	return !errors.As(err, &nr)
}

// downloadOnce 执行一次下载尝试。
func downloadOnce(ctx context.Context, cli *http.Client, url, part, userAgent string, expected int64, progress ProgressFunc) (int64, error) {
	req, err := newRequest(ctx, url, userAgent)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "*/*")
	resp, err := cli.Do(req)
	if err != nil {
		return 0, fmt.Errorf("下载 %s 失败 : %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		se := &HTTPStatusError{
			Status:  resp.Status,
			Code:    resp.StatusCode,
			URL:     url,
			Snippet: strings.TrimSpace(string(body)),
		}
		if retryableStatus(resp.StatusCode) {
			return 0, se
		}
		return 0, &nonRetryable{se}
	}

	total := resp.ContentLength
	if expected > 0 {
		total = expected
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, fmt.Errorf("创建临时文件 %s 失败 : %w", part, err)
	}
	done, copyErr := copyWithProgress(ctx, f, resp.Body, total, progress)
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr != nil {
		return done, copyErr
	}
	if syncErr != nil {
		return done, fmt.Errorf("刷写 %s 失败 : %w", part, syncErr)
	}
	if closeErr != nil {
		return done, fmt.Errorf("关闭 %s 失败 : %w", part, closeErr)
	}
	if expected > 0 && done != expected {
		return done, &nonRetryable{fmt.Errorf("下载 %s 大小不符：期望 %d 字节，实际 %d 字节", url, expected, done)}
	}
	if progress != nil {
		progress(Progress{Phase: "下载完成", Done: done, Total: total, Percent: percent(done, total)})
	}
	return done, nil
}

// copyWithProgress 复制数据并回调进度。
func copyWithProgress(ctx context.Context, dst io.Writer, src io.Reader, total int64, progress ProgressFunc) (int64, error) {
	buf := make([]byte, 64*1024)
	var done int64
	last := int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return done, fmt.Errorf("下载已取消 : %w", err)
		}
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return done, fmt.Errorf("写入临时文件失败 : %w", werr)
			}
			done += int64(n)
			if progress != nil && (done-last >= progressStep || (total > 0 && done >= total)) {
				last = done
				progress(Progress{Phase: "下载中", Done: done, Total: total, Percent: percent(done, total)})
			}
		}
		if rerr == io.EOF {
			return done, nil
		}
		if rerr != nil {
			return done, fmt.Errorf("读取响应体失败 : %w", rerr)
		}
	}
}

// percent 计算百分比。
func percent(done, total int64) float64 {
	if total <= 0 {
		return 0
	}
	p := float64(done) / float64(total) * 100
	if p > 100 {
		return 100
	}
	return p
}

// ---------------------------------------------------------------- 小工具

// orDefault 返回 s，s 为空时返回 def。
func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// itoa 把整数转成字符串。
func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// maxFileNameLen 是落盘文件名长度上限。
const maxFileNameLen = 200

// sanitizeFileName 校验并清洗远端给出的文件名。
//
// 只接受单一文件名（不允许路径分隔符、. 与 ..），超长会被截断。
func sanitizeFileName(name string) (string, error) {
	s := strings.TrimSpace(name)
	if s == "" {
		return "", fmt.Errorf("远端未提供文件名")
	}
	// 统一分隔符后再判断，避免 Windows 风格路径混入。
	if strings.ContainsAny(s, `/\`) {
		return "", fmt.Errorf("文件名 %q 含路径分隔符，已拒绝", name)
	}
	if s == "." || s == ".." {
		return "", fmt.Errorf("文件名 %q 非法", name)
	}
	if strings.ContainsRune(s, 0) {
		return "", fmt.Errorf("文件名含空字符，已拒绝")
	}
	if len(s) > maxFileNameLen {
		ext := filepath.Ext(s)
		if len(ext) > 20 {
			ext = ""
		}
		keep := maxFileNameLen - len(ext)
		if keep < 1 {
			keep = 1
		}
		s = s[:keep] + ext
	}
	if s == "." || s == ".." || strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("文件名 %q 清洗后为空", name)
	}
	return s, nil
}

// resolveDest 解析目标目录与文件名，返回落盘绝对路径。
func resolveDest(dir, name string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("目标目录为空")
	}
	clean, err := sanitizeFileName(orDefault(name, "server.jar"))
	if err != nil {
		return "", err
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("解析目标目录 %s 失败 : %w", dir, err)
	}
	dest := filepath.Join(absDir, clean)
	// 双保险：确认结果仍在目标目录内。
	if filepath.Dir(dest) != absDir {
		return "", fmt.Errorf("目标路径 %s 越出目录 %s，已拒绝", dest, absDir)
	}
	return dest, nil
}

// checkDownloadURL 校验远端给出的下载地址只使用 http/https。
func checkDownloadURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("下载地址 %q 无法解析 : %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("下载地址 %q 协议不受支持（仅允许 http/https）", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("下载地址 %q 缺少主机名", raw)
	}
	return nil
}

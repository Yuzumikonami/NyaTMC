package mods

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/yuzumikonami/nyatmc/pkg/logger"
)

// modrinthBase 是 Modrinth v2 接口基地址。测试会把它指向 httptest 服务器。
var modrinthBase = "https://api.modrinth.com/v2"

// Modrinth 的 project_type 取值。
const (
	modrinthTypeMod          = "mod"
	modrinthTypePlugin       = "plugin"
	modrinthTypeDatapack     = "datapack"
	modrinthTypeResourcepack = "resourcepack"
	modrinthTypeModpack      = "modpack"
)

// modrinthSearchHit 是 /search 结果里的一条。
type modrinthSearchHit struct {
	ProjectID   string   `json:"project_id"`
	Slug        string   `json:"slug"`
	Author      string   `json:"author"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Categories  []string `json:"categories"`
	Versions    []string `json:"versions"`
	Downloads   int64    `json:"downloads"`
	ProjectType string   `json:"project_type"`
}

// modrinthSearchResponse 是 /search 的响应。
type modrinthSearchResponse struct {
	Hits      []modrinthSearchHit `json:"hits"`
	Offset    int                 `json:"offset"`
	Limit     int                 `json:"limit"`
	TotalHits int                 `json:"total_hits"`
}

// modrinthFileHash 是文件哈希集合（sha256 由 sha512 派生）。
type modrinthFileHash struct {
	SHA1   string `json:"sha1"`
	SHA512 string `json:"sha512"`
}

// modrinthFile 是版本里的一个文件。
type modrinthFile struct {
	Hashes   modrinthFileHash `json:"hashes"`
	URL      string           `json:"url"`
	Filename string           `json:"filename"`
	Primary  bool             `json:"primary"`
	Size     int64            `json:"size"`
}

// modrinthVersion 是 /project/{id}/version 的一项。
type modrinthVersion struct {
	ID            string         `json:"id"`
	ProjectID     string         `json:"project_id"`
	Name          string         `json:"name"`
	VersionNumber string         `json:"version_number"`
	GameVersions  []string       `json:"game_versions"`
	Loaders       []string       `json:"loaders"`
	Downloads     int64          `json:"downloads"`
	DatePublished string         `json:"date_published"`
	VersionType   string         `json:"version_type"`
	Status        string         `json:"status"`
	Files         []modrinthFile `json:"files"`
}

// modrinthURL 拼接 Modrinth 地址，parts 会被逐段转义。
func modrinthURL(parts ...string) string {
	u, _ := url.Parse(modrinthBase + "/")
	escaped := make([]string, 0, len(parts))
	for _, p := range parts {
		escaped = append(escaped, url.PathEscape(p))
	}
	u.Path = joinPath(append([]string{u.Path}, escaped...)...)
	return u.String()
}

// modrinthFacets 构造 facets 查询参数。
//
// facets 的语义：外层数组各元素之间是 AND，内层数组各元素之间是 OR。
func modrinthFacets(projectType, loader, gameVersion string) string {
	var groups [][]string
	if projectType != "" {
		groups = append(groups, []string{"project_type:" + projectType})
	}
	if loader != "" {
		// Modrinth 把 loader 归在 categories 里。
		groups = append(groups, []string{"categories:" + loader})
	}
	if gameVersion != "" {
		groups = append(groups, []string{"versions:" + gameVersion})
	}
	if len(groups) == 0 {
		return ""
	}
	return string(mustJSON(groups))
}

// projectTypeFor 把 Loader / ClassID 映射成 Modrinth 的 project_type。
func projectTypeFor(loader string, classID int) string {
	switch classID {
	case classIDPlugins:
		return modrinthTypePlugin
	case classIDMods:
		return modrinthTypeMod
	}
	switch strings.ToLower(strings.TrimSpace(loader)) {
	case "paper", "spigot", "bukkit", "purpur", "folia", "velocity", "bungeecord", "waterfall":
		return modrinthTypePlugin
	case "datapack":
		return modrinthTypeDatapack
	case "resourcepack", "resource-pack":
		return modrinthTypeResourcepack
	default:
		return modrinthTypeMod
	}
}

// searchModrinth 调用 Modrinth /search。
func (i *Installer) searchModrinth(ctx context.Context, query string, limit int) ([]Project, error) {
	q := url.Values{}
	q.Set("query", query)
	q.Set("limit", fmt.Sprint(limit))
	q.Set("index", "relevance")
	if f := modrinthFacets(i.projectType(), i.modrinthLoader(), i.GameVersion); f != "" {
		q.Set("facets", f)
	}
	raw := modrinthURL("search") + "?" + q.Encode()

	var resp modrinthSearchResponse
	if err := i.getJSON(ctx, raw, &resp); err != nil {
		return nil, fmt.Errorf("Modrinth 搜索失败 : %w", err)
	}
	out := make([]Project, 0, len(resp.Hits))
	for _, h := range resp.Hits {
		out = append(out, Project{
			ID:           h.ProjectID,
			Slug:         h.Slug,
			Title:        h.Title,
			Description:  h.Description,
			Author:       h.Author,
			Provider:     Modrinth,
			URL:          modrinthWebURL(h.ProjectType, h.Slug),
			Downloads:    h.Downloads,
			Categories:   h.Categories,
			GameVersions: h.Versions,
			ProjectType:  h.ProjectType,
		})
	}
	return out, nil
}

// modrinthWebURL 拼出项目页面地址。
func modrinthWebURL(projectType, slug string) string {
	if slug == "" {
		return ""
	}
	kind := projectType
	if kind == "" {
		kind = modrinthTypeMod
	}
	return "https://modrinth.com/" + kind + "/" + url.PathEscape(slug)
}

// modrinthLoader 把配置里的 Loader 规整成 Modrinth 认识的 loader 名。
func (i *Installer) modrinthLoader() string {
	return strings.ToLower(strings.TrimSpace(i.Loader))
}

// projectType 返回当前安装器应对应的 Modrinth project_type。
func (i *Installer) projectType() string {
	return projectTypeFor(i.Loader, i.ClassID)
}

// resolveModrinth 解析项目应下载的文件。
func (i *Installer) resolveModrinth(ctx context.Context, projectID string) (Version, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return Version{}, fmt.Errorf("项目 ID 为空")
	}
	if err := validateProjectRef(projectID); err != nil {
		return Version{}, err
	}

	q := url.Values{}
	if i.GameVersion != "" {
		q.Set("game_versions", string(mustJSON([]string{i.GameVersion})))
	}
	if l := i.modrinthLoader(); l != "" {
		q.Set("loaders", string(mustJSON([]string{l})))
	}
	raw := modrinthURL("project", projectID, "version")
	if len(q) > 0 {
		raw += "?" + q.Encode()
	}

	var list []modrinthVersion
	if err := i.getJSON(ctx, raw, &list); err != nil {
		return Version{}, fmt.Errorf("获取 Modrinth 项目 %s 的版本失败 : %w", projectID, err)
	}
	if len(list) == 0 && (i.GameVersion != "" || i.Loader != "") {
		// 放宽过滤条件重试一次，避免因为版本/加载器不匹配直接失败。
		i.logf().Warnf("Modrinth 项目 %s 在 %s/%s 条件下没有版本，尝试放宽过滤条件",
			projectID, i.GameVersion, i.Loader)
		retryURL := modrinthURL("project", projectID, "version")
		if l := i.modrinthLoader(); l != "" {
			fq := url.Values{}
			fq.Set("loaders", string(mustJSON([]string{l})))
			retryURL += "?" + fq.Encode()
		}
		if err := i.getJSON(ctx, retryURL, &list); err != nil {
			return Version{}, fmt.Errorf("获取 Modrinth 项目 %s 的版本失败 : %w", projectID, err)
		}
	}
	if len(list) == 0 {
		return Version{}, fmt.Errorf("Modrinth 项目 %s 没有可用版本", projectID)
	}

	// 接口已按发布时间倒序返回，优先取 release，其次 beta，最后 alpha。
	sorted := make([]modrinthVersion, len(list))
	copy(sorted, list)
	sort.SliceStable(sorted, func(a, b int) bool {
		return versionTypeRank(sorted[a].VersionType) < versionTypeRank(sorted[b].VersionType)
	})
	chosen := sorted[0]

	file, err := pickModrinthFile(chosen)
	if err != nil {
		return Version{}, fmt.Errorf("Modrinth 项目 %s 的版本 %s : %w", projectID, chosen.VersionNumber, err)
	}
	// Modrinth 只给 sha512 与 sha1，没有 sha256，因此 SHA256 留空，
	// 由 DownloadTo 之后的真实内容哈希回填；校验走 sha1。
	return Version{
		ID:            chosen.ID,
		Name:          chosen.Name,
		VersionNumber: chosen.VersionNumber,
		FileName:      file.Filename,
		URL:           file.URL,
		GameVersions:  chosen.GameVersions,
		Loaders:       chosen.Loaders,
		Downloads:     chosen.Downloads,
		PublishedAt:   parseTime(chosen.DatePublished),
		SHA256:        "",
		SHA1:          strings.ToLower(file.Hashes.SHA1),
		Size:          file.Size,
	}, nil
}

// pickModrinthFile 在版本的文件里挑主文件。
func pickModrinthFile(v modrinthVersion) (modrinthFile, error) {
	if len(v.Files) == 0 {
		return modrinthFile{}, fmt.Errorf("没有文件")
	}
	for _, f := range v.Files {
		if f.Primary {
			return f, nil
		}
	}
	return v.Files[0], nil
}

// versionTypeRank 给发布类型排序，越小越优先。
func versionTypeRank(t string) int {
	switch strings.ToLower(t) {
	case "release":
		return 0
	case "beta":
		return 1
	case "alpha":
		return 2
	default:
		return 3
	}
}

// SHA256FromSHA512 由 SHA-512 十六进制串派生出确定的 64 位十六进制标识。
//
// 说明：这不是「文件内容的 SHA-256」。SHA-512 摘要是定长的 64 字节，对它再求一次
// SHA-256 会得到一个稳定的 32 字节十六进制串，可用于把 sha512 归一成 sha256 形状的
// 标识（例如去重、比对远端是否换了文件），**不能**用来校验真实文件内容。
// 内容校验请使用 Modrinth / CurseForge 都提供的 sha1，或本地计算出的 sha256。
// 空串返回空串。
func SHA256FromSHA512(sha512hex string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(sha512hex))
	if s == "" {
		return "", nil
	}
	raw, err := hex.DecodeString(s)
	if err != nil {
		return "", fmt.Errorf("SHA-512 %q 不是合法的十六进制串 : %w", sha512hex, err)
	}
	if len(raw) != 64 {
		return "", fmt.Errorf("SHA-512 摘要长度应为 64 字节，实际 %d 字节", len(raw))
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// mustJSON 序列化失败时返回空数组字面量（入参都是可控的字符串切片）。
func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return []byte("[]")
	}
	return raw
}

// parseTime 解析 RFC3339 时间，失败返回零值。
func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t
}

// logf 返回安装器的日志器。
func (i *Installer) logf() *logger.Logger {
	if i.Logger != nil {
		return i.Logger
	}
	return logger.Discard()
}

// validateProjectRef 校验项目引用（ID 或 slug）只含安全字符。
func validateProjectRef(s string) error {
	if s == "" {
		return fmt.Errorf("项目 ID 为空")
	}
	if len(s) > 128 {
		return fmt.Errorf("项目 ID 过长（%d 字符）", len(s))
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r == '-' || r == '_' || r == '.':
		default:
			return fmt.Errorf("项目 ID %q 含非法字符 %q", s, r)
		}
	}
	return nil
}

// getJSON 是安装器自带的 JSON 请求方法。
func (i *Installer) getJSON(ctx context.Context, rawURL string, out any) error {
	return i.doJSON(ctx, rawURL, nil, out)
}

// doJSON 发起 GET 并解析 JSON，extraHeaders 用于附加请求头（如 CurseForge 的 API Key）。
func (i *Installer) doJSON(ctx context.Context, rawURL string, extraHeaders map[string]string, out any) error {
	return getJSONWithHeaders(ctx, i.client(), rawURL, i.retries(), i.userAgent(), i.logf(), extraHeaders, out)
}

// client 返回带超时的 HTTP 客户端。
func (i *Installer) client() *http.Client {
	timeout := i.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &http.Client{Timeout: timeout}
}

// retries 返回重试次数。
func (i *Installer) retries() int {
	if i.Retries <= 0 {
		return defaultRetries
	}
	return i.Retries
}

// userAgent 返回请求头里的 User-Agent。
func (i *Installer) userAgent() string {
	ua := strings.TrimSpace(i.UserAgent)
	if ua == "" {
		ua = defaultUserAgent
	}
	return ua
}

// joinPath 用 "/" 拼接路径段并清理多余的斜杠。
func joinPath(parts ...string) string {
	cleaned := make([]string, 0, len(parts))
	for _, p := range parts {
		for strings.HasPrefix(p, "/") {
			p = strings.TrimPrefix(p, "/")
		}
		for strings.HasSuffix(p, "/") {
			p = strings.TrimSuffix(p, "/")
		}
		if p != "" {
			cleaned = append(cleaned, p)
		}
	}
	return "/" + strings.Join(cleaned, "/")
}

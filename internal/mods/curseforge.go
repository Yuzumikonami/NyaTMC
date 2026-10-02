package mods

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// curseForgeBase 是 CurseForge v1 接口基地址。测试会把它指向 httptest 服务器。
var curseForgeBase = "https://api.curseforge.com/v1"

// Minecraft 在 CurseForge 上的 gameId。
const curseForgeGameID = 432

// CurseForge 的 classId 分类。
const (
	// classIDMods 对应 Mods。
	classIDMods = 6
	// classIDPlugins 对应 Bukkit Plugins。
	classIDPlugins = 5
	// classIDDatapacks 对应 Data Packs。
	classIDDatapacks = 6945
)

// CurseForge 的 modLoaderType。
const (
	cfLoaderAny      = 0
	cfLoaderForge    = 1
	cfLoaderCauldron = 2
	cfLoaderFabric   = 4
	cfLoaderNeoForge = 6
)

// curseForgePagination 是分页信息。
type curseForgePagination struct {
	Index       int `json:"index"`
	PageSize    int `json:"pageSize"`
	ResultCount int `json:"resultCount"`
	TotalCount  int `json:"totalCount"`
}

// curseForgeMod 是搜索结果里的一条。
type curseForgeMod struct {
	ID            int      `json:"id"`
	GameID        int      `json:"gameId"`
	Name          string   `json:"name"`
	Slug          string   `json:"slug"`
	Links         cfLinks  `json:"links"`
	Summary       string   `json:"summary"`
	DownloadCount float64  `json:"downloadCount"`
	ClassID       int      `json:"classId"`
	Authors       []cfAuth `json:"authors"`
	Categories    []cfCat  `json:"categories"`
	LatestFiles   []cfFile `json:"latestFiles"`
}

// cfLinks 是项目链接。
type cfLinks struct {
	WebsiteURL string `json:"websiteUrl"`
}

// cfAuth 是作者。
type cfAuth struct {
	Name string `json:"name"`
}

// cfCat 是分类。
type cfCat struct {
	Name string `json:"name"`
}

// cfFileHash 是文件哈希。
type cfFileHash struct {
	Value string `json:"value"`
	Algo  int    `json:"algo"`
}

// cfFile 是项目文件。
type cfFile struct {
	ID           int          `json:"id"`
	GameID       int          `json:"gameId"`
	ModID        int          `json:"modId"`
	DisplayName  string       `json:"displayName"`
	FileName     string       `json:"fileName"`
	ReleaseType  int          `json:"releaseType"`
	FileStatus   int          `json:"fileStatus"`
	FileDate     string       `json:"fileDate"`
	FileLength   int64        `json:"fileLength"`
	DownloadURL  *string      `json:"downloadUrl"`
	GameVersions []string     `json:"gameVersions"`
	Hashes       []cfFileHash `json:"hashes"`
}

// curseForgeSearchResponse 是 /mods/search 的响应。
type curseForgeSearchResponse struct {
	Data       []curseForgeMod      `json:"data"`
	Pagination curseForgePagination `json:"pagination"`
}

// curseForgeFilesResponse 是 /mods/{id}/files 的响应。
type curseForgeFilesResponse struct {
	Data       []cfFile             `json:"data"`
	Pagination curseForgePagination `json:"pagination"`
}

// curseForgeModResponse 是 /mods/{id} 的响应。
type curseForgeModResponse struct {
	Data curseForgeMod `json:"data"`
}

// curseForgeURL 拼接 CurseForge 地址。
func curseForgeURL(parts ...string) string {
	u, _ := url.Parse(curseForgeBase + "/")
	escaped := make([]string, 0, len(parts))
	for _, p := range parts {
		escaped = append(escaped, url.PathEscape(p))
	}
	u.Path = joinPath(append([]string{u.Path}, escaped...)...)
	return u.String()
}

// curseForgeHeaders 构造 CurseForge 需要的请求头。
func curseForgeHeaders(apiKey string) map[string]string {
	return map[string]string{
		"x-api-key": apiKey,
		"Accept":    "application/json",
	}
}

// requireAPIKey 校验 API Key，缺失时给出明确的中文提示。
func (i *Installer) requireAPIKey() (string, error) {
	key := strings.TrimSpace(i.APIKey)
	if key == "" {
		return "", fmt.Errorf("使用 CurseForge 需要 API Key：" +
			"请在配置文件的 [mods] 段设置 curseforge_api_key（申请地址 https://console.curseforge.com/），" +
			"或改用 modrinth 作为 provider")
	}
	return key, nil
}

// curseForgeLoaderType 把 Loader 名映射成 CurseForge 的 modLoaderType。
func curseForgeLoaderType(loader string) int {
	switch strings.ToLower(strings.TrimSpace(loader)) {
	case "forge":
		return cfLoaderForge
	case "neoforge":
		return cfLoaderNeoForge
	case "fabric", "quilt":
		return cfLoaderFabric
	case "cauldron":
		return cfLoaderCauldron
	default:
		return cfLoaderAny
	}
}

// curseForgeClassID 推断 classId：优先用显式配置，其次按 Loader 推断。
func (i *Installer) curseForgeClassID() int {
	if i.ClassID > 0 {
		return i.ClassID
	}
	switch strings.ToLower(strings.TrimSpace(i.Loader)) {
	case "paper", "spigot", "bukkit", "purpur", "folia", "velocity", "bungeecord", "waterfall":
		return classIDPlugins
	case "":
		return classIDMods
	default:
		return classIDMods
	}
}

// curseForgeTypePath 返回项目页面 URL 里的类型段。
func curseForgeTypePath(classID int) string {
	switch classID {
	case classIDPlugins:
		return "bukkit-plugins"
	case classIDDatapacks:
		return "data-packs"
	default:
		return "mc-mods"
	}
}

// searchCurseForge 调用 CurseForge /mods/search。
func (i *Installer) searchCurseForge(ctx context.Context, query string, limit int) ([]Project, error) {
	key, err := i.requireAPIKey()
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("gameId", fmt.Sprint(curseForgeGameID))
	q.Set("classId", fmt.Sprint(i.curseForgeClassID()))
	q.Set("searchFilter", query)
	q.Set("pageSize", fmt.Sprint(limit))
	q.Set("sortField", "2") // 2 = Popularity
	q.Set("sortOrder", "desc")
	if i.GameVersion != "" {
		q.Set("gameVersion", i.GameVersion)
	}
	if lt := curseForgeLoaderType(i.Loader); lt != cfLoaderAny {
		q.Set("modLoaderType", fmt.Sprint(lt))
	}
	raw := curseForgeURL("mods", "search") + "?" + q.Encode()

	var resp curseForgeSearchResponse
	if err := i.doJSON(ctx, raw, curseForgeHeaders(key), &resp); err != nil {
		return nil, fmt.Errorf("CurseForge 搜索失败 : %w", err)
	}
	out := make([]Project, 0, len(resp.Data))
	for _, m := range resp.Data {
		out = append(out, curseForgeProject(m))
	}
	return out, nil
}

// curseForgeProject 把接口返回的项目转成统一结构。
func curseForgeProject(m curseForgeMod) Project {
	cats := make([]string, 0, len(m.Categories))
	for _, c := range m.Categories {
		if c.Name != "" {
			cats = append(cats, c.Name)
		}
	}
	var author string
	if len(m.Authors) > 0 {
		author = m.Authors[0].Name
	}
	link := m.Links.WebsiteURL
	if link == "" && m.Slug != "" {
		link = "https://www.curseforge.com/minecraft/" + curseForgeTypePath(m.ClassID) + "/" + url.PathEscape(m.Slug)
	}
	var versions []string
	if len(m.LatestFiles) > 0 {
		versions = m.LatestFiles[0].GameVersions
	}
	return Project{
		ID:           fmt.Sprint(m.ID),
		Slug:         m.Slug,
		Title:        m.Name,
		Description:  m.Summary,
		Author:       author,
		Provider:     CurseForge,
		URL:          link,
		Downloads:    int64(m.DownloadCount),
		Categories:   cats,
		GameVersions: versions,
		ProjectType:  curseForgeProjectType(m.ClassID),
	}
}

// curseForgeProjectType 把 classId 映射成 mod / plugin / datapack。
func curseForgeProjectType(classID int) string {
	switch classID {
	case classIDPlugins:
		return "plugin"
	case classIDDatapacks:
		return "datapack"
	default:
		return "mod"
	}
}

// resolveCurseForge 解析项目应下载的文件。
func (i *Installer) resolveCurseForge(ctx context.Context, projectID string) (Version, error) {
	key, err := i.requireAPIKey()
	if err != nil {
		return Version{}, err
	}
	id := strings.TrimSpace(projectID)
	if id == "" {
		return Version{}, fmt.Errorf("项目 ID 为空")
	}
	if err := validateProjectRef(id); err != nil {
		return Version{}, err
	}

	q := url.Values{}
	q.Set("pageSize", "50")
	if i.GameVersion != "" {
		q.Set("gameVersion", i.GameVersion)
	}
	if lt := curseForgeLoaderType(i.Loader); lt != cfLoaderAny {
		q.Set("modLoaderType", fmt.Sprint(lt))
	}
	raw := curseForgeURL("mods", id, "files") + "?" + q.Encode()

	var resp curseForgeFilesResponse
	if err := i.doJSON(ctx, raw, curseForgeHeaders(key), &resp); err != nil {
		return Version{}, fmt.Errorf("获取 CurseForge 项目 %s 的文件列表失败 : %w", id, err)
	}
	files := resp.Data
	if len(files) == 0 {
		// 接口无过滤结果时，再取一次不带过滤的列表，由本地做筛选。
		var all curseForgeFilesResponse
		if err := i.doJSON(ctx, curseForgeURL("mods", id, "files")+"?pageSize=50", curseForgeHeaders(key), &all); err != nil {
			return Version{}, fmt.Errorf("获取 CurseForge 项目 %s 的文件列表失败 : %w", id, err)
		}
		files = all.Data
	}
	if len(files) == 0 {
		return Version{}, fmt.Errorf("CurseForge 项目 %s 没有可用文件", id)
	}

	picked := pickCurseForgeFile(files, i.GameVersion, i.Loader)
	if picked == nil {
		return Version{}, fmt.Errorf("CurseForge 项目 %s 在 %s/%s 条件下没有匹配的文件",
			id, orDefaultStr(i.GameVersion, "任意版本"), orDefaultStr(i.Loader, "任意加载器"))
	}
	url := curseForgeFileURL(*picked)
	if url == "" {
		return Version{}, fmt.Errorf("CurseForge 项目 %s 的文件 %s 没有可用的下载地址", id, picked.FileName)
	}
	// CurseForge 只给 sha1 / md5，没有 sha256，因此 SHA256 留空，
	// 由落盘后的真实内容哈希回填；校验走 sha1。
	sha1hex := curseForgeSHA1(*picked)
	return Version{
		ID:            fmt.Sprint(picked.ID),
		Name:          picked.DisplayName,
		VersionNumber: orDefaultStr(picked.DisplayName, fmt.Sprint(picked.ID)),
		FileName:      picked.FileName,
		URL:           url,
		GameVersions:  picked.GameVersions,
		Loaders:       loadersFromGameVersions(picked.GameVersions),
		PublishedAt:   parseTime(picked.FileDate),
		SHA256:        "",
		SHA1:          sha1hex,
		Size:          picked.FileLength,
	}, nil
}

// pickCurseForgeFile 按游戏版本 / 加载器挑选最合适的文件。
//
// CurseForge 的 gameVersions 里同时混有游戏版本与加载器名（Forge/Fabric/NeoForge），
// 因此这里逐个字段判断。
func pickCurseForgeFile(files []cfFile, gameVersion, loader string) *cfFile {
	wantLoader := strings.ToLower(strings.TrimSpace(loader))
	var best *cfFile
	bestRank := 1 << 30
	for idx := range files {
		f := &files[idx]
		if f.FileStatus != 0 && f.FileStatus != 4 {
			// 0 = 正常，4 = 已发布；其余（如 1 审核中、2 已删除）跳过。
			continue
		}
		if gameVersion != "" && !containsFold(f.GameVersions, gameVersion) {
			continue
		}
		if wantLoader != "" && !loaderMatches(f.GameVersions, wantLoader) {
			continue
		}
		// releaseType: 1 = release, 2 = beta, 3 = alpha。优先 release。
		rank := f.ReleaseType
		if rank == 0 {
			rank = 1
		}
		if rank < bestRank {
			best, bestRank = f, rank
		}
	}
	return best
}

// loaderMatches 判断文件是否声明了指定加载器。
func loaderMatches(gameVersions []string, wantLoader string) bool {
	alias := map[string]string{
		"forge":    "forge",
		"neoforge": "neoforge",
		"fabric":   "fabric",
		"quilt":    "quilt",
		"paper":    "bukkit",
		"spigot":   "bukkit",
		"bukkit":   "bukkit",
		"purpur":   "bukkit",
		"folia":    "bukkit",
	}
	want := wantLoader
	if a, ok := alias[wantLoader]; ok {
		want = a
	}
	for _, gv := range gameVersions {
		if strings.EqualFold(strings.TrimSpace(gv), want) {
			return true
		}
	}
	return false
}

// loadersFromGameVersions 从 gameVersions 里抽出加载器名。
func loadersFromGameVersions(gameVersions []string) []string {
	known := map[string]bool{
		"forge": true, "neoforge": true, "fabric": true, "quilt": true,
		"bukkit": true, "spigot": true, "cauldron": true, "liteloader": true,
	}
	out := make([]string, 0, 2)
	for _, gv := range gameVersions {
		if known[strings.ToLower(strings.TrimSpace(gv))] {
			out = append(out, gv)
		}
	}
	return out
}

// curseForgeFileURL 取文件的下载地址，缺失时按 CurseForge CDN 规则拼接。
func curseForgeFileURL(f cfFile) string {
	if f.DownloadURL != nil && strings.TrimSpace(*f.DownloadURL) != "" {
		return strings.TrimSpace(*f.DownloadURL)
	}
	if f.ID <= 0 || strings.TrimSpace(f.FileName) == "" {
		return ""
	}
	// 官方规则：/files/<id/1000>/<id%1000>/<fileName>
	return fmt.Sprintf("https://edge.forgecdn.net/files/%d/%d/%s",
		f.ID/1000, f.ID%1000, url.PathEscape(f.FileName))
}

// curseForgeSHA1 从 hashes 里取出 sha1。
//
// CurseForge 的哈希项有 algo 字段（1 = SHA1，2 = MD5），但部分项目只按长度给出，
// 因此这里同时用长度与 algo 兜底判断。sha256 官方不提供，只能留空。
func curseForgeSHA1(f cfFile) string {
	for _, h := range f.Hashes {
		v := strings.ToLower(strings.TrimSpace(h.Value))
		if len(v) == 40 || h.Algo == 1 {
			return v
		}
	}
	return ""
}

// containsFold 大小写不敏感地判断切片里是否包含目标字符串。
func containsFold(list []string, want string) bool {
	for _, v := range list {
		if strings.EqualFold(strings.TrimSpace(v), strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}

// orDefaultStr 返回 s，为空时返回 def。
func orDefaultStr(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

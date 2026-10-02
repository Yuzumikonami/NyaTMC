// 本文件补充 manager_test.go 未覆盖的用例：请求构造细节、真实响应常量的结构断言，
// 以及 Provider / ClassID 推断的组合行为。
//
// 文件名为 debug_test.go 属于历史遗留（创建它的调试过程早于定稿），内容全部是正式用例。
package mods

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// TestCurseForgeFixturesParse 直接对固定响应常量做结构断言，确保字段名没写错。
//
// 说明：CurseForge 的 /v1/mods/search 必须带 x-api-key，无 Key 时官方只返回
// HTTP 403（"Forbidden: API Key missing or invalid"，已实测），因此这两个常量
// 是依据官方 schema 固定下来的，不做联网抓取。
func TestCurseForgeFixturesParse(t *testing.T) {
	var search curseForgeSearchResponse
	if err := json.Unmarshal([]byte(curseForgeSearchJSON), &search); err != nil {
		t.Fatalf("搜索响应解析失败 : %v", err)
	}
	if len(search.Data) != 2 {
		t.Fatalf("应解析出 2 个项目，实际 %d", len(search.Data))
	}
	if search.Pagination.TotalCount != 2 {
		t.Errorf("totalCount 应为 2，实际 %d", search.Pagination.TotalCount)
	}
	first := search.Data[0]
	if first.ID != 238222 || first.Slug != "jei" || first.ClassID != 6 {
		t.Errorf("第一个项目字段异常：%+v", first)
	}
	if first.Links.WebsiteURL == "" {
		t.Error("websiteUrl 应被解析")
	}
	if len(first.Authors) == 0 || first.Authors[0].Name != "mezz" {
		t.Errorf("authors 解析异常：%+v", first.Authors)
	}
	if len(first.LatestFiles) != 1 || first.LatestFiles[0].FileName != "jei-1.21.4-forge-19.21.0.247.jar" {
		t.Errorf("latestFiles 解析异常：%+v", first.LatestFiles)
	}

	var files curseForgeFilesResponse
	if err := json.Unmarshal([]byte(curseForgeFilesJSON), &files); err != nil {
		t.Fatalf("文件列表解析失败 : %v", err)
	}
	if len(files.Data) != 4 {
		t.Fatalf("应解析出 4 个文件，实际 %d", len(files.Data))
	}
	// downloadUrl 为 null 时应解析成 nil 指针。
	if files.Data[3].DownloadURL != nil {
		t.Errorf("downloadUrl 为 null 时应是 nil，实际 %v", *files.Data[3].DownloadURL)
	}
	if files.Data[0].DownloadURL == nil {
		t.Error("downloadUrl 非空时应解析成非 nil 指针")
	}
}

// TestCurseForgeSearchURLNotEscaped 验证 URL 构造不破坏查询参数。
func TestCurseForgeSearchURLNotEscaped(t *testing.T) {
	var raw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw = r.URL.String()
		writeJSON(w, `{"data":[],"pagination":{"index":0,"pageSize":1,"resultCount":0,"totalCount":0}}`)
	}))
	defer srv.Close()
	withBase(t, srv.URL)

	i := &Installer{Provider: CurseForge, APIKey: "k", Loader: "neoforge", GameVersion: "1.21.4", Dir: t.TempDir()}
	if _, err := i.Search(context.Background(), "sodium & lithium", 3); err != nil {
		t.Fatalf("搜索失败 : %v", err)
	}
	q, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("请求 URL %q 无法解析 : %v", raw, err)
	}
	if got := q.Query().Get("searchFilter"); got != "sodium & lithium" {
		t.Errorf("searchFilter 应被正确转义/还原为 %q，实际 %q", "sodium & lithium", got)
	}
	if got := q.Query().Get("modLoaderType"); got != "6" {
		t.Errorf("neoforge 的 modLoaderType 应为 6，实际 %q", got)
	}
	if got := q.Query().Get("classId"); got != "6" {
		t.Errorf("neoforge 的 classId 应为 6，实际 %q", got)
	}
}

// TestInstallerLimitClamping 验证 limit 会被钳制在合理范围内。
func TestInstallerLimitClamping(t *testing.T) {
	var gotLimit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLimit = r.URL.Query().Get("limit")
		writeJSON(w, `{"hits":[],"offset":0,"limit":0,"total_hits":0}`)
	}))
	defer srv.Close()
	withBase(t, srv.URL)

	i := &Installer{Provider: Modrinth, Dir: t.TempDir()}
	if _, err := i.Search(context.Background(), "x", 5000); err != nil {
		t.Fatalf("搜索失败 : %v", err)
	}
	if gotLimit != "100" {
		t.Errorf("超出的 limit 应被钳制到 100，实际 %q", gotLimit)
	}
	if _, err := i.Search(context.Background(), "x", 0); err != nil {
		t.Fatalf("搜索失败 : %v", err)
	}
	if gotLimit != "10" {
		t.Errorf("limit=0 应回落到默认 10，实际 %q", gotLimit)
	}
}

// TestInstallerDefaults 验证 User-Agent / 超时 / 重试的默认值。
func TestInstallerDefaults(t *testing.T) {
	i := &Installer{}
	if got := i.userAgent(); got != defaultUserAgent {
		t.Errorf("默认 User-Agent 应为 %q，实际 %q", defaultUserAgent, got)
	}
	if got := i.retries(); got != defaultRetries {
		t.Errorf("默认重试次数应为 %d，实际 %d", defaultRetries, got)
	}
	if got := i.client().Timeout; got != defaultTimeout {
		t.Errorf("默认超时应为 %v，实际 %v", defaultTimeout, got)
	}
	i2 := &Installer{Timeout: 3 * time.Second, Retries: 1, UserAgent: "custom"}
	if got := i2.client().Timeout; got != 3*time.Second {
		t.Errorf("超时应为 3s，实际 %v", got)
	}
	if got := i2.retries(); got != 1 {
		t.Errorf("重试次数应为 1，实际 %d", got)
	}
	if got := i2.userAgent(); got != "custom" {
		t.Errorf("User-Agent 应为 custom，实际 %q", got)
	}
}

// TestModrinthURLConstruction 验证 URL 拼接。
//
// 注意：net/url 的 URL.String() 会把 Path 再转义一次，所以 modrinthURL 只用于
// 已经过 validateProjectRef 校验的 ID/slug（只含字母数字与 -_.），不会出现特殊字符。
func TestModrinthURLConstruction(t *testing.T) {
	got := modrinthURL("project", "AANobbMI", "version")
	if got != "https://api.modrinth.com/v2/project/AANobbMI/version" {
		t.Errorf("URL 错误：%q", got)
	}
	got = modrinthURL("project", "sodium-extra", "version")
	if got != "https://api.modrinth.com/v2/project/sodium-extra/version" {
		t.Errorf("URL 错误：%q", got)
	}
	// 非法引用应在进入 URL 拼接前就被 validateProjectRef 拒绝。
	if err := validateProjectRef("a/b"); err == nil {
		t.Fatal("含斜杠的项目引用应被拒绝")
	}
	if err := validateProjectRef("a?b"); err == nil {
		t.Fatal("含问号的项目引用应被拒绝")
	}
}

// TestCurseForgeURLConstruction 验证 URL 拼接。
func TestCurseForgeURLConstruction(t *testing.T) {
	got := curseForgeURL("mods", "238222", "files")
	if got != "https://api.curseforge.com/v1/mods/238222/files" {
		t.Errorf("URL 错误：%q", got)
	}
}

// TestProjectTypeForClassIDAndLoader 覆盖 ClassID 与 Loader 的各种组合。
func TestProjectTypeForClassIDAndLoader(t *testing.T) {
	cases := []struct {
		loader  string
		classID int
		want    string
	}{
		{"PAPER", 0, "plugin"},
		{"Velocity", 0, "plugin"},
		{"QUILT", 0, "mod"},
		{"neoforge", 0, "mod"},
		{"datapack", 0, "datapack"},
		{"ResourcePack", 0, "resourcepack"},
		{"fabric", classIDDatapacks, "mod"}, // 显式 ClassID 优先
	}
	for _, c := range cases {
		if got := projectTypeFor(c.loader, c.classID); got != c.want {
			t.Errorf("projectTypeFor(%q,%d) 应为 %q，实际 %q", c.loader, c.classID, c.want, got)
		}
	}
	if got := curseForgeProjectType(classIDPlugins); got != "plugin" {
		t.Errorf("classIDPlugins 应映射为 plugin，实际 %q", got)
	}
	if got := curseForgeProjectType(classIDDatapacks); got != "datapack" {
		t.Errorf("classIDDatapacks 应映射为 datapack，实际 %q", got)
	}
	if got := curseForgeProjectType(classIDMods); got != "mod" {
		t.Errorf("classIDMods 应映射为 mod，实际 %q", got)
	}
}

// TestCurseForgeClassIDInference 验证未显式指定 ClassID 时按 Loader 推断。
func TestCurseForgeClassIDInference(t *testing.T) {
	cases := map[string]int{
		"paper": 5, "spigot": 5, "purpur": 5, "velocity": 5,
		"fabric": 6, "forge": 6, "": 6, "quilt": 6,
	}
	for loader, want := range cases {
		i := &Installer{Loader: loader}
		if got := i.curseForgeClassID(); got != want {
			t.Errorf("loader=%q 的 classId 应为 %d，实际 %d", loader, want, got)
		}
	}
	i := &Installer{Loader: "fabric", ClassID: classIDPlugins}
	if got := i.curseForgeClassID(); got != classIDPlugins {
		t.Errorf("显式 ClassID 应优先，实际 %d", got)
	}
}

// TestPickCurseForgeFileSkipsDeleted 验证已删除/审核中的文件会被跳过。
func TestPickCurseForgeFileSkipsDeleted(t *testing.T) {
	files := []cfFile{
		{ID: 1, FileName: "deleted.jar", ReleaseType: 1, FileStatus: 2, GameVersions: []string{"1.21.4", "Forge"}},
		{ID: 2, FileName: "pending.jar", ReleaseType: 1, FileStatus: 1, GameVersions: []string{"1.21.4", "Forge"}},
		{ID: 3, FileName: "ok.jar", ReleaseType: 3, FileStatus: 4, GameVersions: []string{"1.21.4", "Forge"}},
	}
	got := pickCurseForgeFile(files, "1.21.4", "forge")
	if got == nil || got.FileName != "ok.jar" {
		t.Fatalf("应选中 ok.jar，实际 %+v", got)
	}
	// 全部不可用时返回 nil。
	if got := pickCurseForgeFile(files[:2], "1.21.4", "forge"); got != nil {
		t.Errorf("没有可用文件时应返回 nil，实际 %+v", got)
	}
}

// TestLoadersFromGameVersions 验证从 gameVersions 里剥离加载器名。
func TestLoadersFromGameVersions(t *testing.T) {
	got := loadersFromGameVersions([]string{"1.21.4", "Forge", "1.20.1"})
	if len(got) != 1 || got[0] != "Forge" {
		t.Errorf("应只保留 Forge，实际 %v", got)
	}
	if got := loadersFromGameVersions([]string{"1.21.4"}); len(got) != 0 {
		t.Errorf("没有加载器时应返回空，实际 %v", got)
	}
}

// TestModrinthWebURL 验证项目页面地址拼接。
func TestModrinthWebURL(t *testing.T) {
	if got := modrinthWebURL("mod", "sodium"); got != "https://modrinth.com/mod/sodium" {
		t.Errorf("URL 错误：%q", got)
	}
	if got := modrinthWebURL("", "sodium"); got != "https://modrinth.com/mod/sodium" {
		t.Errorf("空类型应回落为 mod，实际 %q", got)
	}
	if got := modrinthWebURL("plugin", "essentialsx"); got != "https://modrinth.com/plugin/essentialsx" {
		t.Errorf("URL 错误：%q", got)
	}
	if got := modrinthWebURL("mod", ""); got != "" {
		t.Errorf("slug 为空时应返回空串，实际 %q", got)
	}
}

// TestParseTime 验证时间解析的容错。
func TestParseTime(t *testing.T) {
	got := parseTime("2025-04-04T00:09:49.666331Z")
	if got.IsZero() {
		t.Fatal("合法时间应被解析")
	}
	if got.Year() != 2025 || got.Month() != time.April || got.Day() != 4 {
		t.Errorf("时间解析错误：%v", got)
	}
	if !parseTime("not-a-time").IsZero() {
		t.Error("非法时间应返回零值")
	}
	if !parseTime("").IsZero() {
		t.Error("空串应返回零值")
	}
}

// TestVersionTypeRank 验证发布类型优先级。
func TestVersionTypeRank(t *testing.T) {
	if !(versionTypeRank("release") < versionTypeRank("beta") &&
		versionTypeRank("beta") < versionTypeRank("alpha") &&
		versionTypeRank("alpha") < versionTypeRank("unknown")) {
		t.Error("优先级应为 release < beta < alpha < 其它")
	}
}

// TestCurseForgeTypePath 验证项目 URL 类型段映射。
func TestCurseForgeTypePath(t *testing.T) {
	cases := map[int]string{
		classIDMods:      "mc-mods",
		classIDPlugins:   "bukkit-plugins",
		classIDDatapacks: "data-packs",
	}
	for classID, want := range cases {
		if got := curseForgeTypePath(classID); got != want {
			t.Errorf("classID=%d 的路径段应为 %q，实际 %q", classID, want, got)
		}
	}
}

// TestJsonJoinPath 验证路径拼接工具不会产生双斜杠。
func TestJsonJoinPath(t *testing.T) {
	if got := joinPath("v2", "project", "x"); got != "/v2/project/x" {
		t.Errorf("joinPath 错误：%q", got)
	}
	if got := joinPath("/v2/", "/project/", "x/"); got != "/v2/project/x" {
		t.Errorf("joinPath 应清理多余斜杠，实际 %q", got)
	}
	if got := joinPath(); got != "/" {
		t.Errorf("空输入应返回 /，实际 %q", got)
	}
}

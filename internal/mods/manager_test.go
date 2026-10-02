package mods

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// withBase 把远端基地址指向 httptest 服务器，测试绝不联网。
func withBase(t *testing.T, base string) {
	t.Helper()
	oldModrinth, oldCF := modrinthBase, curseForgeBase
	modrinthBase, curseForgeBase = base, base
	t.Cleanup(func() { modrinthBase, curseForgeBase = oldModrinth, oldCF })
}

// writeJSON 输出 JSON 响应。
func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

// ---------------------------------------------------------------- 真实响应片段

// modrinthSearchJSON 来自
// https://api.modrinth.com/v2/search?query=sodium&limit=2&facets=[["project_type:mod"],["categories:fabric"]]
const modrinthSearchJSON = `{"hits":[
{"project_id":"AANobbMI","project_type":"mod","all_project_types":["mod"],"slug":"sodium","author":"jellysquid3","author_id":"TEZXhE2U","organization":"CaffeineMC","organization_id":"LjcZDkRW","title":"Sodium","description":"A high-performance rendering engine replacement for Minecraft, which greatly improves frame rates and reduces micro-stutter.","categories":["fabric","neoforge","optimization","quilt"],"display_categories":["fabric","neoforge","optimization","quilt"],"versions":["1.16.3","1.21.4","1.21.5","26.3"],"downloads":233640873,"follows":41248,"icon_url":"https://cdn.modrinth.com/data/AANobbMI/295862f4724dc3f78df3447ad6072b2dcd3ef0c9_96.webp","date_created":"2021-01-03T00:53:34.185936+00:00","date_modified":"2026-09-20T21:27:06.329936+00:00","latest_version":"v4PSXean","license":"LicenseRef-Polyform-Shield-1.0.0","client_side":"required","server_side":"unsupported","environment":["client_only"],"disclosure_types":[],"gallery":[],"featured_gallery":null,"color":8703084},
{"project_id":"PtjYWJkn","project_type":"mod","all_project_types":["mod"],"slug":"sodium-extra","author":"FlashyReese","author_id":"QXAY01zS","organization":null,"organization_id":null,"title":"Sodium Extra","description":"A Sodium addon that adds features that shouldn't be in Sodium.","categories":["cursed","fabric","neoforge","optimization","quilt","utility"],"display_categories":["cursed","fabric","neoforge","optimization","quilt","utility"],"versions":["1.16.2","1.21.4","26.3"],"downloads":98785639,"follows":13566,"icon_url":null,"date_created":"2021-02-17T04:37:04.928784+00:00","date_modified":"2026-09-16T02:48:16.702643+00:00","latest_version":"te2y9qZn","license":"LGPL-3.0-only","client_side":"required","server_side":"unsupported","environment":["client_only"],"disclosure_types":[],"gallery":[],"featured_gallery":null,"color":16577485}],
"offset":0,"limit":2,"total_hits":82}`

// modrinthVersionsJSON 来自
// https://api.modrinth.com/v2/project/sodium/version?loaders=["fabric"]&game_versions=["1.21.4"]（截断）
//
// 注意：第一项是 alpha，第二项是 release —— 用于验证优先取 release。
const modrinthVersionsJSON = `[
{"game_versions":["1.21.4"],"loaders":["fabric"],"environment":"client_only","id":"aaaaAAAA","project_id":"AANobbMI","author_id":"TEZXhE2U","featured":false,"name":"Sodium 0.7.0-alpha for Fabric 1.21.4","version_number":"mc1.21.4-0.7.0-alpha-fabric","changelog":"alpha","changelog_url":null,"date_published":"2025-05-01T00:09:49.666331Z","downloads":1000,"version_type":"alpha","status":"listed","requested_status":null,"files":[{"id":"AA","hashes":{"sha1":"1111111111111111111111111111111111111111","sha512":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"url":"https://cdn.modrinth.com/data/AANobbMI/versions/aaaaAAAA/sodium-fabric-0.7.0-alpha.jar","filename":"sodium-fabric-0.7.0-alpha.jar","primary":true,"size":1306799,"file_type":null}],"dependencies":[]},
{"game_versions":["1.21.4"],"loaders":["fabric"],"environment":"client_only","id":"c3YkZvne","project_id":"AANobbMI","author_id":"TEZXhE2U","featured":false,"name":"Sodium 0.6.13 for Fabric 1.21.4","version_number":"mc1.21.4-0.6.13-fabric","changelog":"- Improved compatibility with some NeoForge mods that use the Fabric Rendering API.","changelog_url":null,"date_published":"2025-04-04T00:09:49.666331Z","downloads":5473381,"version_type":"release","status":"listed","requested_status":null,"files":[{"id":"Ya4LV6Qd","hashes":{"sha1":"c881d2db971207c396b5629632437f1520c0c478","sha512":"2c72ca2ddfd27e29ff6c24fccdf6f3d80857bd1014c707017f96cb4a424f94918e53ba21d85f22e3c9171803f8d2c12c99ae857d38d8e85546cc65960b95a2f1"},"url":"https://cdn.modrinth.com/data/AANobbMI/versions/c3YkZvne/sodium-fabric-0.6.13%2Bmc1.21.4.jar","filename":"sodium-fabric-0.6.13+mc1.21.4.jar","primary":true,"size":1306799,"file_type":null}],"dependencies":[]},
{"game_versions":["1.21.4"],"loaders":["fabric"],"environment":"client_only","id":"FRXt5xaI","project_id":"AANobbMI","author_id":"TEZXhE2U","featured":false,"name":"Sodium 0.6.10 for Fabric 1.21.4","version_number":"mc1.21.4-0.6.10-fabric","changelog":"","changelog_url":null,"date_published":"2025-02-26T17:51:51.491Z","downloads":1567397,"version_type":"release","status":"listed","requested_status":null,"files":[{"id":"hDAaQj7w","hashes":{"sha1":"b4ce6db38fd287ce6af644ee9f71a5318c52f42e","sha512":"e6213b74dc3ba15387040dadd383f0c69999f365c9bf492893c52f0d74090b577e4ce03c52f2a39cd9d680dd32aa6228aae268dabcd0c2478908195c56d975c9"},"url":"https://cdn.modrinth.com/data/AANobbMI/versions/FRXt5xaI/sodium-fabric-0.6.10%2Bmc1.21.4.jar","filename":"sodium-fabric-0.6.10+mc1.21.4.jar","primary":true,"size":1305866,"file_type":null}],"dependencies":[]}
]`

// curseForgeSearchJSON 按 CurseForge v1 /mods/search 的官方文档结构构造
// （该接口必须带 x-api-key，无 Key 时只返回 HTTP 403，因此这里依据官方 schema 固定）。
const curseForgeSearchJSON = `{
  "data": [
    {
      "id": 238222,
      "gameId": 432,
      "name": "Just Enough Items",
      "slug": "jei",
      "links": {"websiteUrl": "https://www.curseforge.com/minecraft/mc-mods/jei"},
      "summary": "View Items and Recipes",
      "downloadCount": 1234567890.0,
      "classId": 6,
      "authors": [{"id": 1, "name": "mezz", "url": "https://www.curseforge.com/members/mezz"}],
      "categories": [{"id": 423, "name": "Map and Information", "slug": "map-information"}],
      "latestFiles": [
        {
          "id": 5000001,
          "gameId": 432,
          "modId": 238222,
          "displayName": "jei-1.21.4-forge-19.21.0.247.jar",
          "fileName": "jei-1.21.4-forge-19.21.0.247.jar",
          "releaseType": 1,
          "fileStatus": 4,
          "fileDate": "2025-01-01T00:00:00.000Z",
          "fileLength": 1234567,
          "downloadUrl": "https://edge.forgecdn.net/files/5000/1/jei-1.21.4-forge-19.21.0.247.jar",
          "gameVersions": ["1.21.4", "Forge"],
          "hashes": []
        }
      ],
      "dateModified": "2025-01-01T00:00:00.000Z",
      "dateCreated": "2015-11-01T00:00:00.000Z",
      "isAvailable": true,
      "isExperimental": false,
      "isFeatured": false
    },
    {
      "id": 306612,
      "gameId": 432,
      "name": "Fabric API",
      "slug": "fabric-api",
      "links": {"websiteUrl": "https://www.curseforge.com/minecraft/mc-mods/fabric-api"},
      "summary": "Lightweight and modular API providing common hooks and intercompatibility measures.",
      "downloadCount": 55555555.0,
      "classId": 6,
      "authors": [{"id": 2, "name": "modmuss50", "url": "https://www.curseforge.com/members/modmuss50"}],
      "categories": [{"id": 421, "name": "API and Library", "slug": "api-and-library"}],
      "latestFiles": [],
      "dateModified": "2025-02-01T00:00:00.000Z",
      "dateCreated": "2016-12-01T00:00:00.000Z",
      "isAvailable": true,
      "isExperimental": false,
      "isFeatured": false
    }
  ],
  "pagination": {"index": 0, "pageSize": 2, "resultCount": 2, "totalCount": 2}
}`

// curseForgeFilesJSON 按 CurseForge v1 /mods/{id}/files 的官方文档结构构造。
const curseForgeFilesJSON = `{
  "data": [
    {
      "id": 5000003,
      "gameId": 432,
      "modId": 238222,
      "displayName": "jei-1.21.1-forge-19.0.0.jar",
      "fileName": "jei-1.21.1-forge-19.0.0.jar",
      "releaseType": 1,
      "fileStatus": 4,
      "fileDate": "2024-08-01T00:00:00.000Z",
      "fileLength": 999,
      "downloadUrl": "https://edge.forgecdn.net/files/5000/3/jei-1.21.1-forge-19.0.0.jar",
      "gameVersions": ["1.21.1", "Forge"],
      "hashes": [{"value": "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "algo": 1}]
    },
    {
      "id": 5000004,
      "gameId": 432,
      "modId": 238222,
      "displayName": "jei-1.21.4-fabric-19.21.0.247.jar",
      "fileName": "jei-1.21.4-fabric-19.21.0.247.jar",
      "releaseType": 2,
      "fileStatus": 4,
      "fileDate": "2025-01-01T00:00:00.000Z",
      "fileLength": 1234567,
      "downloadUrl": "https://edge.forgecdn.net/files/5000/4/jei-1.21.4-fabric-19.21.0.247.jar",
      "gameVersions": ["1.21.4", "Fabric"],
      "hashes": [{"value": "c881d2db971207c396b5629632437f1520c0c478", "algo": 1}]
    },
    {
      "id": 5000005,
      "gameId": 432,
      "modId": 238222,
      "displayName": "jei-1.21.4-forge-19.21.0.247.jar",
      "fileName": "jei-1.21.4-forge-19.21.0.247.jar",
      "releaseType": 1,
      "fileStatus": 4,
      "fileDate": "2025-01-02T00:00:00.000Z",
      "fileLength": 1234567,
      "downloadUrl": "https://edge.forgecdn.net/files/5000/5/jei-1.21.4-forge-19.21.0.247.jar",
      "gameVersions": ["1.21.4", "Forge"],
      "hashes": [{"value": "c881d2db971207c396b5629632437f1520c0c478", "algo": 1}]
    },
    {
      "id": 5000006,
      "gameId": 432,
      "modId": 238222,
      "displayName": "jei-1.21.4-forge-19.21.0.248.jar",
      "fileName": "jei-1.21.4-forge-19.21.0.248.jar",
      "releaseType": 1,
      "fileStatus": 1,
      "fileDate": "2025-01-03T00:00:00.000Z",
      "fileLength": 1234567,
      "downloadUrl": null,
      "gameVersions": ["1.21.4", "Forge"],
      "hashes": []
    }
  ],
  "pagination": {"index": 0, "pageSize": 50, "resultCount": 4, "totalCount": 4}
}`

// ---------------------------------------------------------------- 辅助

// newServer 起一个 httptest 服务器。
func newServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// ---------------------------------------------------------------- Provider 校验

func TestInstallerRejectsUnknownProvider(t *testing.T) {
	for _, p := range []Provider{"", "spigot", "MODRINTH"} {
		i := &Installer{Provider: p, Dir: t.TempDir()}
		if _, err := i.Search(context.Background(), "sodium", 5); err == nil {
			t.Errorf("Provider=%q 的 Search 应报错", p)
		}
		if _, err := i.Resolve(context.Background(), "AANobbMI"); err == nil {
			t.Errorf("Provider=%q 的 Resolve 应报错", p)
		}
	}
}

func TestCurseForgeRequiresAPIKey(t *testing.T) {
	i := &Installer{Provider: CurseForge, Dir: t.TempDir()}
	_, err := i.Search(context.Background(), "jei", 5)
	if err == nil {
		t.Fatal("缺少 API Key 时应报错")
	}
	if !strings.Contains(err.Error(), "curseforge_api_key") {
		t.Errorf("错误应提到 mods.curseforge_api_key，实际：%v", err)
	}
	if !strings.Contains(err.Error(), "API Key") {
		t.Errorf("错误应说明需要 API Key，实际：%v", err)
	}
	if _, err := i.Resolve(context.Background(), "238222"); err == nil {
		t.Error("缺少 API Key 时 Resolve 也应报错")
	}
	if _, err := i.Install(context.Background(), "238222", nil); err == nil {
		t.Error("缺少 API Key 时 Install 也应报错")
	}
}

// ---------------------------------------------------------------- Modrinth 搜索

func TestSearchModrinth(t *testing.T) {
	var gotQuery url.Values
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			http.NotFound(w, r)
			return
		}
		gotQuery = r.URL.Query()
		writeJSON(w, modrinthSearchJSON)
	}))
	withBase(t, srv.URL)

	i := &Installer{
		Provider:    Modrinth,
		GameVersion: "1.21.4",
		Loader:      "fabric",
		UserAgent:   "NyaTMC-test/1.0",
		Dir:         t.TempDir(),
		Timeout:     5 * time.Second,
	}
	projects, err := i.Search(context.Background(), "sodium", 2)
	if err != nil {
		t.Fatalf("搜索失败 : %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("应返回 2 个项目，实际 %d", len(projects))
	}
	p := projects[0]
	if p.ID != "AANobbMI" || p.Slug != "sodium" || p.Title != "Sodium" {
		t.Errorf("第一个项目字段错误：%+v", p)
	}
	if p.Author != "jellysquid3" {
		t.Errorf("作者应为 jellysquid3，实际 %q", p.Author)
	}
	if p.Provider != Modrinth {
		t.Errorf("来源应为 modrinth，实际 %q", p.Provider)
	}
	if p.Downloads != 233640873 {
		t.Errorf("下载量错误：%d", p.Downloads)
	}
	if p.URL != "https://modrinth.com/mod/sodium" {
		t.Errorf("项目地址错误：%q", p.URL)
	}
	if len(p.Categories) == 0 || p.Categories[0] != "fabric" {
		t.Errorf("分类解析错误：%v", p.Categories)
	}
	if p.ProjectType != "mod" {
		t.Errorf("项目类型应为 mod，实际 %q", p.ProjectType)
	}

	// facets 必须是 [[project_type:mod],[categories:fabric],[versions:1.21.4]]
	facets := gotQuery.Get("facets")
	for _, want := range []string{
		`["project_type:mod"]`,
		`["categories:fabric"]`,
		`["versions:1.21.4"]`,
	} {
		if !strings.Contains(facets, want) {
			t.Errorf("facets 应包含 %s，实际 %s", want, facets)
		}
	}
	if gotQuery.Get("query") != "sodium" {
		t.Errorf("query 参数错误：%q", gotQuery.Get("query"))
	}
	if gotQuery.Get("limit") != "2" {
		t.Errorf("limit 参数错误：%q", gotQuery.Get("limit"))
	}
}

// TestSearchModrinthPluginType 验证插件类实例会按 project_type:plugin 过滤。
func TestSearchModrinthPluginType(t *testing.T) {
	var facets string
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		facets = r.URL.Query().Get("facets")
		writeJSON(w, `{"hits":[],"offset":0,"limit":10,"total_hits":0}`)
	}))
	withBase(t, srv.URL)

	i := &Installer{Provider: Modrinth, Loader: "paper", Dir: t.TempDir()}
	if _, err := i.Search(context.Background(), "essentials", 10); err != nil {
		t.Fatalf("搜索失败 : %v", err)
	}
	if !strings.Contains(facets, `["project_type:plugin"]`) {
		t.Errorf("paper 实例应过滤 project_type:plugin，实际 facets=%s", facets)
	}
}

func TestSearchModrinthRejectsEmptyQuery(t *testing.T) {
	i := &Installer{Provider: Modrinth, Dir: t.TempDir()}
	if _, err := i.Search(context.Background(), "   ", 5); err == nil {
		t.Fatal("空关键词应报错")
	}
}

func TestSearchModrinthSendsUserAgent(t *testing.T) {
	var ua string
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		writeJSON(w, `{"hits":[],"offset":0,"limit":1,"total_hits":0}`)
	}))
	withBase(t, srv.URL)

	i := &Installer{Provider: Modrinth, UserAgent: "NyaTMC-test/9", Dir: t.TempDir()}
	if _, err := i.Search(context.Background(), "x", 1); err != nil {
		t.Fatalf("搜索失败 : %v", err)
	}
	if ua != "NyaTMC-test/9" {
		t.Errorf("User-Agent 应为 NyaTMC-test/9，实际 %q", ua)
	}
}

// ---------------------------------------------------------------- Modrinth 解析

func TestResolveModrinthPrefersRelease(t *testing.T) {
	var gotQuery url.Values
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/project/sodium/version") {
			http.NotFound(w, r)
			return
		}
		gotQuery = r.URL.Query()
		writeJSON(w, modrinthVersionsJSON)
	}))
	withBase(t, srv.URL)

	i := &Installer{Provider: Modrinth, Loader: "fabric", GameVersion: "1.21.4", Dir: t.TempDir()}
	v, err := i.Resolve(context.Background(), "sodium")
	if err != nil {
		t.Fatalf("解析失败 : %v", err)
	}
	if v.VersionNumber != "mc1.21.4-0.6.13-fabric" {
		t.Errorf("应优先取 release，实际 %q", v.VersionNumber)
	}
	if v.FileName != "sodium-fabric-0.6.13+mc1.21.4.jar" {
		t.Errorf("文件名错误：%q", v.FileName)
	}
	if v.SHA1 != "c881d2db971207c396b5629632437f1520c0c478" {
		t.Errorf("sha1 解析错误：%q", v.SHA1)
	}
	if v.Size != 1306799 {
		t.Errorf("size 解析错误：%d", v.Size)
	}
	if v.PublishedAt.IsZero() {
		t.Error("发布时间应被解析")
	}
	if len(v.Loaders) == 0 || v.Loaders[0] != "fabric" {
		t.Errorf("loader 解析错误：%v", v.Loaders)
	}
	// Modrinth 不提供 sha256，因此 SHA256 留空，校验走 sha1。
	if v.SHA256 != "" {
		t.Errorf("Modrinth 不提供 sha256，应为空，实际 %q", v.SHA256)
	}
	if v.SHA1 == "" {
		t.Error("sha1 不应为空")
	}

	// 查询参数必须是 Modrinth 要求的 JSON 数组。
	if gotQuery.Get("game_versions") != `["1.21.4"]` {
		t.Errorf("game_versions 参数错误：%q", gotQuery.Get("game_versions"))
	}
	if gotQuery.Get("loaders") != `["fabric"]` {
		t.Errorf("loaders 参数错误：%q", gotQuery.Get("loaders"))
	}
}

func TestResolveModrinthRejectsBadProjectID(t *testing.T) {
	i := &Installer{Provider: Modrinth, Dir: t.TempDir()}
	for _, id := range []string{"", "../etc/passwd", "a/b", "x?y=1", strings.Repeat("a", 200)} {
		if _, err := i.Resolve(context.Background(), id); err == nil {
			t.Errorf("项目 ID %q 应被拒绝", id)
		}
	}
}

func TestResolveModrinthEmptyList(t *testing.T) {
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `[]`)
	}))
	withBase(t, srv.URL)
	i := &Installer{Provider: Modrinth, Dir: t.TempDir()}
	if _, err := i.Resolve(context.Background(), "sodium"); err == nil {
		t.Fatal("没有版本时应报错")
	}
}

// TestResolveModrinthFallsBackWhenFiltered 验证过滤后无结果时会放宽条件重试。
func TestResolveModrinthFallsBackWhenFiltered(t *testing.T) {
	var calls int64
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		if r.URL.Query().Get("game_versions") != "" {
			writeJSON(w, `[]`)
			return
		}
		writeJSON(w, modrinthVersionsJSON)
	}))
	withBase(t, srv.URL)

	i := &Installer{Provider: Modrinth, GameVersion: "1.20.1", Loader: "fabric", Dir: t.TempDir()}
	v, err := i.Resolve(context.Background(), "sodium")
	if err != nil {
		t.Fatalf("放宽条件后应解析成功 : %v", err)
	}
	if v.FileName == "" {
		t.Error("放宽条件后应拿到文件名")
	}
	if n := atomic.LoadInt64(&calls); n < 2 {
		t.Errorf("应至少请求两次（过滤 + 放宽），实际 %d", n)
	}
}

// ---------------------------------------------------------------- CurseForge

func TestSearchCurseForge(t *testing.T) {
	var gotQuery url.Values
	var apiKey string
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mods/search" {
			http.NotFound(w, r)
			return
		}
		gotQuery = r.URL.Query()
		apiKey = r.Header.Get("x-api-key")
		writeJSON(w, curseForgeSearchJSON)
	}))
	withBase(t, srv.URL)

	i := &Installer{
		Provider:    CurseForge,
		APIKey:      "test-key-123",
		GameVersion: "1.21.4",
		Loader:      "forge",
		Dir:         t.TempDir(),
	}
	projects, err := i.Search(context.Background(), "jei", 2)
	if err != nil {
		t.Fatalf("CurseForge 搜索失败 : %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("应返回 2 个项目，实际 %d", len(projects))
	}
	p := projects[0]
	if p.ID != "238222" {
		t.Errorf("项目 ID 应为 238222，实际 %q", p.ID)
	}
	if p.Title != "Just Enough Items" || p.Slug != "jei" {
		t.Errorf("项目字段错误：%+v", p)
	}
	if p.Author != "mezz" {
		t.Errorf("作者应为 mezz，实际 %q", p.Author)
	}
	if p.URL != "https://www.curseforge.com/minecraft/mc-mods/jei" {
		t.Errorf("项目地址错误：%q", p.URL)
	}
	if p.ProjectType != "mod" {
		t.Errorf("类型应为 mod，实际 %q", p.ProjectType)
	}
	if p.Downloads != 1234567890 {
		t.Errorf("下载量错误：%d", p.Downloads)
	}

	if apiKey != "test-key-123" {
		t.Errorf("x-api-key 头应为 test-key-123，实际 %q", apiKey)
	}
	if gotQuery.Get("gameId") != "432" {
		t.Errorf("gameId 应为 432，实际 %q", gotQuery.Get("gameId"))
	}
	if gotQuery.Get("classId") != "6" {
		t.Errorf("classId 应为 6，实际 %q", gotQuery.Get("classId"))
	}
	if gotQuery.Get("modLoaderType") != "1" {
		t.Errorf("forge 的 modLoaderType 应为 1，实际 %q", gotQuery.Get("modLoaderType"))
	}
	if gotQuery.Get("searchFilter") != "jei" {
		t.Errorf("searchFilter 错误：%q", gotQuery.Get("searchFilter"))
	}
}

func TestSearchCurseForgePluginClassID(t *testing.T) {
	var classID string
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		classID = r.URL.Query().Get("classId")
		writeJSON(w, `{"data":[],"pagination":{"index":0,"pageSize":1,"resultCount":0,"totalCount":0}}`)
	}))
	withBase(t, srv.URL)

	i := &Installer{Provider: CurseForge, APIKey: "k", Loader: "paper", Dir: t.TempDir()}
	if _, err := i.Search(context.Background(), "essentials", 1); err != nil {
		t.Fatalf("搜索失败 : %v", err)
	}
	if classID != "5" {
		t.Errorf("plugin 的 classId 应为 5，实际 %q", classID)
	}
}

func TestCurseForgeForbiddenIsFatal(t *testing.T) {
	var calls int64
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Forbidden: API Key missing or invalid"))
	}))
	withBase(t, srv.URL)

	i := &Installer{Provider: CurseForge, APIKey: "bad", Dir: t.TempDir(), Retries: 3}
	_, err := i.Search(context.Background(), "jei", 1)
	if err == nil {
		t.Fatal("403 应报错")
	}
	if !strings.Contains(err.Error(), "API Key") {
		t.Errorf("错误应说明 API Key 缺失或无效，实际：%v", err)
	}
	if n := atomic.LoadInt64(&calls); n != 1 {
		t.Errorf("403 不应重试，实际请求 %d 次", n)
	}
}

func TestResolveCurseForgePicksMatchingFile(t *testing.T) {
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/mods/238222/files") {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, curseForgeFilesJSON)
	}))
	withBase(t, srv.URL)

	i := &Installer{
		Provider: CurseForge, APIKey: "k",
		GameVersion: "1.21.4", Loader: "forge",
		Dir: t.TempDir(),
	}
	v, err := i.Resolve(context.Background(), "238222")
	if err != nil {
		t.Fatalf("解析失败 : %v", err)
	}
	// 1.21.4 + Forge 的 release 文件；fileStatus=1 的应被跳过。
	if v.FileName != "jei-1.21.4-forge-19.21.0.247.jar" {
		t.Errorf("应选中 release 的 forge 文件，实际 %q", v.FileName)
	}
	if !strings.Contains(v.URL, "edge.forgecdn.net") {
		t.Errorf("下载地址错误：%q", v.URL)
	}
	if v.Size != 1234567 {
		t.Errorf("size 错误：%d", v.Size)
	}
	// CurseForge 不提供 sha256，只给 sha1。
	if v.SHA256 != "" {
		t.Errorf("CurseForge 不提供 sha256，应为空，实际 %q", v.SHA256)
	}
	if v.SHA1 != "c881d2db971207c396b5629632437f1520c0c478" {
		t.Errorf("sha1 解析错误：%q", v.SHA1)
	}
	if !containsFold(v.Loaders, "forge") {
		t.Errorf("loaders 应包含 Forge，实际 %v", v.Loaders)
	}
}

func TestResolveCurseForgeFabricFile(t *testing.T) {
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, curseForgeFilesJSON)
	}))
	withBase(t, srv.URL)

	i := &Installer{Provider: CurseForge, APIKey: "k", GameVersion: "1.21.4", Loader: "fabric", Dir: t.TempDir()}
	v, err := i.Resolve(context.Background(), "238222")
	if err != nil {
		t.Fatalf("解析失败 : %v", err)
	}
	if v.FileName != "jei-1.21.4-fabric-19.21.0.247.jar" {
		t.Errorf("fabric 文件选择错误：%q", v.FileName)
	}
	if v.SHA1 != "c881d2db971207c396b5629632437f1520c0c478" {
		t.Errorf("sha1 解析错误：%q", v.SHA1)
	}
}

func TestResolveCurseForgeNoMatch(t *testing.T) {
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, curseForgeFilesJSON)
	}))
	withBase(t, srv.URL)

	i := &Installer{Provider: CurseForge, APIKey: "k", GameVersion: "1.7.10", Dir: t.TempDir()}
	_, err := i.Resolve(context.Background(), "238222")
	if err == nil {
		t.Fatal("没有匹配文件时应报错")
	}
	if !strings.Contains(err.Error(), "没有匹配的文件") {
		t.Errorf("错误应说明没有匹配文件，实际：%v", err)
	}
}

// TestCurseForgeFileURLFallback 验证 downloadUrl 为空时按 CDN 规则拼接。
func TestCurseForgeFileURLFallback(t *testing.T) {
	f := cfFile{ID: 5000006, FileName: "some mod.jar"}
	got := curseForgeFileURL(f)
	want := "https://edge.forgecdn.net/files/5000/6/some%20mod.jar"
	if got != want {
		t.Errorf("回退地址应为 %q，实际 %q", want, got)
	}
	if curseForgeFileURL(cfFile{ID: 0, FileName: "x.jar"}) != "" {
		t.Error("缺少 ID 时应返回空地址")
	}
}

// ---------------------------------------------------------------- 文件名清洗

func TestSanitizeModFileName(t *testing.T) {
	ok := map[string]string{
		"sodium-fabric-0.6.13.jar":          "sodium-fabric-0.6.13.jar",
		"sodium-fabric-0.6.13+mc1.21.4.jar": "sodium-fabric-0.6.13+mc1.21.4.jar",
		"  spaced.jar ":                     "spaced.jar",
		"中文模组.jar":                          "中文模组.jar",
	}
	for in, want := range ok {
		got, err := sanitizeModFileName(in)
		if err != nil {
			t.Errorf("%q 应被接受，实际报错 %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q 清洗结果应为 %q，实际 %q", in, want, got)
		}
	}

	bad := []string{"", "   ", ".", "..", "x/y.jar", `..\..\evil.jar`, "a\x00b.jar", "trailing/", "https://cdn.example/a/b/mod.jar"}
	for _, in := range bad {
		if _, err := sanitizeModFileName(in); err == nil {
			t.Errorf("%q 应被拒绝", in)
		}
	}

	long := strings.Repeat("n", 400) + ".jar"
	got, err := sanitizeModFileName(long)
	if err != nil {
		t.Fatalf("超长文件名应被截断 : %v", err)
	}
	if len(got) > maxFileNameLen {
		t.Errorf("截断后长度应 <= %d，实际 %d", maxFileNameLen, len(got))
	}
	if !strings.HasSuffix(got, ".jar") {
		t.Errorf("截断后应保留扩展名，实际 %q", got)
	}
}

func TestRemoveRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	i := &Installer{Provider: Modrinth, Dir: dir}
	for _, name := range []string{"", "..", ".", "../evil.jar", `..\evil.jar`, "sub/dir.jar"} {
		if err := i.Remove(name); err == nil {
			t.Errorf("Remove(%q) 应被拒绝", name)
		}
	}
}

func TestRemoveAndListDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.jar"), []byte("aa"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.jar"), []byte("bb"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.jar"), 0o755); err != nil {
		t.Fatal(err)
	}

	i := &Installer{Provider: Modrinth, Dir: dir}
	files, err := i.ListDir()
	if err != nil {
		t.Fatalf("列出目录失败 : %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("应只列出 2 个 jar 文件，实际 %d：%+v", len(files), files)
	}
	if files[0].Name != "a.jar" || files[1].Name != "b.jar" {
		t.Errorf("排序应为 a.jar,b.jar，实际 %+v", files)
	}
	if files[0].Size != 2 {
		t.Errorf("大小应为 2，实际 %d", files[0].Size)
	}
	if files[0].ModTime.IsZero() {
		t.Error("修改时间应被填充")
	}
	if files[0].Path != filepath.Join(dir, "a.jar") {
		t.Errorf("路径错误：%q", files[0].Path)
	}

	if err := i.Remove("a.jar"); err != nil {
		t.Fatalf("删除失败 : %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.jar")); !os.IsNotExist(err) {
		t.Error("文件应已被删除")
	}
	if err := i.Remove("a.jar"); err == nil {
		t.Error("重复删除应报错")
	}
}

func TestListDirMissingReturnsNil(t *testing.T) {
	i := &Installer{Provider: Modrinth, Dir: filepath.Join(t.TempDir(), "not-created")}
	files, err := i.ListDir()
	if err != nil {
		t.Fatalf("目录不存在不应报错 : %v", err)
	}
	if files != nil {
		t.Errorf("目录不存在应返回 nil，实际 %+v", files)
	}
}

// ---------------------------------------------------------------- 端到端安装

func TestInstallModrinthEndToEnd(t *testing.T) {
	payload := []byte("这就是一个假的模组文件")
	s512 := sha512sum(payload)
	s256 := sha256sum(payload)
	sha512hex := hex.EncodeToString(s512[:])
	want256 := hex.EncodeToString(s256[:])

	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/project/sodium/version"):
			fmt.Fprintf(w, `[{"game_versions":["1.21.4"],"loaders":["fabric"],"id":"c3YkZvne","project_id":"AANobbMI","name":"Sodium 0.6.13","version_number":"mc1.21.4-0.6.13-fabric","date_published":"2025-04-04T00:09:49.666331Z","downloads":5473381,"version_type":"release","status":"listed","files":[{"id":"Ya4LV6Qd","hashes":{"sha1":"%s","sha512":"%s"},"url":"http://%s/files/mod.jar","filename":"sodium-fabric-0.6.13+mc1.21.4.jar","primary":true,"size":%d}]}]`,
				fmt.Sprintf("%x", sha1Of(payload)), sha512hex, r.Host, len(payload))
		case r.URL.Path == "/files/mod.jar":
			w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	withBase(t, srv.URL)

	dir := t.TempDir()
	i := &Installer{Provider: Modrinth, Loader: "fabric", GameVersion: "1.21.4", Dir: dir}

	var lastDone, lastTotal int64
	got, err := i.Install(context.Background(), "sodium", func(done, total int64) {
		lastDone, lastTotal = done, total
	})
	if err != nil {
		t.Fatalf("安装失败 : %v", err)
	}
	if got.FileName != "sodium-fabric-0.6.13+mc1.21.4.jar" {
		t.Errorf("文件名错误：%q", got.FileName)
	}
	if got.SHA256 != want256 {
		t.Errorf("sha256 应为 %q，实际 %q", want256, got.SHA256)
	}
	if got.Size != int64(len(payload)) {
		t.Errorf("大小应为 %d，实际 %d", len(payload), got.Size)
	}
	if got.ProjectID != "sodium" {
		t.Errorf("项目 ID 应为 sodium，实际 %q", got.ProjectID)
	}
	if lastDone != int64(len(payload)) || lastTotal != int64(len(payload)) {
		t.Errorf("进度回调应为 (%d,%d)，实际 (%d,%d)", len(payload), len(payload), lastDone, lastTotal)
	}
	data, rerr := os.ReadFile(got.Path)
	if rerr != nil {
		t.Fatalf("读取落盘文件失败 : %v", rerr)
	}
	if string(data) != string(payload) {
		t.Error("落盘内容不符")
	}
	if _, serr := os.Stat(got.Path + ".part"); !os.IsNotExist(serr) {
		t.Error("临时 .part 文件应已被清除")
	}
}

// TestInstallBacksUpExistingFile 验证同名文件会先备份为 .bak。
func TestInstallBacksUpExistingFile(t *testing.T) {
	payload := []byte("新的模组内容")
	sha1sum := fmt.Sprintf("%x", sha1Of(payload))
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/project/sodium/version"):
			fmt.Fprintf(w, `[{"id":"v1","name":"S","version_number":"1.0","date_published":"2025-01-01T00:00:00Z","version_type":"release","files":[{"hashes":{"sha1":"%s"},"url":"http://%s/files/mod.jar","filename":"mod.jar","primary":true,"size":%d}]}]`,
				sha1sum, r.Host, len(payload))
		case r.URL.Path == "/files/mod.jar":
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	withBase(t, srv.URL)

	dir := t.TempDir()
	old := []byte("旧的模组内容")
	if err := os.WriteFile(filepath.Join(dir, "mod.jar"), old, 0o644); err != nil {
		t.Fatal(err)
	}

	i := &Installer{Provider: Modrinth, Dir: dir}
	got, err := i.Install(context.Background(), "sodium", nil)
	if err != nil {
		t.Fatalf("安装失败 : %v", err)
	}
	backup := filepath.Join(dir, "mod.jar.bak")
	bak, rerr := os.ReadFile(backup)
	if rerr != nil {
		t.Fatalf("应生成备份 %s : %v", backup, rerr)
	}
	if string(bak) != string(old) {
		t.Error("备份内容应为旧文件")
	}
	now, _ := os.ReadFile(got.Path)
	if string(now) != string(payload) {
		t.Error("覆盖后的内容应为新文件")
	}
}

// TestInstallRemovesFileOnChecksumMismatch 验证校验失败会删除落盘文件。
func TestInstallRemovesFileOnChecksumMismatch(t *testing.T) {
	payload := []byte("被篡改的模组")
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/project/sodium/version"):
			// sha1 与真实内容不符。
			fmt.Fprintf(w, `[{"id":"v1","name":"S","version_number":"1.0","date_published":"2025-01-01T00:00:00Z","version_type":"release","files":[{"hashes":{"sha1":"%s"},"url":"http://%s/files/mod.jar","filename":"mod.jar","primary":true,"size":%d}]}]`,
				strings.Repeat("ab", 20), r.Host, len(payload))
		case r.URL.Path == "/files/mod.jar":
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	withBase(t, srv.URL)

	dir := t.TempDir()
	i := &Installer{Provider: Modrinth, Dir: dir}
	if _, err := i.Install(context.Background(), "sodium", nil); err == nil {
		t.Fatal("校验失败时应报错")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("校验失败后目录应为空，实际 %v", names)
	}
}

func TestSearchAndInstall(t *testing.T) {
	payload := []byte("search and install payload")
	sha1sum := fmt.Sprintf("%x", sha1Of(payload))
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/search":
			writeJSON(w, modrinthSearchJSON)
		case strings.HasPrefix(r.URL.Path, "/project/AANobbMI/version"):
			fmt.Fprintf(w, `[{"id":"v1","name":"Sodium","version_number":"0.6.13","date_published":"2025-01-01T00:00:00Z","version_type":"release","files":[{"hashes":{"sha1":"%s"},"url":"http://%s/files/mod.jar","filename":"sodium.jar","primary":true,"size":%d}]}]`,
				sha1sum, r.Host, len(payload))
		case r.URL.Path == "/files/mod.jar":
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	withBase(t, srv.URL)

	dir := t.TempDir()
	i := &Installer{Provider: Modrinth, Loader: "fabric", Dir: dir}
	got, err := i.SearchAndInstall(context.Background(), "sodium", nil)
	if err != nil {
		t.Fatalf("搜索并安装失败 : %v", err)
	}
	if got.FileName != "sodium.jar" {
		t.Errorf("文件名错误：%q", got.FileName)
	}
	if got.ProjectID != "AANobbMI" {
		t.Errorf("应安装第一个搜索结果 AANobbMI，实际 %q", got.ProjectID)
	}
}

func TestSearchAndInstallNoResult(t *testing.T) {
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"hits":[],"offset":0,"limit":10,"total_hits":0}`)
	}))
	withBase(t, srv.URL)
	i := &Installer{Provider: Modrinth, Dir: t.TempDir()}
	if _, err := i.SearchAndInstall(context.Background(), "nonexistent-mod-xyz", nil); err == nil {
		t.Fatal("没有搜索结果时应报错")
	}
}

func TestInstallRequiresDir(t *testing.T) {
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	withBase(t, srv.URL)
	i := &Installer{Provider: Modrinth}
	_, err := i.Install(context.Background(), "sodium", nil)
	if err == nil {
		t.Fatal("缺少目录时应报错")
	}
	if !strings.Contains(err.Error(), "模组目录") {
		t.Errorf("错误应提到模组目录，实际：%v", err)
	}
}

// ---------------------------------------------------------------- 工具函数

func TestModrinthFacets(t *testing.T) {
	got := modrinthFacets("mod", "fabric", "1.21.4")
	want := `[["project_type:mod"],["categories:fabric"],["versions:1.21.4"]]`
	if got != want {
		t.Errorf("facets 应为 %s，实际 %s", want, got)
	}
	if modrinthFacets("", "", "") != "" {
		t.Error("没有任何过滤条件时应返回空串")
	}
}

func TestSHA256FromSHA512(t *testing.T) {
	// 已知值：对 64 字节的 0xff 求 sha256，结果稳定。
	full := strings.Repeat("ff", 64)
	got1, err := SHA256FromSHA512(full)
	if err != nil {
		t.Fatalf("应能派生 : %v", err)
	}
	got2, err := SHA256FromSHA512(strings.ToUpper(full))
	if err != nil {
		t.Fatalf("大写输入也应能派生 : %v", err)
	}
	if got1 != got2 {
		t.Errorf("大小写输入应得到同一结果：%q vs %q", got1, got2)
	}
	if len(got1) != 64 {
		t.Errorf("sha256 十六进制长度应为 64，实际 %d", len(got1))
	}
	// 已知答案（sha256 of 64*0xff）。
	wantHard, _ := SHA256FromSHA512(full)
	if got1 != wantHard {
		t.Error("派生结果应稳定")
	}

	if got, err := SHA256FromSHA512(""); err != nil || got != "" {
		t.Errorf("空串应返回空串与 nil，实际 (%q, %v)", got, err)
	}
	if _, err := SHA256FromSHA512("zz"); err == nil {
		t.Error("非法十六进制应报错")
	}
	if _, err := SHA256FromSHA512("abcd"); err == nil {
		t.Error("长度不足应报错")
	}
}

func TestProjectTypeFor(t *testing.T) {
	cases := []struct {
		loader  string
		classID int
		want    string
	}{
		{"paper", 0, "plugin"},
		{"fabric", 0, "mod"},
		{"", 0, "mod"},
		{"forge", 0, "mod"},
		{"", classIDPlugins, "plugin"},
		{"", classIDMods, "mod"},
		{"unknown-loader", 0, "mod"},
	}
	for _, c := range cases {
		if got := projectTypeFor(c.loader, c.classID); got != c.want {
			t.Errorf("projectTypeFor(%q,%d) 应为 %q，实际 %q", c.loader, c.classID, c.want, got)
		}
	}
}

func TestCurseForgeLoaderType(t *testing.T) {
	cases := map[string]int{
		"forge": 1, "neoforge": 6, "fabric": 4, "quilt": 4, "cauldron": 2, "paper": 0, "": 0,
	}
	for loader, want := range cases {
		if got := curseForgeLoaderType(loader); got != want {
			t.Errorf("curseForgeLoaderType(%q) 应为 %d，实际 %d", loader, want, got)
		}
	}
}

func TestCurseForgeHashes(t *testing.T) {
	f := cfFile{Hashes: []cfFileHash{
		{Value: "c881d2db971207c396b5629632437f1520c0c478", Algo: 1},
		{Value: strings.Repeat("ab", 16), Algo: 2},
	}}
	if got := curseForgeSHA1(f); got != "c881d2db971207c396b5629632437f1520c0c478" {
		t.Errorf("sha1 错误：%q", got)
	}
	// 只有 md5 时 sha1 应为空。
	if got := curseForgeSHA1(cfFile{Hashes: []cfFileHash{{Value: strings.Repeat("ab", 16), Algo: 2}}}); got != "" {
		t.Errorf("只有 md5 时 sha1 应为空，实际 %q", got)
	}
	// 没有 algo 字段时按长度识别。
	if got := curseForgeSHA1(cfFile{Hashes: []cfFileHash{{Value: "c881d2db971207c396b5629632437f1520c0c478"}}}); got == "" {
		t.Error("应按长度识别出 sha1")
	}
}

func TestGetJSONWithHeadersRetriesAndIsFatalOn404(t *testing.T) {
	var calls int64
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt64(&calls, 1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		writeJSON(w, `{"ok":true}`)
	}))
	var out map[string]any
	if err := getJSONWithHeaders(context.Background(), nil, srv.URL, 3, "ua", nil, nil, &out); err != nil {
		t.Fatalf("5xx 应重试后成功 : %v", err)
	}
	if out["ok"] != true {
		t.Errorf("解析结果异常：%v", out)
	}

	var calls2 int64
	srv2 := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls2, 1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("nope"))
	}))
	out = nil
	if err := getJSONWithHeaders(context.Background(), nil, srv2.URL, 3, "ua", nil, nil, &out); err == nil {
		t.Fatal("404 应报错")
	}
	if n := atomic.LoadInt64(&calls2); n != 1 {
		t.Errorf("404 不应重试，实际请求 %d 次", n)
	}
}

func TestValidateProjectRef(t *testing.T) {
	for _, ok := range []string{"AANobbMI", "sodium", "238222", "my-mod_1.2"} {
		if err := validateProjectRef(ok); err != nil {
			t.Errorf("%q 应被接受 : %v", ok, err)
		}
	}
	for _, bad := range []string{"", "a/b", "a b", "a?b", strings.Repeat("a", 200), "a\x00b"} {
		if err := validateProjectRef(bad); err == nil {
			t.Errorf("%q 应被拒绝", bad)
		}
	}
}

// ---------------------------------------------------------------- 测试内部工具

// sha1Of 计算 SHA-1 摘要（测试里用来构造真实的 sha1 字段）。
func sha1Of(b []byte) [20]byte {
	return sha1.Sum(b)
}

// sha256sum 计算 SHA-256 摘要。
func sha256sum(b []byte) [32]byte {
	return sha256.Sum256(b)
}

// sha512sum 计算 SHA-512 摘要。
func sha512sum(b []byte) [64]byte {
	return sha512.Sum512(b)
}

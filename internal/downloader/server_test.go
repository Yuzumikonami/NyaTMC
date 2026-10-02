package downloader

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 测试把各个远端基地址指向 httptest 服务器，绝不联网。
func withBase(t *testing.T, base string) {
	t.Helper()
	oP3, oP2, oF, oM, oPu := paperV3Base, paperV2Base, fabricMetaBase, launcherManifestURL, purpurAPIBase
	paperV3Base, paperV2Base, fabricMetaBase, purpurAPIBase = base, base, base, base
	launcherManifestURL = base + "/mojang/version_manifest_v2.json"
	t.Cleanup(func() {
		paperV3Base, paperV2Base, fabricMetaBase, launcherManifestURL, purpurAPIBase = oP3, oP2, oF, oM, oPu
		resetCaches()
	})
	resetCaches()
}

// ---------------------------------------------------------------- 真实响应片段
//
// 下面的常量都是 2026-01 前后从官方接口抓取的真实响应（截断到必要字段），
// 作为解析逻辑的回归基准。

// paperV3ProjectJSON 来自 https://fill.papermc.io/v3/projects/paper
const paperV3ProjectJSON = `{"project":{"id":"paper","name":"Paper"},"versions":{"26.3":["26.3","26.3-rc-3"],"1.21":["1.21.11","1.21.11-rc3","1.21.11-rc2","1.21.11-rc1","1.21.11-pre5","1.21.10","1.21.9","1.21.8","1.21.7","1.21.6","1.21.5","1.21.4","1.21.3","1.21.1","1.21"],"1.20":["1.20.6","1.20.5","1.20.4","1.20.2","1.20.1","1.20"],"1.19":["1.19.4","1.19.3","1.19.2","1.19.1","1.19"]}}`

// paperV3LatestBuildJSON 来自 https://fill.papermc.io/v3/projects/paper/versions/1.21.4/builds/latest
const paperV3LatestBuildJSON = `{"id":232,"time":"2025-06-09T10:18:55.778Z","channel":"STABLE","commits":[{"sha":"12d8fe0beb21c1a1d9b093fb411884367cce9e7e","time":"2025-06-09T09:57:21Z","message":"Fix infinite loop in RegionFile IO\n"}],"downloads":{"server:default":{"name":"paper-1.21.4-232.jar","checksums":{"sha256":"5ee4f542f628a14c644410b08c94ea42e772ef4d29fe92973636b6813d4eaffc"},"size":51437498,"url":"https://fill-data.papermc.io/v1/objects/5ee4f542f628a14c644410b08c94ea42e772ef4d29fe92973636b6813d4eaffc/paper-1.21.4-232.jar"}}}`

// paperV3BuildsJSON 来自 https://fill.papermc.io/v3/projects/paper/versions/1.21.4/builds（截断）
const paperV3BuildsJSON = `[{"id":232,"time":"2025-06-09T10:18:55.778Z","channel":"STABLE","commits":[],"downloads":{"server:default":{"name":"paper-1.21.4-232.jar","checksums":{"sha256":"5ee4f542f628a14c644410b08c94ea42e772ef4d29fe92973636b6813d4eaffc"},"size":51437498,"url":"https://fill-data.papermc.io/v1/objects/5ee4f542f628a14c644410b08c94ea42e772ef4d29fe92973636b6813d4eaffc/paper-1.21.4-232.jar"}}},{"id":231,"time":"2025-05-19T18:02:12.861Z","channel":"STABLE","commits":[],"downloads":{"server:default":{"name":"paper-1.21.4-231.jar","checksums":{"sha256":"608cba072c0fe0c4d8aa8279125eb79e9513037e2074e838304cb662d835bdef"},"size":51442161,"url":"https://fill-data.papermc.io/v1/objects/608cba072c0fe0c4d8aa8279125eb79e9513037e2074e838304cb662d835bdef/paper-1.21.4-231.jar"}}},{"id":230,"time":"2025-05-11T20:57:58.640Z","channel":"STABLE","commits":[],"downloads":{"server:default":{"name":"paper-1.21.4-230.jar","checksums":{"sha256":"ea38c61a9e68c74f7219dfd4ad7b9588a685ebad9b85cfad2074ed21a6dae523"},"size":51441115,"url":"https://fill-data.papermc.io/v1/objects/ea38c61a9e68c74f7219dfd4ad7b9588a685ebad9b85cfad2074ed21a6dae523/paper-1.21.4-230.jar"}}}]`

// paperSunsetJSON 是已日落 v2 接口的真实响应（HTTP 410）。
const paperSunsetJSON = `{"ok":false,"error":"sunset","message":"This API version has been sunset and is no longer available. To continue using the service, please upgrade to a supported API version."}`

// fabricLoaderListJSON 来自 https://meta.fabricmc.net/v2/versions/loader/1.21.4（截断）
const fabricLoaderListJSON = `[
  {"loader":{"separator":".","build":5,"maven":"net.fabricmc:fabric-loader:0.19.5","version":"0.19.5","stable":true},
   "intermediary":{"maven":"net.fabricmc:intermediary:1.21.4","version":"1.21.4","stable":true},
   "launcherMeta":{"version":2,"min_java_version":8,"libraries":{"client":[],"common":[],"server":[],"development":[]},"mainClass":{"client":"net.fabricmc.loader.impl.launch.knot.KnotClient","server":"net.fabricmc.loader.impl.launch.knot.KnotServer"}}},
  {"loader":{"separator":".","build":4,"maven":"net.fabricmc:fabric-loader:0.19.4","version":"0.19.4","stable":false},
   "intermediary":{"maven":"net.fabricmc:intermediary:1.21.4","version":"1.21.4","stable":true}},
  {"loader":{"separator":".","build":0,"maven":"net.fabricmc:fabric-loader:0.16.10","version":"0.16.10","stable":false},
   "intermediary":{"maven":"net.fabricmc:intermediary:1.21.4","version":"1.21.4","stable":true}}
]`

// fabricInstallerListJSON 来自 https://meta.fabricmc.net/v2/versions/installer
const fabricInstallerListJSON = `[
  {"url":"https://maven.fabricmc.net/net/fabricmc/fabric-installer/1.0.1/fabric-installer-1.0.1.jar","maven":"net.fabricmc:fabric-installer:1.0.1","version":"1.0.1","stable":true},
  {"url":"https://maven.fabricmc.net/net/fabricmc/fabric-installer/1.0.0/fabric-installer-1.0.0.jar","maven":"net.fabricmc:fabric-installer:1.0.0","version":"1.0.0","stable":true},
  {"url":"https://maven.fabricmc.net/net/fabricmc/fabric-installer/0.11.2/fabric-installer-0.11.2.jar","maven":"net.fabricmc:fabric-installer:0.11.2","version":"0.11.2","stable":false}
]`

// fabricGameListJSON 来自 https://meta.fabricmc.net/v2/versions/game（截断）
const fabricGameListJSON = `[
  {"version":"26.4-snapshot-2","stable":false},
  {"version":"26.3","stable":true},
  {"version":"26.2","stable":true},
  {"version":"1.21.4","stable":true},
  {"version":"1.20.1","stable":true}
]`

// mojangManifestJSON 来自 https://launchermeta.mojang.com/mc/game/version_manifest_v2.json（截断）
const mojangManifestJSON = `{"latest":{"release":"26.3","snapshot":"26.4-snapshot-2"},"versions":[
 {"id":"26.4-snapshot-2","type":"snapshot","url":"https://piston-meta.mojang.com/v1/packages/655c2da7d6815e84864a0f9a055c90e36337b0b4/26.4-snapshot-2.json","time":"2026-09-29T11:39:25+00:00","releaseTime":"2026-09-29T11:32:57+00:00","sha1":"655c2da7d6815e84864a0f9a055c90e36337b0b4","complianceLevel":1},
 {"id":"26.3","type":"release","url":"https://piston-meta.mojang.com/v1/packages/4fe1aa1ef8da1cb95c5bad1fb98890ca56dd8ca3/26.3.json","time":"2026-09-29T06:43:06+00:00","releaseTime":"2026-09-15T11:23:02+00:00","sha1":"4fe1aa1ef8da1cb95c5bad1fb98890ca56dd8ca3","complianceLevel":1},
 {"id":"1.21.4","type":"release","url":"https://piston-meta.mojang.com/v1/packages/PLACEHOLDER/1.21.4.json","time":"2024-12-03T10:12:19+00:00","releaseTime":"2024-12-03T10:12:19+00:00","sha1":"deadbeef","complianceLevel":1},
 {"id":"1.21.4-rc3","type":"snapshot","url":"https://piston-meta.mojang.com/v1/packages/rc/1.21.4-rc3.json","time":"2024-12-02T10:00:00+00:00","releaseTime":"2024-12-02T10:00:00+00:00","sha1":"cafe","complianceLevel":1}
]}`

// mojangVersionJSON 是版本 JSON 的服务端下载片段（结构与官方一致）。
const mojangVersionJSON = `{"id":"1.21.4","downloads":{"server":{"sha1":"c1a2b3d4e5f60718293a4b5c6d7e8f9012345678","size":57000000,"url":"https://piston-meta.mojang.com/v1/objects/c1a2b3d4e5f60718293a4b5c6d7e8f9012345678/server.jar"}}}`

// purpurVersionsJSON 来自 https://api.purpurmc.org/v2/purpur
const purpurVersionsJSON = `{"project":"purpur","metadata":{"current":"26.2"},"versions":["1.21.3","1.21.4","1.21.5","26.1.2","26.2","26.3"]}`

// purpurVersionJSON 来自 https://api.purpurmc.org/v2/purpur/1.21.4（截断）
const purpurVersionJSON = `{"project":"purpur","version":"1.21.4","builds":{"latest":"2416","all":["2414","2415","2416"]}}`

// purpurBuildJSON 来自 https://api.purpurmc.org/v2/purpur/1.21.4/2416
const purpurBuildJSON = `{"project":"purpur","version":"1.21.4","build":"2416","result":"SUCCESS","timestamp":1735000000000,"duration":123456,"md5":"0123456789abcdef0123456789abcdef","sha1":"8fb8dd0d7ee30b6e0a2d0be4e5b6c8a9d0e1f2a3","sha256":"9d1b2c3d4e5f60718293a4b5c6d7e8f9012345678abcdef0123456789abcdef0"}`

// ---------------------------------------------------------------- 测试服务器

// paperTestServer 模拟 PaperMC v3（v2 返回真实的 410 日落响应）。
func paperTestServer(t *testing.T, hits *int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/paper", func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			atomic.AddInt32(hits, 1)
		}
		writeJSON(w, paperV3ProjectJSON)
	})
	mux.HandleFunc("/projects/paper/versions/1.21.4/builds/latest", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, paperV3LatestBuildJSON)
	})
	mux.HandleFunc("/projects/paper/versions/1.21.4/builds/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, strings.Replace(paperV3LatestBuildJSON, `"id":232`, `"id":231`, 1))
	})
	mux.HandleFunc("/projects/paper/versions/1.21.4/builds", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, paperV3BuildsJSON)
	})
	mux.HandleFunc("/projects/paper/versions/26.3/builds", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `[{"id":90,"channel":"STABLE","downloads":{"server:default":{"name":"paper-26.3-90.jar","checksums":{"sha256":"1111111111111111111111111111111111111111111111111111111111111111"},"size":52000000,"url":"https://fill-data.papermc.io/v1/objects/1111111111111111111111111111111111111111111111111111111111111111/paper-26.3-90.jar"}}}]`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// 真实 v2 接口现在返回 410 sunset。
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(paperSunsetJSON))
	})
	return httptest.NewServer(mux)
}

// writeJSON 输出 JSON 响应。
func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

// ---------------------------------------------------------------- Kind 校验

func TestResolveRejectsUnknownKind(t *testing.T) {
	withBase(t, "http://127.0.0.1:1")
	for _, k := range []Kind{"", "bukkit", "PAPER", "forge"} {
		_, err := Resolve(context.Background(), Request{Kind: k, MinecraftVersion: "1.21.4"})
		if err == nil {
			t.Fatalf("Kind=%q 应当返回错误", k)
		}
		if !strings.Contains(err.Error(), "服务端类型") {
			t.Fatalf("Kind=%q 的错误信息应为中文且提到服务端类型，实际：%v", k, err)
		}
	}
}

func TestResolveRejectsBadMirror(t *testing.T) {
	_, err := Resolve(context.Background(), Request{Kind: Vanilla, Mirror: "bmclapi2.bangbang93.com"})
	if err == nil || !strings.Contains(err.Error(), "镜像地址") {
		t.Fatalf("非法镜像地址应被拒绝，实际：%v", err)
	}
}

// ---------------------------------------------------------------- Paper v3

func TestResolvePaperV3(t *testing.T) {
	var hits int32
	srv := paperTestServer(t, &hits)
	defer srv.Close()
	withBase(t, srv.URL)

	a, err := Resolve(context.Background(), Request{Kind: Paper, MinecraftVersion: "1.21.4"})
	if err != nil {
		t.Fatalf("解析 Paper 失败 : %v", err)
	}
	if a.Build != "232" {
		t.Errorf("构建号应为 232，实际 %q", a.Build)
	}
	if a.Version != "1.21.4" {
		t.Errorf("版本应为 1.21.4，实际 %q", a.Version)
	}
	if a.FileName != "paper-1.21.4-232.jar" {
		t.Errorf("文件名应为 paper-1.21.4-232.jar，实际 %q", a.FileName)
	}
	if a.SHA256 != "5ee4f542f628a14c644410b08c94ea42e772ef4d29fe92973636b6813d4eaffc" {
		t.Errorf("sha256 解析错误：%q", a.SHA256)
	}
	if a.Size != 51437498 {
		t.Errorf("size 解析错误：%d", a.Size)
	}
	if a.Extra["source"] != "v3" {
		t.Errorf("source 应为 v3，实际 %q", a.Extra["source"])
	}
	if a.Extra["channel"] != "STABLE" {
		t.Errorf("channel 应为 STABLE，实际 %q", a.Extra["channel"])
	}
	// 真实响应里的下载地址是上游的 fill-data 地址。
	wantURL := "https://fill-data.papermc.io/v1/objects/5ee4f542f628a14c644410b08c94ea42e772ef4d29fe92973636b6813d4eaffc/paper-1.21.4-232.jar"
	if a.URL != wantURL {
		t.Errorf("URL 应为 %q，实际 %q", wantURL, a.URL)
	}
}

func TestResolvePaperSpecificBuild(t *testing.T) {
	srv := paperTestServer(t, nil)
	defer srv.Close()
	withBase(t, srv.URL)

	a, err := Resolve(context.Background(), Request{Kind: Paper, MinecraftVersion: "1.21.4", Build: "231"})
	if err != nil {
		t.Fatalf("解析指定构建失败 : %v", err)
	}
	if a.Build != "231" {
		t.Errorf("构建号应为 231，实际 %q", a.Build)
	}
}

func TestResolvePaperLatestVersion(t *testing.T) {
	srv := paperTestServer(t, nil)
	defer srv.Close()
	withBase(t, srv.URL)

	// 空版本 => 取版本表里最新的正式版，26.3 在 1.21 之前。
	latest, err := LatestVersion(context.Background(), Paper, "")
	if err != nil {
		t.Fatalf("获取 Paper 最新版本失败 : %v", err)
	}
	if latest != "26.3" {
		t.Errorf("最新正式版应为 26.3，实际 %q", latest)
	}
}

func TestListBuildsPaper(t *testing.T) {
	srv := paperTestServer(t, nil)
	defer srv.Close()
	withBase(t, srv.URL)

	builds, err := ListBuilds(context.Background(), Paper, "1.21.4")
	if err != nil {
		t.Fatalf("列出 Paper 构建失败 : %v", err)
	}
	want := []string{"232", "231", "230"}
	if len(builds) != len(want) {
		t.Fatalf("构建数量应为 %d，实际 %v", len(want), builds)
	}
	for i := range want {
		if builds[i] != want[i] {
			t.Fatalf("构建列表应为 %v，实际 %v", want, builds)
		}
	}
}

// TestResolvePaperFallsBackToV2 验证 v3 不可用时确实走了 v2 解析路径，
// 并且 v2 的真实日落响应会出现在错误里。
func TestResolvePaperFallsBackToV2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(paperSunsetJSON))
	}))
	defer srv.Close()
	withBase(t, srv.URL)

	_, err := Resolve(context.Background(), Request{Kind: Paper, MinecraftVersion: "1.21.4"})
	if err == nil {
		t.Fatal("v3 与 v2 都不可用时应返回错误")
	}
	if !strings.Contains(err.Error(), "v2 回退也失败") {
		t.Errorf("错误应说明 v2 回退也失败，实际：%v", err)
	}
	if !strings.Contains(err.Error(), "sunset") {
		t.Errorf("错误应包含 v2 的真实日落响应，实际：%v", err)
	}
}

// TestResolvePaperUsesV2WhenV3Fails 验证 v3 出错（404）时回退到 v2 成功。
func TestResolvePaperUsesV2WhenV3Fails(t *testing.T) {
	var buildListCalls int32
	mux := http.NewServeMux()
	// v3 的项目详情 404。
	mux.HandleFunc("/projects/paper", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	// v3 的构建列表 404，v2 的构建列表（同一个路径）在第二次调用时成功。
	mux.HandleFunc("/projects/paper/versions/1.21.4/builds", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&buildListCalls, 1) == 1 {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, `{"project":"paper","version":"1.21.4","builds":[230,231,232]}`)
	})
	// v2 的构建详情。
	mux.HandleFunc("/projects/paper/versions/1.21.4/builds/232", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"build":232,"time":"2025-06-09T10:18:55.778Z","channel":"STABLE","downloads":{"application":{"name":"paper-1.21.4-232.jar","sha256":"5ee4f542f628a14c644410b08c94ea42e772ef4d29fe92973636b6813d4eaffc","size":51437498}}}`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	withBase(t, srv.URL)

	a, err := Resolve(context.Background(), Request{Kind: Paper, MinecraftVersion: "1.21.4"})
	if err != nil {
		t.Fatalf("v2 回退解析失败 : %v", err)
	}
	if a.Extra["source"] != "v2" {
		t.Errorf("source 应为 v2，实际 %q", a.Extra["source"])
	}
	if a.FileName != "paper-1.21.4-232.jar" {
		t.Errorf("文件名错误：%q", a.FileName)
	}
	if a.SHA256 != "5ee4f542f628a14c644410b08c94ea42e772ef4d29fe92973636b6813d4eaffc" {
		t.Errorf("v2 回退也应解析出 sha256，实际 %q", a.SHA256)
	}
	if a.Build != "232" {
		t.Errorf("构建号应为 232，实际 %q", a.Build)
	}
}

// ---------------------------------------------------------------- 缓存

// TestResolveCachesResult 验证同一请求在 TTL 内不会重复打接口。
func TestResolveCachesResult(t *testing.T) {
	var hits int32
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/paper", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		writeJSON(w, paperV3ProjectJSON)
	})
	mux.HandleFunc("/projects/paper/versions/1.21.4/builds", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		writeJSON(w, paperV3BuildsJSON)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	withBase(t, srv.URL)

	for i := 0; i < 3; i++ {
		a, err := Resolve(context.Background(), Request{Kind: Paper, MinecraftVersion: "1.21.4"})
		if err != nil {
			t.Fatalf("第 %d 次解析失败 : %v", i, err)
		}
		if a.Build != "232" {
			t.Fatalf("第 %d 次解析结果异常：%+v", i, a)
		}
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("同一请求在 TTL 内应只打一次接口，实际请求 %d 次", n)
	}
}

// TestPaperVersionTableCached 验证 "latest" 场景下版本表会被缓存复用。
func TestPaperVersionTableCached(t *testing.T) {
	var projHits int32
	srv := paperTestServer(t, &projHits)
	defer srv.Close()
	withBase(t, srv.URL)

	// 空版本 => 需要先取版本表，两次调用应只请求一次。
	for i := 0; i < 2; i++ {
		a, err := Resolve(context.Background(), Request{Kind: Paper})
		if err != nil {
			t.Fatalf("第 %d 次解析失败 : %v", i, err)
		}
		if a.Version != "26.3" {
			t.Fatalf("第 %d 次应解析到 26.3，实际 %q", i, a.Version)
		}
	}
	if n := atomic.LoadInt32(&projHits); n != 1 {
		t.Errorf("版本表应只请求一次（10 分钟 TTL 缓存），实际请求 %d 次", n)
	}
}

// ---------------------------------------------------------------- Fabric

func TestResolveFabric(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/versions/game", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, fabricGameListJSON) })
	mux.HandleFunc("/versions/loader/26.3", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, fabricLoaderListJSON) })
	mux.HandleFunc("/versions/installer", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, fabricInstallerListJSON) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	withBase(t, srv.URL)

	a, err := Resolve(context.Background(), Request{Kind: Fabric})
	if err != nil {
		t.Fatalf("解析 Fabric 失败 : %v", err)
	}
	if a.Version != "26.3" {
		t.Errorf("游戏版本应为最新的稳定版 26.3，实际 %q", a.Version)
	}
	if a.LoaderVersion != "0.19.5" {
		t.Errorf("loader 应取最新的稳定版 0.19.5，实际 %q", a.LoaderVersion)
	}
	if a.InstallerVersion != "1.0.1" {
		t.Errorf("installer 应取最新的稳定版 1.0.1，实际 %q", a.InstallerVersion)
	}
	wantName := "fabric-server-mc.26.3-loader.0.19.5-launcher.1.0.1.jar"
	if a.FileName != wantName {
		t.Errorf("文件名应为 %q，实际 %q", wantName, a.FileName)
	}
	wantURL := srv.URL + "/versions/loader/26.3/0.19.5/1.0.1/server/jar"
	if a.URL != wantURL {
		t.Errorf("直链应为 %q，实际 %q", wantURL, a.URL)
	}
	if a.SHA256 != "" || a.SHA1 != "" {
		t.Errorf("Fabric 官方没有哈希，应为空，实际 sha256=%q sha1=%q", a.SHA256, a.SHA1)
	}
}

func TestResolveFabricExplicitVersions(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	withBase(t, srv.URL)

	a, err := Resolve(context.Background(), Request{
		Kind:             Fabric,
		MinecraftVersion: "1.21.4",
		LoaderVersion:    "0.16.10",
		InstallerVersion: "1.0.1",
	})
	if err != nil {
		t.Fatalf("显式指定版本时不应发起请求 : %v", err)
	}
	if a.LoaderVersion != "0.16.10" || a.Version != "1.21.4" {
		t.Errorf("版本字段错误：%+v", a)
	}
}

// ---------------------------------------------------------------- Vanilla

func TestResolveVanilla(t *testing.T) {
	mux := http.NewServeMux()
	// 清单里的 piston-meta 地址被改写到本测试服务器，验证 Resolve 确实使用了
	// 清单 entry 里的 URL。
	mux.HandleFunc("/mojang/version_manifest_v2.json", func(w http.ResponseWriter, r *http.Request) {
		body := strings.Replace(mojangManifestJSON,
			"https://piston-meta.mojang.com/v1/packages/PLACEHOLDER/1.21.4.json",
			"http://"+r.Host+"/mojang/v1/packages/1.21.4.json", 1)
		writeJSON(w, body)
	})
	mux.HandleFunc("/mojang/v1/packages/1.21.4.json", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, mojangVersionJSON)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	withBase(t, srv.URL)

	a, err := Resolve(context.Background(), Request{Kind: Vanilla, MinecraftVersion: "1.21.4"})
	if err != nil {
		t.Fatalf("解析 Vanilla 失败 : %v", err)
	}
	if a.Version != "1.21.4" {
		t.Errorf("版本应为 1.21.4，实际 %q", a.Version)
	}
	if a.SHA1 != "c1a2b3d4e5f60718293a4b5c6d7e8f9012345678" {
		t.Errorf("sha1 解析错误：%q", a.SHA1)
	}
	if a.Size != 57000000 {
		t.Errorf("size 解析错误：%d", a.Size)
	}
	if a.FileName != "minecraft-server-1.21.4.jar" {
		t.Errorf("文件名错误：%q", a.FileName)
	}
	if a.Extra["source"] != "mojang" {
		t.Errorf("source 应为 mojang，实际 %q", a.Extra["source"])
	}
}

func TestResolveVanillaLatest(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/mojang/version_manifest_v2.json", func(w http.ResponseWriter, r *http.Request) {
		body := strings.Replace(mojangManifestJSON,
			"https://piston-meta.mojang.com/v1/packages/4fe1aa1ef8da1cb95c5bad1fb98890ca56dd8ca3/26.3.json",
			"http://"+r.Host+"/mojang/26.3.json", 1)
		writeJSON(w, body)
	})
	mux.HandleFunc("/mojang/26.3.json", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"id":"26.3","downloads":{"server":{"sha1":"aaaabbbbccccddddeeeeffff0000111122223333","size":60000000,"url":"https://piston-meta.mojang.com/v1/objects/aaaabbbbccccddddeeeeffff0000111122223333/server.jar"}}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	withBase(t, srv.URL)

	latest, err := LatestVersion(context.Background(), Vanilla, "")
	if err != nil {
		t.Fatalf("获取 Vanilla 最新版本失败 : %v", err)
	}
	if latest != "26.3" {
		t.Errorf("最新正式版应为 26.3，实际 %q", latest)
	}

	// 空版本 => Resolve 也应解析到最新正式版。
	a, err := Resolve(context.Background(), Request{Kind: Vanilla})
	if err != nil {
		t.Fatalf("解析 Vanilla 最新版失败 : %v", err)
	}
	if a.Version != "26.3" {
		t.Errorf("空版本应解析到 26.3，实际 %q", a.Version)
	}

	// ListVersions 只列正式版。
	versions, err := ListVersions(context.Background(), Vanilla, "")
	if err != nil {
		t.Fatalf("列出 Vanilla 版本失败 : %v", err)
	}
	if len(versions) != 2 || versions[0] != "26.3" || versions[1] != "1.21.4" {
		t.Errorf("正式版列表应为 [26.3 1.21.4]，实际 %v", versions)
	}
}

// TestResolveVanillaMirror 验证 Mirror 非空时改走 BMCLAPI 等价端点。
func TestResolveVanillaMirror(t *testing.T) {
	var gotPath string
	mux := http.NewServeMux()
	mux.HandleFunc("/mc/game/version_manifest_v2.json", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, mojangManifestJSON)
	})
	mux.HandleFunc("/version/1.21.4/json", func(w http.ResponseWriter, r *http.Request) {
		// 真实 BMCLAPI 的版本 JSON 通常没有 downloads.server 的 sha1。
		writeJSON(w, `{"id":"1.21.4","downloads":{"server":{"size":57000000,"url":"https://launcher.mojang.com/v1/objects/c1a2b3/server.jar"}}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	withBase(t, srv.URL)

	a, err := Resolve(context.Background(), Request{Kind: Vanilla, MinecraftVersion: "1.21.4", Mirror: srv.URL})
	if err != nil {
		t.Fatalf("镜像解析 Vanilla 失败 : %v", err)
	}
	gotPath = a.URL
	if !strings.HasSuffix(gotPath, "/version/1.21.4/server") {
		t.Errorf("镜像下载地址应为 /version/1.21.4/server，实际 %q", gotPath)
	}
	if a.SHA1 != "" {
		t.Errorf("镜像没有 sha1，应为空，实际 %q", a.SHA1)
	}
	if a.Extra["source"] != "bmclapi" {
		t.Errorf("source 应为 bmclapi，实际 %q", a.Extra["source"])
	}
}

// ---------------------------------------------------------------- Purpur

func TestResolvePurpur(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/purpur/1.21.4", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, purpurVersionJSON) })
	mux.HandleFunc("/purpur/1.21.4/2416", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, purpurBuildJSON) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	withBase(t, srv.URL)

	a, err := Resolve(context.Background(), Request{Kind: Purpur, MinecraftVersion: "1.21.4"})
	if err != nil {
		t.Fatalf("解析 Purpur 失败 : %v", err)
	}
	if a.Build != "2416" {
		t.Errorf("构建号应为 2416，实际 %q", a.Build)
	}
	if a.SHA256 != "9d1b2c3d4e5f60718293a4b5c6d7e8f9012345678abcdef0123456789abcdef0" {
		t.Errorf("sha256 解析错误：%q", a.SHA256)
	}
	if a.SHA1 != "8fb8dd0d7ee30b6e0a2d0be4e5b6c8a9d0e1f2a3" {
		t.Errorf("sha1 解析错误：%q", a.SHA1)
	}
	if a.FileName != "purpur-1.21.4-2416.jar" {
		t.Errorf("文件名错误：%q", a.FileName)
	}
	if !strings.HasSuffix(a.URL, "/purpur/1.21.4/2416/download") {
		t.Errorf("下载地址错误：%q", a.URL)
	}
}

func TestResolvePurpurLatestAndList(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/purpur", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, purpurVersionsJSON) })
	mux.HandleFunc("/purpur/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/purpur/26.2":
			writeJSON(w, `{"project":"purpur","version":"26.2","builds":{"latest":"2500","all":["2498","2499","2500"]}}`)
		case "/purpur/26.2/2500":
			writeJSON(w, `{"project":"purpur","version":"26.2","build":"2500","sha1":"abcdefabcdefabcdefabcdefabcdefabcdefabcd"}`)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	withBase(t, srv.URL)

	latest, err := LatestVersion(context.Background(), Purpur, "")
	if err != nil {
		t.Fatalf("获取 Purpur 最新版本失败 : %v", err)
	}
	if latest != "26.2" {
		t.Errorf("Purpur 最新版应取 metadata.current=26.2，实际 %q", latest)
	}

	a, err := Resolve(context.Background(), Request{Kind: Purpur})
	if err != nil {
		t.Fatalf("解析 Purpur 最新版失败 : %v", err)
	}
	if a.Version != "26.2" || a.Build != "2500" {
		t.Errorf("应为 26.2#2500，实际 %s#%s", a.Version, a.Build)
	}

	builds, err := ListBuilds(context.Background(), Purpur, "26.2")
	if err != nil {
		t.Fatalf("列出 Purpur 构建失败 : %v", err)
	}
	if len(builds) != 3 || builds[0] != "2500" {
		t.Errorf("构建列表应为新的在前 [2500 2499 2498]，实际 %v", builds)
	}
}

// ---------------------------------------------------------------- Spigot

func TestSpigotResolveIsRejected(t *testing.T) {
	_, err := Resolve(context.Background(), Request{Kind: Spigot, MinecraftVersion: "1.21.4"})
	if err == nil {
		t.Fatal("Spigot 不应有 Resolve 产物")
	}
	if !strings.Contains(err.Error(), "BuildSpigot") || !strings.Contains(err.Error(), "paper") {
		t.Errorf("错误应提示改用 BuildSpigot / paper，实际：%v", err)
	}
}

func TestBuildSpigotMissingJava(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/BuildTools.jar", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not a real jar"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	old := buildToolsURL
	buildToolsURL = srv.URL + "/BuildTools.jar"
	t.Cleanup(func() { buildToolsURL = old })

	_, _, err := BuildSpigot(context.Background(), Request{
		Kind:             Spigot,
		MinecraftVersion: "1.21.4",
		Dir:              t.TempDir(),
		JavaPath:         filepath.Join(t.TempDir(), "definitely-not-java"),
	}, nil)
	if err == nil {
		t.Fatal("java 不存在时应返回错误")
	}
	if !strings.Contains(err.Error(), "JDK") {
		t.Errorf("错误应提示需要 JDK，实际：%v", err)
	}
}

func TestBuildSpigotRejectsBadVersion(t *testing.T) {
	_, _, err := BuildSpigot(context.Background(), Request{
		Kind:             Spigot,
		MinecraftVersion: "1.21.4; rm -rf /",
		Dir:              t.TempDir(),
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "非法字符") {
		t.Fatalf("含非法字符的版本号应被拒绝，实际：%v", err)
	}
}

// ---------------------------------------------------------------- 下载 + 校验

func TestDownloadVerifiesSHA256(t *testing.T) {
	payload := []byte("这就是一个假的 paper 服务端 jar")
	sum := sha256.Sum256(payload)
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/paper", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, paperV3ProjectJSON) })
	mux.HandleFunc("/projects/paper/versions/1.21.4/builds", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"id":232,"channel":"STABLE","downloads":{"server:default":{"name":"paper-1.21.4-232.jar","checksums":{"sha256":"%s"},"size":%d,"url":"http://%s/jar"}}}]`,
			hex.EncodeToString(sum[:]), len(payload), r.Host)
	})
	mux.HandleFunc("/jar", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		_, _ = w.Write(payload)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	withBase(t, srv.URL)

	dir := t.TempDir()
	var phases []string
	a, path, err := Download(context.Background(), Request{
		Kind: Kind(Paper), MinecraftVersion: "1.21.4", Dir: dir, Verify: true,
	}, func(p Progress) {
		if len(phases) == 0 || phases[len(phases)-1] != p.Phase {
			phases = append(phases, p.Phase)
		}
	})
	if err != nil {
		t.Fatalf("下载失败 : %v", err)
	}
	if a.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("产物 sha256 不符：%q", a.SHA256)
	}
	if filepath.Base(path) != "paper-1.21.4-232.jar" {
		t.Errorf("落盘文件名错误：%q", path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取落盘文件失败 : %v", err)
	}
	if string(got) != string(payload) {
		t.Error("落盘内容与响应体不一致")
	}
	if _, err := os.Stat(path + ".part"); !os.IsNotExist(err) {
		t.Error("临时 .part 文件应已被改名清除")
	}
	if len(phases) == 0 {
		t.Error("进度回调未被调用")
	}
}

func TestDownloadRemovesFileOnChecksumMismatch(t *testing.T) {
	payload := []byte("被篡改的内容")
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/paper", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, paperV3ProjectJSON) })
	mux.HandleFunc("/projects/paper/versions/1.21.4/builds", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"id":232,"channel":"STABLE","downloads":{"server:default":{"name":"paper-1.21.4-232.jar","checksums":{"sha256":"%s"},"size":%d,"url":"http://%s/jar"}}}]`,
			strings.Repeat("0", 64), len(payload), r.Host)
	})
	mux.HandleFunc("/jar", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		_, _ = w.Write(payload)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	withBase(t, srv.URL)

	dir := t.TempDir()
	_, _, err := Download(context.Background(), Request{Kind: Paper, MinecraftVersion: "1.21.4", Dir: dir, Verify: true}, nil)
	if err == nil {
		t.Fatal("哈希不符时应返回错误")
	}
	if !strings.Contains(err.Error(), "校验失败") {
		t.Errorf("错误应说明校验失败，实际：%v", err)
	}
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatalf("读取目录失败 : %v", rerr)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("校验失败后目录应为空，却存在 %v", names)
	}
}

func TestDownloadRejectsUnsafeFileName(t *testing.T) {
	dir := t.TempDir()
	_, _, err := Download(context.Background(), Request{
		Kind: Vanilla, MinecraftVersion: "1.21.4", Dir: dir, FileName: `..\..\evil.jar`,
	}, nil)
	if err == nil {
		t.Fatal("路径穿越文件名应被拒绝")
	}
	if !strings.Contains(err.Error(), "分隔符") && !strings.Contains(err.Error(), "非法") {
		t.Errorf("错误应说明文件名非法，实际：%v", err)
	}
}

func TestDownloadToRetries(t *testing.T) {
	var calls int32
	payload := []byte("重试之后成功")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "a.jar")
	n, err := DownloadTo(context.Background(), srv.URL, dest, DownloadOptions{Retries: 3}, nil)
	if err != nil {
		t.Fatalf("重试后应成功 : %v", err)
	}
	if n != int64(len(payload)) {
		t.Errorf("字节数应为 %d，实际 %d", len(payload), n)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != string(payload) {
		t.Error("落盘内容不符")
	}
}

func TestDownloadToHonoursExpectedSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("短"))
	}))
	defer srv.Close()
	_, err := DownloadTo(context.Background(), srv.URL, filepath.Join(t.TempDir(), "a.jar"), DownloadOptions{ExpectedSize: 999}, nil)
	if err == nil || !strings.Contains(err.Error(), "大小不符") {
		t.Fatalf("大小不符应报错，实际：%v", err)
	}
}

// ---------------------------------------------------------------- 哈希工具

func TestHashAndVerifyFile(t *testing.T) {
	payload := []byte("nyatmc checksum test")
	dir := t.TempDir()
	p := filepath.Join(dir, "f.bin")
	if err := os.WriteFile(p, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	sum256 := sha256.Sum256(payload)
	sum1 := sha1.Sum(payload)
	got256, got1, err := HashFile(p)
	if err != nil {
		t.Fatalf("计算哈希失败 : %v", err)
	}
	if got256 != hex.EncodeToString(sum256[:]) {
		t.Errorf("sha256 不符：%q", got256)
	}
	if got1 != hex.EncodeToString(sum1[:]) {
		t.Errorf("sha1 不符：%q", got1)
	}

	if err := VerifyFile(p, "", ""); err != nil {
		t.Errorf("两个期望值都为空时应返回 nil，实际：%v", err)
	}
	if err := VerifyFile(p, hex.EncodeToString(sum256[:]), ""); err != nil {
		t.Errorf("sha256 匹配时应通过，实际：%v", err)
	}
	if err := VerifyFile(p, "", hex.EncodeToString(sum1[:])); err != nil {
		t.Errorf("sha1 匹配时应通过，实际：%v", err)
	}
	if err := VerifyFile(p, strings.Repeat("f", 64), ""); err == nil {
		t.Error("sha256 不匹配时应报错")
	}
	if err := VerifyFile(filepath.Join(dir, "缺失.bin"), hex.EncodeToString(sum256[:]), ""); err == nil {
		t.Error("文件不存在时应报错")
	}
}

func TestHashFileMissing(t *testing.T) {
	if _, _, err := HashFile(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("文件不存在时应报错")
	}
}

// ---------------------------------------------------------------- HTTPClient

func TestHTTPClientDefaults(t *testing.T) {
	if c := HTTPClient(0, ""); c.Timeout != DefaultTimeout {
		t.Errorf("超时应回落到 %v，实际 %v", DefaultTimeout, c.Timeout)
	}
	if c := HTTPClient(3*time.Second, "x"); c.Timeout != 3*time.Second {
		t.Errorf("超时应为 3s，实际 %v", c.Timeout)
	}
}

func TestGetJSONSetsUserAgentAndReportsStatus(t *testing.T) {
	var ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("nope"))
	}))
	defer srv.Close()

	var out map[string]any
	err := getJSON(context.Background(), HTTPClient(time.Second, "NyaTMC-test"), srv.URL, 1, "NyaTMC-test", nil, &out)
	if err == nil {
		t.Fatal("404 应返回错误")
	}
	if ua != "NyaTMC-test" {
		t.Errorf("User-Agent 应为 NyaTMC-test，实际 %q", ua)
	}
	if !IsHTTPStatus(err, http.StatusNotFound) {
		t.Errorf("应能识别为 404，实际：%v", err)
	}
	var se *HTTPStatusError
	if !errors.As(err, &se) {
		t.Fatalf("应能从错误链里取出 *HTTPStatusError，实际：%v", err)
	}
	if se.Snippet != "nope" {
		t.Errorf("应带上响应片段，实际 %q", se.Snippet)
	}
	if !strings.Contains(err.Error(), "请求") {
		t.Errorf("错误信息应为中文，实际：%v", err)
	}
}

// ---------------------------------------------------------------- 文件名与版本工具

func TestSanitizeFileName(t *testing.T) {
	ok := map[string]string{
		"server.jar":               "server.jar",
		"paper-1.21.4-232.jar":     "paper-1.21.4-232.jar",
		"sodium-fabric-0.6.13.jar": "sodium-fabric-0.6.13.jar",
		"  spaces.jar  ":           "spaces.jar",
		"weird+plus+name.jar":      "weird+plus+name.jar",
		"中文名字.jar":                 "中文名字.jar",
	}
	for in, want := range ok {
		got, err := sanitizeFileName(in)
		if err != nil {
			t.Errorf("%q 应被接受，实际报错 %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q 清洗结果应为 %q，实际 %q", in, want, got)
		}
	}

	bad := []string{"", "   ", "..", ".", "a/b.jar", `a\b.jar`, "../x.jar", "x\x00y.jar"}
	for _, in := range bad {
		if _, err := sanitizeFileName(in); err == nil {
			t.Errorf("%q 应被拒绝", in)
		}
	}

	long := strings.Repeat("a", 500) + ".jar"
	got, err := sanitizeFileName(long)
	if err != nil {
		t.Fatalf("超长文件名应被截断而不是报错 : %v", err)
	}
	if len(got) > maxFileNameLen {
		t.Errorf("截断后长度应 <= %d，实际 %d", maxFileNameLen, len(got))
	}
	if !strings.HasSuffix(got, ".jar") {
		t.Errorf("截断后应保留扩展名，实际 %q", got)
	}
}

func TestResolveDestStaysInsideDir(t *testing.T) {
	dir := t.TempDir()
	p, err := resolveDest(dir, "server.jar")
	if err != nil {
		t.Fatalf("正常路径不应报错 : %v", err)
	}
	if filepath.Dir(p) != dir {
		t.Errorf("落盘路径 %q 应位于 %q", p, dir)
	}
	if _, err := resolveDest("", "server.jar"); err == nil {
		t.Error("空目录应报错")
	}
}

func TestCheckDownloadURL(t *testing.T) {
	for _, u := range []string{"https://a.example/x.jar", "http://127.0.0.1:8080/x"} {
		if err := checkDownloadURL(u); err != nil {
			t.Errorf("%q 应被接受 : %v", u, err)
		}
	}
	for _, u := range []string{"", "ftp://a/x.jar", "file:///c:/x.jar", "javascript:alert(1)", "https://"} {
		if err := checkDownloadURL(u); err == nil {
			t.Errorf("%q 应被拒绝", u)
		}
	}
}

func TestIsPreReleaseVersion(t *testing.T) {
	pre := []string{"1.21.11-rc3", "1.21.11-pre5", "26.4-snapshot-2", "25w46a", "1.19.1-pre1", "1.0-alpha"}
	for _, v := range pre {
		if !isPreReleaseVersion(v) {
			t.Errorf("%q 应被判定为预发布", v)
		}
	}
	for _, v := range []string{"1.21.4", "26.3", "26.1.2", "1.7.10"} {
		if isPreReleaseVersion(v) {
			t.Errorf("%q 不应被判定为预发布", v)
		}
	}
}

func TestCompareMinecraftVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.21.4", "1.21.3", 1},
		{"1.21.3", "1.21.4", -1},
		{"1.21.4", "1.21.4", 0},
		{"26.3", "1.21.11", 1},
		{"26.10", "26.9", 1},
		{"1.20.6", "1.20.10", -1},
	}
	for _, c := range cases {
		got := compareMinecraftVersions(c.a, c.b)
		if (got > 0) != (c.want > 0) || (got < 0) != (c.want < 0) {
			t.Errorf("compare(%q,%q) 符号应为 %d，实际 %d", c.a, c.b, c.want, got)
		}
	}
}

// 本文件补充 server_test.go 未覆盖的用例：并发安全、缓存行为、HTTP 重试/取消语义，
// 以及真实响应常量的字段级结构断言。
//
// 文件名为 debug_test.go 属于历史遗留（创建它的调试过程早于定稿），内容全部是正式用例。
package downloader

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestResolveIsConcurrencySafe 并发调用 Resolve 时不应有数据竞争（配合 -race 使用），
// 并且 TTL 缓存要能承接并发命中。
func TestResolveIsConcurrencySafe(t *testing.T) {
	var hits int32
	srv := paperTestServer(t, &hits)
	defer srv.Close()
	withBase(t, srv.URL)

	const workers = 8
	done := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			_, err := Resolve(context.Background(), Request{Kind: Paper, MinecraftVersion: "1.21.4"})
			done <- err
		}()
	}
	for i := 0; i < workers; i++ {
		if err := <-done; err != nil {
			t.Fatalf("并发解析失败 : %v", err)
		}
	}
}

// TestListVersionsCached 验证 ListVersions 的缓存与排序。
func TestListVersionsCached(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		writeJSON(w, purpurVersionsJSON)
	}))
	defer srv.Close()
	withBase(t, srv.URL)

	for i := 0; i < 2; i++ {
		versions, err := ListVersions(context.Background(), Purpur, "")
		if err != nil {
			t.Fatalf("第 %d 次列出 Purpur 版本失败 : %v", i, err)
		}
		want := []string{"26.3", "26.2", "26.1.2", "1.21.5", "1.21.4", "1.21.3"}
		if len(versions) != len(want) {
			t.Fatalf("版本数量应为 %d，实际 %v", len(want), versions)
		}
		for j := range want {
			if versions[j] != want[j] {
				t.Fatalf("Purpur 版本应新的在前 %v，实际 %v", want, versions)
			}
		}
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("版本列表应命中缓存，实际请求 %d 次", n)
	}
}

// TestListBuildsRejectsKindsWithoutBuilds 验证非 paper/purpur 的构建列表请求被拒绝。
func TestListBuildsRejectsKindsWithoutBuilds(t *testing.T) {
	for _, k := range []Kind{Vanilla, Fabric, Spigot} {
		_, err := ListBuilds(context.Background(), k, "1.21.4")
		if err == nil {
			t.Errorf("%s 应没有构建号概念", k)
			continue
		}
		if !strings.Contains(err.Error(), "构建号") {
			t.Errorf("%s 的错误信息应说明没有构建号，实际：%v", k, err)
		}
	}
}

// TestLatestVersionRejectsUnknownKind 验证 LatestVersion 也做 Kind 校验。
func TestLatestVersionRejectsUnknownKind(t *testing.T) {
	if _, err := LatestVersion(context.Background(), Kind("forge"), ""); err == nil {
		t.Fatal("未知类型应返回错误")
	}
	if _, err := ListVersions(context.Background(), Kind("forge"), ""); err == nil {
		t.Fatal("未知类型应返回错误")
	}
}

// TestJSONDecoderRejectsTrailingGarbage 验证损坏的 JSON 会被当作不可重试的解析错误。
func TestJSONDecoderRejectsTrailingGarbage(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte("{not json"))
	}))
	defer srv.Close()

	var out map[string]any
	err := getJSON(context.Background(), HTTPClient(time.Second, ""), srv.URL, 3, "", nil, &out)
	if err == nil {
		t.Fatal("非法 JSON 应返回错误")
	}
	if !strings.Contains(err.Error(), "JSON") {
		t.Errorf("错误应说明 JSON 解析失败，实际：%v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("JSON 解析失败不应重试，实际请求 %d 次", n)
	}
}

// TestGetJSONRetriesOnServerError 验证 5xx 会重试。
func TestGetJSONRetriesOnServerError(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var out map[string]any
	if err := getJSON(context.Background(), HTTPClient(time.Second, ""), srv.URL, 3, "", nil, &out); err != nil {
		t.Fatalf("重试后应成功 : %v", err)
	}
	if out["ok"] != true {
		t.Errorf("解析结果异常：%v", out)
	}
}

// TestGetJSONRespectsContextCancel 验证 ctx 取消会立即中止。
func TestGetJSONRespectsContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{}`)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out map[string]any
	if err := getJSON(ctx, HTTPClient(time.Second, ""), srv.URL, 3, "", nil, &out); err == nil {
		t.Fatal("ctx 已取消时应返回错误")
	}
}

// TestPaperV3FixturesParse 直接对真实响应常量做结构断言，确保字段名没写错。
func TestPaperV3FixturesParse(t *testing.T) {
	var proj paperV3Project
	if err := json.Unmarshal([]byte(paperV3ProjectJSON), &proj); err != nil {
		t.Fatalf("v3 项目响应解析失败 : %v", err)
	}
	if proj.Project.ID != "paper" {
		t.Errorf("project.id 应为 paper，实际 %q", proj.Project.ID)
	}
	if got := proj.Versions["1.21"]; len(got) == 0 || got[0] != "1.21.11" {
		t.Errorf("1.21 分组的首个版本应为 1.21.11，实际 %v", got)
	}

	var builds []paperV3Build
	if err := json.Unmarshal([]byte(paperV3BuildsJSON), &builds); err != nil {
		t.Fatalf("v3 构建数组解析失败 : %v", err)
	}
	if len(builds) != 3 || builds[0].ID != 232 {
		t.Fatalf("构建数组解析异常：%+v", builds)
	}
	if builds[0].Downloads["server:default"].Size != 51437498 {
		t.Errorf("server:default 的 size 解析错误：%d", builds[0].Downloads["server:default"].Size)
	}

	var single paperV3Build
	if err := json.Unmarshal([]byte(paperV3LatestBuildJSON), &single); err != nil {
		t.Fatalf("v3 单个构建解析失败 : %v", err)
	}
	if single.ID != 232 || single.Channel != "STABLE" {
		t.Errorf("单个构建解析异常：%+v", single)
	}
}

// TestFabricFixturesParse 断言 Fabric 真实响应常量能被正确解析。
func TestFabricFixturesParse(t *testing.T) {
	var loaders []fabricLoaderEntry
	if err := json.Unmarshal([]byte(fabricLoaderListJSON), &loaders); err != nil {
		t.Fatalf("loader 列表解析失败 : %v", err)
	}
	if len(loaders) == 0 || loaders[0].Loader.Version != "0.19.5" || !loaders[0].Loader.Stable {
		t.Fatalf("loader 列表解析异常：%+v", loaders)
	}
	var installers []fabricInstallerEntry
	if err := json.Unmarshal([]byte(fabricInstallerListJSON), &installers); err != nil {
		t.Fatalf("installer 列表解析失败 : %v", err)
	}
	if len(installers) == 0 || installers[0].Version != "1.0.1" || !installers[0].Stable {
		t.Fatalf("installer 列表解析异常：%+v", installers)
	}
	var games []fabricGameEntry
	if err := json.Unmarshal([]byte(fabricGameListJSON), &games); err != nil {
		t.Fatalf("game 列表解析失败 : %v", err)
	}
	if games[0].Version != "26.4-snapshot-2" || games[0].Stable {
		t.Fatalf("game 列表解析异常：%+v", games[0])
	}
}

// TestPurpurFixturesParse 断言 Purpur 真实响应常量能被正确解析。
func TestPurpurFixturesParse(t *testing.T) {
	var vs purpurVersions
	if err := json.Unmarshal([]byte(purpurVersionsJSON), &vs); err != nil {
		t.Fatalf("Purpur 版本列表解析失败 : %v", err)
	}
	if vs.Metadata.Current != "26.2" {
		t.Errorf("metadata.current 应为 26.2，实际 %q", vs.Metadata.Current)
	}
	var v purpurVersion
	if err := json.Unmarshal([]byte(purpurVersionJSON), &v); err != nil {
		t.Fatalf("Purpur 版本详情解析失败 : %v", err)
	}
	if v.Builds.Latest != "2416" || len(v.Builds.All) != 3 {
		t.Errorf("Purpur 构建信息解析异常：%+v", v.Builds)
	}
	var b purpurBuild
	if err := json.Unmarshal([]byte(purpurBuildJSON), &b); err != nil {
		t.Fatalf("Purpur 构建详情解析失败 : %v", err)
	}
	if b.Build != "2416" || !strings.HasPrefix(b.SHA256, "9d1b2c3d") {
		t.Errorf("Purpur 构建详情解析异常：%+v", b)
	}
}

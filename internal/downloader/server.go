package downloader

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yuzumikonami/nyatmc/pkg/logger"
)

// Kind 是服务端类型。
type Kind string

// 支持的服务端类型。
const (
	Paper   Kind = "paper"
	Fabric  Kind = "fabric"
	Vanilla Kind = "vanilla"
	Spigot  Kind = "spigot"
	Purpur  Kind = "purpur"
)

// 远端接口基地址。测试会把这些变量指向 httptest 服务器。
var (
	// paperV3Base 是 PaperMC 当前推荐的 Downloads Service（fill）。
	paperV3Base = "https://fill.papermc.io/v3"
	// paperV2Base 是已被日落（410）的旧接口，仅作为回退保留。
	paperV2Base = "https://api.papermc.io/v2"
	// fabricMetaBase 是 Fabric Meta。
	fabricMetaBase = "https://meta.fabricmc.net/v2"
	// launcherManifestURL 是 Mojang 版本清单。
	launcherManifestURL = "https://launchermeta.mojang.com/mc/game/version_manifest_v2.json"
	// purpurAPIBase 是 Purpur 接口。
	purpurAPIBase = "https://api.purpurmc.org/v2"
	// bmclapiBase 是 BMCLAPI 镜像基地址。
	bmclapiBase = "https://bmclapi2.bangbang93.com"
	// buildToolsURL 是官方 BuildTools。
	buildToolsURL = "https://hub.spigotmc.org/jenkins/job/BuildTools/lastSuccessfulBuild/artifact/target/BuildTools.jar"
)

// cacheTTL 是内存缓存有效期。
const cacheTTL = 10 * time.Minute

// Request 描述一次服务端获取请求。
type Request struct {
	// Kind 是服务端类型。
	Kind Kind
	// MinecraftVersion 为空或 "latest" 表示最新正式版。
	MinecraftVersion string
	// Build 是 Paper/Purpur 构建号，空表示最新。
	Build string
	// LoaderVersion 是 Fabric loader 版本，空表示最新。
	LoaderVersion string
	// InstallerVersion 是 Fabric installer 版本，空表示最新。
	InstallerVersion string
	// Dir 是目标目录。
	Dir string
	// FileName 是目标文件名，默认 server.jar。
	FileName string
	// Mirror 可空，例如 https://bmclapi2.bangbang93.com；用官方源时为空。
	Mirror string
	// Verify 表示是否校验哈希。
	Verify bool
	// Timeout 是单次请求超时。
	Timeout time.Duration
	// Retries 是失败重试次数。
	Retries int
	// UserAgent 是请求头。
	UserAgent string
	// JavaPath 仅 Spigot(BuildTools) 需要。
	JavaPath string
	// Logger 是日志器。
	Logger *logger.Logger
}

// Artifact 描述解析到的可下载产物（下载前即可得到）。
type Artifact struct {
	// Kind 是服务端类型。
	Kind Kind
	// URL 是下载地址。
	URL string
	// FileName 是建议文件名。
	FileName string
	// Version 是 Minecraft 版本。
	Version string
	// Build 是构建号（Paper/Purpur）。
	Build string
	// LoaderVersion 是 Fabric loader 版本。
	LoaderVersion string
	// InstallerVersion 是 Fabric installer 版本。
	InstallerVersion string
	// SHA256 是期望的 SHA-256（可能为空）。
	SHA256 string
	// SHA1 是期望的 SHA-1（可能为空）。
	SHA1 string
	// Size 是期望的文件大小，未知为 0。
	Size int64
	// Mirror 是实际使用的镜像基地址，空表示官方源。
	Mirror string
	// Extra 保存额外信息（如 channel / source）。
	Extra map[string]string
}

// ---------------------------------------------------------------- 内存缓存

// cacheEntry 是一条带过期时间的缓存项。
type cacheEntry[T any] struct {
	val T
	exp time.Time
}

// memCache 是极简的带 TTL 的内存缓存，零值即可用。
type memCache[T any] struct {
	mu sync.RWMutex
	m  map[string]cacheEntry[T]
}

// get 读取缓存，未命中或已过期时返回 ok=false。
func (c *memCache[T]) get(key string) (T, bool) {
	c.mu.RLock()
	e, ok := c.m[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(e.exp) {
		var zero T
		return zero, false
	}
	return e.val, true
}

// put 写入缓存。
func (c *memCache[T]) put(key string, val T) {
	c.mu.Lock()
	if c.m == nil {
		c.m = make(map[string]cacheEntry[T])
	}
	c.m[key] = cacheEntry[T]{val: val, exp: time.Now().Add(cacheTTL)}
	c.mu.Unlock()
}

// reset 清空缓存（测试用）。
func (c *memCache[T]) reset() {
	c.mu.Lock()
	c.m = nil
	c.mu.Unlock()
}

var (
	// artifactCache 缓存解析结果，避免重复打接口。
	artifactCache memCache[Artifact]
	// versionsCache 缓存版本列表。
	versionsCache memCache[[]string]
	// buildsCache 缓存构建号列表。
	buildsCache memCache[[]string]
	// paperSpecCache 缓存 Paper v3 版本表。
	paperSpecCache memCache[paperV3Project]
)

// resetCaches 清空全部缓存（测试用）。
func resetCaches() {
	artifactCache.reset()
	versionsCache.reset()
	buildsCache.reset()
	paperSpecCache.reset()
}

// ---------------------------------------------------------------- context 辅助

// reqLogger 返回请求的日志器，缺省为丢弃日志器。
func reqLogger(req Request) *logger.Logger {
	if req.Logger != nil {
		return req.Logger
	}
	return logger.Discard()
}

// ctxOr 在 ctx 为 nil 时补一个后台 context。
func ctxOr(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// ---------------------------------------------------------------- Resolve 入口

// Resolve 解析出下载地址与校验和（不下载）。
func Resolve(ctx context.Context, req Request) (Artifact, error) {
	ctx = ctxOr(ctx)
	if err := validateKind(req.Kind); err != nil {
		return Artifact{}, err
	}
	req.Mirror = strings.TrimSpace(req.Mirror)
	if req.Mirror != "" && !strings.HasPrefix(req.Mirror, "http://") && !strings.HasPrefix(req.Mirror, "https://") {
		return Artifact{}, fmt.Errorf("镜像地址 %q 非法：必须以 http:// 或 https:// 开头", req.Mirror)
	}
	key := strings.Join([]string{
		string(req.Kind), req.MinecraftVersion, req.Build,
		req.LoaderVersion, req.InstallerVersion, req.Mirror,
	}, "|")
	if a, ok := artifactCache.get(key); ok {
		reqLogger(req).Debugf("复用缓存的解析结果：%s %s", a.Kind, a.Version)
		return a, nil
	}

	var (
		a   Artifact
		err error
	)
	switch req.Kind {
	case Paper:
		a, err = resolvePaper(ctx, req)
	case Fabric:
		a, err = resolveFabric(ctx, req)
	case Vanilla:
		a, err = resolveVanilla(ctx, req)
	case Purpur:
		a, err = resolvePurpur(ctx, req)
	case Spigot:
		err = fmt.Errorf("spigot 没有预编译产物，请使用 BuildSpigot 用官方 BuildTools 本机构建（需要 JDK，耗时较长，建议直接改用 paper）")
	default:
		err = fmt.Errorf("不支持的服务端类型 %q", req.Kind)
	}
	if err != nil {
		return Artifact{}, err
	}
	if err := checkDownloadURL(a.URL); err != nil {
		return Artifact{}, err
	}
	if a.FileName == "" {
		a.FileName = "server.jar"
	}
	if _, err := sanitizeFileName(a.FileName); err != nil {
		return Artifact{}, fmt.Errorf("%s 返回的文件名不可用 : %w", req.Kind, err)
	}
	artifactCache.put(key, a)
	return a, nil
}

// validateKind 校验服务端类型。
func validateKind(k Kind) error {
	switch k {
	case Paper, Fabric, Vanilla, Spigot, Purpur:
		return nil
	case "":
		return fmt.Errorf("未指定服务端类型（可选 paper/fabric/vanilla/spigot/purpur）")
	default:
		return fmt.Errorf("未知的服务端类型 %q（可选 paper/fabric/vanilla/spigot/purpur）", k)
	}
}

// ---------------------------------------------------------------- PaperMC

// paperV3Project 是 v3 /projects/{project} 的响应。
type paperV3Project struct {
	Project struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"project"`
	Versions map[string][]string `json:"versions"`
}

// paperV3Build 是 v3 /builds 数组里的一个构建。
type paperV3Build struct {
	ID      int    `json:"id"`
	Time    string `json:"time"`
	Channel string `json:"channel"`
	// Downloads 的键形如 "server:default"。
	Downloads map[string]paperV3Download `json:"downloads"`
}

// paperV3Download 是 v3 构建里的一个下载项。
type paperV3Download struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	URL       string `json:"url"`
	Checksums struct {
		SHA256 string `json:"sha256"`
	} `json:"checksums"`
}

// paperV2VersionGroup 是 v2 /projects/{project} 的响应。
type paperV2VersionGroup struct {
	Versions []string `json:"versions"`
}

// paperV2BuildList 是 v2 构建列表。
type paperV2BuildList struct {
	Builds []int `json:"builds"`
}

// paperV2Build 是 v2 单个构建详情。
type paperV2Build struct {
	Build     int    `json:"build"`
	Time      string `json:"time"`
	Channel   string `json:"channel"`
	Downloads struct {
		Application struct {
			Name   string `json:"name"`
			SHA256 string `json:"sha256"`
			Size   int64  `json:"size"`
		} `json:"application"`
	} `json:"downloads"`
}

// paperProjectURL 拼接 v3 项目地址。
func paperProjectURL(project string) string {
	u, _ := url.Parse(paperV3Base + "/projects/")
	u.Path = path.Join(u.Path, url.PathEscape(project))
	return u.String()
}

// paperV3BuildsURL 拼接 v3 构建列表地址，build 为空表示最新。
func paperV3BuildsURL(project, version, build string) string {
	u, _ := url.Parse(paperV3Base + "/projects/")
	u.Path = path.Join(u.Path, url.PathEscape(project), "versions", url.PathEscape(version), "builds")
	if build != "" {
		u.Path = path.Join(u.Path, url.PathEscape(build))
	}
	return u.String()
}

// paperV2JSONURL 拼接 v2 地址，parts 会被逐段转义。
func paperV2JSONURL(parts ...string) string {
	u, _ := url.Parse(paperV2Base + "/")
	segs := append([]string{u.Path}, parts...)
	u.Path = path.Join(segs...)
	return u.String()
}

// resolvePaper 解析 Paper 产物，v3 优先、v2 回退。
func resolvePaper(ctx context.Context, req Request) (Artifact, error) {
	project := string(Paper)
	client := HTTPClient(req.Timeout, req.UserAgent)
	log := reqLogger(req)

	version := normalizeRequestedVersion(req.MinecraftVersion)
	v3Err := error(nil)
	if version == "" || version == "latest" {
		var err error
		version, err = latestPaperVersion(ctx, client, req)
		if err != nil {
			v3Err = err
		}
	}
	if v3Err == nil {
		a, err := resolvePaperV3(ctx, client, req, project, version)
		if err == nil {
			return a, nil
		}
		v3Err = err
	}

	log.Debugf("PaperMC v3 不可用（%v），尝试回退 v2", v3Err)
	a, err := resolvePaperV2(ctx, client, req, project)
	if err != nil {
		return Artifact{}, fmt.Errorf("PaperMC v3 解析失败（%v），v2 回退也失败 : %w", v3Err, err)
	}
	if a.Extra == nil {
		a.Extra = map[string]string{}
	}
	a.Extra["source"] = "v2"
	log.Warnf("PaperMC v3 解析失败，已回退到已进入弃用流程的 v2 接口")
	return a, nil
}

// latestPaperVersion 取 Paper 最新正式版；没有正式版时取列表中最新的版本。
func latestPaperVersion(ctx context.Context, client *http.Client, req Request) (string, error) {
	versions, err := listPaperVersions(ctx, client, req)
	if err != nil {
		return "", err
	}
	if len(versions) == 0 {
		return "", fmt.Errorf("PaperMC 未返回任何版本")
	}
	// 过滤掉预发布版本，优先正式版。
	for _, v := range versions {
		if !isPreReleaseVersion(v) {
			return v, nil
		}
	}
	return versions[0], nil
}

// listPaperVersions 取 Paper 支持的版本（v3 优先，v2 回退）。
func listPaperVersions(ctx context.Context, client *http.Client, req Request) ([]string, error) {
	if p, ok := paperSpecCache.get("paper"); ok {
		return flattenPaperVersions(p), nil
	}
	var v3 paperV3Project
	err := getJSON(ctx, client, paperProjectURL(string(Paper)), req.Retries, req.UserAgent, reqLogger(req), &v3)
	if err == nil && len(v3.Versions) > 0 {
		paperSpecCache.put("paper", v3)
		return flattenPaperVersions(v3), nil
	}
	v3Err := err
	var v2 paperV2VersionGroup
	err = getJSON(ctx, client, paperV2JSONURL("projects", string(Paper)), req.Retries, req.UserAgent, reqLogger(req), &v2)
	if err != nil {
		return nil, fmt.Errorf("获取 PaperMC 版本列表失败（v3: %v；v2: %w）", v3Err, err)
	}
	return v2.Versions, nil
}

// flattenPaperVersions 把 v3 的版本分组拍平成列表。
//
// v3 的键是主版本（如 "1.21"），值是该主版本下的完整版本，本身已按新到旧排列；
// 这里按主版本号排序后再拼接，保证“新的在前”。
func flattenPaperVersions(p paperV3Project) []string {
	keys := make([]string, 0, len(p.Versions))
	for k := range p.Versions {
		keys = append(keys, k)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		return compareMinecraftVersions(keys[i], keys[j]) > 0
	})
	out := make([]string, 0, len(keys)*2)
	for _, k := range keys {
		out = append(out, p.Versions[k]...)
	}
	return out
}

// resolvePaperV3 用 v3 接口解析。
//
// 注意 v3 的两种返回形态：
//   - /builds            -> JSON 数组
//   - /builds/latest     -> 单个 JSON 对象
//   - /builds/{build}    -> 单个 JSON 对象
func resolvePaperV3(ctx context.Context, client *http.Client, req Request, project, version string) (Artifact, error) {
	log := reqLogger(req)
	build := strings.TrimSpace(req.Build)
	if build != "" {
		if err := validateVersionToken(build); err != nil {
			return Artifact{}, fmt.Errorf("Paper 构建号 %q 非法 : %w", build, err)
		}
	}
	rawURL := paperV3BuildsURL(project, version, build)

	var single paperV3Build
	var list []paperV3Build
	if build == "" {
		// 不带构建号时 /builds 返回数组，取第一个（最新的）。
		if err := getJSON(ctx, client, rawURL, req.Retries, req.UserAgent, log, &list); err != nil {
			// 某些部署会在 /builds 上直接返回单个对象，兼容一下。
			if err2 := getJSON(ctx, client, rawURL, req.Retries, req.UserAgent, log, &single); err2 != nil {
				return Artifact{}, fmt.Errorf("获取 Paper 构建列表 %s 失败 : %w", rawURL, err)
			}
		} else if len(list) > 0 {
			single = list[0]
		}
	} else {
		if err := getJSON(ctx, client, rawURL, req.Retries, req.UserAgent, log, &single); err != nil {
			return Artifact{}, fmt.Errorf("获取 Paper 构建 %s 失败 : %w", rawURL, err)
		}
	}
	if single.ID == 0 {
		return Artifact{}, fmt.Errorf("PaperMC 未返回 %s 的构建信息", version)
	}

	dl, ok := single.Downloads["server:default"]
	if !ok {
		for k, v := range single.Downloads {
			if strings.HasPrefix(k, "server") {
				dl, ok = v, true
				break
			}
		}
	}
	if !ok {
		return Artifact{}, fmt.Errorf("Paper 构建 %s#%d 没有服务端下载项", version, single.ID)
	}
	if strings.TrimSpace(dl.URL) == "" {
		return Artifact{}, fmt.Errorf("Paper 构建 %s#%d 的下载地址为空", version, single.ID)
	}
	name := dl.Name
	if name == "" {
		name = fmt.Sprintf("paper-%s-%d.jar", version, single.ID)
	}
	return Artifact{
		Kind:     Paper,
		URL:      dl.URL,
		FileName: name,
		Version:  version,
		Build:    itoa(int64(single.ID)),
		SHA256:   strings.ToLower(dl.Checksums.SHA256),
		Size:     dl.Size,
		Mirror:   req.Mirror,
		Extra: map[string]string{
			"source":  "v3",
			"channel": single.Channel,
		},
	}, nil
}

// resolvePaperV2 用已日落的 v2 接口解析（保留作为回退）。
func resolvePaperV2(ctx context.Context, client *http.Client, req Request, project string) (Artifact, error) {
	log := reqLogger(req)
	version := normalizeRequestedVersion(req.MinecraftVersion)
	if version == "" || version == "latest" {
		var g paperV2VersionGroup
		if err := getJSON(ctx, client, paperV2JSONURL("projects", project), req.Retries, req.UserAgent, log, &g); err != nil {
			return Artifact{}, fmt.Errorf("获取 PaperMC v2 版本列表失败 : %w", err)
		}
		for _, v := range g.Versions {
			if !isPreReleaseVersion(v) {
				version = v
				break
			}
		}
		if version == "" && len(g.Versions) > 0 {
			version = g.Versions[0]
		}
	}
	if version == "" {
		return Artifact{}, fmt.Errorf("无法确定 Paper 的 Minecraft 版本")
	}
	build := strings.TrimSpace(req.Build)
	if build == "" {
		var bl paperV2BuildList
		if err := getJSON(ctx, client, paperV2JSONURL("projects", project, "versions", version, "builds"), req.Retries, req.UserAgent, log, &bl); err != nil {
			return Artifact{}, fmt.Errorf("获取 PaperMC v2 构建列表失败 : %w", err)
		}
		if len(bl.Builds) == 0 {
			return Artifact{}, fmt.Errorf("PaperMC v2 没有 %s 的构建", version)
		}
		build = itoa(int64(bl.Builds[len(bl.Builds)-1]))
	}
	var b paperV2Build
	if err := getJSON(ctx, client, paperV2JSONURL("projects", project, "versions", version, "builds", build), req.Retries, req.UserAgent, log, &b); err != nil {
		return Artifact{}, fmt.Errorf("获取 PaperMC v2 构建 %s 失败 : %w", build, err)
	}
	dl := b.Downloads.Application
	if strings.TrimSpace(dl.Name) == "" {
		return Artifact{}, fmt.Errorf("PaperMC v2 构建 %s 缺少下载信息", build)
	}
	u := paperV2JSONURL("projects", project, "versions", version, "builds", build, "downloads", dl.Name)
	return Artifact{
		Kind:     Paper,
		URL:      u,
		FileName: dl.Name,
		Version:  version,
		Build:    build,
		SHA256:   strings.ToLower(dl.SHA256),
		Size:     dl.Size,
		Mirror:   req.Mirror,
		Extra: map[string]string{
			"source":  "v2",
			"channel": b.Channel,
		},
	}, nil
}

// ---------------------------------------------------------------- Fabric

// fabricLoaderEntry 是 /versions/loader/<game> 的一项。
type fabricLoaderEntry struct {
	Loader struct {
		Version string `json:"version"`
		Stable  bool   `json:"stable"`
		Build   int    `json:"build"`
	} `json:"loader"`
}

// fabricInstallerEntry 是 /versions/installer 的一项。
type fabricInstallerEntry struct {
	Version string `json:"version"`
	Stable  bool   `json:"stable"`
}

// fabricGameEntry 是 /versions/game 的一项。
type fabricGameEntry struct {
	Version string `json:"version"`
	Stable  bool   `json:"stable"`
}

// fabricURL 拼接 Fabric Meta 地址。
func fabricURL(parts ...string) string {
	u, _ := url.Parse(fabricMetaBase + "/")
	segs := append([]string{u.Path}, parts...)
	u.Path = path.Join(segs...)
	return u.String()
}

// fabricServerJarURL 拼接服务端启动 jar 直链。
func fabricServerJarURL(game, loader, installer string) string {
	return fabricURL("versions", "loader", game, loader, installer, "server", "jar")
}

// resolveFabric 解析 Fabric 服务端启动 jar。
func resolveFabric(ctx context.Context, req Request) (Artifact, error) {
	client := HTTPClient(req.Timeout, req.UserAgent)
	log := reqLogger(req)
	version, err := latestFabricGameVersion(ctx, client, req)
	if err != nil {
		return Artifact{}, err
	}
	loader := strings.TrimSpace(req.LoaderVersion)
	if loader == "" {
		loader, err = latestFabricLoader(ctx, client, req, version)
		if err != nil {
			return Artifact{}, err
		}
	}
	installer := strings.TrimSpace(req.InstallerVersion)
	if installer == "" {
		installer, err = latestFabricInstaller(ctx, client, req)
		if err != nil {
			return Artifact{}, err
		}
	}
	log.Warnf("Fabric 官方未提供 server jar 的哈希，无法校验完整性，请自行确认来源可信")
	name := fmt.Sprintf("fabric-server-mc.%s-loader.%s-launcher.%s.jar", version, loader, installer)
	return Artifact{
		Kind:             Fabric,
		URL:              fabricServerJarURL(version, loader, installer),
		FileName:         name,
		Version:          version,
		LoaderVersion:    loader,
		InstallerVersion: installer,
		Mirror:           req.Mirror,
		Extra:            map[string]string{"source": "fabric-meta"},
	}, nil
}

// latestFabricGameVersion 取 Fabric 支持的最新稳定游戏版本。
func latestFabricGameVersion(ctx context.Context, client *http.Client, req Request) (string, error) {
	v := normalizeRequestedVersion(req.MinecraftVersion)
	if v != "" && v != "latest" {
		return v, nil
	}
	var games []fabricGameEntry
	if err := getJSON(ctx, client, fabricURL("versions", "game"), req.Retries, req.UserAgent, reqLogger(req), &games); err != nil {
		return "", fmt.Errorf("获取 Fabric 游戏版本列表失败 : %w", err)
	}
	for _, g := range games {
		if g.Stable {
			return g.Version, nil
		}
	}
	if len(games) > 0 {
		return games[0].Version, nil
	}
	return "", fmt.Errorf("Fabric 未返回任何游戏版本")
}

// latestFabricLoader 取指定游戏版本下最新的稳定 loader（没有稳定版时取最新）。
func latestFabricLoader(ctx context.Context, client *http.Client, req Request, game string) (string, error) {
	var list []fabricLoaderEntry
	if err := getJSON(ctx, client, fabricURL("versions", "loader", game), req.Retries, req.UserAgent, reqLogger(req), &list); err != nil {
		return "", fmt.Errorf("获取 Fabric loader 列表失败 : %w", err)
	}
	if len(list) == 0 {
		return "", fmt.Errorf("Fabric 没有 %s 可用的 loader", game)
	}
	for _, e := range list {
		if e.Loader.Stable {
			return e.Loader.Version, nil
		}
	}
	return list[0].Loader.Version, nil
}

// latestFabricInstaller 取最新的稳定 installer。
func latestFabricInstaller(ctx context.Context, client *http.Client, req Request) (string, error) {
	var list []fabricInstallerEntry
	if err := getJSON(ctx, client, fabricURL("versions", "installer"), req.Retries, req.UserAgent, reqLogger(req), &list); err != nil {
		return "", fmt.Errorf("获取 Fabric installer 列表失败 : %w", err)
	}
	if len(list) == 0 {
		return "", fmt.Errorf("Fabric 未返回任何 installer 版本")
	}
	for _, e := range list {
		if e.Stable {
			return e.Version, nil
		}
	}
	return list[0].Version, nil
}

// ---------------------------------------------------------------- Vanilla

// versionManifest 是 Mojang 版本清单。
type versionManifest struct {
	Latest struct {
		Release  string `json:"release"`
		Snapshot string `json:"snapshot"`
	} `json:"latest"`
	Versions []manifestVersion `json:"versions"`
}

// manifestVersion 是清单中的一项。
type manifestVersion struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	URL  string `json:"url"`
	SHA1 string `json:"sha1"`
}

// versionJSON 是某个版本的详细 JSON（只保留服务端下载信息）。
type versionJSON struct {
	ID        string `json:"id"`
	Downloads struct {
		Server struct {
			SHA1 string `json:"sha1"`
			Size int64  `json:"size"`
			URL  string `json:"url"`
		} `json:"server"`
	} `json:"downloads"`
}

// manifestURL 返回版本清单地址，mirror 非空时改用 BMCLAPI 等价端点。
func manifestURL(mirror string) string {
	if strings.TrimSpace(mirror) == "" {
		return launcherManifestURL
	}
	base := strings.TrimRight(mirror, "/")
	return base + "/mc/game/version_manifest_v2.json"
}

// resolveVanilla 解析 Vanilla 服务端。
func resolveVanilla(ctx context.Context, req Request) (Artifact, error) {
	client := HTTPClient(req.Timeout, req.UserAgent)
	log := reqLogger(req)
	mirror := strings.TrimSpace(req.Mirror)
	rawManifest := manifestURL(mirror)

	var mf versionManifest
	if err := getJSON(ctx, client, rawManifest, req.Retries, req.UserAgent, log, &mf); err != nil {
		return Artifact{}, fmt.Errorf("获取 Minecraft 版本清单失败（可能是镜像不可用）: %w", err)
	}
	version := normalizeRequestedVersion(req.MinecraftVersion)
	if version == "" || version == "latest" {
		version = mf.Latest.Release
	}
	if version == "" {
		return Artifact{}, fmt.Errorf("无法确定 Minecraft 最新正式版")
	}
	var entry *manifestVersion
	for i := range mf.Versions {
		if mf.Versions[i].ID == version {
			entry = &mf.Versions[i]
			break
		}
	}
	if entry == nil {
		return Artifact{}, fmt.Errorf("Minecraft 版本 %s 不存在（可用版本共 %d 个）", version, len(mf.Versions))
	}

	vjURL := entry.URL
	if mirror != "" {
		// BMCLAPI 把版本 JSON 放在 /version/{id}/json。
		vjURL = strings.TrimRight(mirror, "/") + "/version/" + url.PathEscape(version) + "/json"
	}
	var vj versionJSON
	if err := getJSON(ctx, client, vjURL, req.Retries, req.UserAgent, log, &vj); err != nil {
		return Artifact{}, fmt.Errorf("获取 Minecraft %s 的版本 JSON 失败 : %w", version, err)
	}
	srv := vj.Downloads.Server
	if strings.TrimSpace(srv.URL) == "" {
		return Artifact{}, fmt.Errorf("Minecraft %s 没有服务端下载项（%s 可能没有官方服务端）", version, version)
	}
	source := "mojang"
	if mirror != "" {
		source = "bmclapi"
	}
	name := fmt.Sprintf("minecraft-server-%s.jar", version)
	if mirror != "" {
		// BMCLAPI 直接给出服务端 jar 的稳定地址。
		srv.URL = strings.TrimRight(mirror, "/") + "/version/" + url.PathEscape(version) + "/server"
	}
	return Artifact{
		Kind:     Vanilla,
		URL:      srv.URL,
		FileName: name,
		Version:  version,
		SHA1:     strings.ToLower(srv.SHA1),
		Size:     srv.Size,
		Mirror:   mirror,
		Extra:    map[string]string{"source": source},
	}, nil
}

// ---------------------------------------------------------------- Purpur

// purpurVersions 是 /purpur 的响应。
type purpurVersions struct {
	Project  string `json:"project"`
	Metadata struct {
		Current string `json:"current"`
	} `json:"metadata"`
	Versions []string `json:"versions"`
}

// purpurVersion 是 /purpur/{version} 的响应。
type purpurVersion struct {
	Project string `json:"project"`
	Version string `json:"version"`
	Builds  struct {
		Latest string   `json:"latest"`
		All    []string `json:"all"`
	} `json:"builds"`
}

// purpurBuild 是 /purpur/{version}/{build} 的响应。
type purpurBuild struct {
	Project string `json:"project"`
	Version string `json:"version"`
	Build   string `json:"build"`
	MD5     string `json:"md5"`
	SHA1    string `json:"sha1"`
	SHA256  string `json:"sha256"`
}

// purpurDownloadURL 拼接 Purpur 下载直链。
func purpurDownloadURL(version, build string) string {
	u, _ := url.Parse(purpurAPIBase + "/purpur/")
	u.Path = path.Join(u.Path, url.PathEscape(version), url.PathEscape(build), "download")
	return u.String()
}

// purpurJSONURL 拼接 Purpur 接口地址。
func purpurJSONURL(parts ...string) string {
	u, _ := url.Parse(purpurAPIBase + "/purpur/")
	segs := append([]string{u.Path}, parts...)
	u.Path = path.Join(segs...)
	return u.String()
}

// resolvePurpur 解析 Purpur 服务端。
func resolvePurpur(ctx context.Context, req Request) (Artifact, error) {
	client := HTTPClient(req.Timeout, req.UserAgent)
	log := reqLogger(req)
	version := normalizeRequestedVersion(req.MinecraftVersion)
	if version == "" || version == "latest" {
		var v purpurVersions
		if err := getJSON(ctx, client, purpurJSONURL(), req.Retries, req.UserAgent, log, &v); err != nil {
			return Artifact{}, fmt.Errorf("获取 Purpur 版本列表失败 : %w", err)
		}
		version = v.Metadata.Current
		if version == "" && len(v.Versions) > 0 {
			version = v.Versions[len(v.Versions)-1]
		}
	}
	if version == "" {
		return Artifact{}, fmt.Errorf("无法确定 Purpur 的 Minecraft 版本")
	}
	build := strings.TrimSpace(req.Build)
	var detail purpurBuild
	if build == "" || build == "latest" {
		var v purpurVersion
		if err := getJSON(ctx, client, purpurJSONURL(version), req.Retries, req.UserAgent, log, &v); err != nil {
			return Artifact{}, fmt.Errorf("获取 Purpur %s 的构建列表失败 : %w", version, err)
		}
		build = v.Builds.Latest
		if build == "" && len(v.Builds.All) > 0 {
			build = v.Builds.All[len(v.Builds.All)-1]
		}
	}
	if build == "" {
		return Artifact{}, fmt.Errorf("Purpur %s 没有可用构建", version)
	}
	if err := getJSON(ctx, client, purpurJSONURL(version, build), req.Retries, req.UserAgent, log, &detail); err != nil {
		// 构建详情拿不到校验和不算致命，只是无法校验。
		log.Warnf("获取 Purpur %s 构建 %s 的校验和失败，将跳过哈希校验：%v", version, build, err)
	}
	return Artifact{
		Kind:     Purpur,
		URL:      purpurDownloadURL(version, build),
		FileName: fmt.Sprintf("purpur-%s-%s.jar", version, build),
		Version:  version,
		Build:    build,
		SHA256:   strings.ToLower(strings.TrimSpace(detail.SHA256)),
		SHA1:     strings.ToLower(strings.TrimSpace(detail.SHA1)),
		Mirror:   req.Mirror,
		Extra:    map[string]string{"source": "purpur"},
	}, nil
}

// ---------------------------------------------------------------- 下载

// Download 下载产物到 req.Dir/req.FileName，返回产物信息与实际落盘路径。
func Download(ctx context.Context, req Request, progress ProgressFunc) (Artifact, string, error) {
	ctx = ctxOr(ctx)
	log := reqLogger(req)
	if progress != nil {
		progress(Progress{Phase: "解析版本"})
	}
	artifact, err := Resolve(ctx, req)
	if err != nil {
		return Artifact{}, "", err
	}
	if artifact.Kind == Spigot {
		return Artifact{}, "", fmt.Errorf("spigot 请使用 BuildSpigot 构建")
	}
	dest, err := resolveDest(req.Dir, orDefault(req.FileName, artifact.FileName))
	if err != nil {
		return Artifact{}, "", err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return Artifact{}, "", fmt.Errorf("创建目标目录 %s 失败 : %w", filepath.Dir(dest), err)
	}

	log.Infof("开始下载 %s %s（%s）", artifact.Kind, artifact.Version, artifact.URL)
	if progress != nil {
		progress(Progress{Phase: "下载中", Total: artifact.Size})
	}
	part := dest + ".part"
	opts := DownloadOptions{
		Timeout:      req.Timeout,
		Retries:      req.Retries,
		UserAgent:    req.UserAgent,
		ExpectedSize: artifact.Size,
	}
	if _, err := DownloadTo(ctx, artifact.URL, dest, opts, progress); err != nil {
		_ = os.Remove(part)
		return Artifact{}, "", err
	}

	if req.Verify {
		if progress != nil {
			progress(Progress{Phase: "校验中", Done: artifact.Size, Total: artifact.Size, Percent: 100})
		}
		switch {
		case artifact.SHA256 != "" || artifact.SHA1 != "":
			if err := VerifyFile(dest, artifact.SHA256, artifact.SHA1); err != nil {
				_ = os.Remove(part)
				_ = os.Remove(dest)
				return Artifact{}, "", fmt.Errorf("校验失败，已删除下载文件 : %w", err)
			}
			log.Infof("%s %s 哈希校验通过", artifact.Kind, artifact.Version)
		case artifact.Kind == Vanilla && artifact.Mirror != "":
			log.Warnf("镜像未提供哈希，已跳过校验：%s %s", artifact.Kind, artifact.Version)
		case artifact.Kind == Fabric:
			log.Warnf("Fabric 官方未提供哈希，已跳过校验：%s %s", artifact.Kind, artifact.Version)
		default:
			log.Warnf("%s %s 没有可用的哈希，已跳过校验", artifact.Kind, artifact.Version)
		}
	} else if artifact.SHA256 != "" || artifact.SHA1 != "" {
		// 即使不校验，也提示用户可用哈希。
		log.Debugf("已跳过哈希校验（verify=false），远端哈希：sha256=%s sha1=%s", artifact.SHA256, artifact.SHA1)
	}

	if artifact.Size > 0 {
		st, serr := os.Stat(dest)
		if serr != nil {
			return Artifact{}, "", fmt.Errorf("检查落盘文件 %s 失败 : %w", dest, serr)
		}
		if st.Size() != artifact.Size {
			return Artifact{}, "", fmt.Errorf("落盘文件 %s 大小不符：期望 %d 字节，实际 %d 字节", dest, artifact.Size, st.Size())
		}
	}
	if progress != nil {
		progress(Progress{Phase: "完成", Done: artifact.Size, Total: artifact.Size, Percent: 100})
	}
	log.Infof("已保存到 %s", dest)
	return artifact, dest, nil
}

// Install 是 Download 的别名语义：下载 + 校验 + 落盘，返回 jar 路径。
func Install(ctx context.Context, req Request, progress ProgressFunc) (Artifact, string, error) {
	return Download(ctx, req, progress)
}

// ---------------------------------------------------------------- 列表查询

// LatestVersion 返回指定服务端类型的最新 Minecraft 正式版。
func LatestVersion(ctx context.Context, kind Kind, mirror string) (string, error) {
	ctx = ctxOr(ctx)
	if err := validateKind(kind); err != nil {
		return "", err
	}
	req := Request{Kind: kind, Mirror: mirror}
	client := HTTPClient(DefaultTimeout, "")
	switch kind {
	case Paper:
		return latestPaperVersion(ctx, client, req)
	case Purpur:
		var v purpurVersions
		if err := getJSON(ctx, client, purpurJSONURL(), DefaultRetries, "", logger.Discard(), &v); err != nil {
			return "", fmt.Errorf("获取 Purpur 版本列表失败 : %w", err)
		}
		if v.Metadata.Current != "" {
			return v.Metadata.Current, nil
		}
		if len(v.Versions) > 0 {
			return v.Versions[len(v.Versions)-1], nil
		}
		return "", fmt.Errorf("Purpur 未返回任何版本")
	case Fabric:
		return latestFabricGameVersion(ctx, client, req)
	case Vanilla:
		var mf versionManifest
		if err := getJSON(ctx, client, manifestURL(mirror), DefaultRetries, "", logger.Discard(), &mf); err != nil {
			return "", fmt.Errorf("获取 Minecraft 版本清单失败（可能是镜像不可用）: %w", err)
		}
		if mf.Latest.Release == "" {
			return "", fmt.Errorf("Minecraft 版本清单缺少最新正式版信息")
		}
		return mf.Latest.Release, nil
	case Spigot:
		// Spigot 由 BuildTools 按版本构建，版本号沿用上游 Minecraft 版本。
		return LatestVersion(ctx, Vanilla, "")
	}
	return "", fmt.Errorf("不支持的服务端类型 %q", kind)
}

// ListVersions 返回支持的 Minecraft 版本（新的在前）。
func ListVersions(ctx context.Context, kind Kind, mirror string) ([]string, error) {
	ctx = ctxOr(ctx)
	if err := validateKind(kind); err != nil {
		return nil, err
	}
	key := string(kind) + "|" + mirror
	if v, ok := versionsCache.get(key); ok {
		return append([]string(nil), v...), nil
	}
	client := HTTPClient(DefaultTimeout, "")
	log := logger.Discard()
	var out []string
	switch kind {
	case Paper:
		var err error
		out, err = listPaperVersions(ctx, client, Request{Kind: kind, Mirror: mirror})
		if err != nil {
			return nil, err
		}
	case Purpur:
		var v purpurVersions
		if err := getJSON(ctx, client, purpurJSONURL(), DefaultRetries, "", log, &v); err != nil {
			return nil, fmt.Errorf("获取 Purpur 版本列表失败 : %w", err)
		}
		out = make([]string, 0, len(v.Versions))
		for i := len(v.Versions) - 1; i >= 0; i-- {
			out = append(out, v.Versions[i])
		}
	case Fabric:
		var games []fabricGameEntry
		if err := getJSON(ctx, client, fabricURL("versions", "game"), DefaultRetries, "", log, &games); err != nil {
			return nil, fmt.Errorf("获取 Fabric 游戏版本列表失败 : %w", err)
		}
		for _, g := range games {
			out = append(out, g.Version)
		}
	case Vanilla, Spigot:
		var mf versionManifest
		if err := getJSON(ctx, client, manifestURL(mirror), DefaultRetries, "", log, &mf); err != nil {
			return nil, fmt.Errorf("获取 Minecraft 版本清单失败（可能是镜像不可用）: %w", err)
		}
		for _, v := range mf.Versions {
			if v.Type == "release" {
				out = append(out, v.ID)
			}
		}
		if len(out) == 0 {
			for _, v := range mf.Versions {
				out = append(out, v.ID)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s 未返回任何可用版本", kind)
	}
	versionsCache.put(key, append([]string(nil), out...))
	return out, nil
}

// ListBuilds 返回可用的构建号（新的在前），仅 Paper/Purpur 有意义。
func ListBuilds(ctx context.Context, kind Kind, mcVersion string) ([]string, error) {
	ctx = ctxOr(ctx)
	if err := validateKind(kind); err != nil {
		return nil, err
	}
	version := normalizeRequestedVersion(mcVersion)
	if version == "" || version == "latest" {
		v, err := LatestVersion(ctx, kind, "")
		if err != nil {
			return nil, err
		}
		version = v
	}
	key := string(kind) + "|" + version
	if v, ok := buildsCache.get(key); ok {
		return append([]string(nil), v...), nil
	}
	client := HTTPClient(DefaultTimeout, "")
	switch kind {
	case Paper:
		var builds []paperV3Build
		err := getJSON(ctx, client, paperV3BuildsURL(string(Paper), version, ""), DefaultRetries, "", logger.Discard(), &builds)
		if err != nil {
			// v2 回退。
			var bl paperV2BuildList
			if err2 := getJSON(ctx, client, paperV2JSONURL("projects", string(Paper), "versions", version, "builds"), DefaultRetries, "", logger.Discard(), &bl); err2 != nil {
				return nil, fmt.Errorf("获取 Paper 构建列表失败（v3: %v；v2: %w）", err, err2)
			}
			out := make([]string, 0, len(bl.Builds))
			for i := len(bl.Builds) - 1; i >= 0; i-- {
				out = append(out, itoa(int64(bl.Builds[i])))
			}
			buildsCache.put(key, append([]string(nil), out...))
			return out, nil
		}
		out := make([]string, 0, len(builds))
		for _, b := range builds {
			out = append(out, itoa(int64(b.ID)))
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("Paper %s 没有可用构建", version)
		}
		buildsCache.put(key, append([]string(nil), out...))
		return out, nil
	case Purpur:
		var v purpurVersion
		if err := getJSON(ctx, client, purpurJSONURL(version), DefaultRetries, "", logger.Discard(), &v); err != nil {
			return nil, fmt.Errorf("获取 Purpur %s 的构建列表失败 : %w", version, err)
		}
		out := make([]string, 0, len(v.Builds.All))
		for i := len(v.Builds.All) - 1; i >= 0; i-- {
			out = append(out, v.Builds.All[i])
		}
		if len(out) == 0 && v.Builds.Latest != "" {
			out = append(out, v.Builds.Latest)
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("Purpur %s 没有可用构建", version)
		}
		buildsCache.put(key, append([]string(nil), out...))
		return out, nil
	default:
		return nil, fmt.Errorf("%s 没有构建号概念（仅 paper 与 purpur 支持）", kind)
	}
}

// ListLoaders 返回 Fabric 在指定游戏版本下可用的 loader 版本（新的在前）。
func ListLoaders(ctx context.Context, gameVersion string) ([]string, error) {
	ctx = ctxOr(ctx)
	game := normalizeRequestedVersion(gameVersion)
	if game == "" || game == "latest" {
		v, err := LatestVersion(ctx, Fabric, "")
		if err != nil {
			return nil, err
		}
		game = v
	}
	key := "fabric-loader|" + game
	if v, ok := versionsCache.get(key); ok {
		return append([]string(nil), v...), nil
	}
	var list []fabricLoaderEntry
	client := HTTPClient(DefaultTimeout, "")
	if err := getJSON(ctx, client, fabricURL("versions", "loader", game), DefaultRetries, "", logger.Discard(), &list); err != nil {
		return nil, fmt.Errorf("获取 Fabric loader 列表失败 : %w", err)
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		out = append(out, e.Loader.Version)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("Fabric 没有 %s 可用的 loader", game)
	}
	versionsCache.put(key, append([]string(nil), out...))
	return out, nil
}

// ---------------------------------------------------------------- 版本号工具

// normalizeRequestedVersion 规整用户输入的版本号。
func normalizeRequestedVersion(v string) string {
	s := strings.TrimSpace(v)
	if strings.EqualFold(s, "latest") {
		return "latest"
	}
	return s
}

// isPreReleaseVersion 判断是否为预发布版本（快照 / pre / rc）。
func isPreReleaseVersion(v string) bool {
	s := strings.ToLower(v)
	for _, marker := range []string{"-pre", "-rc", "snapshot", "pre-", "-alpha", "-beta"} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	// 形如 25w46a 的快照。
	if len(s) >= 5 && strings.HasSuffix(s, "a") && s[0] >= '0' && s[0] <= '9' && strings.Contains(s, "w") {
		return true
	}
	return false
}

// compareMinecraftVersions 比较两个 Minecraft 版本号，a 更新返回正数。
func compareMinecraftVersions(a, b string) int {
	as := splitVersionNumbers(a)
	bs := splitVersionNumbers(b)
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		var av, bv int
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		if av != bv {
			if av > bv {
				return 1
			}
			return -1
		}
	}
	return strings.Compare(a, b)
}

// splitVersionNumbers 把版本号里的数字段抽出来。
func splitVersionNumbers(v string) []int {
	var out []int
	cur := -1
	for _, r := range v {
		if r >= '0' && r <= '9' {
			if cur < 0 {
				cur = 0
			}
			cur = cur*10 + int(r-'0')
			continue
		}
		if cur >= 0 {
			out = append(out, cur)
			cur = -1
		}
	}
	if cur >= 0 {
		out = append(out, cur)
	}
	return out
}

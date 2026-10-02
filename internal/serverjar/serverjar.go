// Package serverjar 负责把 Minecraft 服务端文件（Paper / Fabric / Vanilla /
// Spigot / Purpur）准备到实例目录里，并记录安装信息。
//
// 它把 internal/downloader 的解析下载能力与 internal/instance 的目录布局、
// 元数据串在一起，供 nyatmc start 的自动下载与 `nyatmc server` 命令复用。
package serverjar

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/yuzumikonami/nyatmc/internal/downloader"
	"github.com/yuzumikonami/nyatmc/internal/instance"
	"github.com/yuzumikonami/nyatmc/pkg/logger"
)

// Options 控制一次“确保服务端文件就位”的行为。
type Options struct {
	// Force 为 true 时无条件重新下载（即使本地已有）。
	Force bool
	// Update 为 true 时，即使配置写的是 latest 也会去查一次最新版。
	Update bool
	// Kind / MinecraftVersion / Build 为空表示沿用实例配置。
	Kind             string
	MinecraftVersion string
	Build            string
	LoaderVersion    string
	InstallerVersion string
	// Mirror / Verify / Timeout / Retries 为空表示沿用 downloader 配置。
	Mirror    string
	Verify    *bool
	Timeout   time.Duration
	Retries   int
	UserAgent string
	Progress  downloader.ProgressFunc
	Logger    *logger.Logger
}

// Request 依据实例配置构造下载请求。
func Request(inst *instance.Instance, opts Options) downloader.Request {
	dl := inst.Res.Downloader
	srv := inst.Res.Server

	kind := downloader.Paper
	switch strings.ToLower(strings.TrimSpace(firstNonEmpty(opts.Kind, srv.Type))) {
	case "fabric":
		kind = downloader.Fabric
	case "vanilla", "mojang":
		kind = downloader.Vanilla
	case "spigot":
		kind = downloader.Spigot
	case "purpur":
		kind = downloader.Purpur
	default:
		kind = downloader.Paper
	}

	verify := dl.VerifyChecksum
	if opts.Verify != nil {
		verify = *opts.Verify
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = dl.Timeout.Std()
	}
	retries := opts.Retries
	if retries == 0 {
		retries = dl.Retries
	}
	userAgent := firstNonEmpty(opts.UserAgent, dl.UserAgent)

	return downloader.Request{
		Kind:             kind,
		MinecraftVersion: firstNonEmpty(opts.MinecraftVersion, srv.MinecraftVersion),
		Build:            firstNonEmpty(opts.Build, srv.Build),
		LoaderVersion:    firstNonEmpty(opts.LoaderVersion, srv.LoaderVersion),
		InstallerVersion: firstNonEmpty(opts.InstallerVersion, srv.InstallerVersion),
		Dir:              inst.Res.Path,
		FileName:         srv.JarName,
		Mirror:           firstNonEmpty(opts.Mirror, dl.Mirror),
		Verify:           verify,
		Timeout:          timeout,
		Retries:          retries,
		UserAgent:        userAgent,
		JavaPath:         srv.JavaPath,
		Logger:           opts.Logger,
	}
}

// Ensure 确保服务端文件就位。
//
// 返回解析到的产物、是否本次真的下载了、以及错误。
// 本地已有合适文件时不会重复下载（除非 Options.Force / Update）。
func Ensure(ctx context.Context, inst *instance.Instance, opts Options) (downloader.Artifact, bool, error) {
	log := opts.Logger
	if log == nil {
		log = logger.Discard()
	}
	if err := inst.EnsureDirs(); err != nil {
		return downloader.Artifact{}, false, err
	}

	req := Request(inst, opts)
	meta, _ := inst.LoadMetadata()

	// 已经是想要的文件就不折腾：只在“强制/更新/本地缺失/版本与配置不符”时下载
	if !opts.Force && inst.JarExists() {
		configured := strings.TrimSpace(firstNonEmpty(opts.MinecraftVersion, inst.Res.Server.MinecraftVersion))
		installed := strings.TrimSpace(meta.MinecraftVersion)
		sameKind := strings.EqualFold(string(meta.Type), string(req.Kind)) || meta.Type == ""
		versionOK := configured == "" || configured == "latest" || installed == "" || configured == installed
		if sameKind && versionOK {
			if !opts.Update {
				log.Debugf("服务端文件已存在，跳过下载：%s", inst.Res.JarPath)
				return downloader.Artifact{}, false, nil
			}
			// Update：先解析一下，只有在确实有新版本时才继续
			art, err := downloader.Resolve(ctx, req)
			if err != nil {
				return downloader.Artifact{}, false, err
			}
			if art.Version == installed && (art.Build == "" || art.Build == meta.Build) {
				log.Infof("已是最新版本 %s，无需更新", installed)
				return art, false, nil
			}
		}
	}

	log.Infof("准备服务端：类型=%s 版本=%s%s", req.Kind, orLatest(req.MinecraftVersion), buildSuffix(req.Build))
	var (
		art downloader.Artifact
		jar string
		err error
	)
	if req.Kind == downloader.Spigot {
		log.Warnf("Spigot 官方没有直链，需要用 BuildTools 现场编译（耗时较久，且需要本机有 JDK）")
		art, jar, err = downloader.BuildSpigot(ctx, req, opts.Progress)
	} else {
		art, jar, err = downloader.Install(ctx, req, opts.Progress)
	}
	if err != nil {
		return art, false, err
	}

	// 记录安装信息，便于 server info 与后续判断“是否需要更新”
	meta.Name = inst.Name
	meta.Type = string(art.Kind)
	if art.Version != "" {
		meta.MinecraftVersion = art.Version
	}
	meta.Build = art.Build
	meta.LoaderVersion = art.LoaderVersion
	meta.InstallerVersion = art.InstallerVersion
	meta.ServerJar = jar
	meta.JarURL = art.URL
	meta.JarSHA256 = art.SHA256
	meta.JarInstalled = time.Now()
	if st, serr := os.Stat(jar); serr == nil {
		meta.JarSize = st.Size()
	}
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = time.Now()
	}
	if err := inst.SaveMetadata(meta); err != nil {
		log.Warnf("写入实例元数据失败（不影响运行）：%v", err)
	}

	// 顺手把配置里的版本同步回实例覆盖，让 nyatmc info 展示的版本是真实版本
	if art.Version != "" && strings.TrimSpace(inst.Res.Server.MinecraftVersion) == "latest" {
		if ic, ok := inst.Cfg.Instances[inst.Name]; ok {
			ic.MinecraftVersion = art.Version
			inst.Cfg.Instances[inst.Name] = ic
			if err := inst.Cfg.Save(); err != nil {
				log.Debugf("保存解析后的版本失败：%v", err)
			}
		}
	}

	log.Infof("服务端已就位：%s（%s）", jar, art.Version)
	return art, true, nil
}

// EnsureFunc 适配 daemon 的自动下载回调签名。
func EnsureFunc(ctx context.Context, inst *instance.Instance, force bool, log *logger.Logger) error {
	_, _, err := Ensure(ctx, inst, Options{Force: force, Logger: log})
	return err
}

// InstallInfo 描述本地已安装的服务端文件。
type InstallInfo struct {
	Path             string    `json:"path"`
	Exists           bool      `json:"exists"`
	Size             int64     `json:"size"`
	Type             string    `json:"type"`
	Version          string    `json:"version"`
	Build            string    `json:"build,omitempty"`
	LoaderVersion    string    `json:"loader_version,omitempty"`
	InstallerVersion string    `json:"installer_version,omitempty"`
	URL              string    `json:"url,omitempty"`
	SHA256           string    `json:"sha256,omitempty"`
	InstalledAt      time.Time `json:"installed_at,omitempty"`
	NyatmcVersion    string    `json:"nyatmc_version,omitempty"`
}

// Installed 返回本地安装信息。
func Installed(inst *instance.Instance) InstallInfo {
	meta, _ := inst.LoadMetadata()
	info := InstallInfo{
		Path:             inst.Res.JarPath,
		Type:             firstNonEmpty(meta.Type, inst.Res.Server.Type),
		Version:          firstNonEmpty(meta.MinecraftVersion, inst.Res.Server.MinecraftVersion),
		Build:            meta.Build,
		LoaderVersion:    meta.LoaderVersion,
		InstallerVersion: meta.InstallerVersion,
		URL:              meta.JarURL,
		SHA256:           meta.JarSHA256,
		InstalledAt:      meta.JarInstalled,
		NyatmcVersion:    meta.NyatmcVersion,
	}
	if st, err := os.Stat(inst.Res.JarPath); err == nil {
		info.Exists = true
		info.Size = st.Size()
	}
	return info
}

// Check 只解析目标产物，不下载（用于 `nyatmc server check`）。
func Check(ctx context.Context, inst *instance.Instance, opts Options) (downloader.Artifact, error) {
	return downloader.Resolve(ctx, Request(inst, opts))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func orLatest(v string) string {
	if strings.TrimSpace(v) == "" {
		return "latest"
	}
	return v
}

func buildSuffix(build string) string {
	if strings.TrimSpace(build) == "" {
		return ""
	}
	return " 构建=" + build
}

// Available 校验类型是否受支持。
func Available(t string) error {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "paper", "fabric", "vanilla", "spigot", "purpur":
		return nil
	default:
		return fmt.Errorf("不支持的服务端类型 %q（可选：paper/fabric/vanilla/spigot/purpur）", t)
	}
}

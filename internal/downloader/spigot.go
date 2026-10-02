package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// spigotSource 统一 BuildTools 失败时的中文提示。
const spigotHint = "使用官方 BuildTools 构建 Spigot 需要本机安装 JDK（java 可执行文件）、需要联网拉取 Maven 依赖，" +
	"通常耗时 5-20 分钟；若失败请检查网络与 JDK 版本，或直接改用 paper（无需本机构建，下载即用）"

// BuildSpigot 用官方 BuildTools 构建 Spigot（需要本机 java），返回产物与 jar 路径。
func BuildSpigot(ctx context.Context, req Request, progress ProgressFunc) (Artifact, string, error) {
	ctx = ctxOr(ctx)
	log := reqLogger(req)
	javaPath := strings.TrimSpace(req.JavaPath)
	if javaPath == "" {
		javaPath = "java"
	}
	version := normalizeRequestedVersion(req.MinecraftVersion)
	if version == "latest" {
		version = ""
	}
	if version != "" {
		if err := validateVersionToken(version); err != nil {
			return Artifact{}, "", err
		}
	}

	workDir, err := os.MkdirTemp("", "nyatmc-buildtools-*")
	if err != nil {
		return Artifact{}, "", fmt.Errorf("创建 BuildTools 临时目录失败 : %w", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	if progress != nil {
		progress(Progress{Phase: "下载 BuildTools"})
	}
	btJar := filepath.Join(workDir, "BuildTools.jar")
	log.Infof("下载官方 BuildTools 到 %s", btJar)
	if _, err := DownloadTo(ctx, buildToolsURL, btJar, DownloadOptions{
		Timeout:   req.Timeout,
		Retries:   req.Retries,
		UserAgent: req.UserAgent,
	}, nil); err != nil {
		return Artifact{}, "", fmt.Errorf("下载 BuildTools 失败（%s）: %w", spigotHint, err)
	}

	// 构建产物会落在工作目录里。
	args := []string{"-jar", btJar, "--compile", "SPIGOT", "--output-dir", workDir, "--quiet"}
	if version != "" {
		args = append(args, "--rev", version)
		log.Infof("开始用 BuildTools 构建 Spigot %s（需要 JDK，耗时较长，建议直接用 paper）", version)
	} else {
		log.Infof("开始用 BuildTools 构建 Spigot 最新版（需要 JDK，耗时较长，建议直接用 paper）")
	}
	if progress != nil {
		progress(Progress{Phase: "构建中"})
	}

	cmd := exec.CommandContext(ctx, javaPath, args...)
	cmd.Dir = workDir
	cmd.Stdout = log.Writer()
	cmd.Stderr = log.Writer()
	runErr := cmd.Run()
	if runErr != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return Artifact{}, "", fmt.Errorf("Spigot 构建已取消（%s）: %w", spigotHint, ctx.Err())
		}
		if errors.Is(runErr, exec.ErrNotFound) {
			return Artifact{}, "", fmt.Errorf("找不到 java 可执行文件 %q，请安装 JDK 或通过 java_path 指定（%s）", javaPath, spigotHint)
		}
		return Artifact{}, "", fmt.Errorf("BuildTools 执行失败（%s）: %w", spigotHint, runErr)
	}

	built, err := findSpigotJar(workDir)
	if err != nil {
		return Artifact{}, "", err
	}

	dest, err := resolveDest(req.Dir, orDefault(req.FileName, filepath.Base(built)))
	if err != nil {
		return Artifact{}, "", err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return Artifact{}, "", fmt.Errorf("创建目标目录 %s 失败 : %w", filepath.Dir(dest), err)
	}
	if err := copyFile(built, dest); err != nil {
		return Artifact{}, "", err
	}
	sha256hex, sha1hex, err := HashFile(dest)
	if err != nil {
		return Artifact{}, "", err
	}
	st, err := os.Stat(dest)
	if err != nil {
		return Artifact{}, "", fmt.Errorf("检查构建产物 %s 失败 : %w", dest, err)
	}
	artifact := Artifact{
		Kind:     Spigot,
		URL:      buildToolsURL,
		FileName: filepath.Base(dest),
		Version:  orDefault(version, "latest"),
		SHA256:   sha256hex,
		SHA1:     sha1hex,
		Size:     st.Size(),
		Extra: map[string]string{
			"source":     "buildtools",
			"local_hash": "本地构建产物哈希由本机计算",
		},
	}
	if version != "" {
		artifact.Build = version
	}
	if progress != nil {
		progress(Progress{Phase: "完成", Done: st.Size(), Total: st.Size(), Percent: 100})
	}
	log.Infof("Spigot 构建完成：%s", dest)
	return artifact, dest, nil
}

// findSpigotJar 在构建目录里找出 spigot-*.jar。
func findSpigotJar(dir string) (string, error) {
	var found []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		name := strings.ToLower(d.Name())
		if strings.HasPrefix(name, "spigot-") && strings.HasSuffix(name, ".jar") {
			found = append(found, p)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("扫描构建产物失败 : %w", err)
	}
	if len(found) == 0 {
		return "", fmt.Errorf("BuildTools 已退出但未找到 spigot-*.jar 产物（%s）", spigotHint)
	}
	// 取修改时间最新、体积最大的一个（BuildTools 会在多个子目录留下中间产物）。
	sort.Slice(found, func(i, j int) bool {
		si, ei := os.Stat(found[i])
		sj, ej := os.Stat(found[j])
		if ei != nil || ej != nil {
			return len(found[i]) > len(found[j])
		}
		return si.ModTime().After(sj.ModTime())
	})
	return found[0], nil
}

// validateVersionToken 校验版本号只含安全字符，避免作为参数注入。
func validateVersionToken(v string) error {
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r == '.' || r == '-' || r == '_' || r == '+':
		default:
			return fmt.Errorf("版本号 %q 含非法字符 %q，已拒绝", v, r)
		}
	}
	return nil
}

// copyFile 复制文件（保留 0644 权限）。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("打开 %s 失败 : %w", src, err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("创建 %s 失败 : %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("复制 %s 到 %s 失败 : %w", src, dst, err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return fmt.Errorf("刷写 %s 失败 : %w", dst, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("关闭 %s 失败 : %w", dst, err)
	}
	return nil
}

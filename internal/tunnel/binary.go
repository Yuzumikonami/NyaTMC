package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/yuzumikonami/nyatmc/internal/config"
	"github.com/yuzumikonami/nyatmc/pkg/logger"
)

// downloadBase 是 cloudflared 官方发行版地址（latest 会自动跳转到最新版）。
const downloadBase = "https://github.com/cloudflare/cloudflared/releases/latest/download"

// downloadTimeout 是单个二进制下载的整体超时。
const downloadTimeout = 10 * time.Minute

// DefaultBinDir 返回自动下载的 cloudflared 的存放目录（~/.nyatmc/bin）。
func DefaultBinDir() string { return filepath.Join(config.Home(), "bin") }

// localBinaryName 返回落盘时使用的文件名。
func localBinaryName() string {
	if runtime.GOOS == "windows" {
		return "cloudflared.exe"
	}
	return "cloudflared"
}

// BinaryName 返回当前平台对应的官方发行版文件名。
func BinaryName() (string, error) { return platformBinary(runtime.GOOS, runtime.GOARCH) }

// platformBinary 把 GOOS / GOARCH 映射到官方发行版文件名。
func platformBinary(goos, goarch string) (string, error) {
	switch goos + "/" + goarch {
	case "linux/amd64":
		return "cloudflared-linux-amd64", nil
	case "linux/arm64":
		return "cloudflared-linux-arm64", nil
	case "linux/arm":
		return "cloudflared-linux-arm", nil
	case "windows/amd64":
		return "cloudflared-windows-amd64.exe", nil
	}
	return "", fmt.Errorf("暂不支持在 %s/%s 上自动下载 cloudflared：请手动安装后设置 tunnel.bin 或用 --bin 指定路径",
		goos, goarch)
}

// DownloadURL 返回指定平台的官方下载地址。
func DownloadURL(goos, goarch string) (string, error) {
	name, err := platformBinary(goos, goarch)
	if err != nil {
		return "", err
	}
	return downloadBase + "/" + name, nil
}

// FindBinary 依次在 extra 路径、PATH、~/.nyatmc/bin 中查找 cloudflared。
func FindBinary(extra ...string) (string, error) {
	var tried []string
	for _, p := range extra {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if abs, err := exec.LookPath(config.ExpandPath(p)); err == nil {
			return abs, nil
		}
		tried = append(tried, p)
	}
	if abs, err := exec.LookPath("cloudflared"); err == nil {
		return abs, nil
	}
	tried = append(tried, "PATH")
	local := filepath.Join(DefaultBinDir(), localBinaryName())
	if fileExists(local) {
		return local, nil
	}
	tried = append(tried, local)
	return "", fmt.Errorf("找不到 cloudflared（已查找：%s）；运行 nyatmc tunnel install 自动下载，或用 --bin 指定路径",
		strings.Join(tried, "、"))
}

// EnsureBinary 确保 dir 下存在可用的 cloudflared，缺失时从 GitHub Releases 下载。
//
// dir 留空表示 ~/.nyatmc/bin。下载后会校验文件非空、可执行（cloudflared --version），
// 校验失败会删掉文件并返回中文错误。
func EnsureBinary(ctx context.Context, dir string, log *logger.Logger) (string, error) {
	if log == nil {
		log = logger.Default()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(dir) == "" {
		dir = DefaultBinDir()
	}
	dir = config.ExpandPath(dir)

	target := filepath.Join(dir, localBinaryName())
	if fileExists(target) {
		if v, err := Version(ctx, target); err == nil {
			log.Infof("已存在可用的 cloudflared：%s（%s）", target, v)
			return target, nil
		}
		log.Warnf("已存在的 cloudflared 无法执行，准备重新下载：%s", target)
		_ = os.Remove(target)
	}

	url, err := DownloadURL(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建目录 %s 失败: %w", dir, err)
	}

	log.Infof("正在下载 cloudflared：%s", url)
	tmp := target + ".download"
	if err := downloadFile(ctx, url, tmp); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	st, err := os.Stat(tmp)
	if err != nil || st.Size() == 0 {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("下载的 cloudflared 为空，请检查网络后重试：%s", url)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(tmp, 0o755); err != nil {
			_ = os.Remove(tmp)
			return "", fmt.Errorf("设置 cloudflared 可执行权限失败: %w", err)
		}
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("保存 cloudflared 到 %s 失败: %w", target, err)
	}

	ver, err := Version(ctx, target)
	if err != nil {
		_ = os.Remove(target)
		return "", fmt.Errorf("下载的 cloudflared 无法执行（%v），已删除该文件；请检查网络或手动安装后设置 tunnel.bin", err)
	}
	log.Infof("cloudflared 已就绪：%s（%s）", target, ver)
	return target, nil
}

// Version 运行 `cloudflared --version` 并返回版本信息（同时用于校验二进制可用）。
func Version(ctx context.Context, bin string) (string, error) {
	if strings.TrimSpace(bin) == "" {
		return "", errors.New("cloudflared 路径为空")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	out, err := exec.CommandContext(cctx, bin, "--version").CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text != "" {
			return "", fmt.Errorf("%w（输出：%s）", err, firstLine(text))
		}
		return "", err
	}
	if text == "" {
		return "", errors.New("cloudflared --version 没有输出")
	}
	return firstLine(text), nil
}

// downloadFile 下载 url 到 path（先写临时文件）。仅使用标准库。
func downloadFile(ctx context.Context, url, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("构造下载请求失败: %w", err)
	}
	req.Header.Set("User-Agent", "nyatmc/"+config.Version+" (+https://github.com/yuzumikonami/nyatmc)")

	client := &http.Client{Timeout: downloadTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("下载 cloudflared 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 cloudflared 失败：HTTP %d（%s）", resp.StatusCode, url)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("创建文件 %s 失败: %w", path, err)
	}
	defer f.Close()

	n, err := io.Copy(f, resp.Body)
	if err != nil {
		return fmt.Errorf("写入 %s 失败（已下载 %d 字节）: %w", path, n, err)
	}
	if n == 0 {
		return fmt.Errorf("下载内容为空：%s", url)
	}
	return nil
}

// fileExists 判断路径是否存在且是非空普通文件。
func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular() && st.Size() > 0
}

// firstLine 返回文本的第一行。
func firstLine(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		return strings.TrimSpace(text[:i])
	}
	return strings.TrimSpace(text)
}

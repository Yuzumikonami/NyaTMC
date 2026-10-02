package tunnel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlatformBinaryMapping(t *testing.T) {
	cases := []struct {
		goos   string
		goarch string
		want   string
	}{
		{"linux", "amd64", "cloudflared-linux-amd64"},
		{"linux", "arm64", "cloudflared-linux-arm64"},
		{"linux", "arm", "cloudflared-linux-arm"},
		{"windows", "amd64", "cloudflared-windows-amd64.exe"},
	}
	for _, c := range cases {
		got, err := platformBinary(c.goos, c.goarch)
		if err != nil {
			t.Fatalf("platformBinary(%s, %s) 意外失败：%v", c.goos, c.goarch, err)
		}
		if got != c.want {
			t.Fatalf("platformBinary(%s, %s) = %q，期望 %q", c.goos, c.goarch, got, c.want)
		}
		wantURL := "https://github.com/cloudflare/cloudflared/releases/latest/download/" + c.want
		url, err := DownloadURL(c.goos, c.goarch)
		if err != nil {
			t.Fatalf("DownloadURL(%s, %s) 意外失败：%v", c.goos, c.goarch, err)
		}
		if url != wantURL {
			t.Fatalf("DownloadURL(%s, %s) = %q，期望 %q", c.goos, c.goarch, url, wantURL)
		}
	}
}

func TestPlatformBinaryUnsupported(t *testing.T) {
	for _, c := range [][2]string{{"darwin", "amd64"}, {"windows", "arm64"}, {"freebsd", "amd64"}} {
		_, err := platformBinary(c[0], c[1])
		if err == nil {
			t.Fatalf("platformBinary(%s, %s) 应返回错误", c[0], c[1])
		}
		if !strings.Contains(err.Error(), "暂不支持") || !strings.Contains(err.Error(), "tunnel.bin") {
			t.Fatalf("错误信息应为中文提示，实际 %q", err.Error())
		}
		if _, err := DownloadURL(c[0], c[1]); err == nil {
			t.Fatalf("DownloadURL(%s, %s) 应返回错误", c[0], c[1])
		}
	}
}

func TestBinaryNameForCurrentPlatform(t *testing.T) {
	name, err := BinaryName()
	if err != nil {
		// 当前平台不在支持列表（例如 darwin）时跳过
		t.Skipf("当前平台不支持自动下载：%v", err)
	}
	if !strings.HasPrefix(name, "cloudflared-") {
		t.Fatalf("二进制名应以 cloudflared- 开头，实际 %q", name)
	}
	if strings.Contains(name, "/") || strings.Contains(name, `\`) {
		t.Fatalf("二进制名不应包含路径分隔符，实际 %q", name)
	}
}

func TestLocalBinaryName(t *testing.T) {
	name := localBinaryName()
	if name != "cloudflared" && name != "cloudflared.exe" {
		t.Fatalf("落盘文件名异常：%q", name)
	}
}

func TestFirstLine(t *testing.T) {
	if got := firstLine("cloudflared version 2024.4.1 (built 2024-04-01)\nmore"); got != "cloudflared version 2024.4.1 (built 2024-04-01)" {
		t.Fatalf("firstLine 结果异常：%q", got)
	}
	if got := firstLine("  \n  "); got != "" {
		t.Fatalf("空文本应返回空串，实际 %q", got)
	}
}

func TestFileExists(t *testing.T) {
	home := useTempHome(t)
	if fileExists(home) {
		t.Fatal("目录不应算作可执行文件")
	}
	empty := filepath.Join(home, "empty")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if fileExists(empty) {
		t.Fatal("空文件不应算作可执行文件")
	}
	full := filepath.Join(home, "full")
	if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !fileExists(full) {
		t.Fatal("非空文件应算作已存在")
	}
}

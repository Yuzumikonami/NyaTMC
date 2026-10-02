package downloader

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

// HashFile 计算文件的 sha256 与 sha1（十六进制小写）。
func HashFile(path string) (sha256hex string, sha1hex string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("打开文件 %s 失败 : %w", path, err)
	}
	defer func() { _ = f.Close() }()

	h256 := sha256.New()
	h1 := sha1.New()
	mw := io.MultiWriter(h256, h1)
	if _, err := io.Copy(mw, f); err != nil {
		return "", "", fmt.Errorf("读取文件 %s 失败 : %w", path, err)
	}
	return hex.EncodeToString(h256.Sum(nil)), hex.EncodeToString(h1.Sum(nil)), nil
}

// VerifyFile 校验文件哈希；两个期望值都为空时返回 nil。
func VerifyFile(path, wantSHA256, wantSHA1 string) error {
	w256 := strings.ToLower(strings.TrimSpace(wantSHA256))
	w1 := strings.ToLower(strings.TrimSpace(wantSHA1))
	if w256 == "" && w1 == "" {
		return nil
	}
	got256, got1, err := HashFile(path)
	if err != nil {
		return err
	}
	if w256 != "" && got256 != w256 {
		return fmt.Errorf("文件 %s 的 SHA-256 校验失败：期望 %s，实际 %s", path, w256, got256)
	}
	if w1 != "" && got1 != w1 {
		return fmt.Errorf("文件 %s 的 SHA-1 校验失败：期望 %s，实际 %s", path, w1, got1)
	}
	return nil
}

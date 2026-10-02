package assets

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/yuzumikonami/nyatmc/internal/config"
)

// TestConfigTemplateIsNotEmpty 确认 go:embed 真的把模板打进了二进制。
func TestConfigTemplateIsNotEmpty(t *testing.T) {
	raw := ConfigTemplate()
	if len(raw) == 0 {
		t.Fatal("ConfigTemplate() 为空：检查 assets.go 里的 go:embed 指令与 embed/config.toml")
	}
	if !bytes.Contains(raw, []byte("[daemon]")) {
		t.Errorf("模板内容看起来不对，缺少 [daemon] 段：\n%s", head(raw, 200))
	}
}

// TestConfigTemplateParses 确认模板能被 config.Parse 解析并通过校验。
func TestConfigTemplateParses(t *testing.T) {
	cfg, err := config.Parse(ConfigTemplate())
	if err != nil {
		t.Fatalf("config.Parse(ConfigTemplate()) 失败：%v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("模板配置未通过 config.Validate()：%v", err)
	}
	// 抽查几个模板里写明的值，防止模板被改坏。
	if cfg.Server.Type != "paper" {
		t.Errorf("server.type = %q，模板里应为 paper", cfg.Server.Type)
	}
	if cfg.Server.Port != 25565 {
		t.Errorf("server.port = %d，模板里应为 25565", cfg.Server.Port)
	}
	if cfg.Daemon.WatchdogEnabled() != true {
		t.Error("daemon.watchdog 缺省应为 true")
	}
	if cfg.Server.EULAEnabled() {
		t.Error("server.eula 缺省应为 false（需要用户显式同意）")
	}
}

// TestConfigTemplateMatchesDefaultTOML 确认 embed/config.toml 与
// internal/config.DefaultTOML 逐字一致（模板的唯一真相在 defaults.go）。
func TestConfigTemplateMatchesDefaultTOML(t *testing.T) {
	got := ConfigTemplate()
	want := []byte(config.DefaultTOML)
	if bytes.Equal(got, want) {
		return
	}
	t.Fatalf("embed/config.toml 与 internal/config.DefaultTOML 不一致：%s", firstDifference(got, want))
}

// TestReadmeText 确认 embed/README.md 可用。
func TestReadmeText(t *testing.T) {
	txt := ReadmeText()
	if strings.TrimSpace(txt) == "" {
		t.Fatal("ReadmeText() 为空：检查 assets.go 里的 go:embed 指令与 embed/README.md")
	}
	if !strings.Contains(txt, "go:embed") {
		t.Error("embed/README.md 里应当说明资源是通过 go:embed 打包的")
	}
}

// TestFSServesBothFiles 确认 FS 里确实有两个约定的文件。
func TestFSServesBothFiles(t *testing.T) {
	for _, name := range []string{ConfigTemplatePath, ReadmePath} {
		data, err := FS.ReadFile(name)
		if err != nil {
			t.Errorf("FS.ReadFile(%q) 失败：%v", name, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("FS 中的 %q 是空文件", name)
		}
	}
}

// TestConfigTemplateReturnsCopy 确认调用方改不动内部缓存。
func TestConfigTemplateReturnsCopy(t *testing.T) {
	first := ConfigTemplate()
	if len(first) == 0 {
		t.Fatal("ConfigTemplate() 为空")
	}
	first[0] = 'X'
	if second := ConfigTemplate(); second[0] == 'X' {
		t.Error("ConfigTemplate() 返回的应当是副本，修改后不应影响下一次调用")
	}
}

// firstDifference 定位两份文本的首个差异，便于模板与常量不同步时排查。
func firstDifference(got, want []byte) string {
	n := len(got)
	if len(want) < n {
		n = len(want)
	}
	for i := 0; i < n; i++ {
		if got[i] != want[i] {
			line := bytes.Count(got[:i], []byte{'\n'}) + 1
			return fmt.Sprintf("第 %d 行第 %d 字节起不同：\n  embed/config.toml -> %q\n  DefaultTOML       -> %q",
				line, i, context(got, i), context(want, i))
		}
	}
	return fmt.Sprintf("前缀相同但长度不同：embed/config.toml = %d 字节，DefaultTOML = %d 字节", len(got), len(want))
}

// context 返回 pos 附近的一小段文本，便于肉眼比对。
func context(data []byte, pos int) string {
	start := pos - 20
	if start < 0 {
		start = 0
	}
	end := pos + 40
	if end > len(data) {
		end = len(data)
	}
	return string(data[start:end])
}

func head(data []byte, n int) string {
	if len(data) > n {
		return string(data[:n]) + "…"
	}
	return string(data)
}

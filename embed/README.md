# embed —— 随二进制分发的模板资源

此目录下的资源通过 Go 的 `//go:embed` 直接打包进 `nyatmc` 二进制，运行时不需要
任何外部文件（这正是「单个二进制、零运行时依赖」的一部分）。

| 文件 | 用途 |
| --- | --- |
| `config.toml` | 首次运行时写出的配置模板（`nyatmc init` / 自动创建 `~/.nyatmc/config.toml`） |
| `README.md` | 本说明文件 |

## 保持同步

`config.toml` 必须与 `internal/config/defaults.go` 里的 `DefaultTOML` 常量**逐字一致**：

* `internal/config/defaults.go` 是唯一真相，写配置模板时改那边；
* `embed/assets_test.go` 里有一条断言，直接比较 `ConfigTemplate()` 与 `config.DefaultTOML`，
  不一致时测试会失败并给出首个差异位置。

> 注意：包名是 `assets`（目录名是 `embed`），这样才不会和标准库的 `embed` 包混淆。

```go
import assets "github.com/yuzumikonami/nyatmc/embed"

raw := assets.ConfigTemplate() // 配置模板（[]byte）
txt := assets.ReadmeText()     // 本文件内容（string）
```

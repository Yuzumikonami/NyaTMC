// Package assets 通过 go:embed 把随二进制分发的模板资源打包进来。
//
// 目录名是 embed，包名故意取 assets：如果包名也叫 embed，就会与标准库的
// embed 包（go:embed 指令需要的那个）同名，import 时不得不写别名，容易出错。
//
// 资源在编译期就被写进二进制，运行时不需要任何外部文件，这与 nyatmc
// 「单个二进制、零运行时依赖」的目标一致。
package assets

import (
	"embed"
	"sync"
)

// FS 是打包进二进制的资源集合。
//
//go:embed config.toml README.md
var FS embed.FS

const (
	// ConfigTemplatePath 是配置模板在 FS 中的路径。
	ConfigTemplatePath = "config.toml"
	// ReadmePath 是本目录说明文件在 FS 中的路径。
	ReadmePath = "README.md"
)

var (
	configOnce sync.Once
	configRaw  []byte

	readmeOnce sync.Once
	readmeVal  string
)

// ConfigTemplate 返回配置文件模板的内容。
//
// 模板与 internal/config 里的 DefaultTOML 常量逐字一致（embed/assets_test.go
// 会断言这一点），也就是 `nyatmc init` 写出的 ~/.nyatmc/config.toml 的原样。
// 返回的是一份副本，调用方可以随意修改。
func ConfigTemplate() []byte {
	configOnce.Do(func() {
		data, err := FS.ReadFile(ConfigTemplatePath)
		if err != nil {
			// embed 的内容在编译期就已确定，走到这里只可能是 go:embed 指令
			// 漏了文件，属于构建配置问题；返回 nil 让调用方与测试立刻发现。
			return
		}
		configRaw = data
	})
	if configRaw == nil {
		return nil
	}
	out := make([]byte, len(configRaw))
	copy(out, configRaw)
	return out
}

// ReadmeText 返回本目录说明文件的内容。
func ReadmeText() string {
	readmeOnce.Do(func() {
		data, err := FS.ReadFile(ReadmePath)
		if err != nil {
			return
		}
		readmeVal = string(data)
	})
	return readmeVal
}

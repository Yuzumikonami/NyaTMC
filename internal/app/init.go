package app

import (
	"errors"
	"os"

	"github.com/yuzumikonami/nyatmc/assets"
)

// init.go
// 负责读取配置，检查更新等运行前初始化，由 main.go 调用 | Called by main.go to check for updates and read the TOML config.

func checkUpdates() {
	// TODO
}

func CheckConfig(path string, file string) ([]byte, error) {
	_, err := os.Stat(file) // 检查是否存在file | Check for file
	if err == nil {
		config, _ := os.ReadFile(file) // 存在就直接读 | If file is available, read it
		if string(config) != string(assets.DefaultConfig) {
			// TODO 更新配置文件
		}
		return config, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0755); err != nil { // 不存在创建 | If file is unavailable, creat it
			return nil, err
		}
		if err := os.WriteFile(file, assets.DefaultConfig, 0644); err != nil {
			return nil, err
		}
		return nil, nil
	}
	return nil, err
}

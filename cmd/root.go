package cmd

// root.go
// cobra初始化 | Init cobra

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/yuzumikonami/nyatmc/internal/version"
)

var rootCmd = &cobra.Command{
	Use:           "Nyatmc",
	Short:         "一款用Go开发的的Minecraft服务器管理工具喵/A tool for managing Minecraft servers.",
	SilenceErrors: true,
	SilenceUsage:  true,
}

func Execute() error {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(os.Stderr, "运行出错喵！/Crashed：", err)
		os.Exit(1)
	}
	return nil
}

func init() {
	rootCmd.Version = fmt.Sprintf("版本/Version: %v\n构建时间/Build Time: %v\n自动更新支持/Auto Update: %v", version.Version, version.Date, version.Autoupdate)
}

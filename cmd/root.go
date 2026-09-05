package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "NyaTMC",
	Short: "NyaTMC - Minecraft 服务器全能管理工具",
	Long:  `NyaTMC 是一个用 Go 编写的 Minecraft 服务器管理工具，支持 CLI、TUI 和 Web 三种模式。`,
	// Run: 这里暂时不写默认行为，只显示帮助
}

// Execute main.go 调用
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "执行命令出错: %v\n", err)
		os.Exit(1)
	}
}

func init() {
	// 这里先空着
}

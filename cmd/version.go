package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	// 把 version 子命令挂到根命令下
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "显示 nyatmc 版本信息",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("nyatmc 版本: v0.1.0 (开发版)")
	},
}

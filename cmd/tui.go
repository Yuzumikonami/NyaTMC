package cmd

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/yuzumikonami/nyatmc/internal/app"
)

// tui.go
// 通过命令调用tui包 | Call tui

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "使用Tui界面/Use Tui",
	RunE: func(cmd *cobra.Command, args []string) error {
		home, err1 := os.UserHomeDir()
		if err1 != nil {
			return err1
		}
		configDir := filepath.Join(home, ".nyatmc", "config")
		configFile := filepath.Join(configDir, "config.toml")
		_, err := app.CheckConfig(configDir, configFile)
		// TODO tui界面
		return err
	},
}

func init() {
	rootCmd.AddCommand(tuiCmd)
}

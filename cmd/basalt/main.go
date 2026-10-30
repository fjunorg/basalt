// basalt 是框架的统一 CLI。
//
// 子命令：serve（启动服务）、version、new module（生成模块脚手架）。
// 后续：migrate（升级迁移）、secure --check（等保基线自查）、seed（数据工厂）。
package main

import (
	"os"

	"github.com/spf13/cobra"
)

var version = "1.1.0" // 由 -ldflags "-X main.version=..." 注入正式版本

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "basalt",
		Short:         "Basalt - open-source, AI-native backend platform framework (ToB/ToG)",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(newServeCmd(), newVersionCmd(), newNewCmd())
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the basalt version",
		Run: func(cmd *cobra.Command, _ []string) {
			cmd.Println("basalt", version)
		},
	}
}

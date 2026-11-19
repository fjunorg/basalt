// basalt 是框架的统一 CLI。
//
// 子命令：serve（启动服务）、version、new module（生成模块脚手架）、
// secure --check（等保基线自查）。
// 后续：migrate（升级迁移）、seed（数据工厂）。
package main

import (
	"os"

	"github.com/spf13/cobra"
)

var version = "1.7.0"  // 由 -ldflags "-X main.version=..." 注入正式版本
var embedHash = ""     // 由 -ldflags "-X main.embedHash=..." 注入 console/dist 产物总 hash

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
	root.AddCommand(newServeCmd(), newVersionCmd(), newNewCmd(), newSecureCmd(), newSeedCmd())
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the basalt version",
		Run: func(cmd *cobra.Command, _ []string) {
			cmd.Println("basalt", version)
			if embedHash != "" {
				cmd.Println("console.embed:", embedHash)
			}
		},
	}
}

// BinVersion 返回 ldflags 注入的版本号（healthz / 日志系统消费）。
func BinVersion() string { return version }

// BinEmbedHash 返回 ldflags 注入的 console/dist 产物 hash（healthz 消费，
// 便于线上比对二进制是否与源码同步）。空字符串表示构建时未计算。
func BinEmbedHash() string { return embedHash }

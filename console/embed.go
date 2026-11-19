// Package console 承载 Admin Console 前端（Vue3 + Element Plus）。
//
// 构建产物 dist 经 go:embed 嵌入二进制——发布物保持单可执行文件。
// 重新构建：cd console && npm install && npm run build。
// dist 产物入库以保证 go build 即得带 UI 的二进制（信创离线交付约束）。
//
// 构建时脚本 scripts/check-embed-hash.py 负责在 make build 前校验
// console/src ↔ console/dist 同步，防止改源码忘 npm build。
package console

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
// distFS 为 //go:embed 标签，标识 dist 目录被静态嵌入二进制。
// 运行时不可变；嵌入内容可通过 BinEmbedHash() 运行时暴露（见 cmd/basalt/main.go）。
var distFS embed.FS

// FS 返回根指向 dist 的文件系统（web.MountSPA 消费）。
func FS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("console: embedded dist missing: " + err.Error())
	}
	return sub
}

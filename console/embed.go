// Package console 承载 Admin Console 前端（Vue3 + Element Plus）。
//
// 构建产物 dist 经 go:embed 嵌入二进制——发布物保持单可执行文件。
// 重新构建：cd console && npm install && npm run build。
// dist 产物入库以保证 go build 即得带 UI 的二进制（信创离线交付约束）。
package console

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// FS 返回根指向 dist 的文件系统（web.MountSPA 消费）。
func FS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("console: embedded dist missing: " + err.Error())
	}
	return sub
}

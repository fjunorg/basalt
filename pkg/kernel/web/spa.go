package web

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// MountSPA 将前端单页应用（go:embed 静态资源）挂载到根路径。
// 规则：已注册的 API 路由优先；未命中路径且非 /api 前缀时回退
// index.html（hash 路由下常规访问均为静态文件，此兜底覆盖
// 嵌套路由直开与刷新场景）。
func (r *ginRouter) MountSPA(fsys fs.FS) {
	fileServer := http.FileServer(http.FS(fsys))
	r.engine.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/api/") {
			c.JSON(http.StatusNotFound, Envelope{Code: http.StatusNotFound, Message: "not found"})
			return
		}
		if p != "/" && fileExists(fsys, p) {
			fileServer.ServeHTTP(c.Writer, c.Request)
			return
		}
		// SPA 入口兜底
		index, err := fs.ReadFile(fsys, "index.html")
		if err != nil {
			c.String(http.StatusNotFound, "console not built")
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", index)
	})
}

func fileExists(fsys fs.FS, path string) bool {
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return false
	}
	f, err := fsys.Open(path)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

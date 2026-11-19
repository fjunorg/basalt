package web

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// Router 是内核的 HTTP 路由抽象。模块只面对标准库类型：
// 处理器为 http.HandlerFunc，路径参数经 r.PathValue("name") 读取，
// 路径声明使用 Go 1.22 标准风格 {param}。
//
// 底层实现当前为 gin（见 docs/01 L0 选型）；模块代码零 gin 依赖，
// 引擎替换时模块无需改动。
type Router interface {
	// Handle 注册路由。method 为标准 HTTP 动词，path 如 "/api/v1/users/{id}"。
	Handle(method, path string, h http.HandlerFunc)
	// Group 返回带统一前缀的子路由。
	Group(prefix string) Router
	// Handler 返回组装完成的 http.Handler，供 http.Server 使用。
	Handler() http.Handler
	// MountSPA 挂载前端单页应用静态资源（未命中且非 /api 时回退 index.html）。
	MountSPA(fsys fs.FS)
}

// NewRouter 构造 gin 底层的 Router。生产环境自动切换 ReleaseMode，
// 并挂载默认安全中间件（CSP / XFO / 限流）。gin 要求中间件先于路由注册，
// 因此 Recovery、SecureHeaders、RateLimit 在此挂载。
func NewRouter(prod bool) Router {
	if prod {
		gin.SetMode(gin.ReleaseMode)
	}
	e := gin.New()
	e.Use(gin.Recovery())
	if prod {
		e.Use(SecureHeaders())
		// 生产默认：IP 每秒 100 次，突发 30（正常 API 调用充足，挡爆破）
		e.Use(RateLimit(100, 30))
	}
	return &ginRouter{engine: e}
}

type ginRouter struct {
	engine *gin.Engine
	group  *gin.RouterGroup // nil 表示挂载在 engine 根
}

func (r *ginRouter) Handle(method, path string, h http.HandlerFunc) {
	mount := func(c *gin.Context) {
		req := c.Request
		// 把 gin 解析的路径参数桥接为标准库 PathValue，
		// 使处理器侧无需感知 gin。
		for _, p := range c.Params {
			req.SetPathValue(p.Key, p.Value)
		}
		// c.Writer（gin.ResponseWriter）实现标准 http.ResponseWriter，
		// 并额外保留状态码/字节数统计能力（供未来访问日志与限流使用）。
		h(c.Writer, req)
	}
	if r.group == nil {
		r.engine.Handle(method, toGinPath(path), mount)
	} else {
		r.group.Handle(method, toGinPath(path), mount)
	}
}

func (r *ginRouter) Group(prefix string) Router {
	if r.group == nil {
		return &ginRouter{engine: r.engine, group: r.engine.Group(prefix)}
	}
	return &ginRouter{engine: r.engine, group: r.group.Group(prefix)}
}

func (r *ginRouter) Handler() http.Handler { return r.engine }

// toGinPath 把标准风格 {param} 转为 gin 风格 :param。
func toGinPath(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		if p[i] == '{' {
			j := strings.IndexByte(p[i:], '}')
			if j < 0 {
				return p // 非法路径，交给 gin 注册时报错
			}
			b.WriteByte(':')
			b.WriteString(p[i+1 : i+j])
			i += j
			continue
		}
		b.WriteByte(p[i])
	}
	return b.String()
}

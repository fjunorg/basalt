package web

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// SecureHeaders 生产环境默认安全响应头中间件。
//
// CSP 策略说明：
//   - default-src 'self'  — 全局兜底，所有未显式声明的资源只能同源加载
//   - script-src 'self' 'unsafe-eval'
//      Vite 生产构建将全部源码外部化为 chunk.js 文件（零内联 <script>），
//      仅 Element Plus / Vue runtime 含 Function("return this")() polyfill，
//      需 unsafe-eval。开发模式会自动加 'unsafe-inline' 配合 HMR。
//   - style-src 'self' 'unsafe-inline'
//      Vue SFC scoped style 会被构建为独立 .css 文件并经 <link> 引入，
//      但 Element Plus 运行时会动态注入少量内联样式做组件形态调整
//      （el-tooltip 位置、el-dialog 遮罩淡入淡出）。若后续审计确认无
//      动态样式注入可移除，将显著提升抗 XSS 能力。
//   - img-src 'self' data: blob:  — 头像 base64、SSE blob URL
//   - connect-src 'self'          — 统一 API 调用
//   - frame-ancestors 'none'      — 防点击劫持（管理后台严禁 iframe 嵌入）
//
// 其余响应头：nosniff / XFO / Referrer-Policy / Permissions-Policy。
func SecureHeaders() gin.HandlerFunc {
	csp := strings.Join([]string{
		"default-src 'self'",
		"script-src 'self' 'unsafe-eval'",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob:",
		"font-src 'self' data:",
		"connect-src 'self'",
		"frame-ancestors 'none'",
		"base-uri 'self'",
		"form-action 'self'",
	}, "; ")

	return func(c *gin.Context) {
		w := c.Writer.Header()
		w.Set("Content-Security-Policy", csp)
		w.Set("X-Content-Type-Options", "nosniff")
		w.Set("X-Frame-Options", "DENY")
		w.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Set("Permissions-Policy", "geolocation=(), camera=(), microphone=()")
		c.Next()
	}
}

// RateLimit 进程内 IP 基础限流（令牌桶思想）。
//
// 实现：滑动窗口（每秒窗口 + max 阈值）。窗口粒度=1s 足够挡暴力爆破
// 而对正常用户无感；分布式限流（Redis）留待多实例部署时引入。
// 命中阈值返回 429 Too Many Requests + Retry-After。
//
// 参数：max = 窗口内最大请求数；burst = 突发允许（0 表示不允许突发）。
func RateLimit(max, burst int) gin.HandlerFunc {
	type entry struct {
		count  int       // 当前 1s 窗口计数
		window time.Time // 窗口起点（秒精度）
	}
	var (
		mu      sync.Mutex
		buckets = map[string]*entry{}
	)

	getOrCreate := func(ip string) *entry {
		mu.Lock()
		defer mu.Unlock()
		e, ok := buckets[ip]
		if !ok {
			e = &entry{window: time.Now().Truncate(time.Second)}
			buckets[ip] = e
		}
		return e
	}

	return func(c *gin.Context) {
		// 白名单：健康检查 + 静态资源不计入（SPA 刷新时频繁请求）
		path := c.Request.URL.Path
		if path == "/healthz" || strings.HasPrefix(path, "/assets/") {
			c.Next()
			return
		}

		ip := c.ClientIP()
		now := time.Now().Truncate(time.Second)
		e := getOrCreate(ip)

		mu.Lock()
		if !e.window.Equal(now) {
			e.window = now
			e.count = 0
		}
		e.count++
		exceeded := e.count > max+burst
		mu.Unlock()

		if exceeded {
			c.Writer.Header().Set("Retry-After", "1")
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"code":    http.StatusTooManyRequests,
				"message": "rate limit exceeded, retry after 1s",
			})
			return
		}
		c.Next()
	}
}

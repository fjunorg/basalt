// Package oplog 是操作/API 日志模块（竞品 Gap P1#12）：HTTP 中间件
// 采集业务请求日志 + 分页查询 API + 轻量运行时指标。
//
// 与 audit 防篡改审计链的分工：audit 链面向合规取证（选择性事件、
// 哈希链防篡改）；oplog 面向日常排障（全量/变更操作、可查询可丢弃、
// 无防篡改语义）。
//
// 采集与写入架构（异步不阻塞请求）：
//
//	请求 → Middleware(外层包装) ──select chan(1024)──→ 后台聚合器
//	  │（构造 Entry ~1µs，通道满则非阻塞丢弃并计 dropped）  │（100ms/64 条批量 Append）
//	  └─ next.ServeHTTP（业务路径零额外 IO 等待）           ↓
//	                                              Store（内存环形滚动/可选 GORM）
//
// 采样策略（BASALT_OPLOG_MODE）：
//   - all（默认）：全量采集——排障价值优先（慢 GET 同样是线索），
//     内嵌私有化部署 QPS 量级下内存滚动窗口完全可承载；
//   - write：仅变更操作（POST/PUT/PATCH/DELETE）——高流量场景降噪；
//   - off：关闭采集（查询/指标端点仍可用，返回已积累数据）。
//
// 数据保留：内存环形缓冲（默认 10000 条滚动覆盖，~2MB 上界）；
// BASALT_OPLOG_PERSIST=sqlite 时同步落库（operation_logs 表，
// 保留策略交由运维按库清理——模块不自动删行）。
package oplog

import (
	"strings"
	"sync"
	"time"
)

const Version = "0.1.0"

// 权限点。
const PermRead = "oplog:read"

// 采样模式。
const (
	ModeAll   = "all"
	ModeWrite = "write"
	ModeOff   = "off"
)

// Entry 是一条操作/API 日志。
type Entry struct {
	ID         string    `json:"id" gorm:"primaryKey;size:40"`
	Time       time.Time `json:"time" gorm:"index"`
	Method     string    `json:"method" gorm:"size:8;index"`
	Path       string    `json:"path" gorm:"size:255;index"`
	Status     int       `json:"status" gorm:"index"`
	DurationMS float64   `json:"duration_ms"`
	UserID     string    `json:"user_id,omitempty" gorm:"size:40;index"`
	Remote     string    `json:"remote" gorm:"size:64"`
	UserAgent  string    `json:"user_agent,omitempty" gorm:"size:255"`
	Module     string    `json:"module" gorm:"size:32;index"` // path 首段推导
}

// TableName 显式表名。
func (Entry) TableName() string { return "operation_logs" }

// QueryFilter 分页过滤（零值字段不参与过滤）。
type QueryFilter struct {
	Module      string
	UserID      string
	Since       time.Time
	Until       time.Time
	StatusClass string // "2xx"|"4xx"|"5xx"（空=全部）
	Page        int    // 1 起
	PageSize    int    // 默认 20，上限 100
}

// PageResult 分页结果。
type PageResult struct {
	Rows     []Entry `json:"rows"`
	Total    int     `json:"total"`
	Page     int     `json:"page"`
	PageSize int     `json:"page_size"`
}

// Store 抽象日志存储。
type Store interface {
	Append(entries []Entry) error
	Query(f QueryFilter) (PageResult, error)
}

// ---------- 内存实现（环形滚动） ----------

// MemStore 环形缓冲：容量固定、满则覆盖最旧（保留策略即容量上界）。
type MemStore struct {
	mu   sync.RWMutex
	buf  []Entry
	cap  int
	head int // 下一个写入位
	n    int
	seq  int
}

// NewMemStore 构造（cap ≤0 时默认 10000）。
func NewMemStore(capacity int) *MemStore {
	if capacity <= 0 {
		capacity = 10000
	}
	return &MemStore{buf: make([]Entry, capacity), cap: capacity}
}

func (s *MemStore) Append(entries []Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range entries {
		s.seq++
		if e.ID == "" {
			e.ID = itoa(s.seq)
		}
		s.buf[s.head] = e
		s.head = (s.head + 1) % s.cap
		if s.n < s.cap {
			s.n++
		}
	}
	return nil
}

func (s *MemStore) Query(f QueryFilter) (PageResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// 展开为时间倒序切片（环形：head 之前为最旧）。
	ordered := make([]Entry, 0, s.n)
	for i := 0; i < s.n; i++ {
		idx := (s.head - 1 - i + s.cap) % s.cap // 新→旧
		ordered = append(ordered, s.buf[idx])
	}
	// 过滤。
	filtered := make([]Entry, 0, len(ordered))
	for _, e := range ordered {
		if !match(e, f) {
			continue
		}
		filtered = append(filtered, e)
	}
	// 分页。
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 {
		f.PageSize = 20
	}
	if f.PageSize > 100 {
		f.PageSize = 100
	}
	total := len(filtered)
	start := (f.Page - 1) * f.PageSize
	if start > total {
		start = total
	}
	end := start + f.PageSize
	if end > total {
		end = total
	}
	return PageResult{
		Rows:     filtered[start:end],
		Total:    total,
		Page:     f.Page,
		PageSize: f.PageSize,
	}, nil
}

func match(e Entry, f QueryFilter) bool {
	if f.Module != "" && e.Module != f.Module {
		return false
	}
	if f.UserID != "" && e.UserID != f.UserID {
		return false
	}
	if !f.Since.IsZero() && e.Time.Before(f.Since) {
		return false
	}
	if !f.Until.IsZero() && e.Time.After(f.Until) {
		return false
	}
	if f.StatusClass != "" && classOf(e.Status) != f.StatusClass {
		return false
	}
	return true
}

// classOf 状态码归并（2xx/4xx/5xx/other）。
func classOf(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 400 && status < 500:
		return "4xx"
	case status >= 500:
		return "5xx"
	default:
		return "other"
	}
}

// moduleOf 从路径推导模块名（/api/v1/<module>/... → module；
// 非该前缀取首段；探针路径返回空）。
func moduleOf(path string) string {
	rest := strings.TrimPrefix(path, "/api/v1/")
	if i := strings.IndexByte(rest, '/'); i > 0 {
		rest = rest[:i]
	}
	if rest == "" || rest == path {
		// 非模块前缀（如 /healthz）：不归入业务模块。
		if !strings.HasPrefix(path, "/api/") {
			return ""
		}
		seg := strings.Trim(path, "/")
		if i := strings.IndexByte(seg, '/'); i > 0 {
			seg = seg[:i]
		}
		return seg
	}
	return rest
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// skipPath 探针与静态资源不采集（噪音）。
func skipPath(p string) bool {
	return p == "/healthz" || p == "/readyz" || !strings.HasPrefix(p, "/api/")
}

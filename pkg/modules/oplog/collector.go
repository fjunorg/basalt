package oplog

import (
	"net/http"
	"runtime"
	"sort"
	"sync"
	"time"
)

// Collector 采集中间件与异步写入器。
//
// 性能机制：请求路径上仅做「构造 Entry + 非阻塞 channel 发送」
// （select default 丢弃），无锁无 IO 等待；落库/入store 由后台聚合
// goroutine 批量执行（100ms 或 64 条触发 flush）。
type Collector struct {
	mode     string // all|write|off
	store    Store
	ch       chan Entry
	dropped  int64
	buffered int

	parseUser func(r *http.Request) string // JWT 验签提 sub（identity 注入）

	metrics *windowMetrics

	stopOnce sync.Once
	stopCh   chan struct{}
	flushWG  sync.WaitGroup
}

// NewCollector 构造采集器并启动后台聚合。
func NewCollector(mode string, store Store, parseUser func(*http.Request) string) *Collector {
	switch mode {
	case ModeAll, ModeWrite, ModeOff:
	default:
		mode = ModeAll
	}
	c := &Collector{
		mode:      mode,
		store:     store,
		ch:        make(chan Entry, 1024),
		parseUser: parseUser,
		metrics:   newWindowMetrics(90, time.Second),
		stopCh:    make(chan struct{}),
	}
	c.flushWG.Add(1)
	go c.aggregate()
	return c
}

// Middleware 外层包装（装配层经 App.UseHandler 注入，覆盖全部 API 请求）。
func (c *Collector) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c.mode == ModeOff || skipPath(r.URL.Path) {
			c.metrics.observeRequest(r.Method, 0, time.Time{}) // 指标照记（QPS 面），日志不记
			next.ServeHTTP(w, r)
			return
		}
		if c.mode == ModeWrite && r.Method == http.MethodGet {
			c.metrics.observe(r.Method, 0, 0)
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		dur := time.Since(start)

		// 指标（全模式记录——QPS/延迟面板完整）。
		c.metrics.observe(r.Method, sw.status, dur)

		// 日志（mode 过滤已在上方返回）。
		e := Entry{
			Time:       start.UTC(),
			Method:     r.Method,
			Path:       r.URL.Path,
			Status:     sw.status,
			DurationMS: float64(dur.Microseconds()) / 1000.0,
			Remote:     remoteOf(r),
			UserAgent:  r.UserAgent(),
			Module:     moduleOf(r.URL.Path),
		}
		if c.parseUser != nil {
			e.UserID = c.parseUser(r)
		}
		select {
		case c.ch <- e:
			c.setBuffered(len(c.ch))
		default:
			c.drop()
		}
	})
}

// aggregate 后台聚合：批量消费通道写入 store。
func (c *Collector) aggregate() {
	defer c.flushWG.Done()
	batch := make([]Entry, 0, 64)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case e := <-c.ch:
			batch = append(batch, e)
			if len(batch) >= 64 {
				_ = c.store.Append(batch)
				batch = batch[:0]
			}
		case <-tick.C:
			if len(batch) > 0 {
				_ = c.store.Append(batch)
				batch = batch[:0]
			}
		case <-c.stopCh:
			// 排空通道后收尾。
			for {
				select {
				case e := <-c.ch:
					batch = append(batch, e)
				default:
					_ = c.store.Append(batch)
					return
				}
			}
		}
	}
}

// Close 停止聚合并刷出（进程优雅退出时调用）。
func (c *Collector) Close() {
	c.stopOnce.Do(func() { close(c.stopCh) })
	c.flushWG.Wait()
}

// droppedCount / bufferedCount 指标暴露（原子语义经锁保护足够——
// 诊断值非精确计量）。
var dropMu sync.Mutex

func (c *Collector) drop() {
	dropMu.Lock()
	c.dropped++
	dropMu.Unlock()
}

func (c *Collector) droppedCount() int64 {
	dropMu.Lock()
	defer dropMu.Unlock()
	return c.dropped
}

func (c *Collector) setBuffered(n int) {
	dropMu.Lock()
	c.buffered = n
	dropMu.Unlock()
}

func (c *Collector) bufferedCount() int {
	dropMu.Lock()
	defer dropMu.Unlock()
	return c.buffered
}

// ---------- 滑动窗口指标 ----------

// bucket 一秒聚合桶：计数 + 延迟样本（内嵌部署量级下全量样本
// 内存可控：90 桶 × 低 QPS；样本超限时截断保留最新）。
type bucket struct {
	second   int64 // unix 秒
	count    int64
	errCount int64
	durs     []float64 // ms
}

const maxSamplesPerBucket = 2048

// windowMetrics 环形秒桶滑动窗口。
type windowMetrics struct {
	mu      sync.RWMutex
	buckets []bucket
	size    int
}

func newWindowMetrics(size int, _ time.Duration) *windowMetrics {
	return &windowMetrics{buckets: make([]bucket, size), size: size}
}

func (m *windowMetrics) observeRequest(method string, status int, _ time.Time) {
	m.observe(method, status, 0)
}

// observe 记录一次请求（status>0 时计错误；dur 为毫秒）。
func (m *windowMetrics) observe(method string, status int, dur time.Duration) {
	now := time.Now().Unix()
	m.mu.Lock()
	defer m.mu.Unlock()
	// 找到当前秒桶（环形按 second 对齐）。
	idx := int(now % int64(m.size))
	b := &m.buckets[idx]
	if b.second != now {
		*b = bucket{second: now} // 新秒：覆盖旧桶
	}
	b.count++
	if status >= 500 || status == http.StatusTooManyRequests {
		b.errCount++
	}
	if ms := float64(dur.Microseconds()) / 1000.0; ms > 0 {
		if len(b.durs) < maxSamplesPerBucket {
			b.durs = append(b.durs, ms)
		}
	}
}

// MetricsSnapshot 指标快照。
type MetricsSnapshot struct {
	Window struct {
		Seconds   float64 `json:"window_seconds"`
		Requests  int64   `json:"requests"`
		QPS       float64 `json:"qps"`
		ErrorRate float64 `json:"error_rate"` // 0-1
		P50MS     float64 `json:"p50_ms"`
		P95MS     float64 `json:"p95_ms"`
		P99MS     float64 `json:"p99_ms"`
	} `json:"window"`
	Process struct {
		Goroutines  int     `json:"goroutines"`
		HeapAllocMB float64 `json:"heap_alloc_mb"`
		SysMB       float64 `json:"sys_mb"`
		NumGC       uint32  `json:"num_gc"`
		GoVersion   string  `json:"go_version"`
	} `json:"process"`
	Collector struct {
		Dropped  int64  `json:"dropped"`
		Buffered int    `json:"buffered"`
		Mode     string `json:"mode"`
	} `json:"collector"`
}

// Snapshot 计算窗口指标 + 进程指标。
func (c *Collector) Snapshot() MetricsSnapshot {
	var s MetricsSnapshot
	now := time.Now().Unix()

	c.metrics.mu.RLock()
	var total, errs int64
	var durs []float64
	active := 0
	for i := range c.metrics.buckets {
		b := &c.metrics.buckets[i]
		if b.second > 0 && now-b.second < int64(c.metrics.size) {
			active++
			total += b.count
			errs += b.errCount
			durs = append(durs, b.durs...)
		}
	}
	c.metrics.mu.RUnlock()

	s.Window.Seconds = float64(active)
	if active > 0 {
		s.Window.Requests = total
		s.Window.QPS = float64(total) / float64(active)
	}
	if total > 0 {
		s.Window.ErrorRate = float64(errs) / float64(total)
	}
	if len(durs) > 0 {
		s.Window.P50MS = percentile(durs, 50)
		s.Window.P95MS = percentile(durs, 95)
		s.Window.P99MS = percentile(durs, 99)
	}

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	s.Process.Goroutines = runtime.NumGoroutine()
	s.Process.HeapAllocMB = round2(float64(ms.HeapAlloc) / 1024 / 1024)
	s.Process.SysMB = round2(float64(ms.Sys) / 1024 / 1024)
	s.Process.NumGC = ms.NumGC
	s.Process.GoVersion = runtime.Version()

	s.Collector.Dropped = c.droppedCount()
	s.Collector.Buffered = c.bufferedCount()
	s.Collector.Mode = c.mode
	return s
}

// percentile 就地排序取分位（排障量级可接受；样本上界 90×2048）。
func percentile(durs []float64, p int) float64 {
	sort.Float64s(durs)
	idx := p * len(durs) / 100
	if idx >= len(durs) {
		idx = len(durs) - 1
	}
	return round2(durs[idx])
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }

// remoteOf 提取客户端 IP（httptest/反代场景取 RemoteAddr 主机段）。
func remoteOf(r *http.Request) string {
	host := r.RemoteAddr
	if i := lastColon(host); i > 0 {
		host = host[:i]
	}
	return host
}

func lastColon(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			return i
		}
	}
	return -1
}

// statusWriter 捕获响应状态码（默认 200——直接写 w 不显式
// WriteHeader 的路径）。
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(b)
}

// ---------- 测试与诊断辅助 ----------

// NewCollectorForTest 构造不启动后台聚合的采集器（指标窗口可直灌）。
func NewCollectorForTest() *Collector {
	return &Collector{
		mode:    ModeAll,
		store:   NewMemStore(100),
		metrics: newWindowMetrics(90, time.Second),
	}
}

// ObserveForTest 直接注入一次观测（测试窗口计算，不经 HTTP）。
func (c *Collector) ObserveForTest(status int, dur time.Duration) {
	c.metrics.observe(http.MethodGet, status, dur)
}

// SnapshotForTest 暴露指标快照（与 metricsView 端点同数据源）。
func (c *Collector) SnapshotForTest() MetricsSnapshot { return c.Snapshot() }

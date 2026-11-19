// Package job 是 L1 轻量任务调度模块：内置任务注册表 + 间隔调度 +
// 启停/手动触发/运行记录。刻意不引 Quartz/XXL-JOB（零外部依赖、单体
// 自洽）；分布式调度（多实例选主）随微服务形态评估。
package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/web"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/identity"
)

const Version = "0.1.0"

const (
	PermList   = "job:list"
	PermManage = "job:manage"
)

var ErrNotFound = errors.New("job: not found")

var (
	ErrBadSchedule  = errors.New("job: exactly one of interval_sec / cron_expr required")
	ErrBuiltinNoDel = errors.New("job: builtin job cannot be deleted")
	ErrBadTarget    = errors.New("job: http target must be http(s) URL with host")
)

// Runner 是任务执行体：返回结果摘要或错误（均计入运行记录）。
type Runner func(ctx context.Context) (string, error)

// 任务类型。
const (
	KindBuiltin = "builtin" // 代码注册型（定义来源=代码）
	KindHTTP    = "http"    // 在线动态注册（HTTP 回调，定义来源=持久层）
)

// Job 是一个注册任务的定义与运行态。调度二选一：IntervalSec（间隔
// 秒）或 CronExpr（标准 5 段）；CronExpr 非空时优先。
type Job struct {
	ID          string     `json:"id" gorm:"primaryKey;size:64"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	IntervalSec int        `json:"interval_sec" gorm:"default:60"`
	CronExpr    string     `json:"cron_expr,omitempty" gorm:"size:32"`
	Kind        string     `json:"kind" gorm:"size:16;default:builtin"`
	URL         string     `json:"url,omitempty" gorm:"size:512"`
	Method      string     `json:"method,omitempty" gorm:"size:8"`
	Payload     string     `json:"payload,omitempty" gorm:"type:text"`
	Enabled     bool       `json:"enabled"`
	LastRun     *time.Time `json:"last_run,omitempty"`
	LastResult  string     `json:"last_result,omitempty" gorm:"size:255"`
	LastErr     bool       `json:"last_err"`
	NextRun     time.Time  `json:"next_run"`
}

// cronOf 惰性解析（定义不变，可缓存于进程内）。
var cronCache sync.Map // expr -> *Cron

func (j *Job) cron() (*Cron, error) {
	if j.CronExpr == "" {
		return nil, nil
	}
	if c, ok := cronCache.Load(j.CronExpr); ok {
		return c.(*Cron), nil
	}
	c, err := ParseCron(j.CronExpr)
	if err != nil {
		return nil, err
	}
	cronCache.Store(j.CronExpr, c)
	return c, nil
}

// scheduleNext 统一重排：interval 型 now+n 秒；cron 型取 now 之后的
// 下一个触发点（错过的不补跑，从当前时间对齐——与 interval 语义
// 一致，避免停机补偿突发）。
func (j *Job) scheduleNext(now time.Time) time.Time {
	if c, err := j.cron(); err == nil && c != nil {
		return c.Next(now)
	}
	return now.Add(time.Duration(j.IntervalSec) * time.Second)
}

// RunRecord 是一次执行记录。持久化模式经 Store 落库（Seq 为追加
// 序，分页查询键）；内存模式保留环形最近 keepRuns 条（快速读）。
type RunRecord struct {
	Seq      int64     `json:"seq" gorm:"primaryKey;autoIncrement"`
	JobID    string    `json:"job_id" gorm:"size:64;index"`
	Start    time.Time `json:"start"`
	Duration string    `json:"duration"`
	Result   string    `json:"result,omitempty" gorm:"size:512"`
	Err      string    `json:"err,omitempty" gorm:"size:512"`
}

const keepRuns = 20

// Store 抽象任务与运行记录持久化（nil = 纯内存）。
// 内置任务：代码是定义唯一来源，持久层存运行态；HTTP 动态任务：
// 定义本身存持久层（重启经 LoadDynamic 恢复）。
type Store interface {
	GetJob(ctx context.Context, id string) (Job, error)
	SaveJob(ctx context.Context, j Job) error // UPSERT 全量
	ListJobs(ctx context.Context) ([]Job, error)
	DeleteJob(ctx context.Context, id string) error
	SaveRun(ctx context.Context, rec RunRecord) (RunRecord, error) // 回填 Seq
	ListRuns(ctx context.Context, jobID string, offset, limit int) ([]RunRecord, int, error)
}

type Manager struct {
	mu       sync.Mutex
	jobs     map[string]*Job
	runners  map[string]Runner
	runs     map[string][]RunRecord // jobID -> 倒序记录
	logger   *slog.Logger
	store    Store // 可选持久化；nil 为纯内存
	lastTick time.Time
}

func NewManager(logger *slog.Logger) *Manager {
	return &Manager{
		jobs: map[string]*Job{}, runners: map[string]Runner{}, runs: map[string][]RunRecord{},
		logger: logger,
	}
}

// NewManagerWithStore 构造带持久化的管理器：任务启停与执行进度落盘，
// 重启后经 Register 恢复运行态（见 Register 的恢复语义说明）。
func NewManagerWithStore(logger *slog.Logger, s Store) *Manager {
	m := NewManager(logger)
	m.store = s
	return m
}

// Register 注册内置任务（默认停用，等待启用）。ID 即调度键。
//
// 持久化模式下的重启恢复语义：
//   - 内置任务定义（Name/Description/调度参数）以代码注册值为准——
//     代码是定义的唯一来源，升级改调度即生效并回写持久层；
//   - 运行态（Enabled/LastRun/LastResult/LastErr）从持久层恢复；
//   - NextRun 恢复后若已过期（停机期间错过触发点），不补跑，按
//     当前时间重排（interval=now+n / cron=now 后下一触发点）——
//     轻量调度器的可预期语义（避免停机 N 小时后启动突发补偿）。
func (m *Manager) Register(id, name, desc string, intervalSec int, run Runner) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runners[id] = run
	if _, ok := m.jobs[id]; ok {
		return // 重复注册：仅刷新 runner
	}
	now := time.Now().UTC()
	j := &Job{ID: id, Name: name, Description: desc, Kind: KindBuiltin,
		IntervalSec: intervalSec, Enabled: false}
	if intervalSec <= 0 {
		j.IntervalSec = 60
	}
	j.NextRun = j.scheduleNext(now)
	if m.store != nil {
		if saved, err := m.store.GetJob(context.Background(), id); err == nil && saved.Kind != KindHTTP {
			j.Enabled = saved.Enabled
			j.LastRun, j.LastResult, j.LastErr = saved.LastRun, saved.LastResult, saved.LastErr
		}
		if err := m.store.SaveJob(context.Background(), *j); err != nil {
			m.logWarn("job persist failed", id, err)
		}
	}
	m.jobs[id] = j
}

// LoadDynamic 重启恢复：从持久层重建全部 HTTP 动态任务（定义+运行态），
// NextRun 按当前时间重算（不补跑）。仅持久化模式有意义。
func (m *Manager) LoadDynamic(ctx context.Context) error {
	if m.store == nil {
		return nil
	}
	saved, err := m.store.ListJobs(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sj := range saved {
		if sj.Kind != KindHTTP {
			continue // 内置任务由代码注册恢复
		}
		j := sj
		if err := validHTTPTarget(j.URL); err != nil {
			m.logWarn("skip dynamic job with bad target", j.ID, err)
			continue
		}
		j.NextRun = j.scheduleNext(now) // 重算（cron 从当前起对齐）
		m.runners[j.ID] = httpRunner(j)
		m.jobs[j.ID] = &j
	}
	return nil
}

// RegisterHTTP 在线注册 HTTP 回调任务（动态型，定义持久化）。
// intervalSec 与 cronExpr 二选一（cron 优先）；返回错误即时拒绝。
func (m *Manager) RegisterHTTP(id, name, desc, cronExpr string, intervalSec int, url, method, payload string) error {
	if id == "" || name == "" {
		return errors.New("job: id and name required")
	}
	if cronExpr == "" && intervalSec <= 0 {
		return ErrBadSchedule
	}
	if cronExpr != "" {
		if err := ValidCron(cronExpr); err != nil {
			return err
		}
		intervalSec = 0
	}
	if err := validHTTPTarget(url); err != nil {
		return err
	}
	if method == "" {
		method = http.MethodPost
	}
	method = strings.ToUpper(method)
	if method != http.MethodGet && method != http.MethodPost && method != http.MethodPut {
		return fmt.Errorf("job: method must be GET/POST/PUT")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.jobs[id]; exists {
		return fmt.Errorf("job: id %q already exists", id)
	}
	now := time.Now().UTC()
	j := &Job{ID: id, Name: name, Description: desc, Kind: KindHTTP,
		CronExpr: cronExpr, IntervalSec: intervalSec,
		URL: url, Method: method, Payload: payload, Enabled: false}
	j.NextRun = j.scheduleNext(now)
	if m.store != nil {
		if err := m.store.SaveJob(context.Background(), *j); err != nil {
			return err
		}
	}
	m.runners[id] = httpRunner(*j)
	m.jobs[id] = j
	return nil
}

// Delete 删除任务（仅动态型；内置任务定义属代码，不可删）。
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return ErrNotFound
	}
	if j.Kind != KindHTTP {
		return ErrBuiltinNoDel
	}
	if m.store != nil {
		if err := m.store.DeleteJob(context.Background(), id); err != nil {
			return err
		}
	}
	delete(m.jobs, id)
	delete(m.runners, id)
	delete(m.runs, id)
	return nil
}

// validHTTPTarget 动态任务目标校验：http(s) 且有 host（管理权限已
// 限定创建者；SSRF 深度防御如内网 IP 黑名单见任务边界说明——目标
// 为内部服务是合法场景，v1 不做 IP 过滤，权限面收敛到 job:manage）。
func validHTTPTarget(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return ErrBadTarget
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ErrBadTarget
	}
	if u.Host == "" {
		return ErrBadTarget
	}
	return nil
}

// httpRunner 构造 HTTP 回调执行体：按 Method 请求 URL（Payload 为
// JSON body），结果 = 状态码 + 截断响应体；非 2xx 记为错误。
func httpRunner(j Job) Runner {
	client := &http.Client{Timeout: 30 * time.Second}
	return func(ctx context.Context) (string, error) {
		var body io.Reader
		if j.Payload != "" && j.Method != http.MethodGet {
			body = strings.NewReader(j.Payload)
		}
		req, err := http.NewRequestWithContext(ctx, j.Method, j.URL, body)
		if err != nil {
			return "", err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		summary := fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return summary, fmt.Errorf("http status %d", resp.StatusCode)
		}
		return summary, nil
	}
}

func (m *Manager) logWarn(msg, jobID string, err error) {
	if m.logger != nil {
		m.logger.Warn(msg, "job", jobID, "err", err)
	}
}

// SetEnabled 启停任务（持久化模式下先落盘成功再更新内存，保证
// 返回 nil 即已持久）。启用即按当前时间重排调度。
func (m *Manager) SetEnabled(id string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return ErrNotFound
	}
	updated := *j
	updated.Enabled = enabled
	updated.NextRun = j.scheduleNext(time.Now().UTC())
	if m.store != nil {
		if err := m.store.SaveJob(context.Background(), updated); err != nil {
			m.logWarn("job persist failed", id, err)
			return err
		}
	}
	*j = updated
	return nil
}

// Trigger 立即执行一次（不受 Enabled 限制）。
func (m *Manager) Trigger(ctx context.Context, id string) error {
	m.mu.Lock()
	j, ok := m.jobs[id]
	run, hasRunner := m.runners[id]
	m.mu.Unlock()
	if !ok || !hasRunner {
		return ErrNotFound
	}
	m.exec(ctx, j, run)
	return nil
}

// exec 执行并记录（锁外运行任务体）。执行记录：内存环形（快速读）
// + 持久层追加（分页查询）。
func (m *Manager) exec(ctx context.Context, j *Job, run Runner) {
	start := time.Now()
	result, err := run(ctx)
	dur := time.Since(start)
	rec := RunRecord{JobID: j.ID, Start: start.UTC(), Duration: dur.Round(time.Millisecond).String()}
	if err != nil {
		rec.Err = err.Error()
	} else {
		rec.Result = result
	}
	if m.store != nil {
		if saved, serr := m.store.SaveRun(ctx, rec); serr == nil {
			rec.Seq = saved.Seq
		} else {
			m.logWarn("run persist failed", j.ID, serr)
		}
	}
	m.mu.Lock()
	j.LastRun = &rec.Start
	j.LastResult, j.LastErr = firstNonEmpty(rec.Result, rec.Err), err != nil
	j.NextRun = j.scheduleNext(time.Now().UTC())
	m.runs[j.ID] = append([]RunRecord{rec}, m.runs[j.ID]...)
	if len(m.runs[j.ID]) > keepRuns {
		m.runs[j.ID] = m.runs[j.ID][:keepRuns]
	}
	if m.store != nil {
		if perr := m.store.SaveJob(ctx, *j); perr != nil {
			m.logWarn("job persist failed", j.ID, perr)
		}
	}
	m.mu.Unlock()
	if m.logger != nil {
		m.logger.Info("job executed", "job", j.ID, "err", err != nil, "duration", rec.Duration)
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// tick 由模块 Start 的循环调用：执行全部到期任务。
func (m *Manager) tick(ctx context.Context) {
	now := time.Now().UTC()
	m.mu.Lock()
	m.lastTick = now
	m.mu.Unlock()

	type pair struct {
		j   *Job
		run Runner
	}
	var due []pair
	m.mu.Lock()
	for id, j := range m.jobs {
		if j.Enabled && !j.NextRun.After(now) {
			due = append(due, pair{j, m.runners[id]})
		}
	}
	m.mu.Unlock()
	for _, p := range due {
		m.exec(ctx, p.j, p.run)
	}
}

// SchedulerPing 供 readyz 使用：检查调度循环是否存活。
// 返回 error 表示调度器卡死（自 Start 起从未 tick 或最近 tick 超过 30s）。
func (m *Manager) SchedulerPing() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastTick.IsZero() {
		// 还没 tick 过：给 5s 启动宽限
		return nil
	}
	if time.Since(m.lastTick) > 30*time.Second {
		return fmt.Errorf("scheduler tick stall: last tick at %s (over 30s ago)", m.lastTick)
	}
	return nil
}

// List 返回任务定义快照。
func (m *Manager) List() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, *j)
	}
	return out
}

// Runs 返回任务最近运行记录（内存环形，倒序）。
func (m *Manager) Runs(id string) []RunRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]RunRecord(nil), m.runs[id]...)
}

// RunsPage 分页查询执行日志：持久层优先（全量历史，倒序 + total）；
// 纯内存模式降级为环形切片分页（无全量保证）。
func (m *Manager) RunsPage(ctx context.Context, id string, offset, limit int) ([]RunRecord, int, error) {
	if m.store != nil {
		return m.store.ListRuns(ctx, id, offset, limit)
	}
	all := m.Runs(id)
	total := len(all)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if limit <= 0 || end > total {
		end = total
	}
	return all[offset:end], total, nil
}

func itoa(n int) string {
	return json.Number(fmtInt(n)).String()
}

func fmtInt(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

type Module struct {
	mgr    *Manager
	facade *identity.Facade
}

func New(logger *slog.Logger) *Module { return &Module{mgr: NewManager(logger)} }

// NewWithStore 注入持久化实现（运行态落盘、重启恢复）；
// New() 的纯内存路径保留（测试/演示场景可退回）。
func NewWithStore(logger *slog.Logger, s Store) *Module {
	return &Module{mgr: NewManagerWithStore(logger, s)}
}

func (*Module) Name() string           { return "job" }
func (*Module) Version() string        { return Version }
func (*Module) Dependencies() []string { return []string{"identity", "audit"} }

func (*Module) Describe() app.ModuleInfo {
	return app.ModuleInfo{Name: "job", Version: Version, Permissions: []string{PermList, PermManage}}
}

func (m *Module) Init(_ context.Context, a *app.App) error {
	idm, ok := a.Registry().MustGet("identity").(*identity.Module)
	if !ok {
		return errors.New("job: identity module not available")
	}
	m.facade = idm.Facade()

	// 内置任务：审计链统计（任务系统与审计模块打通的示例）。
	if adm, ok := a.Registry().MustGet("audit").(*audit.Module); ok {
		m.mgr.Register("audit.stats", "审计链统计", "统计审计事件总数并记入运行结果", 300,
			func(ctx context.Context) (string, error) {
				n := adm.Count(ctx)
				return "audit events: " + itoa(n), nil
			})
	}

	auth := m.facade.Auth
	req := m.facade.Require
	g := a.Router().Group("/api/v1/jobs")
	g.Handle(http.MethodGet, "", auth(req(PermList, m.list)))
	g.Handle(http.MethodPatch, "/{id}/status", auth(req(PermManage, m.setStatus)))
	g.Handle(http.MethodPost, "/{id}/trigger", auth(req(PermManage, m.trigger)))
	g.Handle(http.MethodGet, "/{id}/runs", auth(req(PermList, m.runs)))
	// 在线动态任务（HTTP 回调型）：创建/删除均为管理权限——动态
	// 注册的执行体可以是任意 HTTP 端点，属高敏操作（安全边界说明
	// 见 RegisterHTTP/validHTTPTarget 注释：job:manage 限定 + scheme
	// 白名单 + host 必填；SSRF IP 过滤不做的理由同注）。
	g.Handle(http.MethodPost, "/http", auth(req(PermManage, m.createHTTP)))
	g.Handle(http.MethodDelete, "/{id}", auth(req(PermManage, m.deleteJob)))
	return nil
}

// Start 启动调度循环（1 秒粒度扫描，任务体在到期时执行）；
// 持久化模式先恢复 HTTP 动态任务（NextRun 按当前重算，不补跑）。
func (m *Module) Start(ctx context.Context) error {
	if err := m.mgr.LoadDynamic(ctx); err != nil {
		if m.mgr.logger != nil {
			m.mgr.logger.Warn("load dynamic jobs failed", "err", err)
		}
	}
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.mgr.tick(ctx)
			}
		}
	}()
	return nil
}

func (*Module) Stop(context.Context) error { return nil }

// Manager 供装配内置任务。
func (m *Module) Manager() *Manager { return m.mgr }

func (m *Module) list(w http.ResponseWriter, _ *http.Request) {
	web.OK(w, m.mgr.List())
}

func (m *Module) setStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if err := m.mgr.SetEnabled(r.PathValue("id"), req.Enabled); err != nil {
		web.Err(w, http.StatusNotFound, "job not found")
		return
	}
	web.OK(w, nil)
}

func (m *Module) trigger(w http.ResponseWriter, r *http.Request) {
	if err := m.mgr.Trigger(r.Context(), r.PathValue("id")); err != nil {
		web.Err(w, http.StatusNotFound, "job not found")
		return
	}
	web.OK(w, m.mgr.Runs(r.PathValue("id"))[0])
}

func (m *Module) runs(w http.ResponseWriter, r *http.Request) {
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	recs, total, err := m.mgr.RunsPage(r.Context(), r.PathValue("id"), offset, limit)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, map[string]any{"total": total, "offset": offset, "items": recs})
}

// createHTTP 在线注册 HTTP 回调任务（job:manage）。
func (m *Module) createHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		CronExpr    string `json:"cron_expr"`
		IntervalSec int    `json:"interval_sec"`
		URL         string `json:"url"`
		Method      string `json:"method"`
		Payload     string `json:"payload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return
	}
	j := Job{ID: req.ID, Name: req.Name, Description: req.Description,
		CronExpr: req.CronExpr, IntervalSec: req.IntervalSec,
		URL: req.URL, Method: req.Method, Payload: req.Payload}
	if err := m.mgr.RegisterHTTP(j.ID, j.Name, j.Description, j.CronExpr, j.IntervalSec,
		j.URL, j.Method, j.Payload); err != nil {
		web.Err(w, http.StatusBadRequest, err.Error())
		return
	}
	created, _ := func() (*Job, bool) {
		for _, x := range m.mgr.List() {
			if x.ID == req.ID {
				return &x, true
			}
		}
		return nil, false
	}()
	if created == nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.Created(w, created)
}

// deleteJob 删除动态任务（内置任务 409）。
func (m *Module) deleteJob(w http.ResponseWriter, r *http.Request) {
	err := m.mgr.Delete(r.PathValue("id"))
	switch {
	case errors.Is(err, ErrNotFound):
		web.Err(w, http.StatusNotFound, "job not found")
	case errors.Is(err, ErrBuiltinNoDel):
		web.Err(w, http.StatusConflict, ErrBuiltinNoDel.Error())
	case err != nil:
		web.Err(w, http.StatusInternalServerError, err.Error())
	default:
		web.OK(w, nil)
	}
}

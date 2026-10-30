// Package job 是 L1 轻量任务调度模块：内置任务注册表 + 间隔调度 +
// 启停/手动触发/运行记录。刻意不引 Quartz/XXL-JOB（零外部依赖、单体
// 自洽）；分布式调度（多实例选主）随微服务形态评估。
package job

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
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

// Runner 是任务执行体：返回结果摘要或错误（均计入运行记录）。
type Runner func(ctx context.Context) (string, error)

// Job 是一个注册任务的定义与运行态。
type Job struct {
	ID          string     `json:"id" gorm:"primaryKey;size:64"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	IntervalSec int        `json:"interval_sec" gorm:"default:60"`
	Enabled     bool       `json:"enabled"`
	LastRun     *time.Time `json:"last_run,omitempty"`
	LastResult  string     `json:"last_result,omitempty" gorm:"size:255"`
	LastErr     bool       `json:"last_err"`
	NextRun     time.Time  `json:"next_run"`
}

// RunRecord 是一次执行记录（保留最近 N 条/任务，内存环形）。
type RunRecord struct {
	JobID    string    `json:"job_id"`
	Start    time.Time `json:"start"`
	Duration string    `json:"duration"`
	Result   string    `json:"result,omitempty"`
	Err      string    `json:"err,omitempty"`
}

const keepRuns = 20

type Manager struct {
	mu      sync.Mutex
	jobs    map[string]*Job
	runners map[string]Runner
	runs    map[string][]RunRecord // jobID -> 倒序记录
	logger  *slog.Logger
}

func NewManager(logger *slog.Logger) *Manager {
	return &Manager{
		jobs: map[string]*Job{}, runners: map[string]Runner{}, runs: map[string][]RunRecord{},
		logger: logger,
	}
}

// Register 注册任务（默认停用，等待启用）。ID 即调度键。
func (m *Manager) Register(id, name, desc string, intervalSec int, run Runner) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runners[id] = run
	if _, ok := m.jobs[id]; !ok {
		m.jobs[id] = &Job{ID: id, Name: name, Description: desc,
			IntervalSec: intervalSec, Enabled: false,
			NextRun: time.Now().UTC().Add(time.Duration(intervalSec) * time.Second)}
	}
}

// SetEnabled 启停任务。
func (m *Manager) SetEnabled(id string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return ErrNotFound
	}
	j.Enabled = enabled
	j.NextRun = time.Now().UTC().Add(time.Duration(j.IntervalSec) * time.Second)
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

// exec 执行并记录（锁外运行任务体）。
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
	m.mu.Lock()
	j.LastRun = &rec.Start
	j.LastResult, j.LastErr = firstNonEmpty(rec.Result, rec.Err), err != nil
	j.NextRun = time.Now().UTC().Add(time.Duration(j.IntervalSec) * time.Second)
	m.runs[j.ID] = append([]RunRecord{rec}, m.runs[j.ID]...)
	if len(m.runs[j.ID]) > keepRuns {
		m.runs[j.ID] = m.runs[j.ID][:keepRuns]
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

// Runs 返回任务运行记录。
func (m *Manager) Runs(id string) []RunRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]RunRecord(nil), m.runs[id]...)
}

func itoa(n int) string {
	return json.Number(fmtInt(n)).String()
}

func fmtInt(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type Module struct {
	mgr    *Manager
	facade *identity.Facade
}

func New(logger *slog.Logger) *Module { return &Module{mgr: NewManager(logger)} }

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
	return nil
}

// Start 启动调度循环（1 秒粒度扫描，任务体在到期时执行）。
func (m *Module) Start(ctx context.Context) error {
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
	web.OK(w, m.mgr.Runs(r.PathValue("id")))
}

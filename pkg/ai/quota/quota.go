// Package quota 提供租户级 Token 配额管理（docs/03 P0 空白项）。
//
// v1.0 语义：按月度账期（UTC "YYYY-MM"）计量——跨账期自动开新账、
// 用量归零、限额延续；限额 <0 表示不限量；未显式设置的租户回落到
// 默认限额。存储可插拔（内存 / GORM），Manager 逻辑不感知实现。
package quota

import (
	"context"
	"sync"
	"time"
)

// Store 抽象配额持久化。limit 跨账期延续，usage 按账期隔离。
type Store interface {
	GetLimit(ctx context.Context, tenantID string) (int64, bool)
	SetLimit(ctx context.Context, tenantID string, limit int64) error
	GetUsage(ctx context.Context, tenantID, period string) (int64, bool)
	AddUsage(ctx context.Context, tenantID, period string, delta int64) error
}

// Manager 配额管理器。now 可注入用于账期测试。
type Manager struct {
	store Store
	deflt int64
	mu    sync.RWMutex
	now   func() time.Time
}

func NewManager(defaultLimit int64) *Manager {
	return NewManagerWithStore(NewMemStore(), defaultLimit)
}

func NewManagerWithStore(store Store, defaultLimit int64) *Manager {
	return &Manager{store: store, deflt: defaultLimit, now: time.Now}
}

// CurrentPeriod 返回当前月度账期（UTC）。
func (m *Manager) CurrentPeriod() string { return m.now().UTC().Format("2006-01") }

// SetClock 注入时钟函数（跨账期测试用）；生产代码不得调用。
func (m *Manager) SetClock(now func() time.Time) { m.now = now }

// SetLimit 设置租户限额（跨账期延续）。limit < 0 = 不限量。
func (m *Manager) SetLimit(tenantID string, limit int64) {
	_ = m.store.SetLimit(context.Background(), tenantID, limit)
}

// Limit 返回租户当前生效限额。
func (m *Manager) Limit(tenantID string) int64 {
	if l, ok := m.store.GetLimit(context.Background(), tenantID); ok {
		return l
	}
	return m.deflt
}

// Allow 预检：当前账期累计用量是否已到顶。n 为本次预估消耗。
func (m *Manager) Allow(tenantID string, n int64) bool {
	limit := m.Limit(tenantID)
	if limit < 0 {
		return true
	}
	used, _ := m.store.GetUsage(context.Background(), tenantID, m.CurrentPeriod())
	return used+n <= limit
}

// Consume 当前账期记账（以模型返回的真实 total_tokens 为准）。
func (m *Manager) Consume(tenantID string, tokens int64) {
	_ = m.store.AddUsage(context.Background(), tenantID, m.CurrentPeriod(), tokens)
}

// Usage 返回租户当前账期用量与限额。
func (m *Manager) Usage(tenantID string) (used, limit int64) {
	used, _ = m.store.GetUsage(context.Background(), tenantID, m.CurrentPeriod())
	return used, m.Limit(tenantID)
}

// ---------- 内存实现 ----------

type memStore struct {
	mu     sync.RWMutex
	limits map[string]int64
	usage  map[string]int64 // key: tenant + "\x00" + period
}

func NewMemStore() *memStore {
	return &memStore{limits: map[string]int64{}, usage: map[string]int64{}}
}

func (s *memStore) GetLimit(_ context.Context, t string) (int64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	l, ok := s.limits[t]
	return l, ok
}

func (s *memStore) SetLimit(_ context.Context, t string, limit int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limits[t] = limit
	return nil
}

func (s *memStore) GetUsage(_ context.Context, t, period string) (int64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.usage[t+"\x00"+period]
	return u, ok
}

func (s *memStore) AddUsage(_ context.Context, t, period string, delta int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usage[t+"\x00"+period] += delta
	return nil
}

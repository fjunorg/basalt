// Package aisafety 是 L2 AI 内容安全模块（docs/03 P0 合规空白项）：
// 敏感词库管理 + 按租户可配审核策略 + AI 调用链内容审核。
//
// 三类命中动作（对齐 gateway.Safety* 常量）：
//   - reject：输入侧 422 拦截 / 输出侧断流（OpenAI 风格 error 事件）
//   - replace：命中词替换为安全文本后放行（词库可配替换词，默认按词长 *）
//   - mark：原样放行，命中记入审计链（观察模式）
//
// 策略回落：租户策略 → 平台默认策略（tenant_id=""）→ 内置默认 reject
//（G 端合规哲学：默认从严，见 DefaultQuotaLimit 注释）。
package aisafety

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/basalt-go/basalt/pkg/ai/gateway"
)

const Version = "0.1.0"

// 权限点。
const (
	PermList   = "safety:list"
	PermManage = "safety:manage"
)

var (
	ErrNotFound = errors.New("safety: not found")
	ErrDupWord  = errors.New("safety: term word already exists")
	ErrBadAction = errors.New("safety: action must be reject, replace or mark")
)

// DefaultAction 是未配置任何策略时的内置默认动作（从严）。
const DefaultAction = gateway.SafetyReject

// Term 是一条敏感词。Replacement 为空时按词长生成 '*'。
type Term struct {
	ID          string    `json:"id" gorm:"primaryKey;size:40"`
	Word        string    `json:"word" gorm:"uniqueIndex;size:128"`
	Category    string    `json:"category,omitempty" gorm:"size:32"`
	Replacement string    `json:"replacement,omitempty" gorm:"size:128"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
}

// Policy 是租户级审核策略；TenantID 为空表示平台默认。
type Policy struct {
	TenantID  string    `json:"tenant_id" gorm:"primaryKey;size:64"`
	Action    string    `json:"action" gorm:"size:16"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Settings 是模块全局开关（单行，ID 恒为 1）。
type Settings struct {
	ID      int64     `json:"-" gorm:"primaryKey"`
	Enabled bool      `json:"enabled"`
	Updated time.Time `json:"updated_at"`
}

// SettingsID 是全局设置行的固定主键。
const SettingsID int64 = 1

// Store 抽象持久化（内存实现先行，GORM 见 stores/gormstore）。
type Store interface {
	CreateTerm(ctx context.Context, t Term) error
	UpdateTerm(ctx context.Context, t Term) error
	DeleteTerm(ctx context.Context, id string) error
	ListTerms(ctx context.Context) ([]Term, error)
	GetPolicy(ctx context.Context, tenantID string) (Policy, error)
	SetPolicy(ctx context.Context, p Policy) error // UPSERT
	ListPolicies(ctx context.Context) ([]Policy, error)
	GetSettings(ctx context.Context) (Settings, error)
	SaveSettings(ctx context.Context, s Settings) error
}

// ---------- 内存实现 ----------

type MemStore struct {
	mu       sync.RWMutex
	terms    map[string]Term
	policies map[string]Policy
	settings Settings
}

func NewMemStore() *MemStore {
	return &MemStore{
		terms:    map[string]Term{},
		policies: map[string]Policy{},
		settings: Settings{ID: SettingsID, Enabled: true, Updated: time.Now().UTC()},
	}
}

func (s *MemStore) CreateTerm(_ context.Context, t Term) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.terms[t.Word]; dup {
		return ErrDupWord
	}
	s.terms[t.Word] = t
	return nil
}

func (s *MemStore) UpdateTerm(_ context.Context, t Term) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var oldWord string
	found := false
	for w, e := range s.terms {
		if e.ID == t.ID {
			oldWord, found = w, true
			break
		}
	}
	if !found {
		return ErrNotFound
	}
	if t.Word != oldWord {
		if _, dup := s.terms[t.Word]; dup {
			return ErrDupWord
		}
		delete(s.terms, oldWord)
	}
	s.terms[t.Word] = t
	return nil
}

func (s *MemStore) DeleteTerm(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for w, e := range s.terms {
		if e.ID == id {
			delete(s.terms, w)
			return nil
		}
	}
	return ErrNotFound
}

func (s *MemStore) ListTerms(_ context.Context) ([]Term, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Term, 0, len(s.terms))
	for _, t := range s.terms {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Word < out[j].Word })
	return out, nil
}

func (s *MemStore) GetPolicy(_ context.Context, tenantID string) (Policy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.policies[tenantID]
	if !ok {
		return Policy{}, ErrNotFound
	}
	return p, nil
}

func (s *MemStore) SetPolicy(_ context.Context, p Policy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policies[p.TenantID] = p
	return nil
}

func (s *MemStore) ListPolicies(_ context.Context) ([]Policy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Policy, 0, len(s.policies))
	for _, p := range s.policies {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TenantID < out[j].TenantID })
	return out, nil
}

func (s *MemStore) GetSettings(_ context.Context) (Settings, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings, nil
}

func (s *MemStore) SaveSettings(_ context.Context, st Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st.ID = SettingsID
	s.settings = st
	return nil
}

// ---------- 匹配引擎 ----------

// Engine 是审核引擎：词库与策略以内存快照服务热路径（AI 调用链上
// 零 DB 查询），词库/策略/开关变更后 Reload 刷新。
type Engine struct {
	mu       sync.RWMutex
	byFirst  map[rune][]Term // 首字索引（启用词，归一小写）
	maxLen   int             // 最长词 rune 数（流式缓冲窗口）
	policies map[string]Policy
	settings Settings
}

func NewEngine() *Engine {
	return &Engine{
		byFirst:  map[rune][]Term{},
		policies: map[string]Policy{},
		settings: Settings{ID: SettingsID, Enabled: true, Updated: time.Now().UTC()},
	}
}

// Reload 从存储重建快照（词库/策略/开关变更后由模块调用）。
func (e *Engine) Reload(ctx context.Context, s Store) error {
	terms, err := s.ListTerms(ctx)
	if err != nil {
		return err
	}
	policies, err := s.ListPolicies(ctx)
	if err != nil {
		return err
	}
	settings, err := s.GetSettings(ctx)
	if err != nil {
		return err
	}
	byFirst := map[rune][]Term{}
	maxLen := 0
	for _, t := range terms {
		if !t.Enabled {
			continue
		}
		w := normalizeWord(t.Word)
		if w == "" {
			continue
		}
		r := []rune(w)
		byFirst[r[0]] = append(byFirst[r[0]], t)
		if len(r) > maxLen {
			maxLen = len(r)
		}
	}
	pm := make(map[string]Policy, len(policies))
	for _, p := range policies {
		pm[p.TenantID] = p
	}
	e.mu.Lock()
	e.byFirst, e.maxLen, e.policies, e.settings = byFirst, maxLen, pm, settings
	e.mu.Unlock()
	return nil
}

// Enabled 全局开关（gateway 零开销直通判定）。
func (e *Engine) Enabled() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.settings.Enabled
}

// CheckInput 实现 gateway.Inspector：逐条消息审核。任一消息命中
// reject 即拒绝（false, nil, viol）；replace 改写消息副本（不污染
// 调用方切片）；首个命中信息上报。
func (e *Engine) CheckInput(_ context.Context, tenantID string, msgs []gateway.Message) (bool, []gateway.Message, *gateway.SafetyViolation) {
	out := make([]gateway.Message, len(msgs))
	copy(out, msgs)
	var report *gateway.SafetyViolation
	for i := range out {
		filtered, viol := e.CheckText(tenantID, out[i].Content)
		if viol == nil {
			continue
		}
		if viol.Rejected() {
			return false, nil, viol
		}
		out[i].Content = filtered
		if report == nil {
			report = viol
		}
	}
	return true, out, report
}

// CheckOutput 实现 gateway.Inspector：非流式输出审核。
func (e *Engine) CheckOutput(_ context.Context, tenantID, text string) (string, *gateway.SafetyViolation) {
	return e.CheckText(tenantID, text)
}

// actionFor 策略回落：租户 → 平台默认 → 内置 reject。
func (e *Engine) actionFor(tenantID string) string {
	if p, ok := e.policies[tenantID]; ok && validAction(p.Action) {
		return p.Action
	}
	if p, ok := e.policies[""]; ok && validAction(p.Action) {
		return p.Action
	}
	return DefaultAction
}

func validAction(a string) bool {
	return a == gateway.SafetyReject || a == gateway.SafetyReplace || a == gateway.SafetyMark
}

// normalizeWord 词归一（小写 + 去首尾空白）。
func normalizeWord(w string) string { return strings.ToLower(strings.TrimSpace(w)) }

// normalizeText 文本归一（与词归一同一规则，保证命中一致）。
func normalizeText(t string) string { return strings.ToLower(t) }

// scan 在归一文本 runes 中查找第一个命中的启用词。
// 返回（词、原文起始位置、原文结束位置）。ok=false 表示无命中。
func (e *Engine) scan(runes []rune) (Term, int, int, bool) {
	for i := 0; i < len(runes); i++ {
		cands, ok := e.byFirst[runes[i]]
		if !ok {
			continue
		}
		for _, t := range cands {
			w := []rune(normalizeWord(t.Word))
			end := i + len(w)
			if end <= len(runes) && string(runes[i:end]) == string(w) {
				return t, i, end, true
			}
		}
	}
	return Term{}, 0, 0, false
}

// replacementFor 词的替换文本（词库配置或按词长 *）。
func replacementFor(t Term) string {
	if t.Replacement != "" {
		return t.Replacement
	}
	return strings.Repeat("*", len([]rune(t.Word)))
}

// CheckText 对完整文本执行审核：返回处理后的文本与命中信息。
// mark 动作返回原文；reject 动作文本无意义（调用方应拒绝）。
func (e *Engine) CheckText(tenantID, text string) (string, *gateway.SafetyViolation) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.maxLen == 0 {
		return text, nil // 空词库：直通
	}
	action := e.actionFor(tenantID)
	if action == gateway.SafetyMark {
		if _, _, _, hit := e.scan([]rune(normalizeText(text))); hit {
			// 观察模式：记录全部命中词后原样放行
			return text, &gateway.SafetyViolation{Action: action, Words: e.allHits(text)}
		}
		return text, nil
	}
	runes := []rune(text)
	lower := []rune(normalizeText(text))
	var words []string
	// replace / reject 都需要定位命中；replace 循环替换直到无命中
	//（替换文本不参与再匹配，天然收敛）。
	for {
		t, start, end, ok := e.scan(lower)
		if !ok {
			break
		}
		words = append(words, t.Word)
		if action == gateway.SafetyReject {
			return text, &gateway.SafetyViolation{Action: action, Words: words}
		}
		rep := []rune(replacementFor(t))
		// 同步替换原文与归一文本（等长性不保证，两份分别拼接）
		runes = append(runes[:start], append(rep, runes[end:]...)...)
		lower = append(lower[:start], append([]rune(normalizeText(replacementFor(t))), lower[end:]...)...)
	}
	if len(words) == 0 {
		return text, nil
	}
	return string(runes), &gateway.SafetyViolation{Action: action, Words: words}
}

// allHits 收集文本中的全部命中词（mark 模式审计用）。
func (e *Engine) allHits(text string) []string {
	lower := []rune(normalizeText(text))
	seen := map[string]bool{}
	var out []string
	for i := 0; i < len(lower); {
		if t, _, end, ok := e.scan(lower[i:]); ok {
			if !seen[t.Word] {
				seen[t.Word] = true
				out = append(out, t.Word)
			}
			i += end // end 是相对 lower[i:] 的终点偏移
			continue
		}
		i++
	}
	return out
}

// scanHit 带读锁的全量命中探测（streamChecker 调用方未持锁）。
func (e *Engine) scanHit(text string) (Term, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.maxLen == 0 {
		return Term{}, false
	}
	t, _, _, ok := e.scan([]rune(normalizeText(text)))
	return t, ok
}

// scanRange 带读锁的定位扫描：返回首个命中的词与 [start, end) 区间。
func (e *Engine) scanRange(runes []rune) (Term, int, int, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.maxLen == 0 {
		return Term{}, 0, 0, false
	}
	return e.scan(runes)
}

// NewStreamChecker 构造流式检查器。跨 chunk 词边界由「尾部窗口
// 缓冲」处理：每轮保留最长词长-1 个 rune 不下发，下一 chunk 到来
// 时与窗口拼接；命中处理始终作用于全量 pending（先替换/判定再
// 切分安全前缀），词头不会被切进已下发前缀。残余在 Finish 冲洗。
func (e *Engine) NewStreamChecker(tenantID string) gateway.StreamChecker {
	e.mu.RLock()
	maxLen := e.maxLen
	action := e.actionFor(tenantID)
	e.mu.RUnlock()
	return &streamChecker{engine: e, tenant: tenantID, maxLen: maxLen, action: action}
}

// streamChecker 每次流式调用一个实例（非并发安全）。
type streamChecker struct {
	engine  *Engine
	tenant  string
	maxLen  int
	action  string
	pending []rune // 尾部窗口缓冲（上一 chunk 未下发的窗口）
}

// Chunk 处理增量文本，返回可安全下发的前缀（可能为空——窗口未满）。
func (c *streamChecker) Chunk(text string) (string, *gateway.SafetyViolation) {
	c.pending = append(c.pending, []rune(text)...)
	keep := c.maxLen - 1
	if keep < 0 {
		keep = 0
	}

	// 第一步：命中处理始终作用于全量 pending（含窗口）。
	if t, s, e2, hit := c.engine.scanRange([]rune(normalizeText(string(c.pending)))); hit {
		switch c.action {
		case gateway.SafetyReject:
			return "", &gateway.SafetyViolation{Action: c.action, Words: []string{t.Word}}
		case gateway.SafetyReplace:
			var out []rune
			out = append(out, c.pending[:s]...)
			out = append(out, []rune(replacementFor(t))...)
			c.pending = append([]rune(nil), c.pending[e2:]...)
			// 继续消费 pending 内的后续完整命中（替换文本不再参与匹配）
			for {
				t2, s2, e3, hit2 := c.engine.scanRange([]rune(normalizeText(string(c.pending))))
				if !hit2 {
					break
				}
				out = append(out, c.pending[:s2]...)
				out = append(out, []rune(replacementFor(t2))...)
				c.pending = append([]rune(nil), c.pending[e3:]...)
			}
			// 第二步：无命中残余，输出排除尾部窗口的安全前缀
			if len(c.pending) > keep {
				out = append(out, c.pending[:len(c.pending)-keep]...)
				c.pending = append([]rune(nil), c.pending[len(c.pending)-keep:]...)
			}
			return string(out), &gateway.SafetyViolation{Action: c.action, Words: []string{t.Word}}
		}
	}

	// 无命中（或 mark）：输出排除尾部窗口的前缀；mark 原样转发并上报。
	if len(c.pending) <= keep {
		return "", nil
	}
	split := len(c.pending) - keep
	prefix := string(c.pending[:split])
	c.pending = append([]rune(nil), c.pending[split:]...)
	if c.action == gateway.SafetyMark {
		if t, hit := c.engine.scanHit(prefix); hit {
			return prefix, &gateway.SafetyViolation{Action: c.action, Words: []string{t.Word}}
		}
		return prefix, nil
	}
	return prefix, nil
}

// Finish 冲洗残余缓冲（[DONE] 前调用）。
func (c *streamChecker) Finish() (string, *gateway.SafetyViolation) {
	rest := string(c.pending)
	c.pending = c.pending[:0]
	if rest == "" {
		return "", nil
	}
	if c.action == gateway.SafetyMark {
		if t, hit := c.engine.scanHit(rest); hit {
			return rest, &gateway.SafetyViolation{Action: c.action, Words: []string{t.Word}}
		}
		return rest, nil
	}
	filtered, viol := c.engine.CheckText(c.tenant, rest)
	return filtered, viol
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

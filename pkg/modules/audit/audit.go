// Package audit 是 L1 防篡改审计模块（M2 交付）。
//
// 每条审计事件携带前一条的哈希形成链（类区块链结构）：单条记录被
// 修改或删除时，其后所有记录的校验立即失败。哈希算法经
// kernel/crypto Provider 注入——当前 SHA-256 参考实现，接入
// emmansun/gmsm 后自动升级为 SM3（docs/03 的 G 端差异化项）。
//
// 事件来源：模块经 Recorder 写入（identity/tenant/workflow/gateway
// 已接入）；等保场景审计日志留存策略（≥6 个月）由存储实现负责。
package audit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// 事件模块标识。
const (
	ModIdentity = "identity"
	ModTenant   = "tenant"
	ModWorkflow = "workflow"
	ModAIGw     = "ai-gateway"
	ModAudit    = "audit"
)

// Event 是一条不可变审计记录。Hash 覆盖除自身外的全部字段 + PrevHash。
// Seq 为持久化链序（自增，内存实现不使用）。
type Event struct {
	Seq       uint64          `json:"seq" gorm:"primaryKey;autoIncrement"`
	ID        string          `json:"id" gorm:"uniqueIndex;size:40"`
	Time      time.Time       `json:"time" gorm:"index"`
	Module    string          `json:"module" gorm:"size:32;index"`
	Event     string          `json:"event" gorm:"size:64"` // 如 login.success / user.created
	ActorID   string          `json:"actor_id" gorm:"size:40;index"`
	ActorName string          `json:"actor_name" gorm:"size:64"`
	Resource  string          `json:"resource,omitempty" gorm:"size:128"` // 操作对象，如 user_id
	Remote    string          `json:"remote,omitempty" gorm:"size:64"`
	Detail    json.RawMessage `json:"detail,omitempty" gorm:"serializer:json"`
	PrevHash  string          `json:"prev_hash" gorm:"size:64"`
	Hash      string          `json:"hash" gorm:"size:64"`
}

var ErrChainBroken = errors.New("audit: hash chain verification failed")

// Hasher 抽象链式哈希（由 kernel/crypto Provider 适配）。
type Hasher interface {
	Hash(ctx context.Context, data []byte) ([]byte, error)
	Algorithm() string
}

// Store 抽象审计持久化。Append 必须串行化保证链一致。
type Store interface {
	Append(ctx context.Context, e Event) error
	List(ctx context.Context, module string, limit int) ([]Event, error)
	// Verify 全链校验，返回首个断点序号（1-based）与错误。
	Verify(ctx context.Context) (int, error)
}

// Recorder 是模块写审计的通道（identity Facade 等持有）。
type Recorder interface {
	Record(ctx context.Context, e Event) error
}

// Chain 是默认实现：内存存储 + 链式哈希。
type Chain struct {
	mu     sync.Mutex
	events []Event
	hasher Hasher
}

func NewChain(h Hasher) *Chain {
	return &Chain{hasher: h}
}

// Record 实现 Recorder 接口（与 Append 等价，面向模块消费方命名）。
func (c *Chain) Record(ctx context.Context, e Event) error { return c.Append(ctx, e) }

// Append 追加事件并计算链哈希。e.ID/Time 为空时自动填充。
func (c *Chain) Append(ctx context.Context, e Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e.ID == "" {
		e.ID = NewEventID()
	}
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	if len(c.events) > 0 {
		e.PrevHash = c.events[len(c.events)-1].Hash
	}
	digest, err := canonical(c.hasher, ctx, e)
	if err != nil {
		return err
	}
	e.Hash = digest
	c.events = append(c.events, e)
	return nil
}

func (c *Chain) List(_ context.Context, module string, limit int) ([]Event, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Event
	for i := len(c.events) - 1; i >= 0 && (limit <= 0 || len(out) < limit); i-- {
		if module == "" || c.events[i].Module == module {
			out = append(out, c.events[i])
		}
	}
	return out, nil
}

// Verify 校验链条完整性：重算每条哈希并比对 PrevHash 链接。
// 任何记录被篡改或删除都会在断点处暴露。
func (c *Chain) Verify(ctx context.Context) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prev := ""
	for i, e := range c.events {
		if e.PrevHash != prev {
			return i + 1, ErrChainBroken
		}
		digest, err := canonical(c.hasher, ctx, e)
		if err != nil {
			return i + 1, err
		}
		if digest != e.Hash {
			return i + 1, ErrChainBroken
		}
		prev = e.Hash
	}
	return 0, nil
}

// canonical 生成事件的规范序列化哈希（不含 Hash 与 Seq 字段：
// 前者是被计算对象，后者是持久化层排序细节）。
func canonical(h Hasher, ctx context.Context, e Event) (string, error) {
	e.Hash = ""
	e.Seq = 0
	b, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	d, err := h.Hash(ctx, b)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(d), nil
}

// NewEventID 生成审计事件 ID（供内存链与持久化链共用）。
func NewEventID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("audit: crypto/rand unavailable: %v", err))
	}
	return hex.EncodeToString(b)
}

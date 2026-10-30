package gormstore

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/basalt-go/basalt/pkg/modules/audit"
)

// AuditChain 是审计链的 GORM 持久化实现：重启后从最后一条记录续链。
//
// 单实例写安全（进程内互斥）；多实例部署需在数据库层引入
// 写者互斥（Select for update / 分布式锁），随微服务形态交付。
type AuditChain struct {
	db     *gorm.DB
	hasher audit.Hasher
	mu     sync.Mutex
}

// NewAuditChain 迁移表结构并返回链实现。
func NewAuditChain(db *gorm.DB, h audit.Hasher) (*AuditChain, error) {
	if err := db.AutoMigrate(&audit.Event{}); err != nil {
		return nil, err
	}
	return &AuditChain{db: db, hasher: h}, nil
}

// Record 实现 audit.Recorder。
func (c *AuditChain) Record(ctx context.Context, e audit.Event) error {
	return c.Append(ctx, e)
}

// Append 追加事件：读最后一条哈希 → 续链 → 落库。
func (c *AuditChain) Append(ctx context.Context, e audit.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e.ID == "" {
		e.ID = audit.NewEventID()
	}
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	// 时间统一 UTC+秒级截断：SQLite TEXT 时间往返会丢失纳秒与时区
	// 信息，写入时即规范化才能保证哈希跨往返稳定。
	e.Time = e.Time.UTC().Truncate(time.Second)
	var last audit.Event
	err := c.db.WithContext(ctx).Order("seq DESC").First(&last).Error
	switch {
	case err == nil:
		e.PrevHash = last.Hash
	case errors.Is(err, gorm.ErrRecordNotFound):
		e.PrevHash = "" // 链头
	default:
		return err
	}
	digest, err := c.canonical(e)
	if err != nil {
		return err
	}
	e.Hash = digest
	return c.db.WithContext(ctx).Create(&e).Error
}

// List 倒序（最新在前），支持模块过滤与条数上限。
func (c *AuditChain) List(ctx context.Context, module string, limit int) ([]audit.Event, error) {
	q := c.db.WithContext(ctx).Model(&audit.Event{}).Order("seq DESC")
	if module != "" {
		q = q.Where("module = ?", module)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	var out []audit.Event
	err := q.Find(&out).Error
	return out, err
}

// Verify 按 Seq 升序全链校验，返回首个断点（1-based）。
func (c *AuditChain) Verify(ctx context.Context) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var events []audit.Event
	if err := c.db.WithContext(ctx).Order("seq ASC").Find(&events).Error; err != nil {
		return 0, err
	}
	prev := ""
	for i, e := range events {
		if e.PrevHash != prev {
			return i + 1, audit.ErrChainBroken
		}
		digest, err := c.canonical(e)
		if err != nil {
			return i + 1, err
		}
		if digest != e.Hash {
			return i + 1, audit.ErrChainBroken
		}
		prev = e.Hash
	}
	return 0, nil
}

// canonical 与内存链一致的规范序列化。Hash 与 Seq 清零：Hash 是被
// 计算对象本身；Seq 是数据库自增排序键，落库后才产生，链序由
// PrevHash 保证，Seq 不参与哈希。
func (c *AuditChain) canonical(e audit.Event) (string, error) {
	e.Hash = ""
	e.Seq = 0
	e.Time = e.Time.UTC().Truncate(time.Second)
	b, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	d, err := c.hasher.Hash(context.Background(), b)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(d), nil
}

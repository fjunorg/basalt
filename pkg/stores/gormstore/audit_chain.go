package gormstore

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/basalt-go/basalt/pkg/kernel/crypto"
	"github.com/basalt-go/basalt/pkg/modules/audit"
)

// AuditChain 是审计链的 GORM 持久化实现：重启后从最后一条记录续链。
//
// 单实例写安全（进程内互斥）；多实例部署需在数据库层引入
// 写者互斥（Select for update / 分布式锁），随微服务形态交付。
type AuditChain struct {
	db     *gorm.DB
	hasher audit.Hasher
	// altHasher 备用算法哈希器：BASALT_CRYPTO_ALGO 中途切换（如
	// sha256→sm3）后，历史段记录仍以原算法落链。Verify 对不匹配
	// 主算法的段尝试备用算法——历史算法段核验通过（防"算法已切换"
	// 被误报为链断裂/篡改）；两种算法均不匹配才是真断链。Append
	// 恒用主算法（当前配置），链因此自然形成算法分段。
	altHasher audit.Hasher
	mu        sync.Mutex
}

// NewAuditChain 迁移表结构并返回链实现。
func NewAuditChain(db *gorm.DB, h audit.Hasher) (*AuditChain, error) {
	if err := db.AutoMigrate(&audit.Event{}, &audit.ChainSignature{}); err != nil {
		return nil, err
	}
	c := &AuditChain{db: db, hasher: h}
	// 补另一族算法（SM3 ↔ SHA-256）作为历史段核验 fallback。
	switch h.Algorithm() {
	case "SM3":
		c.altHasher = sha256Fallback{}
	case "SHA-256":
		c.altHasher = sm3Fallback{}
	}
	return c, nil
}

type sha256Fallback struct{}

func (sha256Fallback) Hash(ctx context.Context, data []byte) ([]byte, error) {
	return crypto.NewProvider(crypto.AlgoSHA256).Hash(ctx, data)
}
func (sha256Fallback) Algorithm() string { return "SHA-256" }

type sm3Fallback struct{}

func (sm3Fallback) Hash(ctx context.Context, data []byte) ([]byte, error) {
	return crypto.NewProvider(crypto.AlgoSM3).Hash(ctx, data)
}
func (sm3Fallback) Algorithm() string { return "SM3" }

// 编译期接口对齐检查：链尾签名扩展。
var _ audit.SignedChain = (*AuditChain)(nil)

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
	return list(out), err
}

// Verify 按 Seq 升序全链校验，返回首个断点（1-based）。
//
// 多算法逐段核验：每段先用当前主算法重算，不匹配再试备用算法
// （覆盖 BASALT_CRYPTO_ALGO 中途切换前的历史段）；两种均不匹配才
// 判断裂——错误信息注明算法切换的可能形态，避免误导为篡改。
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
			return i + 1, fmt.Errorf("%w at %d (prev-hash mismatch; 若 BASALT_CRYPTO_ALGO 中途切换过，历史段以原算法落链，本核验已自动兼容)", audit.ErrChainBroken, i+1)
		}
		digest, err := c.canonical(e)
		if err != nil {
			return i + 1, err
		}
		if digest != e.Hash {
			// 主算法不匹配：尝试备用算法（历史段可能是切换前的算法）
			if c.altHasher != nil {
				if alt := c.canonicalWith(c.altHasher, e); alt == e.Hash {
					prev = e.Hash
					continue // 历史算法段，核验通过
				}
			}
			return i + 1, fmt.Errorf("%w at %d (both %s and fallback mismatch; tamper or unknown legacy algo)", audit.ErrChainBroken, i+1, c.hasher.Algorithm())
		}
		prev = e.Hash
	}
	return 0, nil
}

// canonicalWith 以指定哈希器计算规范摘要。
func (c *AuditChain) canonicalWith(h audit.Hasher, e audit.Event) string {
	e.Hash = ""
	e.Seq = 0
	e.Time = e.Time.UTC().Truncate(time.Second)
	b, err := json.Marshal(e)
	if err != nil {
		return ""
	}
	d, err := h.Hash(context.Background(), b)
	if err != nil {
		return ""
	}
	return hex.EncodeToString(d)
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

// ---------- 链尾签名（audit.SignedChain 实现） ----------

// SignHead 对当前链尾 (seq, hash) 签名并持久化。同 seq 重复签名
// 覆盖旧记录（重签即更新时点锚，非追加语义——链尾只会前进）。
func (c *AuditChain) SignHead(ctx context.Context, s audit.ChainSigner) (audit.ChainSignature, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var last audit.Event
	if err := c.db.WithContext(ctx).Order("seq DESC").First(&last).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return audit.ChainSignature{}, audit.ErrEmptyChain
		}
		return audit.ChainSignature{}, err
	}
	data := audit.ChainSignData(last.Seq, last.Hash)
	sig, err := s.Sign(ctx, data)
	if err != nil {
		return audit.ChainSignature{}, fmt.Errorf("audit: sign head: %w", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	rec := audit.ChainSignature{
		Seq: last.Seq, Hash: last.Hash,
		Sig: hex.EncodeToString(sig), Algo: s.Algorithm(),
		PubKey: s.PublicKeyHex(), SignTime: now,
	}
	if err := c.db.WithContext(ctx).Save(&rec).Error; err != nil {
		return audit.ChainSignature{}, err
	}
	return rec, nil
}

// VerifyHeadSignatures 核验签名记录：SigOK（签名密码学有效，按
// 记录内公钥）+ ChainOK（该 seq 链事件在位且哈希一致）。
func (c *AuditChain) VerifyHeadSignatures(ctx context.Context, verifier audit.ChainSigner, limit int) ([]audit.ChainSignatureCheck, error) {
	q := c.db.WithContext(ctx).Model(&audit.ChainSignature{}).Order("seq DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var recs []audit.ChainSignature
	if err := q.Find(&recs).Error; err != nil {
		return nil, err
	}
	out := make([]audit.ChainSignatureCheck, 0, len(recs))
	for _, rec := range recs {
		check := audit.ChainSignatureCheck{ChainSignature: rec, ChainOK: false}
		sig, err := hex.DecodeString(rec.Sig)
		if err == nil {
			check.SigOK, _ = verifier.VerifyWith(ctx, rec.PubKey, audit.ChainSignData(rec.Seq, rec.Hash), sig)
		}
		var ev audit.Event
		if err := c.db.WithContext(ctx).First(&ev, "seq = ?", rec.Seq).Error; err == nil {
			check.ChainOK = ev.Hash == rec.Hash
		}
		out = append(out, check)
	}
	return out, nil
}

// ListHeadSignatures 倒序列出签名记录。
func (c *AuditChain) ListHeadSignatures(ctx context.Context, limit int) ([]audit.ChainSignature, error) {
	q := c.db.WithContext(ctx).Model(&audit.ChainSignature{}).Order("seq DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var out []audit.ChainSignature
	err := q.Find(&out).Error
	return list(out), err
}

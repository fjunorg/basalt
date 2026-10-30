package audit

import (
	"context"
	"fmt"
	"time"
)

// ---------- 链尾签名（防整库重算伪造历史） ----------

// 哈希链防单点篡改（改一条链断），但不防"拥有全部数据的攻击者
// 整库重算"——重算者可构造一条全新的自洽链替换历史。链尾签名补上
// 这一环：签名私钥不入库，重算者无法对历史时点的链尾再签名。
// 签名记录持久化（公钥随记），事后任意时刻可独立核验：
//   - SigOK   = 签名密码学有效（该公钥确实签过该 seq+hash）
//   - ChainOK = 该 seq 的链事件仍在且哈希一致（历史未被删改）
// 防抵赖语义：重算/删改历史 → ChainOK=false；伪造签名 → SigOK=false。

// ChainSignature 是一条链尾签名记录。
type ChainSignature struct {
	Seq      uint64    `json:"seq" gorm:"primaryKey"` // 被签名链尾的 Seq
	Hash     string    `json:"hash" gorm:"size:64"`   // 被签名链尾哈希
	Sig      string    `json:"sig" gorm:"size:512"`   // ASN.1 签名 hex
	Algo     string    `json:"algo" gorm:"size:16"`   // 如 SM2
	PubKey   string    `json:"pub_key" gorm:"size:200"`
	SignTime time.Time `json:"sign_time"`
}

// ChainSignatureCheck 是一条签名记录的核验结论。
type ChainSignatureCheck struct {
	ChainSignature
	SigOK   bool `json:"sig_ok"`   // 签名密码学有效
	ChainOK bool `json:"chain_ok"` // 对应链事件在位且哈希一致
}

// ChainSigner 抽象链尾签名器（kernel/crypto 的 SM2Provider 实现；
// 接口缝：HSM/密钥服务落地后在此替换，模块零改动）。
type ChainSigner interface {
	// Sign 对数据签名（SM2 语义：消息原文，内部 ZA||M → SM3）。
	Sign(ctx context.Context, data []byte) ([]byte, error)
	// VerifyWith 按给定 hex 公钥验签（第三方公钥，验签方无私钥）。
	VerifyWith(ctx context.Context, pubKeyHex string, data, sig []byte) (bool, error)
	// Algorithm 算法标识（审计日志）。
	Algorithm() string
	// PublicKeyHex 签名方公钥（随签名记录持久化）。
	PublicKeyHex() string
}

// SignedChain 是持久化链的链尾签名扩展能力（AuditChain GORM 实现）。
type SignedChain interface {
	// SignHead 对当前链尾签名并持久化记录。空链返回 ErrEmptyChain。
	SignHead(ctx context.Context, s ChainSigner) (ChainSignature, error)
	// VerifyHeadSignatures 核验全部（或最近 limit 条）签名记录。
	VerifyHeadSignatures(ctx context.Context, verifier ChainSigner, limit int) ([]ChainSignatureCheck, error)
	// ListHeadSignatures 列出签名记录（倒序，最新在前）。
	ListHeadSignatures(ctx context.Context, limit int) ([]ChainSignature, error)
}

// ErrEmptyChain 空链不可签名。
var ErrEmptyChain = fmt.Errorf("audit: empty chain cannot be signed")

// ChainSignData 链尾签名的规范化被签数据（跨实现单一事实来源）：
// "basalt-audit-head|{seq}|{hash}"——前缀防跨域签名重用。
func ChainSignData(seq uint64, hash string) []byte {
	return []byte(fmt.Sprintf("basalt-audit-head|%d|%s", seq, hash))
}

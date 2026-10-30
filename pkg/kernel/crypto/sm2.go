package crypto

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"

	"github.com/emmansun/gmsm/sm2"
)

// DefaultSM2UID 国密默认用户标识（GB/T 32918.2-2016 附录 A）。
const DefaultSM2UID = "1234567812345678"

// SM2Provider 是国密 SM2 签名 + SM3 摘要实现。
//
// Sign/Verify 语义（国密标准模式 GB/T 32918.2）：入参为**消息原文**，
// 内部计算 ZA(UID)||M 的 SM3 摘要后签名/验签——与官方向量可直接
// 对齐，也是合规签名形态（Z 值参与运算）。Hash 为 SM3，与
// sm3Provider 一致：CRYPTO_ALGO=sm3→sm2 是纯增量（哈希链路不变），
// 差异仅在 Sign/Verify 从"planned 报错"变为可用。
//
// 并发安全（gmsm 私钥签名无内部可变状态）。
type SM2Provider struct {
	priv      *sm2.PrivateKey
	uid       []byte
	generated bool // 密钥为进程内生成（未显式配置，重启换钥）
}

// NewSM2Provider 从 hex 私钥构造（32 字节，GB/T 32918.1 私钥范围校验
// 由 gmsm 完成）。uid 空用 DefaultSM2UID。
func NewSM2Provider(privHex, uid string) (*SM2Provider, error) {
	b, err := hex.DecodeString(privHex)
	if err != nil {
		return nil, fmt.Errorf("crypto: SM2 private key must be hex: %w", err)
	}
	if len(b) != 32 {
		return nil, errors.New("crypto: SM2 private key must be 32 bytes (64 hex chars)")
	}
	priv, err := sm2.NewPrivateKey(b)
	if err != nil {
		return nil, fmt.Errorf("crypto: SM2 private key: %w", err)
	}
	return &SM2Provider{priv: priv, uid: sm2UID(uid)}, nil
}

// GenerateSM2Provider 随机生成密钥对。适用于开发/演示与"未配置私钥
// 但启用了签名开关"的降级路径——重启换钥，仅影响新签名（历史签名
// 的公钥随签名记录持久化，验签不受影响）。
func GenerateSM2Provider(uid string) (*SM2Provider, error) {
	priv, err := sm2.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("crypto: SM2 generate key: %w", err)
	}
	return &SM2Provider{priv: priv, uid: sm2UID(uid), generated: true}, nil
}

// NewSM2ProviderFromEnv 经 BASALT_SM2_PRIVATE_KEY/BASALT_SM2_UID 构造；
// 私钥未配置时生成进程内密钥（Generated()==true，调用方应日志提示）。
func NewSM2ProviderFromEnv() (*SM2Provider, error) {
	if hexKey := os.Getenv("BASALT_SM2_PRIVATE_KEY"); hexKey != "" {
		return NewSM2Provider(hexKey, os.Getenv("BASALT_SM2_UID"))
	}
	return GenerateSM2Provider(os.Getenv("BASALT_SM2_UID"))
}

func sm2UID(uid string) []byte {
	if uid == "" {
		return []byte(DefaultSM2UID)
	}
	return []byte(uid)
}

// Hash 返回 SM3 摘要（与 sm3Provider 等价）。
func (p *SM2Provider) Hash(_ context.Context, data []byte) ([]byte, error) {
	return (sm3Provider{}).Hash(context.Background(), data)
}

// Sign 对消息原文做国密标准签名（内部 ZA||M → SM3 → SM2），
// 返回 ASN.1 DER 签名（r,s 序列）。
func (p *SM2Provider) Sign(_ context.Context, msg []byte) ([]byte, error) {
	return p.priv.Sign(rand.Reader, msg, sm2.NewSM2SignerOption(true, p.uid))
}

// Verify 校验本方公钥下的签名（消息原文语义同 Sign）。
func (p *SM2Provider) Verify(_ context.Context, msg, sig []byte) (bool, error) {
	return sm2.VerifyASN1WithSM2(&p.priv.PublicKey, p.uid, msg, sig), nil
}

// VerifyWith 按任意 hex 公钥验签（验签方不持有私钥的场景，如审计
// 链尾签名核验：公钥随签名记录持久化）。
func (p *SM2Provider) VerifyWith(_ context.Context, pubKeyHex string, msg, sig []byte) (bool, error) {
	pub, err := sm2.NewPublicKey(mustHex(pubKeyHex))
	if err != nil {
		return false, fmt.Errorf("crypto: SM2 public key: %w", err)
	}
	return sm2.VerifyASN1WithSM2(pub, p.uid, msg, sig), nil
}

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}

// Algorithm 返回算法标识（审计日志用）。
func (*SM2Provider) Algorithm() string { return "SM2" }

// PublicKeyHex 返回未压缩公钥（04||X||Y，65 字节 hex）。
func (p *SM2Provider) PublicKeyHex() string {
	buf := make([]byte, 65)
	buf[0] = 4
	p.priv.PublicKey.X.FillBytes(buf[1:33])
	p.priv.PublicKey.Y.FillBytes(buf[33:65])
	return hex.EncodeToString(buf)
}

// UID 返回当前用户标识。
func (p *SM2Provider) UID() string { return string(p.uid) }

// Generated 密钥是否为进程内生成（非显式配置）。
func (p *SM2Provider) Generated() bool { return p.generated }

// PrivateKeyHex 导出私钥 hex（仅运维初始化场景使用，勿入日志）。
func (p *SM2Provider) PrivateKeyHex() string {
	buf := make([]byte, 32)
	p.priv.D.FillBytes(buf)
	return hex.EncodeToString(buf)
}

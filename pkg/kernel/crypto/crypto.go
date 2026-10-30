// Package crypto 定义国密算法抽象。
//
// 接口先行：骨架阶段提供 SHA-256 参考实现保证链路可用；M2 接入
// emmansun/gmsm（Apache 2.0）提供 SM2/SM3/SM4 与 GMTLS 生产实现。
// 业务代码只依赖本包接口，不直接触达具体国密库。
package crypto

import "context"

// Provider 是密码学能力抽象。实现必须并发安全。
type Provider interface {
	// Hash 返回数据的摘要，算法由实现决定（SM3 或 SHA-256）。
	Hash(ctx context.Context, data []byte) ([]byte, error)
	// Sign 用私钥对摘要签名（SM2/ECDSA）。
	Sign(ctx context.Context, digest []byte) ([]byte, error)
	// Verify 校验签名。
	Verify(ctx context.Context, digest, signature []byte) (bool, error)
	// Algorithm 返回算法标识，用于审计日志（如 "SM3"、"SHA-256"）。
	Algorithm() string
}

// 算法标识。信创/等保场景选 SM3；非国密环境用 SHA-256。
const (
	AlgoSM3    = "sm3"
	AlgoSHA256 = "sha256"
)

// NewProvider 按算法标识构造 Provider；未知标识回落 SHA-256 并由
// 调用方日志可见（Algorithm()）。
func NewProvider(algo string) Provider {
	if algo == AlgoSM3 {
		return sm3Provider{}
	}
	return sha256Provider{}
}

// Default 返回当前默认 Provider（SHA-256 参考实现）。
// 生产按配置 BASALT_CRYPTO_ALGO 经 NewProvider 选择。
func Default() Provider { return sha256Provider{} }

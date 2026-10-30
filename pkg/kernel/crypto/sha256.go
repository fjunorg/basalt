package crypto

import (
	"context"
	"crypto/sha256"
	"fmt"
)

// sha256Provider 是非国密环境的参考实现，仅用于骨架联通与测试，
// 不得用于等保/国密合规场景（生产由 gmsm 实现替代）。
type sha256Provider struct{}

func (sha256Provider) Hash(_ context.Context, data []byte) ([]byte, error) {
	h := sha256.Sum256(data)
	return h[:], nil
}

func (p sha256Provider) Sign(_ context.Context, digest []byte) ([]byte, error) {
	// 参考实现不做真实签名（无密钥体系），仅回写摘要哈希占位，
	// 保证接口形状在骨架阶段可被模块消费与测试。
	h := sha256.Sum256(digest)
	return h[:], nil
}

func (sha256Provider) Verify(_ context.Context, digest, signature []byte) (bool, error) {
	h := sha256.Sum256(digest)
	if len(signature) != len(h) {
		return false, fmt.Errorf("crypto: signature length mismatch")
	}
	for i := range h {
		if h[i] != signature[i] {
			return false, nil
		}
	}
	return true, nil
}

func (sha256Provider) Algorithm() string { return "SHA-256" }

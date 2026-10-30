package crypto

import (
	"context"
	"errors"

	"github.com/emmansun/gmsm/sm3"
)

// sm3Provider 是国密 SM3 摘要实现（信创合规默认推荐）。
// Hash 用于审计链与完整性校验；Sign/Verify 属非对称能力（SM2），
// 依赖密钥管理体系，M1 后期密钥服务落地前返回显式错误。
type sm3Provider struct{}

func (sm3Provider) Hash(_ context.Context, data []byte) ([]byte, error) {
	h := sm3.New()
	h.Write(data)
	return h.Sum(nil), nil
}

func (sm3Provider) Sign(_ context.Context, _ []byte) ([]byte, error) {
	return nil, errors.New("crypto: SM2 sign requires key management (planned), use SM3 hash only")
}

func (sm3Provider) Verify(_ context.Context, _, _ []byte) (bool, error) {
	return false, errors.New("crypto: SM2 verify requires key management (planned), use SM3 hash only")
}

func (sm3Provider) Algorithm() string { return "SM3" }

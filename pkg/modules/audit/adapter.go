package audit

import (
	"context"

	"github.com/basalt-go/basalt/pkg/kernel/crypto"
)

// hasherAdapter 把 kernel/crypto Provider 桥接为审计链哈希器。
// 切换国密（SM3）时仅需更换 Provider 实现，链结构与校验逻辑不变。
type hasherAdapter struct{ p crypto.Provider }

func (h hasherAdapter) Hash(ctx context.Context, data []byte) ([]byte, error) {
	return h.p.Hash(ctx, data)
}

func (h hasherAdapter) Algorithm() string { return h.p.Algorithm() }

var _ Hasher = hasherAdapter{}

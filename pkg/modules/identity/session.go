package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"
)

// 在线用户子域（竞品 Gap P0#5）：会话注册表 + 在线列表 + 踢下线。
//
// JWT 无状态下的吊销机制选型（任务要求说明取舍）：
//   - 方案 A：短 TTL + refresh 轮换——踢下线靠 access token 快速过期。
//     否决理由：需要改全局 TokenTTL 并给 Console 前端加静默刷新层，
//     侵入面大；且「立即生效」仍要等当前 access 剩余寿命。
//   - 方案 B：token 级黑名单（jti 列表）——每签发一个令牌占一行，
//     列表随登录次数线性膨胀，需过期回收。
//   - 选定方案 C：用户级吊销时间戳（kicked_at）+ 会话级登记表。
//     踢下线 = 记录 user→kicked_at 并吊销其全部登记会话；AuthMiddleware
//     校验令牌签发时刻 iat < kicked_at 即拒绝——O(1) 查询、无膨胀
//     （每用户一个时间戳）、零 TTL 侵入、踢一次全端下线（与竞品
//     「在线用户踢下线」语义一致），且下一个请求立即生效。
//   - 登出（单会话吊销）由会话级 revoked_at 承担，与用户级互不混淆。

// 权限点：在线会话管理（Console 侧）。
const PermSessionManage = "identity:session:manage"

var ErrSessionNotFound = errors.New("identity: session not found")

// OnlineSession 是一次登录的会话登记（在线用户注册表行）。
// TokenID 为令牌指纹 SHA-256(token) 前 16 字节 hex——注册表不存
// 原始令牌（泄漏面最小），校验时按指纹比对。
type OnlineSession struct {
	TokenID   string    `json:"token_id" gorm:"primaryKey;size:40"`
	UserID    string    `json:"user_id" gorm:"size:40;index"`
	Username  string    `json:"username" gorm:"size:64"`
	TenantID  string    `json:"tenant_id,omitempty" gorm:"size:40;index"`
	Remote    string    `json:"remote" gorm:"size:64"`
	UserAgent string    `json:"user_agent" gorm:"size:255"`
	LoginAt   time.Time `json:"login_at" gorm:"index"`
	ExpiresAt time.Time `json:"expires_at" gorm:"index"`
	// RevokedAt 为指针：nil 落库为 NULL（SQL 判定 revoked_at IS NULL 的
	// 前提——time.Time 零值会写成 0001-01-01 而非 NULL）。
	RevokedAt *time.Time `json:"revoked_at,omitempty" gorm:"index"`
}

// Online 报告该会话当前是否在线（未吊销且未过期）。
func (s OnlineSession) Online(now time.Time) bool {
	return s.RevokedAt == nil && now.Before(s.ExpiresAt)
}

// UserKick 是用户级吊销时间戳（踢下线的生效锚）。
type UserKick struct {
	UserID   string    `json:"user_id" gorm:"primaryKey;size:40"`
	KickedAt time.Time `json:"kicked_at"`
}

// OnlineStore 抽象在线会话注册表（内存实现先行，GORM 见 gormstore）。
// CheckToken 同时实现 TokenIssuer 的吊销检查器契约（登录后的每个
// 请求经 AuthMiddleware 调用，实现须低开销）。
type OnlineStore interface {
	// Register 登录时登记会话（同 token 重登记幂等覆盖）。
	Register(ctx context.Context, s OnlineSession) error
	// Get 按指纹取会话。
	Get(ctx context.Context, tokenID string) (OnlineSession, error)
	// Revoke 吊销单会话（登出）。
	Revoke(ctx context.Context, tokenID string, at time.Time) error
	// RevokeByUser 吊销用户全部在线会话（踢下线），返回吊销数。
	RevokeByUser(ctx context.Context, userID string, at time.Time) (int, error)
	// ListOnline 列出未吊销未过期会话（在线列表，按登录时间倒序）。
	ListOnline(ctx context.Context, now time.Time) ([]OnlineSession, error)
	// CheckToken 吊销裁决：iat 早于用户级 kicked_at 或会话已吊销 → false。
	CheckToken(ctx context.Context, userID, tokenID string, iat time.Time) bool
	// Cleanup 清理过期登记行（运维调用；在线视图本身惰性过滤过期行）。
	Cleanup(ctx context.Context, now time.Time) error
}

// TokenFingerprint 令牌指纹（注册表主键口径）。
func TokenFingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:16])
}

// ---------- 内存实现 ----------

type MemOnlineStore struct {
	mu       sync.RWMutex
	sessions map[string]OnlineSession
	kicks    map[string]time.Time // userID → kicked_at
}

// NewMemOnlineStore 构造内存在线会话注册表。
func NewMemOnlineStore() *MemOnlineStore {
	return &MemOnlineStore{sessions: map[string]OnlineSession{}, kicks: map[string]time.Time{}}
}

func (s *MemOnlineStore) Register(_ context.Context, ses OnlineSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[ses.TokenID] = ses
	// 新登录清除该用户此前的踢出标记（重新登录恢复合法身份）。
	delete(s.kicks, ses.UserID)
	return nil
}

func (s *MemOnlineStore) Get(_ context.Context, tokenID string) (OnlineSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ses, ok := s.sessions[tokenID]
	if !ok {
		return OnlineSession{}, ErrSessionNotFound
	}
	return ses, nil
}

func (s *MemOnlineStore) Revoke(_ context.Context, tokenID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ses, ok := s.sessions[tokenID]
	if !ok {
		return ErrSessionNotFound
	}
	ses.RevokedAt = &at
	s.sessions[tokenID] = ses
	return nil
}

func (s *MemOnlineStore) RevokeByUser(_ context.Context, userID string, at time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kicks[userID] = at
	n := 0
	for id, ses := range s.sessions {
		if ses.UserID == userID && ses.RevokedAt == nil {
			ses.RevokedAt = &at
			s.sessions[id] = ses
			n++
		}
	}
	return n, nil
}

func (s *MemOnlineStore) ListOnline(_ context.Context, now time.Time) ([]OnlineSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []OnlineSession
	for _, ses := range s.sessions {
		if ses.Online(now) {
			out = append(out, ses)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LoginAt.After(out[j].LoginAt) })
	return out, nil
}

func (s *MemOnlineStore) CheckToken(_ context.Context, userID, tokenID string, iat time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if k, ok := s.kicks[userID]; ok && iat.Before(k) {
		return false // 踢下线后签发的旧令牌一律失效
	}
	if ses, ok := s.sessions[tokenID]; ok && ses.RevokedAt != nil {
		return false // 单会话已登出
	}
	return true
}

func (s *MemOnlineStore) Cleanup(_ context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, ses := range s.sessions {
		if now.After(ses.ExpiresAt) {
			delete(s.sessions, id)
		}
	}
	return nil
}

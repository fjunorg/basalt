package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/basalt-go/basalt/pkg/kernel/crypto/randutil"
)

// OAuth2 2.1 / SSO 子域（竞品 Gap P0#3）：应用管理、授权码（含 PKCE）
// 与客户端凭证模式、轮换式 refresh、三方登录接口缝。
//
// 工程边界：SSO 是 identity 的声明式扩展（非独立模块）——token 签发
// 复用同包 TokenIssuer（同一 JWT 密钥体系），权限点/审计事件/租户
// 上下文与 identity 现有机制同构。

// 权限点：SSO 应用管理（Console 侧）。admin 角色持 "*" 自动覆盖。
const PermSSOManage = "identity:sso:manage"

// 授权类型（client 声明式启用）。
const (
	GrantAuthorizationCode = "authorization_code"
	GrantClientCredentials = "client_credentials"
	GrantRefreshToken      = "refresh_token"
)

var (
	ErrClientNotFound     = errors.New("identity: sso client not found")
	ErrClientIDTaken      = errors.New("identity: sso client_id already taken")
	ErrAuthCodeNotFound   = errors.New("identity: sso auth code not found")
	ErrAuthCodeUsed       = errors.New("identity: sso auth code already used")
	ErrAuthCodeExpired    = errors.New("identity: sso auth code expired")
	ErrRefreshNotFound    = errors.New("identity: sso refresh token not found")
	ErrRefreshReplayed    = errors.New("identity: sso refresh token replayed")
	ErrRefreshExpired     = errors.New("identity: sso refresh token expired")
	ErrRefreshRevoked     = errors.New("identity: sso refresh token revoked")
	ErrSocialNotFound     = errors.New("identity: social provider not registered")
	ErrSocialUnconfigured = errors.New("identity: social provider credentials not configured")
)

// Client 是 SSO 应用（OAuth2 客户端）注册信息。
//   - SecretHash 为 SHA-256(client_secret) hex：client secret 是高熵随机串
//     （非人记忆口令），无慢哈希抗暴力需求，快哈希即可，且每次客户端
//     认证都执行比对，bcrypt 的 ~100ms 成本不可接受。
//   - IsPublic 标记公共客户端（SPA/原生应用）：不持 secret，授权码流程
//     强制 PKCE（OAuth 2.1 §7.6 对公共客户端的硬要求）。
//   - RedirectURIs 为精确匹配白名单：authorize 与 token 两处均须与注册值
//     逐字节相等（RFC 6749 §3.1.2.3 严格模式，不做前缀/模式匹配）。
type Client struct {
	ID           string    `json:"id" gorm:"primaryKey;size:40"`
	ClientID     string    `json:"client_id" gorm:"uniqueIndex;size:64"`
	SecretHash   string    `json:"-" gorm:"size:128"`
	Name         string    `json:"name" gorm:"size:128"`
	RedirectURIs []string  `json:"redirect_uris" gorm:"serializer:json"`
	Scopes       []string  `json:"scopes" gorm:"serializer:json"`
	Grants       []string  `json:"grants" gorm:"serializer:json"`
	IsPublic     bool      `json:"is_public" gorm:"default:false"`
	Status       string    `json:"status" gorm:"size:16;index"`
	TenantID     string    `json:"tenant_id,omitempty" gorm:"size:40;index"`
	CreatedAt    time.Time `json:"created_at"`
}

// Confidential 报告机密客户端（持 secret）。
func (c Client) Confidential() bool { return !c.IsPublic }

// HasGrant 报告客户端声明启用某授权类型。
func (c Client) HasGrant(g string) bool {
	for _, x := range c.Grants {
		if x == g {
			return true
		}
	}
	return false
}

// AuthCode 授权码：单次使用、短时效（默认 5 分钟）。
// CodeChallenge 存 PKCE S256 挑战值；RequirePKCE 记录签发时的强制要求
// （public 客户端 true），兑换时据此决定 code_verifier 是否必需。
type AuthCode struct {
	Code          string    `json:"code" gorm:"primaryKey;size:80"`
	ClientID      string    `json:"client_id" gorm:"size:64;index"`
	UserID        string    `json:"user_id" gorm:"size:40;index"`
	RedirectURI   string    `json:"redirect_uri" gorm:"size:512"`
	Scopes        []string  `json:"scopes" gorm:"serializer:json"`
	CodeChallenge string    `json:"code_challenge,omitempty" gorm:"size:128"`
	RequirePKCE   bool      `json:"require_pkce" gorm:"default:false"`
	ExpiresAt     time.Time `json:"expires_at" gorm:"index"`
	Used          bool      `json:"used" gorm:"default:false"`
	CreatedAt     time.Time `json:"created_at"`
}

// RefreshToken 轮换式刷新令牌。
//   - 存储形态为 TokenHash = SHA-256(token) hex：库泄漏不直接暴露可用
//     凭据（与 Client.SecretHash 同一策略）。
//   - ChainID 标识同一授权链：每次刷新签发新 refresh（继承 ChainID），
//     旧 refresh 标记 Revoked；已 Revoked 的 refresh 被再次使用视为重放
//     （令牌被盗的信号），立即吊销整条链（OAuth 2.1 §4.14.2 最佳实践）。
//   - client_credentials 不签发 refresh（机器对机器重走凭证即可，
//     RFC 6749 §4.4.3 明示可选）。
type RefreshToken struct {
	TokenHash string    `json:"-" gorm:"primaryKey;size:128"`
	ChainID   string    `json:"chain_id" gorm:"size:40;index"`
	ClientID  string    `json:"client_id" gorm:"size:64;index"`
	UserID    string    `json:"user_id" gorm:"size:40"`
	Scopes    []string  `json:"scopes" gorm:"serializer:json"`
	ExpiresAt time.Time `json:"expires_at" gorm:"index"`
	Revoked   bool      `json:"revoked" gorm:"default:false"`
	CreatedAt time.Time `json:"created_at"`
}

// SSOStore 抽象 SSO 三实体持久化。语义要点：
//   - ConsumeAuthCode 原子「取出 + 标记已用」——单次使用由存储层保证，
//     并发双花第二个调用方收到 ErrAuthCodeUsed；
//   - RotateRefresh 原子「校验 + 吊销旧值」，重放（已 Revoked 再用）
//     返回 ErrRefreshReplayed 并附记录供链吊销。
type SSOStore interface {
	// Client（按内部 ID 与公开 client_id 双键）。
	CreateClient(ctx context.Context, c Client) error
	GetClient(ctx context.Context, id string) (Client, error)
	GetClientByClientID(ctx context.Context, clientID string) (Client, error)
	ListClients(ctx context.Context) ([]Client, error)
	UpdateClient(ctx context.Context, c Client) error
	DeleteClient(ctx context.Context, id string) error

	// AuthCode。
	SaveAuthCode(ctx context.Context, ac AuthCode) error
	ConsumeAuthCode(ctx context.Context, code string) (AuthCode, error)

	// RefreshToken。
	SaveRefresh(ctx context.Context, rt RefreshToken) error
	RotateRefresh(ctx context.Context, tokenHash string) (RefreshToken, error)
	RevokeChain(ctx context.Context, chainID string) error
}

// ---------- 随机值与哈希 ----------

func randHex(n int) string { return randutil.Hex(n) }

// NewSSOClientID 生成公开 client_id（32 hex 字符）。
func NewSSOClientID() string { return randHex(16) }

// NewSSOSecret 生成 client secret（43 字符 base64url，256 位熵）。
func NewSSOSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("identity: crypto/rand unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// HashSecret 统一哈希（SHA-256 hex）：client secret 与 refresh token 同策略。
func HashSecret(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// NewSSOClientIDSecret 供测试与种子构造（生成 ID 并哈希 secret）。
func NewSSOClientIDSecret() (clientID, secret, secretHash string) {
	clientID = NewSSOClientID()
	secret = NewSSOSecret()
	return clientID, secret, HashSecret(secret)
}

// ---------- 内存实现（开发/测试/All-in-One 默认） ----------

type MemSSOStore struct {
	mu      sync.RWMutex
	clients map[string]Client // by internal ID
	byCID   map[string]string // client_id -> id
	codes   map[string]AuthCode
	refresh map[string]RefreshToken // by token hash
}

// NewMemSSOStore 构造内存 SSO 存储。
func NewMemSSOStore() *MemSSOStore {
	return &MemSSOStore{
		clients: map[string]Client{},
		byCID:   map[string]string{},
		codes:   map[string]AuthCode{},
		refresh: map[string]RefreshToken{},
	}
}

func (s *MemSSOStore) CreateClient(_ context.Context, c Client) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byCID[c.ClientID]; ok {
		return ErrClientIDTaken
	}
	s.clients[c.ID] = c
	s.byCID[c.ClientID] = c.ID
	return nil
}

func (s *MemSSOStore) GetClient(_ context.Context, id string) (Client, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.clients[id]
	if !ok {
		return Client{}, ErrClientNotFound
	}
	return c, nil
}

func (s *MemSSOStore) GetClientByClientID(_ context.Context, clientID string) (Client, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byCID[clientID]
	if !ok {
		return Client{}, ErrClientNotFound
	}
	return s.clients[id], nil
}

func (s *MemSSOStore) ListClients(_ context.Context) ([]Client, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Client, 0, len(s.clients))
	for _, c := range s.clients {
		out = append(out, c)
	}
	return out, nil
}

func (s *MemSSOStore) UpdateClient(_ context.Context, c Client) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.clients[c.ID]
	if !ok {
		return ErrClientNotFound
	}
	delete(s.byCID, old.ClientID)
	s.clients[c.ID] = c
	s.byCID[c.ClientID] = c.ID
	return nil
}

func (s *MemSSOStore) DeleteClient(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.clients[id]
	if !ok {
		return ErrClientNotFound
	}
	delete(s.clients, id)
	delete(s.byCID, c.ClientID)
	return nil
}

func (s *MemSSOStore) SaveAuthCode(_ context.Context, ac AuthCode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codes[ac.Code] = ac
	return nil
}

// ConsumeAuthCode 原子取出并标记已用：单次使用语义在存储层保证。
func (s *MemSSOStore) ConsumeAuthCode(_ context.Context, code string) (AuthCode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ac, ok := s.codes[code]
	if !ok {
		return AuthCode{}, ErrAuthCodeNotFound
	}
	if ac.Used {
		return AuthCode{}, ErrAuthCodeUsed
	}
	ac.Used = true
	s.codes[code] = ac
	return ac, nil
}

func (s *MemSSOStore) SaveRefresh(_ context.Context, rt RefreshToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh[rt.TokenHash] = rt
	return nil
}

// RotateRefresh 原子校验并吊销旧 refresh；重放返回旧记录供链吊销。
func (s *MemSSOStore) RotateRefresh(_ context.Context, tokenHash string) (RefreshToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rt, ok := s.refresh[tokenHash]
	if !ok {
		return RefreshToken{}, ErrRefreshNotFound
	}
	if rt.Revoked {
		return rt, ErrRefreshReplayed
	}
	rt.Revoked = true
	s.refresh[tokenHash] = rt
	return rt, nil
}

func (s *MemSSOStore) RevokeChain(_ context.Context, chainID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for h, rt := range s.refresh {
		if rt.ChainID == chainID {
			rt.Revoked = true
			s.refresh[h] = rt
		}
	}
	return nil
}

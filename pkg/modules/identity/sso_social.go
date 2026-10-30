package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// SocialProvider 三方登录接口缝（企微/钉钉/…）。
//
// 实现约定：
//   - AuthorizeURL 只做参数拼装（无 IO），state 原样嵌入；
//   - Exchange 完成三方 code → 身份的交换（有外呼 IO）；
//   - 实现不得持有进程级可变状态（注册后只读）。
//
// 选型说明（钉钉先行实现、企微留缝的理由）：
//   - 钉钉新版 OAuth（login.dingtalk.com /oauth2/auth + /v1.0/oauth2/
//     userAccessToken + /v1.0/contact/users/me）三步纯 HTTP、无扫码
//     会话轮询，凭据仅 client_id/secret 一对，实现面最小且凭据全部
//     经环境变量注入（BASALT_SSO_DINGTALK_CLIENT_ID/SECRET/CALLBACK）；
//   - 企微 Web 登录（qrConnect）需要 corpid + agent 凭据两跳换取
//     access_token 并管理其 2 小时缓存，接入面更大，留作后续迭代——
//     实现路径与钉钉同构（实现本接口 + RegisterSocial 注册即可），
//     本文件尾部附企微实现路径说明。
type SocialProvider interface {
	Name() string
	AuthorizeURL(state string) string
	Exchange(ctx context.Context, code string) (SocialIdentity, error)
}

// SocialIdentity 是三方身份的最小映射（openid 为提供方内的稳定标识）。
type SocialIdentity struct {
	Provider string
	OpenID   string
	Name     string
	Email    string
}

// SocialRegistry 三方登录提供方注册表（Init 期写入，请求期只读）。
type SocialRegistry struct {
	mu        sync.RWMutex
	providers map[string]SocialProvider
}

// NewSocialRegistry 构造空注册表。
func NewSocialRegistry() *SocialRegistry {
	return &SocialRegistry{providers: map[string]SocialProvider{}}
}

// Register 注册提供方（重名后者覆盖——显式再配置语义）。
func (r *SocialRegistry) Register(p SocialProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.Name()] = p
}

// Get 按名取提供方。
func (r *SocialRegistry) Get(name string) (SocialProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	if !ok {
		return nil, ErrSocialNotFound
	}
	return p, nil
}

// Names 已注册提供方清单（描述/诊断用）。
func (r *SocialRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.providers))
	for n := range r.providers {
		out = append(out, n)
	}
	return out
}

func bcryptHash(s string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(s), bcrypt.DefaultCost)
	return string(b), err
}

// ---------- 钉钉（先行实现） ----------

// DingTalkProvider 钉钉扫码/账号登录。
// 凭据经环境变量注入（不落库）：
//   - BASALT_SSO_DINGTALK_CLIENT_ID
//   - BASALT_SSO_DINGTALK_CLIENT_SECRET
//   - BASALT_SSO_DINGTALK_CALLBACK（默认 http://localhost:8080/api/v1/identity/sso/social/dingtalk/callback）
type DingTalkProvider struct {
	clientID     string
	clientSecret string
	callback     string
	authBase     string // OAuth 授权页域名（默认 https://login.dingtalk.com）
	apiBase      string // 开放 API 域名（默认 https://api.dingtalk.com）
	httpClient   *http.Client
}

// NewDingTalkProviderFromEnv 从环境变量构造；凭据缺失返回
// ErrSocialUnconfigured（调用方据此跳过注册——未配置的提供方不下发）。
func NewDingTalkProviderFromEnv() (*DingTalkProvider, error) {
	id, secret := os.Getenv("BASALT_SSO_DINGTALK_CLIENT_ID"), os.Getenv("BASALT_SSO_DINGTALK_CLIENT_SECRET")
	if id == "" || secret == "" {
		return nil, ErrSocialUnconfigured
	}
	cb := os.Getenv("BASALT_SSO_DINGTALK_CALLBACK")
	if cb == "" {
		cb = "http://localhost:8080/api/v1/identity/sso/social/dingtalk/callback"
	}
	return &DingTalkProvider{
		clientID:     id,
		clientSecret: secret,
		callback:     cb,
		authBase:     "https://login.dingtalk.com",
		apiBase:      "https://api.dingtalk.com",
		httpClient:   &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (p *DingTalkProvider) Name() string { return "dingtalk" }

func (p *DingTalkProvider) AuthorizeURL(state string) string {
	q := url.Values{}
	q.Set("redirect_uri", p.callback)
	q.Set("response_type", "code")
	q.Set("client_id", p.clientID)
	q.Set("scope", "openid")
	q.Set("state", state)
	q.Set("prompt", "consent")
	return p.authBase + "/oauth2/auth?" + q.Encode()
}

// Exchange 两步换身份：code → userAccessToken → 用户信息。
func (p *DingTalkProvider) Exchange(ctx context.Context, code string) (SocialIdentity, error) {
	// 1. userAccessToken。
	tokReq := map[string]string{
		"clientId":     p.clientID,
		"clientSecret": p.clientSecret,
		"code":         code,
		"grantType":    "authorization_code",
	}
	tokBody, _ := json.Marshal(tokReq)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.apiBase+"/v1.0/oauth2/userAccessToken", bytes.NewReader(tokBody))
	if err != nil {
		return SocialIdentity{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return SocialIdentity{}, fmt.Errorf("dingtalk token exchange: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return SocialIdentity{}, fmt.Errorf("dingtalk token exchange: status %d body %s", resp.StatusCode, string(b))
	}
	var tok struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    int    `json:"expireIn"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil || tok.AccessToken == "" {
		return SocialIdentity{}, errors.New("dingtalk token exchange: empty access token")
	}

	// 2. 用户信息。
	req2, err := http.NewRequestWithContext(ctx, http.MethodGet,
		p.apiBase+"/v1.0/contact/users/me", nil)
	if err != nil {
		return SocialIdentity{}, err
	}
	req2.Header.Set("x-acs-dingtalk-access-token", tok.AccessToken)
	resp2, err := p.httpClient.Do(req2)
	if err != nil {
		return SocialIdentity{}, fmt.Errorf("dingtalk userinfo: %w", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		return SocialIdentity{}, fmt.Errorf("dingtalk userinfo: status %d", resp2.StatusCode)
	}
	var me struct {
		OpenID string `json:"openId"`
		Nick   string `json:"nick"`
		Email  string `json:"email"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&me); err != nil {
		return SocialIdentity{}, err
	}
	if me.OpenID == "" {
		return SocialIdentity{}, errors.New("dingtalk userinfo: empty openId")
	}
	return SocialIdentity{Provider: p.Name(), OpenID: me.OpenID, Name: me.Nick, Email: me.Email}, nil
}

// NewDingTalkProviderForTest 供单测注入自定义 endpoint（httptest server）。
func NewDingTalkProviderForTest(id, secret, authEndpoint, apiEndpoint, callback string) *DingTalkProvider {
	return &DingTalkProvider{
		clientID:     id,
		clientSecret: secret,
		callback:     callback,
		httpClient:   &http.Client{Timeout: 5 * time.Second},
		authBase:     authEndpoint,
		apiBase:      apiEndpoint,
	}
}

// ---------- 企微（留缝：实现路径说明） ----------
//
// WeComProvider（企业微信 Web 登录）实现路径（后续迭代按此补齐）：
//  1. AuthorizeURL：
//     https://login.work.weixin.qq.com/wwlogin/sso/login?login_type=CorpApp&
//     appid={corpid}&agentid={agentid}&redirect_uri={cb}&state={state}；
//  2. Exchange 两步：
//     a. access_token（企业级凭据，2h 有效需缓存）：
//        GET https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid=&corpsecret=
//     b. GET https://qyapi.weixin.qq.com/cgi-bin/auth/getuserinfo?access_token=&code=
//        → userid（企业内成员标识，映射 OpenID）；
//  3. 凭据 env：BASALT_SSO_WECOM_CORP_ID / AGENT_ID / SECRET / CALLBACK；
//  4. 实现本接口后 socialRegistry.Register(WeComProvider{...}) 即接入，
//     handler/自动注册/审计链路零改动。
//
// 当前选择留缝而非实现的原因：企微登录绑定企业成员身份（userid 属于
// 特定 corpid），开源场景的通用部署无法预配企业凭据，先交付钉钉
// （个人身份体系）验证全链路，企微随首个 G 端需求落地。

// sanitizeSocialName 辅助：display name 安全截断。
func sanitizeSocialName(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

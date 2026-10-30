package identity

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"time"
)

// OAuth2 2.1 流程核心（纯逻辑层，与 HTTP 解耦——HTTP 适配见 sso_handler.go）。
//
// token 语义（与既有 JWT 体系的关系）：
//   - access token 复用 TokenIssuer（同密钥 HS256、同 AuthMiddleware 校验
//     路径），claims 附加 client_id/scope 区分「Console 直登」与「OAuth2
//     授权」两类令牌；
//   - refresh token 为 opaque 随机串（非 JWT）：需服务端吊销能力（重放
//     检测），库存 SHA-256 哈希；轮换式（rotation），默认 TTL 14 天；
//   - client_credentials 模式 Subject 为 "client:<client_id>"（机器身份），
//     不签发 refresh。

// ssoTTL 汇集 SSO 时效参数（config 注入，测试可构造短 TTL）。
type ssoTTL struct {
	CodeTTL    time.Duration // 授权码有效期（默认 5 分钟）
	RefreshTTL time.Duration // refresh token 有效期（默认 14 天）
}

// defaultSSOTTL 与 config.Defaults 对齐。
func defaultSSOTTL() ssoTTL { return ssoTTL{CodeTTL: 5 * time.Minute, RefreshTTL: 14 * 24 * time.Hour} }

// AuthorizeRequest 是授权端点的规范化请求（GET query 与 POST 确认共用，
// POST 时经 JSON tag 解码）。
type AuthorizeRequest struct {
	ResponseType        string `json:"response_type"`
	ClientID            string `json:"client_id"`
	RedirectURI         string `json:"redirect_uri"`
	Scope               string `json:"scope"` // 空格分隔
	State               string `json:"state"`
	CodeChallenge       string `json:"code_challenge"`
	CodeChallengeMethod string `json:"code_challenge_method"`
}

// ParseScopes 把空格分隔 scope 拆分为去重有序切片（导出供契约与测试复用）。
func ParseScopes(s string) []string {
	fields := strings.Fields(s)
	seen := make(map[string]bool, len(fields))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// parseScopes 内部短名别名。
func parseScopes(s string) []string { return ParseScopes(s) }

// ScopeAllowed 请求 scope 是否全部在客户端允许集合内。
// 客户端注册空 scopes 视为不限制（仅校验后续业务授权）。
func ScopeAllowed(allowed, requested []string) bool {
	if len(allowed) == 0 {
		return true
	}
	set := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		set[a] = true
	}
	for _, r := range requested {
		if !set[r] {
			return false
		}
	}
	return true
}

func scopeAllowed(allowed, requested []string) bool { return ScopeAllowed(allowed, requested) }

// validRedirectURI 校验注册值：绝对 URI、无 fragment（RFC 6749 §3.1.2）。
func validRedirectURI(u string) bool {
	p, err := url.Parse(u)
	if err != nil {
		return false
	}
	if !p.IsAbs() || p.Host == "" {
		return false
	}
	return p.Fragment == "" && p.RawFragment == ""
}

// matchRedirectURI 严格匹配：注册白名单内逐字节相等，绝不前缀/模式匹配。
func matchRedirectURI(registered []string, u string) bool {
	if !validRedirectURI(u) {
		return false
	}
	for _, r := range registered {
		if r == u {
			return true
		}
	}
	return false
}

// validateAuthorize 校验授权请求。
// 返回值 ok=false 时：redirectSafe=true 表示可以安全重定向错误（redirect_uri
// 与 client 已验证），false 表示必须直接 400（RFC 6749 §4.1.2.1——
// redirect_uri 或 client 无效时不重定向，防开放重定向）。
func validateAuthorize(c Client, req AuthorizeRequest) (scopeList []string, redirectSafe bool, err oauth2Error) {
	fail := func(code, desc string, safe bool) oauth2Error {
		return oauth2Error{Code: code, Description: desc, redirectSafe: safe}
	}
	if req.ResponseType != "code" {
		return nil, true, fail("unsupported_response_type", "response_type must be \"code\"", true)
	}
	if c.Status != StatusActive {
		return nil, true, fail("unauthorized_client", "client disabled", true)
	}
	if !matchRedirectURI(c.RedirectURIs, req.RedirectURI) {
		// redirect_uri 无效：不重定向（开放重定向防护）。
		return nil, false, fail("invalid_request", "redirect_uri not registered for client", false)
	}
	if !c.HasGrant(GrantAuthorizationCode) {
		return nil, true, fail("unauthorized_client", "client not authorized for authorization_code", true)
	}
	scopes := parseScopes(req.Scope)
	if !scopeAllowed(c.Scopes, scopes) {
		return nil, true, fail("invalid_scope", "requested scope exceeds client registration", true)
	}
	// PKCE（OAuth 2.1）：公共客户端强制；机密客户端可选但同样只接受 S256。
	needPKCE := c.IsPublic
	if req.CodeChallengeMethod != "" && req.CodeChallengeMethod != "S256" {
		return nil, true, fail("invalid_request", "code_challenge_method must be S256", true)
	}
	if req.CodeChallenge == "" {
		if needPKCE {
			return nil, true, fail("invalid_request", "PKCE code_challenge required for public clients", true)
		}
	} else {
		// 提供了 challenge：method 必须显式 S256（2.1 不允许默认 plain）。
		if req.CodeChallengeMethod != "S256" {
			return nil, true, fail("invalid_request", "code_challenge_method must be S256", true)
		}
		if len(req.CodeChallenge) < 43 || len(req.CodeChallenge) > 128 {
			return nil, true, fail("invalid_request", "code_challenge must be 43-128 chars", true)
		}
	}
	return scopes, true, oauth2Error{}
}

// issueAuthCode 生成并保存授权码（用户已确认授权后调用）。
func issueAuthCode(ctx context.Context, st SSOStore, c Client, req AuthorizeRequest, userID string, ttl ssoTTL, now time.Time) (AuthCode, error) {
	scopes, _, oerr := validateAuthorize(c, req)
	if oerr.Code != "" {
		return AuthCode{}, oerr
	}
	ac := AuthCode{
		Code:          randHex(32), // 256 位熵
		ClientID:      c.ClientID,
		UserID:        userID,
		RedirectURI:   req.RedirectURI,
		Scopes:        scopes,
		CodeChallenge: req.CodeChallenge,
		RequirePKCE:   c.IsPublic,
		ExpiresAt:     now.Add(ttl.CodeTTL),
		CreatedAt:     now,
	}
	if err := st.SaveAuthCode(ctx, ac); err != nil {
		return AuthCode{}, err
	}
	return ac, nil
}

// verifyPKCE_S256 校验 verifier：BASE64URL(SHA256(verifier)) == challenge。
func verifyPKCE_S256(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:]) == challenge
}

// tokenPair 是令牌签发结果（授权码/refresh 两条路径共用）。
type tokenPair struct {
	AccessToken  string
	ExpiresAt    time.Time
	RefreshToken string
	TokenType    string
	Scope        []string
}

// exchangeAuthCode 消费授权码换取令牌（token 端点 authorization_code 分支）。
// 单次使用由 store.ConsumeAuthCode 原子保证。
func exchangeAuthCode(ctx context.Context, f *Facade, st SSOStore, ttl ssoTTL, clientID, secret, code, redirectURI, codeVerifier string, now time.Time) (tokenPair, oauth2Error) {
	oerr := func(code, desc string) oauth2Error { return oauth2Error{Code: code, Description: desc} }

	c, err := st.GetClientByClientID(ctx, clientID)
	if err != nil {
		return tokenPair{}, oerr("invalid_client", "unknown client")
	}
	if !authenticateClient(c, secret) {
		return tokenPair{}, oerr("invalid_client", "client authentication failed")
	}
	if c.Status != StatusActive || !c.HasGrant(GrantAuthorizationCode) {
		return tokenPair{}, oerr("unauthorized_client", "client disabled or grant not enabled")
	}
	ac, err := st.ConsumeAuthCode(ctx, code)
	switch {
	case err == nil:
	case isErr(err, ErrAuthCodeNotFound):
		return tokenPair{}, oerr("invalid_grant", "unknown authorization code")
	case isErr(err, ErrAuthCodeUsed):
		// 已用码重放：按 2.1 建议吊销该码已签发的 refresh 链（无法定位
		// 链 ID，Code 即链锚不可行——保守策略：仅拒绝并审计）。
		return tokenPair{}, oerr("invalid_grant", "authorization code already used")
	default:
		return tokenPair{}, oerr("invalid_grant", "authorization code invalid")
	}
	if ac.ClientID != c.ClientID {
		return tokenPair{}, oerr("invalid_grant", "code issued to another client")
	}
	if now.After(ac.ExpiresAt) {
		return tokenPair{}, oerr("invalid_grant", "authorization code expired")
	}
	if ac.RedirectURI != redirectURI || redirectURI == "" {
		return tokenPair{}, oerr("invalid_grant", "redirect_uri mismatch")
	}
	// PKCE：签发时记录了 RequirePKCE；提供了 challenge 一律校验。
	if ac.RequirePKCE && codeVerifier == "" {
		return tokenPair{}, oerr("invalid_grant", "code_verifier required")
	}
	if ac.CodeChallenge != "" {
		if codeVerifier == "" || !verifyPKCE_S256(codeVerifier, ac.CodeChallenge) {
			return tokenPair{}, oerr("invalid_grant", "PKCE verification failed")
		}
	}

	u, err := f.users.Get(ctx, ac.UserID)
	if err != nil || u.Status != StatusActive {
		return tokenPair{}, oerr("invalid_grant", "user unavailable")
	}
	return signUserTokens(ctx, f.issuer, st, ttl, c, u, ac.Scopes, now)
}

// clientCredentialsToken 客户端凭证模式：机器身份，无 refresh。
func clientCredentialsToken(ctx context.Context, st SSOStore, ttl ssoTTL, clientID, secret, scope string, iss *TokenIssuer, now time.Time) (tokenPair, oauth2Error) {
	oerr := func(code, desc string) oauth2Error { return oauth2Error{Code: code, Description: desc} }
	c, err := st.GetClientByClientID(ctx, clientID)
	if err != nil {
		return tokenPair{}, oerr("invalid_client", "unknown client")
	}
	if !authenticateClient(c, secret) {
		return tokenPair{}, oerr("invalid_client", "client authentication failed")
	}
	if c.Status != StatusActive || !c.HasGrant(GrantClientCredentials) {
		return tokenPair{}, oerr("unauthorized_client", "client disabled or grant not enabled")
	}
	scopes := parseScopes(scope)
	if !scopeAllowed(c.Scopes, scopes) {
		return tokenPair{}, oerr("invalid_scope", "requested scope exceeds client registration")
	}
	// 机器身份主体：Subject = "client:<client_id>"，可经 AuthMiddleware
	// 通过签名校验；用户级端点 Require 会因查无用户而拒绝（预期语义）。
	tok, exp, err := iss.IssueOAuth2(User{ID: "client:" + c.ClientID}, c.ClientID, scopes, now)
	if err != nil {
		return tokenPair{}, oerr("server_error", "token signing failed")
	}
	return tokenPair{AccessToken: tok, ExpiresAt: exp, TokenType: "Bearer", Scope: scopes}, oauth2Error{}
}

// refreshTokens refresh_token 模式：轮换签发 + 重放检测吊链。
func refreshTokens(ctx context.Context, st SSOStore, ttl ssoTTL, clientID, secret, refreshToken, scope string, iss *TokenIssuer, users UserStore, now time.Time) (tokenPair, oauth2Error) {
	oerr := func(code, desc string) oauth2Error { return oauth2Error{Code: code, Description: desc} }
	c, err := st.GetClientByClientID(ctx, clientID)
	if err != nil {
		return tokenPair{}, oerr("invalid_client", "unknown client")
	}
	if !authenticateClient(c, secret) {
		return tokenPair{}, oerr("invalid_client", "client authentication failed")
	}
	if c.Status != StatusActive {
		return tokenPair{}, oerr("unauthorized_client", "client disabled")
	}
	rt, err := st.RotateRefresh(ctx, HashSecret(refreshToken))
	switch {
	case err == nil:
	case isErr(err, ErrRefreshNotFound):
		return tokenPair{}, oerr("invalid_grant", "unknown refresh token")
	case isErr(err, ErrRefreshReplayed):
		// 重放：被盗令牌信号——吊销整条链并拒绝。
		_ = st.RevokeChain(ctx, rt.ChainID)
		return tokenPair{}, oerr("invalid_grant", "refresh token replay detected, chain revoked")
	default:
		return tokenPair{}, oerr("invalid_grant", "refresh token invalid")
	}
	if rt.ClientID != c.ClientID {
		return tokenPair{}, oerr("invalid_grant", "refresh token issued to another client")
	}
	if now.After(rt.ExpiresAt) {
		return tokenPair{}, oerr("invalid_grant", "refresh token expired")
	}
	// scope 参数只允许收窄（RFC 6749 §6）。
	scopes := rt.Scopes
	if scope != "" {
		req := parseScopes(scope)
		if !scopeAllowed(rt.Scopes, req) {
			return tokenPair{}, oerr("invalid_scope", "scope may only narrow on refresh")
		}
		scopes = req
	}
	u, err := users.Get(ctx, rt.UserID)
	if err != nil || u.Status != StatusActive {
		return tokenPair{}, oerr("invalid_grant", "user unavailable")
	}
	pair, err := signRefreshableTokens(ctx, iss, st, ttl, c, u, scopes, rt.ChainID, now)
	if err != nil {
		return tokenPair{}, oerr("server_error", "token signing failed")
	}
	return pair, oauth2Error{}
}

// signUserTokens 授权码路径签发（新链）。
func signUserTokens(ctx context.Context, iss *TokenIssuer, st SSOStore, ttl ssoTTL, c Client, u User, scopes []string, now time.Time) (tokenPair, oauth2Error) {
	pair, err := signRefreshableTokens(ctx, iss, st, ttl, c, u, scopes, randHex(16), now)
	if err != nil {
		return tokenPair{}, oauth2Error{Code: "server_error", Description: "token signing failed"}
	}
	return pair, oauth2Error{}
}

// signRefreshableTokens 签发 access（JWT）+ refresh（opaque，继承链 ID）。
func signRefreshableTokens(_ context.Context, iss *TokenIssuer, st SSOStore, ttl ssoTTL, c Client, u User, scopes []string, chainID string, now time.Time) (tokenPair, error) {
	var pair tokenPair
	tok, exp, err := iss.IssueOAuth2(u, c.ClientID, scopes, now)
	if err != nil {
		return pair, err
	}
	raw := randHex(32)
	rt := RefreshToken{
		TokenHash: HashSecret(raw),
		ChainID:   chainID,
		ClientID:  c.ClientID,
		UserID:    u.ID,
		Scopes:    scopes,
		ExpiresAt: now.Add(ttl.RefreshTTL),
		CreatedAt: now,
	}
	if err := st.SaveRefresh(context.Background(), rt); err != nil {
		return pair, err
	}
	return tokenPair{AccessToken: tok, ExpiresAt: exp, RefreshToken: raw, TokenType: "Bearer", Scope: scopes}, nil
}

// authenticateClient 机密客户端 secret 比对（公共客户端无需 secret）。
// 恒定失败语义：secret 缺失/不匹配统一 false，不区分细节防枚举。
func authenticateClient(c Client, secret string) bool {
	if c.IsPublic {
		return true // 公共客户端免 secret（PKCE 承担防截获）
	}
	return secret != "" && HashSecret(secret) == c.SecretHash
}

func isErr(err, target error) bool { return errors.Is(err, target) }

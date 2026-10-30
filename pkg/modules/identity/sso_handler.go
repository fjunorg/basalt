package identity

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/basalt-go/basalt/pkg/kernel/web"
)

// oauth2Error 遵循 RFC 6749 §5.2 错误响应格式（token 端点契约，
// 客户端 SDK 依赖该格式，不走平台 Envelope）。
type oauth2Error struct {
	Code         string `json:"error"`
	Description  string `json:"error_description,omitempty"`
	redirectSafe bool   // authorize 端点：是否可安全重定向到 redirect_uri
}

// Error 实现 error：流程层（sso_flow.go）以 error 语义传递，
// handler 层断言还原为 OAuth2 响应格式。
func (e oauth2Error) Error() string { return e.Code + ": " + e.Description }

// SSOHandler 承载 SSO/OAuth2 全部 HTTP 端点。
//
// 前后端契约（登录页跳转/授权确认语义，Console 前端按此对接）：
//  1. 用户被第三方应用引导到 Console 授权页（query 携带标准 OAuth2
//     authorize 参数）；
//  2. Console 调 GET /sso/authorize（Bearer）拿到授权描述
//     {client_name, scopes, redirect_uri, state} 渲染确认页；
//  3. 用户点确认 → POST /sso/authorize（Bearer，同参数 + action=approve）
//     返回 {redirect_to}，Console 顶层跳转该 URL（携 code/state）；
//     action=deny 同样返回 redirect_to（携 error=access_denied）。
//  4. 第三方应用后端用 code 换 token：POST /sso/token（form 编码）。
type SSOHandler struct {
	f        *Facade
	sso      SSOStore
	ttl      ssoTTL
	social   *SocialRegistry
	stateKey []byte // social state HMAC 密钥（复用 JWT secret）
}

// NewSSOHandler 构造 SSO 端点处理器（codeTTL/refreshTTL ≤0 时用默认值）。
func NewSSOHandler(f *Facade, sso SSOStore, codeTTL, refreshTTL time.Duration, social *SocialRegistry, stateKey string) *SSOHandler {
	ttl := ssoTTL{CodeTTL: codeTTL, RefreshTTL: refreshTTL}
	if ttl.CodeTTL <= 0 {
		ttl.CodeTTL = 5 * time.Minute
	}
	if ttl.RefreshTTL <= 0 {
		ttl.RefreshTTL = 14 * 24 * time.Hour
	}
	return &SSOHandler{f: f, sso: sso, ttl: ttl, social: social, stateKey: []byte(stateKey)}
}

// RegisterSSORoutes 在 identity 前缀下注册 SSO 端点。
func (h *SSOHandler) RegisterSSORoutes(r web.Router) {
	auth := h.f.Auth
	req := h.f.Require
	g := r.Group("/api/v1/identity/sso")

	// 应用管理（Console API）。
	g.Handle(http.MethodPost, "/apps", auth(req(PermSSOManage, h.createApp)))
	g.Handle(http.MethodGet, "/apps", auth(req(PermSSOManage, h.listApps)))
	g.Handle(http.MethodGet, "/apps/{id}", auth(req(PermSSOManage, h.getApp)))
	g.Handle(http.MethodPatch, "/apps/{id}", auth(req(PermSSOManage, h.updateApp)))
	g.Handle(http.MethodDelete, "/apps/{id}", auth(req(PermSSOManage, h.deleteApp)))
	g.Handle(http.MethodPost, "/apps/{id}/secret", auth(req(PermSSOManage, h.rotateSecret)))

	// OAuth2 授权端点（资源所有者须已登录 Console）。
	g.Handle(http.MethodGet, "/authorize", auth(h.describeAuthorize))
	g.Handle(http.MethodPost, "/authorize", auth(h.decideAuthorize))

	// OAuth2 令牌端点（客户端凭据认证，不走 Bearer）。
	g.Handle(http.MethodPost, "/token", h.token)

	// 三方登录（未登录 Console 的入口通道）。
	g.Handle(http.MethodGet, "/social/{provider}/authorize-url", h.socialAuthorizeURL)
	g.Handle(http.MethodGet, "/social/{provider}/callback", h.socialCallback)
}

// ---------- 应用管理 ----------

type createAppReq struct {
	Name         string   `json:"name"`
	RedirectURIs []string `json:"redirect_uris"`
	Scopes       []string `json:"scopes"`
	Grants       []string `json:"grants"`
	IsPublic     bool     `json:"is_public"`
	TenantID     string   `json:"tenant_id"`
}

type appResp struct {
	Client
	// Secret 仅在创建与轮换响应中出现一次（库中只存哈希）。
	Secret string `json:"secret,omitempty"`
}

func (h *SSOHandler) createApp(w http.ResponseWriter, r *http.Request) {
	var req createAppReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if strings.TrimSpace(req.Name) == "" || len(req.Name) > 128 {
		web.Err(w, http.StatusBadRequest, "app name required (1-128 chars)")
		return
	}
	// 授权码模式必须有回调；纯 client_credentials 应用可不注册回调。
	hasAuthCode := false
	for _, g := range req.Grants {
		if g != GrantAuthorizationCode && g != GrantClientCredentials {
			web.Err(w, http.StatusBadRequest, "grants must be authorization_code and/or client_credentials")
			return
		}
		if g == GrantAuthorizationCode {
			hasAuthCode = true
		}
	}
	if hasAuthCode && len(req.RedirectURIs) == 0 {
		web.Err(w, http.StatusBadRequest, "authorization_code grant requires at least one redirect_uri")
		return
	}
	for _, u := range req.RedirectURIs {
		if !validRedirectURI(u) {
			web.Err(w, http.StatusBadRequest, "redirect_uri must be absolute URI without fragment: "+u)
			return
		}
	}
	if req.IsPublic && !hasAuthCode {
		web.Err(w, http.StatusBadRequest, "public client only supports authorization_code grant")
		return
	}
	c := Client{
		ID:           NewUserID(),
		ClientID:     NewSSOClientID(),
		Name:         strings.TrimSpace(req.Name),
		RedirectURIs: req.RedirectURIs,
		Scopes:       req.Scopes,
		Grants:       req.Grants,
		IsPublic:     req.IsPublic,
		Status:       StatusActive,
		TenantID:     req.TenantID,
		CreatedAt:    time.Now().UTC(),
	}
	resp := appResp{Client: c}
	if !c.IsPublic {
		secret := NewSSOSecret()
		c.SecretHash = HashSecret(secret)
		resp.Secret = secret
	}
	if err := h.sso.CreateClient(r.Context(), c); err != nil {
		if errors.Is(err, ErrClientIDTaken) {
			web.Err(w, http.StatusConflict, "client_id collision, retry")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.f.audit(r, "sso.client.created", "client_id", c.ClientID, "name", c.Name, "tenant_id", c.TenantID)
	web.Created(w, resp)
}

func (h *SSOHandler) listApps(w http.ResponseWriter, r *http.Request) {
	clients, err := h.sso.ListClients(r.Context())
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, clients)
}

func (h *SSOHandler) getApp(w http.ResponseWriter, r *http.Request) {
	c, err := h.sso.GetClient(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrClientNotFound) {
			web.Err(w, http.StatusNotFound, "app not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, c)
}

type updateAppReq struct {
	Name         *string  `json:"name"`
	RedirectURIs []string `json:"redirect_uris"`
	Scopes       []string `json:"scopes"`
	Grants       []string `json:"grants"`
	Status       *string  `json:"status"`
}

func (h *SSOHandler) updateApp(w http.ResponseWriter, r *http.Request) {
	var req updateAppReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return
	}
	c, err := h.sso.GetClient(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrClientNotFound) {
			web.Err(w, http.StatusNotFound, "app not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		c.Name = strings.TrimSpace(*req.Name)
	}
	if req.RedirectURIs != nil {
		for _, u := range req.RedirectURIs {
			if !validRedirectURI(u) {
				web.Err(w, http.StatusBadRequest, "redirect_uri must be absolute URI without fragment: "+u)
				return
			}
		}
		c.RedirectURIs = req.RedirectURIs
	}
	if req.Scopes != nil {
		c.Scopes = req.Scopes
	}
	if req.Grants != nil {
		for _, g := range req.Grants {
			if g != GrantAuthorizationCode && g != GrantClientCredentials {
				web.Err(w, http.StatusBadRequest, "invalid grant type: "+g)
				return
			}
		}
		c.Grants = req.Grants
	}
	if req.Status != nil {
		if *req.Status != StatusActive && *req.Status != StatusDisabled {
			web.Err(w, http.StatusBadRequest, "status must be active or disabled")
			return
		}
		c.Status = *req.Status
	}
	if err := h.sso.UpdateClient(r.Context(), c); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.f.audit(r, "sso.client.updated", "client_id", c.ClientID, "status", c.Status)
	web.OK(w, c)
}

func (h *SSOHandler) deleteApp(w http.ResponseWriter, r *http.Request) {
	if err := h.sso.DeleteClient(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, ErrClientNotFound) {
			web.Err(w, http.StatusNotFound, "app not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.f.audit(r, "sso.client.deleted", "app_id", r.PathValue("id"))
	web.OK(w, nil)
}

// rotateSecret 轮换 client secret（旧 secret 立即失效；公共客户端无 secret，409）。
func (h *SSOHandler) rotateSecret(w http.ResponseWriter, r *http.Request) {
	c, err := h.sso.GetClient(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrClientNotFound) {
			web.Err(w, http.StatusNotFound, "app not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	if c.IsPublic {
		web.Err(w, http.StatusConflict, "public client has no secret")
		return
	}
	secret := NewSSOSecret()
	c.SecretHash = HashSecret(secret)
	if err := h.sso.UpdateClient(r.Context(), c); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.f.audit(r, "sso.client.secret_rotated", "client_id", c.ClientID)
	web.OK(w, appResp{Client: c, Secret: secret})
}

// ---------- 授权端点 ----------

// authorizeQuery 从 query 解析标准 OAuth2 authorize 参数。
func authorizeQuery(r *http.Request) AuthorizeRequest {
	q := r.URL.Query()
	return AuthorizeRequest{
		ResponseType:        q.Get("response_type"),
		ClientID:            q.Get("client_id"),
		RedirectURI:         q.Get("redirect_uri"),
		Scope:               q.Get("scope"),
		State:               q.Get("state"),
		CodeChallenge:       q.Get("code_challenge"),
		CodeChallengeMethod: q.Get("code_challenge_method"),
	}
}

type authorizeDesc struct {
	ClientName  string   `json:"client_name"`
	Scopes      []string `json:"scopes"`
	RedirectURI string   `json:"redirect_uri"`
	State       string   `json:"state"`
}

// describeAuthorize GET：校验并返回授权描述（Console 渲染确认页用）。
// 校验失败按 RFC 6749 §4.1.2.1：redirect_uri/client 无效时直接 400
// （不重定向，防开放重定向），其余以错误码返回供前端展示。
func (h *SSOHandler) describeAuthorize(w http.ResponseWriter, r *http.Request) {
	req := authorizeQuery(r)
	c, err := h.sso.GetClientByClientID(r.Context(), req.ClientID)
	if err != nil {
		writeOAuth2Error(w, http.StatusBadRequest, oauth2Error{Code: "invalid_client", Description: "unknown client_id"})
		return
	}
	scopes, _, oerr := validateAuthorize(c, req)
	if oerr.Code != "" {
		// describe 阶段统一 400 + OAuth2 错误体（redirectSafe 仅约束
		// decide 阶段的重定向行为）。
		writeOAuth2Error(w, http.StatusBadRequest, oerr)
		return
	}
	web.OK(w, authorizeDesc{ClientName: c.Name, Scopes: scopes, RedirectURI: req.RedirectURI, State: req.State})
}

type decideReq struct {
	Action string `json:"action"` // approve | deny
	AuthorizeRequest
}

// decideAuthorize POST：用户确认/拒绝授权，签发授权码并返回重定向地址。
// 确认页由 Console 渲染；本端点只负责裁决与 code 签发。
func (h *SSOHandler) decideAuthorize(w http.ResponseWriter, r *http.Request) {
	var req decideReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return
	}
	u, err := h.f.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	c, err := h.sso.GetClientByClientID(r.Context(), req.ClientID)
	if err != nil {
		writeOAuth2Error(w, http.StatusBadRequest, oauth2Error{Code: "invalid_client", Description: "unknown client_id"})
		return
	}
	if !matchRedirectURI(c.RedirectURIs, req.RedirectURI) {
		// 不重定向（开放重定向防护）。
		writeOAuth2Error(w, http.StatusBadRequest, oauth2Error{Code: "invalid_request", Description: "redirect_uri not registered"})
		return
	}
	if req.Action == "deny" {
		h.f.audit(r, "sso.authorize.denied", "user_id", u.ID, "client_id", c.ClientID)
		web.OK(w, map[string]string{
			"redirect_to": redirectWithError(req.RedirectURI, "access_denied", "user denied", req.State),
		})
		return
	}
	ac, err := issueAuthCode(r.Context(), h.sso, c, req.AuthorizeRequest, u.ID, h.ttl, time.Now().UTC())
	if err != nil {
		if oe, ok := err.(oauth2Error); ok {
			writeOAuth2Error(w, http.StatusBadRequest, oe)
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.f.audit(r, "sso.authorize.approved", "user_id", u.ID, "client_id", c.ClientID, "scopes", ac.Scopes)
	web.OK(w, map[string]string{"redirect_to": redirectWithCode(req.RedirectURI, ac.Code, req.State)})
}

func redirectWithCode(base, code, state string) string {
	q := url.Values{}
	q.Set("code", code)
	if state != "" {
		q.Set("state", state)
	}
	return appendQuery(base, q)
}

func redirectWithError(base, ecode, desc, state string) string {
	q := url.Values{}
	q.Set("error", ecode)
	if desc != "" {
		q.Set("error_description", desc)
	}
	if state != "" {
		q.Set("state", state)
	}
	return appendQuery(base, q)
}

func appendQuery(base string, q url.Values) string {
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + q.Encode()
}

// ---------- 令牌端点 ----------

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// token POST：OAuth2 令牌端点（application/x-www-form-urlencoded）。
// 客户端认证支持 HTTP Basic（推荐）与 form 参数（RFC 6749 §2.3.1）。
func (h *SSOHandler) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeOAuth2Error(w, http.StatusBadRequest, oauth2Error{Code: "invalid_request", Description: "malformed form body"})
		return
	}
	clientID, clientSecret := r.FormValue("client_id"), r.FormValue("client_secret")
	if id, sec, ok := r.BasicAuth(); ok {
		clientID, clientSecret = id, sec
	}
	now := time.Now().UTC()
	var (
		pair tokenPair
		oerr oauth2Error
	)
	switch grant := r.FormValue("grant_type"); grant {
	case GrantAuthorizationCode:
		pair, oerr = exchangeAuthCode(r.Context(), h.f, h.sso, h.ttl, clientID, clientSecret,
			r.FormValue("code"), r.FormValue("redirect_uri"), r.FormValue("code_verifier"), now)
	case GrantClientCredentials:
		pair, oerr = clientCredentialsToken(r.Context(), h.sso, h.ttl, clientID, clientSecret,
			r.FormValue("scope"), h.f.issuer, now)
	case GrantRefreshToken:
		pair, oerr = refreshTokens(r.Context(), h.sso, h.ttl, clientID, clientSecret,
			r.FormValue("refresh_token"), r.FormValue("scope"), h.f.issuer, h.f.users, now)
	default:
		oerr = oauth2Error{Code: "unsupported_grant_type", Description: "grant_type must be authorization_code, client_credentials or refresh_token"}
	}
	if oerr.Code != "" {
		status := http.StatusBadRequest
		if oerr.Code == "invalid_client" {
			status = http.StatusUnauthorized
		}
		h.f.audit(r, "sso.token.rejected", "grant", grant(r), "reason", oerr.Code)
		writeOAuth2Error(w, status, oerr)
		return
	}
	h.f.audit(r, "sso.token.issued", "grant", r.FormValue("grant_type"), "client_id", clientID)
	resp := tokenResp{
		AccessToken:  pair.AccessToken,
		TokenType:    pair.TokenType,
		ExpiresIn:    int64(time.Until(pair.ExpiresAt).Seconds()),
		RefreshToken: pair.RefreshToken,
		Scope:        strings.Join(pair.Scope, " "),
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	_ = json.NewEncoder(w).Encode(resp)
}

func grant(r *http.Request) string { return r.FormValue("grant_type") }

func writeOAuth2Error(w http.ResponseWriter, status int, e oauth2Error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(e)
}

// ---------- 三方登录 ----------

type socialURLResp struct {
	URL   string `json:"url"`
	State string `json:"state"`
}

// socialAuthorizeURL 构造三方登录跳转地址（state 由服务端 HMAC 签名，
// 回调时验签防伪造；浏览器会话绑定由 Console 用 sessionStorage 对照
// state 完成——契约：Console 保存返回的 state，回调页比对一致才继续）。
func (h *SSOHandler) socialAuthorizeURL(w http.ResponseWriter, r *http.Request) {
	p, err := h.social.Get(r.PathValue("provider"))
	if err != nil {
		web.Err(w, http.StatusNotFound, "provider not registered")
		return
	}
	state := h.signState(randHex(16))
	web.OK(w, socialURLResp{URL: p.AuthorizeURL(state), State: state})
}

// socialCallback 三方回调：换身份 → 自动注册/复用用户 → 签发平台 token。
// 响应与 Console /login 同构（token + expires_at），前端同通道消费。
func (h *SSOHandler) socialCallback(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	p, err := h.social.Get(provider)
	if err != nil {
		web.Err(w, http.StatusNotFound, "provider not registered")
		return
	}
	code, state := r.URL.Query().Get("code"), r.URL.Query().Get("state")
	if code == "" {
		web.Err(w, http.StatusBadRequest, "missing code")
		return
	}
	if !h.verifyState(state) {
		h.f.audit(r, "sso.social.rejected", "provider", provider, "reason", "bad_state")
		web.Err(w, http.StatusBadRequest, "invalid state")
		return
	}
	si, err := p.Exchange(r.Context(), code)
	if err != nil {
		h.f.audit(r, "sso.social.rejected", "provider", provider, "reason", "exchange_failed")
		web.Err(w, http.StatusUnauthorized, "provider exchange failed")
		return
	}
	// 身份映射：username = social:<provider>:<openid>（≤64 字符）。
	username := "social:" + provider + ":" + si.OpenID
	if len(username) > 64 {
		username = username[:64]
	}
	u, err := h.f.users.GetByUsername(r.Context(), username)
	if err != nil {
		// 首次登录：自动注册（随机口令防本地口令登录——三方身份是唯一通道）。
		hash, herr := bcryptHash(randHex(32))
		if herr != nil {
			web.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		u = User{
			ID:           NewUserID(),
			Username:     username,
			PasswordHash: hash,
			Email:        si.Email,
			Status:       StatusActive,
			TenantID:     "", // 平台级用户；归属租户由管理员后续调整
			CreatedAt:    time.Now().UTC(),
		}
		if err := h.f.users.Create(r.Context(), u); err != nil {
			web.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
	}
	if u.Status != StatusActive {
		h.f.audit(r, "sso.social.rejected", "provider", provider, "reason", "disabled")
		web.Err(w, http.StatusForbidden, "account disabled")
		return
	}
	tok, exp, err := h.f.issuer.Issue(u, time.Now().UTC())
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.f.audit(r, "sso.social.login", "provider", provider, "user_id", u.ID, "username", u.Username)
	web.OK(w, loginResp{Token: tok, ExpiresAt: exp})
}

// signState / verifyState：state = nonce.hmac(nonce)，密钥复用 JWT secret
// （同源信任域），签名截断 16 hex（防伪造足够：暴力在短时效内不可行）。
func (h *SSOHandler) signState(nonce string) string {
	mac := hmac.New(sha256.New, h.stateKey)
	mac.Write([]byte(nonce))
	return nonce + "." + hex.EncodeToString(mac.Sum(nil))[:16]
}

func (h *SSOHandler) verifyState(state string) bool {
	nonce, sig, ok := strings.Cut(state, ".")
	if !ok || nonce == "" || len(sig) != 16 {
		return false
	}
	mac := hmac.New(sha256.New, h.stateKey)
	mac.Write([]byte(nonce))
	return hmac.Equal([]byte(sig), []byte(hex.EncodeToString(mac.Sum(nil))[:16]))
}

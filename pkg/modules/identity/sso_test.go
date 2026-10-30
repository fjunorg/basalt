package identity_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/identity"
)

// ---------- 测试装配 ----------

// auditCapture 实现 identity.AuditSink：收集事件供断言。
type auditCapture struct {
	mu     sync.Mutex
	events []string
}

func (a *auditCapture) Sink(_, event, _, _ string, _ ...any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, event)
}

func (a *auditCapture) has(ev string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, e := range a.events {
		if e == ev {
			return true
		}
	}
	return false
}

// ssoFixture 手工装配的 SSO 测试环境（不经 Module.Init，控制 social 注册）。
type ssoFixture struct {
	handler  http.Handler
	store    *identity.MemSSOStore
	users    *identity.MemUserStore
	sink     *auditCapture
	ownerTok string // 已登录用户 token（授权者）
}

func newSSOFixture(t *testing.T, providers ...identity.SocialProvider) *ssoFixture {
	t.Helper()
	a := app.New(nil)
	users := identity.NewMemUserStore()
	roles := identity.NewMemRoleStore()
	// 预置 admin 角色（权限 "*"）——owner 绑定后才可过 Require。
	if err := roles.Create(context.Background(), identity.Role{
		ID: "role-admin", Code: "admin", Name: "Administrator", Permissions: []string{"*"},
	}); err != nil {
		t.Fatalf("create admin role: %v", err)
	}
	issuer := identity.NewTokenIssuer("basalt-dev-secret", 2*time.Hour)
	facade := identity.NewFacade(users, roles, issuer, a.Logger())
	sink := &auditCapture{}
	facade.SetAuditSink(sink)

	// 预置授权者用户（密码哈希为占位——不走密码登录，直接签发 token）。
	u := identity.User{
		ID: "u-owner", Username: "owner", Status: identity.StatusActive,
		RoleIDs: []string{"role-admin"}, CreatedAt: time.Now().UTC(),
	}
	if err := users.Create(context.Background(), u); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	tok, _, err := issuer.Issue(u, time.Now())
	if err != nil {
		t.Fatalf("issue owner token: %v", err)
	}

	store := identity.NewMemSSOStore()
	social := identity.NewSocialRegistry()
	for _, p := range providers {
		social.Register(p)
	}
	identity.NewSSOHandler(facade, store, 0, 0, social, "basalt-dev-secret").RegisterSSORoutes(a.Router())

	return &ssoFixture{handler: a.Router().Handler(), store: store, users: users, sink: sink, ownerTok: tok}
}

// ---------- 工具 ----------

func doForm(t *testing.T, h http.Handler, path string, form url.Values) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec, m
}

func doGetJSON(t *testing.T, h http.Handler, path, token string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec, m
}

func doPostJSON(t *testing.T, h http.Handler, path, token string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec, m
}

// pkcePair 生成 S256 verifier/challenge（43 字符合法 verifier）。
func pkcePair() (verifier, challenge string) {
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return
}

// createAppViaAPI 走管理 API 创建应用，返回响应与内部 client_id。
func createAppViaAPI(t *testing.T, f *ssoFixture, isPublic bool) (map[string]any, string, string) {
	t.Helper()
	grants := []string{"authorization_code", "client_credentials"}
	body := map[string]any{
		"name": "demo-app", "redirect_uris": []string{"https://app.example.com/cb"},
		"scopes": []string{"profile", "email"}, "grants": grants, "is_public": isPublic,
	}
	rec, m := doPostJSON(t, f.handler, "/api/v1/identity/sso/apps", f.ownerTok, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create app: %d %s", rec.Code, rec.Body.String())
	}
	data, _ := m["data"].(map[string]any)
	return data, data["client_id"].(string), ""
}

// fullAuthCodeFlow 完成「描述→确认→换码」三步，返回 code。
// scope 为请求的 scope 串（须 ⊆ 应用注册范围）。
func fullAuthCodeFlow(t *testing.T, f *ssoFixture, clientID, redirectURI, scope, verifier, challenge, method string) string {
	t.Helper()
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", scope)
	q.Set("state", "st-123")
	if challenge != "" {
		q.Set("code_challenge", challenge)
		q.Set("code_challenge_method", method)
	}
	// 1. GET 描述。
	rec, _ := doGetJSON(t, f.handler, "/api/v1/identity/sso/authorize?"+q.Encode(), f.ownerTok)
	if rec.Code != http.StatusOK {
		t.Fatalf("describe authorize: %d %s", rec.Code, rec.Body.String())
	}
	// 2. POST 确认。
	rec, m := doPostJSON(t, f.handler, "/api/v1/identity/sso/authorize", f.ownerTok, map[string]any{
		"action":        "approve",
		"response_type": "code", "client_id": clientID, "redirect_uri": redirectURI,
		"scope": scope, "state": "st-123",
		"code_challenge": challenge, "code_challenge_method": method,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("approve authorize: %d %s", rec.Code, rec.Body.String())
	}
	data, _ := m["data"].(map[string]any)
	redirectTo, _ := data["redirect_to"].(string)
	if redirectTo == "" {
		t.Fatal("empty redirect_to")
	}
	u, err := url.Parse(redirectTo)
	if err != nil {
		t.Fatalf("redirect_to parse: %v", err)
	}
	if u.Query().Get("state") != "st-123" {
		t.Fatalf("state not echoed: %q", u.Query().Get("state"))
	}
	code := u.Query().Get("code")
	if code == "" {
		t.Fatal("no code in redirect")
	}
	return code
}

// ---------- 应用管理 ----------

func TestSSOAppManagementCRUD(t *testing.T) {
	f := newSSOFixture(t)
	data, clientID, _ := createAppViaAPI(t, f, false)
	if data["secret"] == "" || data["secret_hash"] != nil {
		t.Fatal("confidential app must return one-time secret; hash must not leak")
	}
	appID := data["id"].(string)

	// 未认证/无权限（无 token 的请求者）401。
	if rec, _ := doPostJSON(t, f.handler, "/api/v1/identity/sso/apps", "", map[string]any{"name": "x"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated create: %d", rec.Code)
	}

	// 列表 / 单查。
	if rec, _ := doGetJSON(t, f.handler, "/api/v1/identity/sso/apps", f.ownerTok); rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	rec, _ := doGetJSON(t, f.handler, "/api/v1/identity/sso/apps/"+appID, f.ownerTok)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d", rec.Code)
	}
	var got identity.Client
	_ = json.Unmarshal(mustData(t, rec), &got)
	if got.ClientID != clientID || got.SecretHash != "" {
		t.Fatalf("client leaked or mismatch: %+v", got)
	}

	// 更新（禁用）。
	rec, _ = doPostJSON(t, f.handler, "/api/v1/identity/sso/apps/"+appID, f.ownerTok, map[string]any{"status": "disabled"})
	if rec.Code == http.StatusOK {
		t.Fatal("PATCH via POST should not be routed; expect 405/404")
	}

	// secret 轮换：新 secret 一次性返回。
	rec, m := doPostJSON(t, f.handler, "/api/v1/identity/sso/apps/"+appID+"/secret", f.ownerTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate secret: %d %s", rec.Code, rec.Body.String())
	}
	rd, _ := m["data"].(map[string]any)
	if rd["secret"] == "" {
		t.Fatal("rotated secret missing")
	}

	// 删除。
	if rec := doDelete(t, f.handler, "/api/v1/identity/sso/apps/"+appID, f.ownerTok); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec, _ := doGetJSON(t, f.handler, "/api/v1/identity/sso/apps/"+appID, f.ownerTok); rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: %d", rec.Code)
	}
	// 审计入链。
	if !f.sink.has("sso.client.created") || !f.sink.has("sso.client.deleted") || !f.sink.has("sso.client.secret_rotated") {
		t.Fatalf("audit events missing: %v", f.sink.events)
	}
}

func mustData(t *testing.T, rec *httptest.ResponseRecorder) json.RawMessage {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return env.Data
}

func doDelete(t *testing.T, h http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// ---------- 授权码 + PKCE ----------

func TestAuthorizationCodePKCEFullFlow(t *testing.T) {
	f := newSSOFixture(t)
	_, clientID, _ := createAppViaAPI(t, f, true) // public client
	verifier, challenge := pkcePair()
	code := fullAuthCodeFlow(t, f, clientID, "https://app.example.com/cb", "profile email", verifier, challenge, "S256")

	// 兑换：public client 无 secret，form client_id。
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", clientID)
	form.Set("code", code)
	form.Set("redirect_uri", "https://app.example.com/cb")
	form.Set("code_verifier", verifier)
	rec, m := doForm(t, f.handler, "/api/v1/identity/sso/token", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("token exchange: %d %s", rec.Code, rec.Body.String())
	}
	if m["access_token"] == nil || m["refresh_token"] == nil || m["token_type"] != "Bearer" {
		t.Fatalf("token response malformed: %v", m)
	}
	if !f.sink.has("sso.token.issued") || !f.sink.has("sso.authorize.approved") {
		t.Fatalf("audit missing: %v", f.sink.events)
	}
}

func TestPKCENegativeCases(t *testing.T) {
	f := newSSOFixture(t)
	_, clientID, _ := createAppViaAPI(t, f, true)
	_, challenge := pkcePair()

	// 1. public client 缺 code_challenge → describe 400。
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", "https://app.example.com/cb")
	q.Set("scope", "profile")
	if rec, _ := doGetJSON(t, f.handler, "/api/v1/identity/sso/authorize?"+q.Encode(), f.ownerTok); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing PKCE must 400, got %d", rec.Code)
	}
	// 2. plain method 拒绝（OAuth 2.1）。
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "plain")
	if rec, _ := doGetJSON(t, f.handler, "/api/v1/identity/sso/authorize?"+q.Encode(), f.ownerTok); rec.Code != http.StatusBadRequest {
		t.Fatalf("plain method must 400, got %d", rec.Code)
	}
	// 3. S256 签发但错误 verifier 兑换失败。
	code := fullAuthCodeFlow(t, f, clientID, "https://app.example.com/cb", "profile", "wrong-verifier-43-chars-aaaaaaaaaaaaaaaaaaaa", challenge, "S256")
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", clientID)
	form.Set("code", code)
	form.Set("redirect_uri", "https://app.example.com/cb")
	form.Set("code_verifier", "another-wrong-verifier-43-chars-aaaaaaaaaaaaa")
	rec, m := doForm(t, f.handler, "/api/v1/identity/sso/token", form)
	if rec.Code != http.StatusBadRequest || m["error"] != "invalid_grant" {
		t.Fatalf("wrong verifier: %d %v", rec.Code, m)
	}
}

func TestAuthCodeSingleUseAndExpiry(t *testing.T) {
	f := newSSOFixture(t)
	_, clientID, secret := createAppViaAPI(t, f, false)
	// 从 createAppViaAPI 拿不到 secret——机密客户端兑换需要 secret；
	// 改用直接构造（管理 API 的 secret 在 data["secret"]）。
	rec, m0 := doPostJSON(t, f.handler, "/api/v1/identity/sso/apps", f.ownerTok, map[string]any{
		"name": "conf-app", "redirect_uris": []string{"https://conf.example.com/cb"},
		"scopes": []string{"profile"}, "grants": []string{"authorization_code"}, "is_public": false,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d", rec.Code)
	}
	d0, _ := m0["data"].(map[string]any)
	confID, confSecret := d0["client_id"].(string), d0["secret"].(string)
	_ = clientID
	_ = secret

	// 机密客户端走 Basic Auth，无 PKCE。
	code := fullAuthCodeFlow(t, f, confID, "https://conf.example.com/cb", "profile", "", "", "")
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", "https://conf.example.com/cb")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/identity/sso/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(confID, confSecret)
	recw := httptest.NewRecorder()
	f.handler.ServeHTTP(recw, req)
	if recw.Code != http.StatusOK {
		t.Fatalf("confidential exchange: %d %s", recw.Code, recw.Body.String())
	}

	// 同一 code 二次使用 → invalid_grant（单次使用）。
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/identity/sso/token", strings.NewReader(form.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.SetBasicAuth(confID, confSecret)
	rec2 := httptest.NewRecorder()
	f.handler.ServeHTTP(rec2, req2)
	var em map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &em)
	if rec2.Code != http.StatusBadRequest || em["error"] != "invalid_grant" {
		t.Fatalf("code reuse: %d %v", rec2.Code, em)
	}

	// 过期 code：直接向 store 注入过期授权码。
	ac := identity.AuthCode{
		Code: "expired-code-123", ClientID: confID, UserID: "u-owner",
		RedirectURI: "https://conf.example.com/cb", Scopes: []string{"profile"},
		ExpiresAt: time.Now().Add(-time.Minute), CreatedAt: time.Now().Add(-2 * time.Minute),
	}
	if err := f.store.SaveAuthCode(context.Background(), ac); err != nil {
		t.Fatal(err)
	}
	form2 := url.Values{}
	form2.Set("grant_type", "authorization_code")
	form2.Set("code", ac.Code)
	form2.Set("redirect_uri", ac.RedirectURI)
	req3 := httptest.NewRequest(http.MethodPost, "/api/v1/identity/sso/token", strings.NewReader(form2.Encode()))
	req3.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req3.SetBasicAuth(confID, confSecret)
	rec3 := httptest.NewRecorder()
	f.handler.ServeHTTP(rec3, req3)
	var em3 map[string]any
	_ = json.Unmarshal(rec3.Body.Bytes(), &em3)
	if rec3.Code != http.StatusBadRequest || em3["error"] != "invalid_grant" {
		t.Fatalf("expired code: %d %v", rec3.Code, em3)
	}
}

func TestRedirectURIStrictValidation(t *testing.T) {
	f := newSSOFixture(t)
	_, clientID, _ := createAppViaAPI(t, f, true)
	base := url.Values{}
	base.Set("response_type", "code")
	base.Set("client_id", clientID)
	base.Set("scope", "profile")

	// 未注册 URI → 400 不重定向（开放重定向防护）。
	q := base
	q.Set("redirect_uri", "https://evil.example.com/cb")
	if rec, _ := doGetJSON(t, f.handler, "/api/v1/identity/sso/authorize?"+q.Encode(), f.ownerTok); rec.Code != http.StatusBadRequest {
		t.Fatalf("unregistered redirect_uri: %d", rec.Code)
	}
	// 注册 URI + 附加 query 参数也算不匹配（逐字节严格）。
	q.Set("redirect_uri", "https://app.example.com/cb?extra=1")
	if rec, _ := doGetJSON(t, f.handler, "/api/v1/identity/sso/authorize?"+q.Encode(), f.ownerTok); rec.Code != http.StatusBadRequest {
		t.Fatalf("redirect_uri with extra query: %d", rec.Code)
	}
	// 注册时 fragment 拒绝。
	rec, _ := doPostJSON(t, f.handler, "/api/v1/identity/sso/apps", f.ownerTok, map[string]any{
		"name": "bad", "redirect_uris": []string{"https://x.example.com/cb#frag"},
		"grants": []string{"authorization_code"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("fragment redirect_uri must be rejected: %d", rec.Code)
	}
}

// ---------- 客户端凭证模式 ----------

func TestClientCredentialsGrant(t *testing.T) {
	f := newSSOFixture(t)
	rec, m0 := doPostJSON(t, f.handler, "/api/v1/identity/sso/apps", f.ownerTok, map[string]any{
		"name": "m2m", "grants": []string{"client_credentials"},
		"scopes": []string{"api:read"}, "is_public": false,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d", rec.Code)
	}
	d, _ := m0["data"].(map[string]any)
	cid, secret := d["client_id"].(string), d["secret"].(string)

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", cid)
	form.Set("client_secret", secret)
	form.Set("scope", "api:read")
	rec1, m := doForm(t, f.handler, "/api/v1/identity/sso/token", form)
	if rec1.Code != http.StatusOK {
		t.Fatalf("client_credentials: %d %s", rec1.Code, rec1.Body.String())
	}
	if m["refresh_token"] != nil {
		t.Fatal("client_credentials must not issue refresh token")
	}
	if m["access_token"] == nil {
		t.Fatal("missing access token")
	}

	// 错误 secret → 401 invalid_client。
	form.Set("client_secret", "wrong-secret")
	rec2, m2 := doForm(t, f.handler, "/api/v1/identity/sso/token", form)
	if rec2.Code != http.StatusUnauthorized || m2["error"] != "invalid_client" {
		t.Fatalf("bad secret: %d %v", rec2.Code, m2)
	}
	// scope 超范围。
	form.Set("client_secret", secret)
	form.Set("scope", "api:admin")
	rec3, m3 := doForm(t, f.handler, "/api/v1/identity/sso/token", form)
	if rec3.Code != http.StatusBadRequest || m3["error"] != "invalid_scope" {
		t.Fatalf("scope exceeded: %d %v", rec3.Code, m3)
	}
}

// ---------- refresh 轮换与重放 ----------

func TestRefreshRotationAndReplayRevocation(t *testing.T) {
	f := newSSOFixture(t)
	rec, m0 := doPostJSON(t, f.handler, "/api/v1/identity/sso/apps", f.ownerTok, map[string]any{
		"name": "rot", "redirect_uris": []string{"https://rot.example.com/cb"},
		"scopes": []string{"profile", "email"}, "grants": []string{"authorization_code"}, "is_public": false,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d", rec.Code)
	}
	d, _ := m0["data"].(map[string]any)
	cid, secret := d["client_id"].(string), d["secret"].(string)

	code := fullAuthCodeFlow(t, f, cid, "https://rot.example.com/cb", "profile email", "", "", "")
	first := tokenExchange(t, f, cid, secret, code, "https://rot.example.com/cb", "")
	rt1, _ := first["refresh_token"].(string)

	// 第一次刷新：轮换成功，新 refresh。
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", cid)
	form.Set("client_secret", secret)
	form.Set("refresh_token", rt1)
	rec2, m2 := doForm(t, f.handler, "/api/v1/identity/sso/token", form)
	if rec2.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", rec2.Code, rec2.Body.String())
	}
	rt2, _ := m2["refresh_token"].(string)
	if rt2 == "" || rt2 == rt1 {
		t.Fatal("refresh token must rotate")
	}
	// scope 收窄允许。
	form.Set("refresh_token", rt2)
	form.Set("scope", "profile")
	rec3, m3 := doForm(t, f.handler, "/api/v1/identity/sso/token", form)
	if rec3.Code != http.StatusOK {
		t.Fatalf("refresh narrow scope: %d %s", rec3.Code, rec3.Body.String())
	}
	rt3, _ := m3["refresh_token"].(string)

	// 旧 rt2 重放（rt3 已轮换 rt2）→ invalid_grant + 整链吊销。
	form.Set("refresh_token", rt2)
	rec4, m4 := doForm(t, f.handler, "/api/v1/identity/sso/token", form)
	if rec4.Code != http.StatusBadRequest || m4["error"] != "invalid_grant" {
		t.Fatalf("replay must fail: %d %v", rec4.Code, m4)
	}
	// 链头 rt3 也应已被吊销。
	form.Set("refresh_token", rt3)
	rec5, m5 := doForm(t, f.handler, "/api/v1/identity/sso/token", form)
	if rec5.Code != http.StatusBadRequest || m5["error"] != "invalid_grant" {
		t.Fatalf("chain revocation: %d %v", rec5.Code, m5)
	}
}

func tokenExchange(t *testing.T, f *ssoFixture, cid, secret, code, redirectURI, verifier string) map[string]any {
	t.Helper()
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", cid)
	form.Set("client_secret", secret)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	if verifier != "" {
		form.Set("code_verifier", verifier)
	}
	rec, m := doForm(t, f.handler, "/api/v1/identity/sso/token", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("exchange: %d %s", rec.Code, rec.Body.String())
	}
	return m
}

// ---------- 三方登录（mock 钉钉） ----------

func TestSocialLoginDingTalkMock(t *testing.T) {
	// mock 钉钉开放平台 API。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1.0/oauth2/userAccessToken":
			_, _ = w.Write([]byte(`{"accessToken":"at-123","expireIn":7200}`))
		case "/v1.0/contact/users/me":
			_, _ = w.Write([]byte(`{"openId":"openid-abc","nick":"张三","email":"zs@example.com"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	prov := identity.NewDingTalkProviderForTest("cid", "csec", srv.URL, srv.URL, "https://console.example.com/cb")
	f := newSSOFixture(t, prov)

	// 1. authorize-url。
	rec, m := doGetJSON(t, f.handler, "/api/v1/identity/sso/social/dingtalk/authorize-url", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("authorize-url: %d %s", rec.Code, rec.Body.String())
	}
	d, _ := m["data"].(map[string]any)
	authURL, state := d["url"].(string), d["state"].(string)
	if !strings.Contains(authURL, "login_type") || state == "" {
		// 钉钉 AuthorizeURL 无 login_type——校验 client_id 与 state。
		if !strings.Contains(authURL, "client_id=cid") || state == "" {
			t.Fatalf("authorize url malformed: %v", d)
		}
	}

	// 2. callback（坏 state → 302 回跳错误态）。
	bad := "/api/v1/identity/sso/social/dingtalk/callback?code=c-1&state=forged.0123456789abcdef"
	recBad := httptest.NewRecorder()
	reqBad := httptest.NewRequest(http.MethodGet, bad, nil)
	f.handler.ServeHTTP(recBad, reqBad)
	if recBad.Code != http.StatusFound || !strings.Contains(recBad.Header().Get("Location"), "error=bad_state") {
		t.Fatalf("forged state: %d %s", recBad.Code, recBad.Header().Get("Location"))
	}

	// 3. callback 合法 state → 302 回跳 Console 回调页（token + expires_at）。
	cb := "/api/v1/identity/sso/social/dingtalk/callback?code=c-1&state=" + url.QueryEscape(state)
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, cb, nil)
	f.handler.ServeHTTP(rec2, req2)
	loc := rec2.Header().Get("Location")
	if rec2.Code != http.StatusFound || !strings.HasPrefix(loc, "/#/sso/social/callback?") {
		t.Fatalf("callback redirect: %d %q", rec2.Code, loc)
	}
	if !strings.Contains(loc, "token=") || !strings.Contains(loc, "expires_at=") {
		t.Fatalf("callback params missing: %q", loc)
	}
	// 用户已注册。
	if _, err := f.users.GetByUsername(context.Background(), "social:dingtalk:openid-abc"); err != nil {
		t.Fatalf("social user not created: %v", err)
	}
	if !f.sink.has("sso.social.login") {
		t.Fatalf("audit missing: %v", f.sink.events)
	}

	// 4. 二次登录复用同一用户。
	users, _ := f.users.List(context.Background())
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, cb, nil)
	f.handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusFound {
		t.Fatalf("second callback: %d", rec3.Code)
	}
	users2, _ := f.users.List(context.Background())
	if len(users2) != len(users) {
		t.Fatalf("second login must reuse user: %d -> %d", len(users), len(users2))
	}
}

// ---------- 模块级装配（路由确实挂载） ----------

func TestSSORegisteredViaModule(t *testing.T) {
	h := initRouter(t, newTestApp(t))
	admin := adminLogin(t, h)
	// 应用管理路由存在（401 → 200/403 分支证明挂载）。
	if rec, _ := doGetJSON(t, h, "/api/v1/identity/sso/apps", admin); rec.Code != http.StatusOK {
		t.Fatalf("module-registered apps route: %d", rec.Code)
	}
	// 未登录 authorize 401。
	if rec, _ := doGetJSON(t, h, "/api/v1/identity/sso/authorize?response_type=code", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("authorize unauthenticated: %d", rec.Code)
	}
}

// ---------- 纯函数单测 ----------

func TestParseScopesAndScopeAllowed(t *testing.T) {
	if got := identity.ParseScopes("  a  b a "); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("ParseScopes: %v", got)
	}
	if !identity.ScopeAllowed(nil, []string{"x"}) {
		t.Fatal("empty allowed = unrestricted")
	}
	if identity.ScopeAllowed([]string{"a", "b"}, []string{"a", "c"}) {
		t.Fatal("c not allowed")
	}
}

func TestHashSecretDeterministic(t *testing.T) {
	if identity.HashSecret("abc") != identity.HashSecret("abc") {
		t.Fatal("hash not deterministic")
	}
	if identity.HashSecret("abc") == identity.HashSecret("abd") {
		t.Fatal("hash collision")
	}
	s := identity.NewSSOSecret()
	if len(s) != 43 {
		t.Fatalf("secret length: %d", len(s))
	}
}

var (
	_ = errors.New
	_ = fmt.Sprintf
)

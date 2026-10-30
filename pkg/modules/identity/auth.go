package identity

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/basalt-go/basalt/pkg/kernel/web"
)

// TokenIssuer 签发与校验访问令牌（HS256）。
// M2 演进：refresh token 轮换、国密 SM2 签名算法可插拔（接口不变）。
type TokenIssuer struct {
	secret []byte
	ttl    time.Duration
	revoke RevocationChecker // 可选：吊销检查器（在线用户踢下线）
}

func NewTokenIssuer(secret string, ttl time.Duration) *TokenIssuer {
	return &TokenIssuer{secret: []byte(secret), ttl: ttl}
}

// Issue 为用户签发令牌，返回令牌与过期时刻。
// claims 含随机 jti：同一秒内同用户重复签发也产生可区分的唯一令牌
// （登出按令牌指纹吊销单会话的前提，见 session.go）。
func (t *TokenIssuer) Issue(u User, now time.Time) (string, time.Time, error) {
	exp := now.Add(t.ttl)
	claims := jwt.RegisteredClaims{
		ID:        randTokenID(),
		Subject:   u.ID,
		ExpiresAt: jwt.NewNumericDate(exp),
		IssuedAt:  jwt.NewNumericDate(now),
		Issuer:    "basalt/identity",
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString(t.secret)
	return s, exp, err
}

// IssueOAuth2 为 OAuth2 流程（SSO）签发令牌：与 Issue 同一密钥/算法/
// 过期语义（AuthMiddleware 校验路径不变），claims 附加 client_id 与
// scope 以区分「Console 直登」与「OAuth2 授权」两类令牌。subject 为
// 授权用户 ID（授权码/refresh 路径）或 "client:<client_id>" 机器身份
// （客户端凭证路径）。
func (t *TokenIssuer) IssueOAuth2(u User, clientID string, scopes []string, now time.Time) (string, time.Time, error) {
	exp := now.Add(t.ttl)
	claims := jwt.MapClaims{
		"jti":       randTokenID(),
		"sub":       u.ID,
		"exp":       exp.Unix(),
		"iat":       now.Unix(),
		"iss":       "basalt/identity",
		"client_id": clientID,
		"scope":     strings.Join(scopes, " "),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString(t.secret)
	return s, exp, err
}

// randTokenID 生成令牌唯一标识（128 位熵 hex）。
func randTokenID() string {
	b := make([]byte, 16)
	if _, err := cryptorand.Read(b); err != nil {
		panic("identity: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

type ctxKey int

const (
	userIDKey  ctxKey = iota
	tokenFPKey        // 令牌指纹（吊销检查启用后注入）
)

// RevocationChecker 是令牌吊销检查器（在线用户踢下线/登出的生效点）。
// AuthMiddleware 在签名校验通过后调用；iat 为令牌签发时刻。
type RevocationChecker interface {
	CheckToken(ctx context.Context, userID, tokenID string, iat time.Time) bool
}

// SetRevocationChecker 注入吊销检查器（identity Init 期装配 OnlineStore；
// nil 或未注入 = 无吊销能力，保持纯无状态语义——与既有行为兼容）。
func (t *TokenIssuer) SetRevocationChecker(c RevocationChecker) { t.revoke = c }

// AuthMiddleware 校验 Bearer 令牌并把用户 ID 注入请求上下文。
// 失败统一返回 401，不区分"缺失/过期/伪造"，避免向攻击者泄露状态。
// 注入吊销检查器后额外执行踢下线/登出裁决（session.go 选型说明）。
func (t *TokenIssuer) AuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(auth, "Bearer ")
		if !ok || token == "" {
			web.Err(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		parsed, err := jwt.Parse(token, func(tk *jwt.Token) (any, error) {
			return t.secret, nil
		}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
		if err != nil || !parsed.Valid {
			web.Err(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		sub, err := parsed.Claims.GetSubject()
		if err != nil || sub == "" {
			web.Err(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if t.revoke != nil {
			// 令牌指纹（在线会话注册表主键口径）+ 签发时刻。
			fp := TokenFingerprint(token)
			iat := time.Now()
			if ts, err := parsed.Claims.GetIssuedAt(); err == nil && ts != nil {
				iat = ts.Time
			}
			if !t.revoke.CheckToken(r.Context(), sub, fp, iat) {
				web.Err(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			// 指纹供登出（单会话吊销）定位当前会话。
			r = r.WithContext(context.WithValue(r.Context(), tokenFPKey, fp))
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userIDKey, sub)))
	}
}

// UserIDFrom 取出中间件注入的用户 ID。
func UserIDFrom(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(userIDKey).(string)
	return id, ok
}

// TokenFPFrom 取出中间件注入的令牌指纹（吊销检查启用后有效）。
func TokenFPFrom(ctx context.Context) (string, bool) {
	fp, ok := ctx.Value(tokenFPKey).(string)
	return fp, ok
}

// WithUserID 把用户 ID 注入上下文（复用 AuthMiddleware 的注入语义，
// 供测试与非 HTTP 链路构造等价上下文，如租户路由 store 的解析）。
func WithUserID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, userIDKey, id)
}

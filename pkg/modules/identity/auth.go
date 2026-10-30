package identity

import (
	"context"
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
}

func NewTokenIssuer(secret string, ttl time.Duration) *TokenIssuer {
	return &TokenIssuer{secret: []byte(secret), ttl: ttl}
}

// Issue 为用户签发令牌，返回令牌与过期时刻。
func (t *TokenIssuer) Issue(u User, now time.Time) (string, time.Time, error) {
	exp := now.Add(t.ttl)
	claims := jwt.RegisteredClaims{
		Subject:   u.ID,
		ExpiresAt: jwt.NewNumericDate(exp),
		IssuedAt:  jwt.NewNumericDate(now),
		Issuer:    "basalt/identity",
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString(t.secret)
	return s, exp, err
}

type ctxKey int

const userIDKey ctxKey = iota

// AuthMiddleware 校验 Bearer 令牌并把用户 ID 注入请求上下文。
// 失败统一返回 401，不区分"缺失/过期/伪造"，避免向攻击者泄露状态。
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
		next(w, r.WithContext(context.WithValue(r.Context(), userIDKey, sub)))
	}
}

// UserIDFrom 取出中间件注入的用户 ID。
func UserIDFrom(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(userIDKey).(string)
	return id, ok
}

// WithUserID 把用户 ID 注入上下文（复用 AuthMiddleware 的注入语义，
// 供测试与非 HTTP 链路构造等价上下文，如租户路由 store 的解析）。
func WithUserID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, userIDKey, id)
}

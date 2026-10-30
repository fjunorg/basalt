package identity

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/basalt-go/basalt/pkg/kernel/web"
)

// AuditSink 是审计事件接收端，由 audit 模块注入——identity 不反向
// 依赖 audit（避免环），事件经此接口进入防篡改审计链。
type AuditSink interface {
	Sink(module, event, actorID, remote string, kv ...any)
}

// Facade 是 identity 模块对依赖模块暴露的鉴权门面：
// Auth（认证）与 Require（权限点校验）可组合使用，使 tenant 等模块
// 无需复制授权逻辑。这是模块间协作的官方通道。
type Facade struct {
	users      UserStore
	roles      RoleStore
	issuer     *TokenIssuer
	logger     *slog.Logger
	sink       AuditSink   // Init 期注入，请求期只读
	sessions   OnlineStore // 在线会话注册表（登录登记/登出用，可选）
	permFilter func(ctx context.Context, u User, perms []string) []string
}

func NewFacade(users UserStore, roles RoleStore, issuer *TokenIssuer, logger *slog.Logger) *Facade {
	return &Facade{users: users, roles: roles, issuer: issuer, logger: logger}
}

// SetAuditSink 注入审计接收端（audit 模块 Init 时调用）。
func (f *Facade) SetAuditSink(s AuditSink) { f.sink = s }

// SetSessionStore 注入在线会话注册表（identity Init 期装配）。
func (f *Facade) SetSessionStore(s OnlineStore) { f.sessions = s }

// SetPermFilter 注入权限过滤钩子（tenantpkg 模块 Init 时调用）：
// permissionsOf 汇总角色权限后经此过滤再返回（套餐裁剪交集语义）。
// Require/Can 及依赖它们的动态菜单下发（menu /me）在同一链路生效。
// 平台级用户（无租户）由钩子实现自行放行。
func (f *Facade) SetPermFilter(fn func(ctx context.Context, u User, perms []string) []string) {
	f.permFilter = fn
}

// Auth 是令牌校验中间件（原 TokenIssuer.AuthMiddleware 的门面转发）。
func (f *Facade) Auth(next http.HandlerFunc) http.HandlerFunc {
	return f.issuer.AuthMiddleware(next)
}

// Require 是权限点校验中间件，必须置于 Auth 之后。
// 无权限返回 403（区别于未认证的 401），事件计入审计日志。
func (f *Facade) Require(perm string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, ok := UserIDFrom(r.Context())
		if !ok {
			web.Err(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		u, err := f.users.Get(r.Context(), uid)
		if err != nil {
			if errors.Is(err, ErrUserNotFound) {
				web.Err(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			web.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		granted, err := f.permissionsOf(r, u)
		if err != nil {
			web.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		if !HasPermission(granted, perm) {
			f.audit(r, "authz.denied", "user_id", uid, "permission", perm)
			web.Err(w, http.StatusForbidden, "forbidden")
			return
		}
		next(w, r)
	}
}

// Can 判定当前用户是否具有指定权限点（通配规则同 Require）。
// 供业务模块做非中间件式的权限分支（如列表 all/mine 分流）。
func (f *Facade) Can(r *http.Request, perm string) bool {
	u, err := f.User(r)
	if err != nil {
		return false
	}
	granted, err := f.permissionsOf(r, u)
	return err == nil && HasPermission(granted, perm)
}

// User 从请求上下文还原当前用户（认证后有效）。
func (f *Facade) User(r *http.Request) (User, error) {
	uid, ok := UserIDFrom(r.Context())
	if !ok {
		return User{}, ErrUserNotFound
	}
	return f.users.Get(r.Context(), uid)
}

// PermissionsOf 汇总用户全部角色的权限点。
func (f *Facade) permissionsOf(r *http.Request, u User) ([]string, error) {
	var out []string
	for _, rid := range u.RoleIDs {
		role, err := f.roles.Get(r.Context(), rid)
		if err != nil {
			if errors.Is(err, ErrRoleNotFound) {
				continue // 角色已删除但关联残留：跳过而非拒绝全部
			}
			return nil, err
		}
		out = append(out, role.Permissions...)
	}
	// 套餐裁剪钩子（tenantpkg 注入；未注入 = 不过滤，向后兼容）。
	if f.permFilter != nil {
		out = f.permFilter(r.Context(), u, out)
	}
	return out, nil
}

// RolesOf 返回用户全部有效角色（数据权限解析用；角色缺失跳过）。
func (f *Facade) RolesOf(ctx context.Context, u User) ([]Role, error) {
	var out []Role
	for _, rid := range u.RoleIDs {
		role, err := f.roles.Get(ctx, rid)
		if err != nil {
			if errors.Is(err, ErrRoleNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, role)
	}
	return out, nil
}

func (f *Facade) audit(r *http.Request, event string, kv ...any) {
	if f.logger != nil {
		args := append([]any{"event", event, "remote", r.RemoteAddr}, kv...)
		f.logger.Info("audit", args...)
	}
	if f.sink != nil {
		f.sink.Sink("identity", event, kvString(kv, "user_id"), r.RemoteAddr, kv...)
	}
}

func kvString(kv []any, key string) string {
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok && k == key {
			if v, ok := kv[i+1].(string); ok {
				return v
			}
		}
	}
	return ""
}

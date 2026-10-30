package identity

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/basalt-go/basalt/pkg/kernel/dataperm"
	"github.com/basalt-go/basalt/pkg/kernel/web"
)

// Handler 承载 identity 模块全部 HTTP 端点。
// 认证与授权统一经 Facade（Auth/Require），授权逻辑因此对依赖模块复用。
// 审计说明：关键事件以结构化日志输出（"audit"）；M2 的 mod-audit 落地后
// 切换为防篡改审计管道，处理器签名不变。
type Handler struct {
	f *Facade
}

func NewHandler(f *Facade) *Handler { return &Handler{f: f} }

// RegisterRoutes 在 /api/v1/identity 前缀下注册全部端点。
// 写操作均要求认证 + 对应权限点（admin 内置角色持有 "*"）。
func (h *Handler) RegisterRoutes(r web.Router) {
	auth := h.f.Auth
	req := h.f.Require
	g := r.Group("/api/v1/identity")
	g.Handle(http.MethodGet, "/info", h.info)

	g.Handle(http.MethodPost, "/users", auth(req(PermUserCreate, h.createUser)))
	g.Handle(http.MethodGet, "/users", auth(req(PermUserList, h.listUsers)))
	g.Handle(http.MethodGet, "/users/{id}", auth(req(PermUserGet, h.getUser)))
	g.Handle(http.MethodPatch, "/users/{id}/status", auth(req(PermUserStatus, h.updateStatus)))
	g.Handle(http.MethodPut, "/users/{id}/roles", auth(req(PermRoleManage, h.assignRoles)))

	g.Handle(http.MethodPost, "/roles", auth(req(PermRoleManage, h.createRole)))
	g.Handle(http.MethodGet, "/roles", auth(req(PermRoleManage, h.listRoles)))
	g.Handle(http.MethodPatch, "/roles/{id}", auth(req(PermRoleManage, h.updateRole)))
	g.Handle(http.MethodDelete, "/roles/{id}", auth(req(PermRoleManage, h.deleteRole)))

	g.Handle(http.MethodPost, "/login", h.login)
	g.Handle(http.MethodGet, "/me", auth(h.me))

	// 在线会话（注册表启用时挂载）：登出本人会话 + 管理员在线列表/踢下线。
	if h.f.sessions != nil {
		g.Handle(http.MethodPost, "/logout", auth(h.logout))
		g.Handle(http.MethodGet, "/sessions", auth(req(PermSessionManage, h.listSessions)))
		g.Handle(http.MethodDelete, "/sessions/user/{id}", auth(req(PermSessionManage, h.kickUser)))
	}
}

func (h *Handler) info(w http.ResponseWriter, _ *http.Request) {
	web.OK(w, map[string]any{"module": "identity", "version": Version})
}

type createUserReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Email    string `json:"email"`
	TenantID string `json:"tenant_id"`
	DeptID   string `json:"dept_id"`
	PostID   string `json:"post_id"`
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request) {
	var req createUserReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if err := validateNewUser(req.Username, req.Password, req.Email); err != nil {
		web.Err(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	u := User{
		ID:           NewUserID(),
		Username:     req.Username,
		PasswordHash: string(hash),
		Email:        req.Email,
		Status:       StatusActive,
		TenantID:     req.TenantID,
		DeptID:       req.DeptID,
		PostID:       req.PostID,
		CreatedAt:    time.Now().UTC(),
	}
	if err := h.f.users.Create(r.Context(), u); err != nil {
		if errors.Is(err, ErrUsernameTaken) {
			web.Err(w, http.StatusConflict, "username already taken")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.f.audit(r, "user.created", "username", u.Username, "user_id", u.ID, "tenant_id", u.TenantID)
	web.Created(w, u)
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.f.users.List(r.Context())
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, users)
}

func (h *Handler) getUser(w http.ResponseWriter, r *http.Request) {
	u, err := h.f.users.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			web.Err(w, http.StatusNotFound, "user not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, u)
}

type updateStatusReq struct {
	Status string `json:"status"`
}

func (h *Handler) updateStatus(w http.ResponseWriter, r *http.Request) {
	var req updateStatusReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return
	}
	err := h.f.users.UpdateStatus(r.Context(), r.PathValue("id"), req.Status)
	switch {
	case errors.Is(err, ErrUserNotFound):
		web.Err(w, http.StatusNotFound, "user not found")
		return
	case errors.Is(err, ErrBadStatus):
		web.Err(w, http.StatusBadRequest, "status must be active or disabled")
		return
	case err != nil:
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.f.audit(r, "user.status_changed", "user_id", r.PathValue("id"), "status", req.Status)
	web.OK(w, nil)
}

type assignRolesReq struct {
	RoleIDs []string `json:"role_ids"`
}

func (h *Handler) assignRoles(w http.ResponseWriter, r *http.Request) {
	var req assignRolesReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return
	}
	// 全部角色必须存在，避免静默授予空权限集合。
	for _, rid := range req.RoleIDs {
		if _, err := h.f.roles.Get(r.Context(), rid); err != nil {
			web.Err(w, http.StatusBadRequest, "unknown role id: "+rid)
			return
		}
	}
	if err := h.f.users.AssignRoles(r.Context(), r.PathValue("id"), req.RoleIDs); err != nil {
		if errors.Is(err, ErrUserNotFound) {
			web.Err(w, http.StatusNotFound, "user not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.f.audit(r, "user.roles_assigned", "user_id", r.PathValue("id"), "role_ids", req.RoleIDs)
	web.OK(w, nil)
}

type createRoleReq struct {
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
	DataScope   *string  `json:"data_scope"`
	DeptIDs     []string `json:"dept_ids"`
}

// applyDataScope 应用并校验数据范围声明（创建/更新共用）。
func applyDataScope(cur, scope string, deptIDs []string, has bool) (string, []string, error) {
	if !has {
		return cur, nil, nil // 未传不动
	}
	if scope == "" {
		scope = "all"
	}
	if !dataperm.ValidScope(scope) {
		return "", nil, fmt.Errorf("invalid data_scope (want all|dept|dept_and_below|self|custom)")
	}
	if scope != dataperm.ScopeCustom {
		deptIDs = nil
	}
	return scope, deptIDs, nil
}

func (h *Handler) createRole(w http.ResponseWriter, r *http.Request) {
	var req createRoleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if !isValidRoleCode(req.Code) || req.Name == "" {
		web.Err(w, http.StatusBadRequest, "role code (a-z, 0-9, -) and name are required")
		return
	}
	scope, deptIDs, err := applyDataScope("all", deref(req.DataScope), req.DeptIDs, req.DataScope != nil)
	if err != nil {
		web.Err(w, http.StatusBadRequest, err.Error())
		return
	}
	role := Role{ID: NewUserID(), Code: req.Code, Name: req.Name, Permissions: req.Permissions,
		DataScope: scope, DeptIDs: deptIDs}
	if err := h.f.roles.Create(r.Context(), role); err != nil {
		if errors.Is(err, ErrRoleCodeTaken) {
			web.Err(w, http.StatusConflict, "role code already taken")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.f.audit(r, "role.created", "role", req.Code, "role_id", role.ID)
	web.Created(w, role)
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (h *Handler) listRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := h.f.roles.List(r.Context())
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, roles)
}

type updateRoleReq struct {
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
	DataScope   *string  `json:"data_scope"`
	DeptIDs     []string `json:"dept_ids"`
}

func (h *Handler) updateRole(w http.ResponseWriter, r *http.Request) {
	var req updateRoleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return
	}
	role, err := h.f.roles.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrRoleNotFound) {
			web.Err(w, http.StatusNotFound, "role not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	if req.Name != "" {
		role.Name = req.Name
	}
	if req.Permissions != nil {
		role.Permissions = req.Permissions
	}
	scope, deptIDs, err := applyDataScope(role.DataScope, deref(req.DataScope), req.DeptIDs, req.DataScope != nil)
	if err != nil {
		web.Err(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.DataScope != nil {
		role.DataScope, role.DeptIDs = scope, deptIDs
	}
	if err := h.f.roles.Update(r.Context(), role); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.f.audit(r, "role.updated", "role_id", role.ID, "permissions", role.Permissions)
	web.OK(w, role)
}

func (h *Handler) deleteRole(w http.ResponseWriter, r *http.Request) {
	if err := h.f.roles.Delete(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, ErrRoleNotFound) {
			web.Err(w, http.StatusNotFound, "role not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.f.audit(r, "role.deleted", "role_id", r.PathValue("id"))
	web.OK(w, nil)
}

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResp struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return
	}
	u, err := h.f.users.GetByUsername(r.Context(), req.Username)
	if err != nil {
		h.f.audit(r, "login.failed", "username", req.Username, "reason", "unknown_user")
		web.Err(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)) != nil {
		h.f.audit(r, "login.failed", "username", req.Username, "reason", "bad_password")
		web.Err(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if u.Status != StatusActive {
		h.f.audit(r, "login.failed", "username", req.Username, "reason", "disabled")
		web.Err(w, http.StatusForbidden, "account disabled")
		return
	}
	token, exp, err := h.f.issuer.Issue(u, time.Now())
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	// 在线会话登记（注册表启用时）：指纹为主键，不存原始令牌。
	// 登记失败不阻断登录（注册表是视图能力），仅日志告警。
	if h.f.sessions != nil {
		now := time.Now()
		ses := OnlineSession{
			TokenID:   TokenFingerprint(token),
			UserID:    u.ID,
			Username:  u.Username,
			TenantID:  u.TenantID,
			Remote:    r.RemoteAddr,
			UserAgent: r.UserAgent(),
			LoginAt:   now,
			ExpiresAt: exp,
		}
		if err := h.f.sessions.Register(r.Context(), ses); err != nil && h.f.logger != nil {
			h.f.logger.Warn("session register failed", "user_id", u.ID, "err", err.Error())
		}
	}
	h.f.audit(r, "login.success", "username", u.Username, "user_id", u.ID, "tenant_id", u.TenantID)
	web.OK(w, loginResp{Token: token, ExpiresAt: exp})
}

type meResp struct {
	User
	Permissions []string `json:"permissions"`
}

// logout 登出当前会话（单会话吊销，立即生效——下一个请求 401）。
func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	fp, ok := TokenFPFrom(r.Context())
	if ok {
		if err := h.f.sessions.Revoke(r.Context(), fp, time.Now()); err != nil && h.f.logger != nil {
			h.f.logger.Warn("session revoke failed", "err", err.Error())
		}
	}
	uid, _ := UserIDFrom(r.Context())
	h.f.audit(r, "logout", "user_id", uid)
	web.OK(w, nil)
}

// listSessions 在线用户列表（未吊销未过期，登录时间倒序）。
func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := h.f.sessions.ListOnline(r.Context(), time.Now())
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	if sessions == nil {
		sessions = []OnlineSession{}
	}
	web.OK(w, sessions)
}

// kickUser 踢下线：吊销目标用户全部会话并记录用户级吊销时间戳
// （iat < kicked_at 的旧令牌后续请求一律 401，立即生效）。
func (h *Handler) kickUser(w http.ResponseWriter, r *http.Request) {
	target := r.PathValue("id")
	if _, err := h.f.users.Get(r.Context(), target); err != nil {
		if errors.Is(err, ErrUserNotFound) {
			web.Err(w, http.StatusNotFound, "user not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	n, err := h.f.sessions.RevokeByUser(r.Context(), target, time.Now())
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	uid, _ := UserIDFrom(r.Context())
	h.f.audit(r, "session.kicked", "actor_id", uid, "user_id", target, "revoked", n)
	web.OK(w, map[string]any{"revoked": n})
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	u, err := h.f.User(r)
	if err != nil {
		// 令牌有效但用户已被删除：按 401 处理强制重新认证。
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	perms, err := h.f.permissionsOf(r, u)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, meResp{User: u, Permissions: perms})
}

// validateNewUser 校验创建用户请求。密码最短 8 位对应 SECURITY.md
// 默认安全基线；M2 接入可配置密码策略（复杂度/字典/历史）。
func validateNewUser(username, password, email string) error {
	username = strings.TrimSpace(username)
	if len(username) < 3 || len(username) > 32 {
		return errors.New("username must be 3-32 characters")
	}
	if len(password) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	if email != "" && !strings.Contains(email, "@") {
		return errors.New("invalid email")
	}
	return nil
}

func isValidRoleCode(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

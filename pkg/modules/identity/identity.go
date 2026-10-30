// Package identity 是 L1 身份模块：用户、角色（RBAC）、认证与令牌。
//
// M1 当前交付：用户/角色 CRUD、bcrypt 口令、JWT、权限点中间件、
// 引导管理员、结构化审计事件。M1 后续迭代：数据权限/OAuth2-SSO，
// 存储接入 GORM（含国产数据库方言）。
package identity

import (
	"context"
	"errors"

	"golang.org/x/crypto/bcrypt"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/config"
)

const Version = "0.3.0"

type Module struct {
	users  UserStore
	roles  RoleStore
	sso    SSOStore
	online OnlineStore
	facade *Facade
	boot   bootstrap
}

type bootstrap struct{ username, password string }

func New() *Module {
	return &Module{
		users:  NewMemUserStore(),
		roles:  NewMemRoleStore(),
		sso:    NewMemSSOStore(),
		online: NewMemOnlineStore(),
	}
}

// NewWithStores 允许注入自定义存储（测试与 GORM 装配）。
func NewWithStores(users UserStore, roles RoleStore) *Module {
	return &Module{users: users, roles: roles, sso: NewMemSSOStore(), online: NewMemOnlineStore()}
}

// NewWithStoresAndSSO 显式注入 SSO 存储（GORM 持久化装配）。
func NewWithStoresAndSSO(users UserStore, roles RoleStore, sso SSOStore) *Module {
	return &Module{users: users, roles: roles, sso: sso, online: NewMemOnlineStore()}
}

// NewFull 全量注入（GORM 装配：SSO + 在线会话注册表均持久化）。
func NewFull(users UserStore, roles RoleStore, sso SSOStore, online OnlineStore) *Module {
	return &Module{users: users, roles: roles, sso: sso, online: online}
}

// Users/Roles/Facade 供依赖模块（如 tenant）按声明式依赖取用；
// Facade 提供跨模块复用的认证与权限校验中间件。
func (m *Module) Users() UserStore { return m.users }
func (m *Module) Roles() RoleStore { return m.roles }
func (m *Module) Facade() *Facade  { return m.facade }

func (*Module) Name() string           { return "identity" }
func (*Module) Version() string        { return Version }
func (*Module) Dependencies() []string { return nil }

func (m *Module) Init(_ context.Context, a *app.App) error {
	cfg := a.Config()
	// SECURITY.md 默认安全基线：生产环境禁止开发默认密钥与默认口令。
	def := config.Defaults()
	if cfg.IsProd() {
		if cfg.JWTSecret == def.JWTSecret {
			return errors.New("identity: BASALT_JWT_SECRET must be set to a non-default value in prod")
		}
		if cfg.BootstrapAdminPassword == def.BootstrapAdminPassword {
			return errors.New("identity: BASALT_BOOTSTRAP_ADMIN_PASS must be set to a non-default value in prod")
		}
	}

	// 内置超级角色：权限 "*"。创建失败（如已存在）不视为错误，
	// 保证模块重入安全。
	_ = m.roles.Create(context.Background(), Role{
		ID: "role-admin", Code: RoleAdmin, Name: "Administrator", Permissions: []string{"*"},
	})

	issuer := NewTokenIssuer(cfg.JWTSecret, cfg.TokenTTL)
	m.facade = NewFacade(m.users, m.roles, issuer, a.Logger())
	// 在线会话注册表：登记（login）/登出/踢下线（kicked_at 立即生效）。
	if m.online != nil {
		m.facade.SetSessionStore(m.online)
		issuer.SetRevocationChecker(m.online)
	}
	NewHandler(m.facade).RegisterRoutes(a.Router())

	// SSO/OAuth2 子域（可注入装配）：SSOStore 为 nil 时跳过（最小装配）。
	if m.sso != nil {
		social := NewSocialRegistry()
		// 三方登录提供方：凭据 env 注入，未配置不下发（端点 404）。
		if dt, err := NewDingTalkProviderFromEnv(); err == nil {
			social.Register(dt)
		}
		NewSSOHandler(m.facade, m.sso, cfg.SSOAuthCodeTTL, cfg.SSORefreshTTL, social, cfg.JWTSecret).
			RegisterSSORoutes(a.Router())
	}
	m.boot = bootstrap{username: cfg.BootstrapAdminUsername, password: cfg.BootstrapAdminPassword}
	return nil
}

// Start 完成首启引导：引导管理员不存在时创建并绑定 admin 角色。
// 幂等——已存在（含口令已改）则不动，不构成重置口令后门。
func (m *Module) Start(ctx context.Context) error {
	if m.boot.username == "" {
		return nil
	}
	if _, err := m.users.GetByUsername(ctx, m.boot.username); err == nil {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(m.boot.password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	admin, err := m.roles.GetByCode(ctx, RoleAdmin)
	if err != nil {
		return errors.New("identity: built-in admin role missing")
	}
	u := User{
		ID:           NewUserID(),
		Username:     m.boot.username,
		PasswordHash: string(hash),
		Email:        "",
		Status:       StatusActive,
		RoleIDs:      []string{admin.ID},
	}
	if err := m.users.Create(ctx, u); err != nil {
		return err
	}
	return nil
}

func (*Module) Stop(context.Context) error { return nil }

// 编译期校验接口契约。
var _ app.Module = (*Module)(nil)

// Describe 实现模块自描述：权限点供前端动态菜单与平台 API 暴露。
func (*Module) Describe() app.ModuleInfo {
	return app.ModuleInfo{
		Name:    "identity",
		Version: Version,
		Permissions: []string{
			PermUserCreate, PermUserList, PermUserGet, PermUserStatus, PermRoleManage,
			PermSSOManage, PermSessionManage,
		},
	}
}

package gormstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"gorm.io/gorm"

	"github.com/basalt-go/basalt/pkg/modules/dict"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/tenant"
)

// ErrTenantDBUnavailable 表示 dedicated-db 租户库不可用（DSN 未配置
// 或连接/迁移失败）。携带此错误即明确失败——调用方不得静默回退主库，
// 回退意味着租户数据落错库、隔离被破坏。
var ErrTenantDBUnavailable = errors.New("gormstore: tenant database unavailable")

// TenantRouter 为 dedicated-db 租户解析、初始化并缓存专属库连接。
//
// 连接串约定：租户 DBRef（如 "gov-db"）→ 环境变量
// BASALT_TENANT_DSN_<REF大写>（如 BASALT_TENANT_DSN_GOV-DB →
// BASALT_TENANT_DSN_GOV_DB，非字母数字转下划线）。凭据只经环境
// 变量注入、不落库（docs/tenant-isolation.md）。
//
// 库初始化：首次访问懒建（gormstore.Open = 打开连接 + 全量
// AutoMigrate，与主库同一迁移路径），成功后缓存复用；重启后缓存
// 重建、数据延续（租户库是普通文件/网络库）。
type TenantRouter struct {
	mu    sync.RWMutex
	dbs   map[string]*gorm.DB
	dsnOf func(ref string) string
}

// NewTenantRouter 构造按环境变量约定解析 DSN 的路由器。
func NewTenantRouter() *TenantRouter {
	return &TenantRouter{
		dbs:   map[string]*gorm.DB{},
		dsnOf: dsnFromEnv,
	}
}

// NewTenantRouterWithDSN 注入自定义 DSN 解析（测试用）。
func NewTenantRouterWithDSN(dsnOf func(ref string) string) *TenantRouter {
	return &TenantRouter{dbs: map[string]*gorm.DB{}, dsnOf: dsnOf}
}

// dsnFromEnv 环境变量约定解析。
func dsnFromEnv(ref string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(ref) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return os.Getenv("BASALT_TENANT_DSN_" + b.String())
}

// DB 返回 dbRef 对应的租户库连接（懒初始化 + 缓存）。
// 任何失败均返回 ErrTenantDBUnavailable 包装错误——绝不回退主库。
func (r *TenantRouter) DB(dbRef string) (*gorm.DB, error) {
	if dbRef == "" {
		return nil, fmt.Errorf("%w: tenant has no db_ref configured", ErrTenantDBUnavailable)
	}
	r.mu.RLock()
	db, ok := r.dbs[dbRef]
	r.mu.RUnlock()
	if ok {
		return db, nil
	}
	dsn := r.dsnOf(dbRef)
	if dsn == "" {
		return nil, fmt.Errorf("%w: BASALT_TENANT_DSN for ref %q not configured", ErrTenantDBUnavailable, dbRef)
	}
	st, err := Open(dsn)
	if err != nil {
		return nil, fmt.Errorf("%w: open %q: %v", ErrTenantDBUnavailable, dbRef, err)
	}
	db = st.DB
	r.mu.Lock()
	r.dbs[dbRef] = db
	r.mu.Unlock()
	return db, nil
}

// Close 关闭全部缓存的租户库连接（进程退出前调用）。
func (r *TenantRouter) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for ref, db := range r.dbs {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
		delete(r.dbs, ref)
	}
}

// tenantResolver 是路由型 store 的公共底座：按请求上下文解析目标库
//（规则见 RoutedDictStore 类型注释）。
type tenantResolver struct {
	master  *gorm.DB
	users   identity.UserStore
	tenants tenant.TenantStore
	router  *TenantRouter
}

// dbFor 按请求上下文解析目标库：
//   - 未认证 / 平台用户 / shared-schema 租户 → 主库（现状行为）
//   - dedicated-db 租户 → 专属库（懒初始化；不可用明确报错不回退）
func (r *tenantResolver) dbFor(ctx context.Context) (*gorm.DB, error) {
	uid, ok := identity.UserIDFrom(ctx)
	if !ok {
		return r.master, nil // 未认证链路（内部任务等）：主库
	}
	u, err := r.users.Get(ctx, uid)
	if err != nil || u.TenantID == "" {
		return r.master, nil // 平台用户或用户已删除：主库
	}
	t, err := r.tenants.Get(ctx, u.TenantID)
	if err != nil {
		return r.master, nil
	}
	if t.IsolationMode != tenant.IsolationDedicatedDB {
		return r.master, nil // 行级隔离租户：主库（现状）
	}
	return r.router.DB(t.DBRef)
}

// RoutedDictStore 是 dict.Store 的租户路由实现（dedicated-db 端到端
// 示范；全量接入见 routed_stores.go）。
type RoutedDictStore struct{ tenantResolver }

// 编译期接口对齐检查。
var _ dict.Store = (*RoutedDictStore)(nil)

// NewRoutedDictStore 构造路由型字典存储。
func NewRoutedDictStore(master *gorm.DB, users identity.UserStore, tenants tenant.TenantStore, router *TenantRouter) *RoutedDictStore {
	return &RoutedDictStore{tenantResolver{master: master, users: users, tenants: tenants, router: router}}
}

func (s *RoutedDictStore) CreateType(ctx context.Context, t dict.Type) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return dictCreateType(db, t)
}

func (s *RoutedDictStore) UpdateType(ctx context.Context, t dict.Type) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return dictUpdateType(db, t)
}

func (s *RoutedDictStore) DeleteType(ctx context.Context, id string) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return dictDeleteType(db, id)
}

func (s *RoutedDictStore) ListTypes(ctx context.Context) ([]dict.Type, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return dictListTypes(db)
}

func (s *RoutedDictStore) CreateData(ctx context.Context, d dict.Data) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return dictCreateData(db, d)
}

func (s *RoutedDictStore) UpdateData(ctx context.Context, d dict.Data) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return dictUpdateData(db, d)
}

func (s *RoutedDictStore) DeleteData(ctx context.Context, id string) error {
	db, err := s.dbFor(ctx)
	if err != nil {
		return err
	}
	return dictDeleteData(db, id)
}

func (s *RoutedDictStore) ListDataByType(ctx context.Context, typeCode string) ([]dict.Data, error) {
	db, err := s.dbFor(ctx)
	if err != nil {
		return nil, err
	}
	return dictListDataByType(db, typeCode)
}

// Package gormstore 提供基于 GORM 的持久化实现（M1 收尾交付）。
//
// 方言策略（信创，见 docs/01）：
//   - 开发/测试/All-in-One：glebarez/sqlite（纯 Go，无 CGO，交叉编译友好）
//   - openGauss：postgres 驱动（协议兼容）—— OpenDB(gorm.Open(postgres.Open(...)))
//   - 达梦/OceanBase：经 OpenDB 注入社区驱动方言；CI 真机矩阵随 M1 后期建设
//
// 模块经 NewWithStore(s) / NewWithStores(...) 注入，替换内存实现，
// 模块逻辑零改动——存储可插拔是 L1 的硬约束。
package gormstore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/basalt-go/basalt/pkg/modules/agent"
	"github.com/basalt-go/basalt/pkg/modules/aisafety"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/contract"
	"github.com/basalt-go/basalt/pkg/modules/dict"
	"github.com/basalt-go/basalt/pkg/modules/document"
	"github.com/basalt-go/basalt/pkg/modules/file"
	"github.com/basalt-go/basalt/pkg/modules/finance"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/job"
	"github.com/basalt-go/basalt/pkg/modules/notice"
	"github.com/basalt-go/basalt/pkg/modules/org"
	"github.com/basalt-go/basalt/pkg/modules/rag"
	"github.com/basalt-go/basalt/pkg/modules/sysconfig"
	"github.com/basalt-go/basalt/pkg/modules/tenant"
	"github.com/basalt-go/basalt/pkg/modules/workflow"
)

// Open 打开 SQLite（含自动迁移）。dsn 如 ":memory:" 或文件路径。
func Open(dsn string) (*Stores, error) {
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	return OpenDB(db)
}

// OpenDB 基于任意方言的 *gorm.DB 构建（postgres/openGauss、达梦等）。
// 调用方负责驱动选择与连接参数；自动迁移在此统一执行。
func OpenDB(db *gorm.DB) (*Stores, error) {
	if err := db.AutoMigrate(
		&identity.User{}, &identity.Role{}, &tenant.Tenant{},
		&org.Dept{}, &org.Post{},
		&dict.Type{}, &dict.Data{},
		&sysconfig.Item{},
		&notice.Notice{},
		&file.Meta{},
		&job.Job{},
		&finance.Expense{}, &finance.Invoice{}, &finance.Budget{}, &finance.Payment{},
		&document.Doc{},
		&contract.Contract{},
		&workflow.Instance{}, &workflow.Action{}, &workflow.Definition{},
		&aisafety.Term{}, &aisafety.Policy{}, &aisafety.Settings{},
		&rag.KnowledgeBase{}, &rag.Document{}, &rag.Chunk{},
		&agent.Definition{}, &agent.Run{}, &agent.RunMessage{},
	); err != nil {
		return nil, err
	}
	return &Stores{DB: db}, nil
}

// Stores 聚合存储实现，并暴露底层 *gorm.DB 供扩展。
type Stores struct {
	DB *gorm.DB
}

func (s *Stores) UserStore() identity.UserStore   { return &UserStore{db: s.DB} }
func (s *Stores) RoleStore() identity.RoleStore   { return &RoleStore{db: s.DB} }
func (s *Stores) TenantStore() tenant.TenantStore { return &TenantStore{db: s.DB} }

func (s *Stores) OrgStore() org.Store                 { return &OrgStore{db: s.DB} }
func (s *Stores) DictStore() dict.Store               { return &DictStore{db: s.DB} }
func (s *Stores) SysConfigStore() sysconfig.Store     { return &SysConfigStore{db: s.DB} }
func (s *Stores) NoticeStore() notice.Store           { return &NoticeStore{db: s.DB} }
func (s *Stores) FileStore() file.Store               { return &FileStore{db: s.DB} }
func (s *Stores) JobStore() job.Store                 { return &JobStore{db: s.DB} }
func (s *Stores) FinanceStore() finance.Store         { return &FinanceStore{db: s.DB} }
func (s *Stores) DocumentStore() document.Store       { return &DocumentStore{db: s.DB} }
func (s *Stores) ContractStore() contract.Store       { return &ContractStore{db: s.DB} }
func (s *Stores) WorkflowStore() workflow.Store       { return &WorkflowStore{db: s.DB} }
func (s *Stores) WorkflowDefStore() workflow.DefStore { return &WorkflowDefStore{db: s.DB} }
func (s *Stores) AISafetyStore() aisafety.Store       { return &AISafetyStore{db: s.DB} }
func (s *Stores) RagStore() rag.Store                 { return &RagStore{db: s.DB} }
func (s *Stores) AgentStore() agent.Store             { return &AgentStore{db: s.DB} }

// AuditChainStore 按传入哈希器构造持久化审计链（SM3/SHA-256 由调用方
// 依 BASALT_CRYPTO_ALGO 决定）。
func (s *Stores) AuditChainStore(hasher audit.Hasher) (*AuditChain, error) {
	return NewAuditChain(s.DB, hasher)
}
func (s *Stores) QuotaStore() (*QuotaStore, error) {
	return NewQuotaStore(s.DB)
}

// ---------- UserStore ----------

type UserStore struct{ db *gorm.DB }

func (s *UserStore) Create(_ context.Context, u identity.User) error {
	var cnt int64
	if err := s.db.Model(&identity.User{}).Where("username = ?", u.Username).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return identity.ErrUsernameTaken
	}
	return s.db.Create(&u).Error
}

func (s *UserStore) Get(_ context.Context, id string) (identity.User, error) {
	var u identity.User
	err := s.db.First(&u, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return identity.User{}, identity.ErrUserNotFound
	}
	return u, err
}

func (s *UserStore) GetByUsername(_ context.Context, username string) (identity.User, error) {
	var u identity.User
	err := s.db.First(&u, "username = ?", username).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return identity.User{}, identity.ErrUserNotFound
	}
	return u, err
}

func (s *UserStore) List(_ context.Context) ([]identity.User, error) {
	var out []identity.User
	err := s.db.Order("created_at").Find(&out).Error
	return out, err
}

// ListByTenant 列出指定租户的用户；tenantID 为空串表示平台级用户
// （与 identity.UserStore 接口语义一致，行级隔离读取路径）。
func (s *UserStore) ListByTenant(_ context.Context, tenantID string) ([]identity.User, error) {
	var out []identity.User
	err := s.db.Where("tenant_id = ?", tenantID).Order("created_at").Find(&out).Error
	return out, err
}

func (s *UserStore) UpdateStatus(_ context.Context, id, status string) error {
	if status != identity.StatusActive && status != identity.StatusDisabled {
		return identity.ErrBadStatus
	}
	res := s.db.Model(&identity.User{}).Where("id = ?", id).Update("status", status)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return identity.ErrUserNotFound
	}
	return nil
}

func (s *UserStore) AssignRoles(_ context.Context, userID string, roleIDs []string) error {
	// serializer:json 仅在 struct 写入路径生效，单列 Update 需手动序列化。
	b, err := json.Marshal(roleIDs)
	if err != nil {
		return err
	}
	res := s.db.Model(&identity.User{}).Where("id = ?", userID).
		Update("role_ids", string(b))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return identity.ErrUserNotFound
	}
	return nil
}

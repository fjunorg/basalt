package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/basalt-go/basalt/console"
	"github.com/basalt-go/basalt/pkg/ai/gateway"
	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/config"
	"github.com/basalt-go/basalt/pkg/kernel/crypto"
	"github.com/basalt-go/basalt/pkg/kernel/web"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/aisafety"
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
	"github.com/basalt-go/basalt/pkg/stores/gormstore"
)

// hasherFor 按配置构造审计链哈希器（SM3/SHA-256）。
func hasherFor(cfg *config.Config) audit.Hasher {
	p := crypto.NewProvider(cfg.CryptoAlgo)
	return providerHasher{p}
}

type providerHasher struct{ p crypto.Provider }

func (h providerHasher) Hash(ctx context.Context, data []byte) ([]byte, error) {
	return h.p.Hash(ctx, data)
}
func (h providerHasher) Algorithm() string { return h.p.Algorithm() }

// ragFileSource 适配 rag.FileResolver：经 file 模块元信息 + 本地字节
// 存储读出文件内容（file 包零改动，接线收敛在装配层）。
type ragFileSource struct {
	store   file.Store
	storage file.Storage
}

func (s *ragFileSource) ResolveFile(ctx context.Context, fileID string) (string, io.ReadCloser, error) {
	meta, err := s.store.Get(ctx, fileID)
	if err != nil {
		return "", nil, rag.ErrFileNotFound
	}
	rc, err := s.storage.Open(ctx, meta.StoragePath)
	if err != nil {
		return "", nil, err
	}
	return meta.Name, rc, nil
}

func newServeCmd() *cobra.Command {
	var addr, env string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the HTTP server",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg := config.Load()
			if addr != "" {
				cfg.Addr = addr
			}
			if env != "" {
				cfg.Env = env
			}
			a := app.New(cfg)
			web.MountHealth(a.Router())
			a.Router().MountSPA(console.FS())

			// 上传目录（file 模块文件本体落此磁盘目录；元数据随所选
			// 存储装配持久化，二者共同构成重启恢复态）。
			uploadDir := os.Getenv("BASALT_UPLOAD_DIR")
			if uploadDir == "" {
				uploadDir = "uploads"
			}
			localStorage, err := file.NewLocalStorage(uploadDir)
			if err != nil {
				return fmt.Errorf("upload dir: %w", err)
			}

			// rag 模块依赖件：向量化客户端。BASALT_AI_PROVIDER_URL/
			// BASALT_AI_EMBED_MODEL 未配置时 ragEmb 为 nil——知识库/
			// 切片照常可用，向量化与检索在请求期明确报错（不静默降级）。
			// file 引用通道（ragFiles）在各存储分支内随 fileM 接线。
			var ragFiles rag.FileResolver
			ragEmb, embErr := rag.NewOpenAIEmbeddingsFromEnv()
			if embErr != nil && !errors.Is(embErr, rag.ErrEmbedNotConfigured) {
				return fmt.Errorf("rag embedding client: %w", embErr)
			}

			// 存储装配：memory（默认零依赖）或 sqlite（持久化：
			// 全部模块落盘，重启延续）。postgres/openGauss 经
			// gormstore.OpenDB 注入对应方言连接（驱动接线随信创
			// 真机矩阵落地，见 docs/01），装配结构已就绪。
			var idm *identity.Module
			var tnt *tenant.Module
			var adm *audit.Module
			var gw = gateway.New()
			var orgM *org.Module
			var dictM *dict.Module
			var sysM *sysconfig.Module
			var notM *notice.Module
			var fileM *file.Module
			var jobM *job.Module
			var finM *finance.Module
			var docM *document.Module
			var conM *contract.Module
			var wfM *workflow.Module
			var ragM *rag.Module
			var safetyM *aisafety.Module
			switch cfg.Storage {
			case "sqlite":
				dsn := cfg.DBDSN
				if dsn == "" {
					dsn = "basalt.db"
				}
				st, err := gormstore.Open(dsn)
				if err != nil {
					return fmt.Errorf("open storage %s: %w", dsn, err)
				}
				chain, err := st.AuditChainStore(hasherFor(cfg))
				if err != nil {
					return fmt.Errorf("audit chain: %w", err)
				}
				qs, err := st.QuotaStore()
				if err != nil {
					return fmt.Errorf("quota store: %w", err)
				}
				idm = identity.NewWithStores(st.UserStore(), st.RoleStore())
				tnt = tenant.NewWithStore(st.TenantStore())
				adm = audit.NewWithStore(chain)
				gw = gateway.NewWithQuotaStore(qs)
				orgM = org.NewWithStore(st.OrgStore())
				dictM = dict.NewWithStore(st.DictStore())
				// dedicated-db 租户路由（可选，默认关闭维持现状）：
				// BASALT_TENANT_ROUTING=on 时 dict 模块按请求租户路由到
				// 专属库（示范接入，其余模块照 docs/tenant-isolation.md）。
				if os.Getenv("BASALT_TENANT_ROUTING") == "on" {
					tRouter := gormstore.NewTenantRouter()
					defer tRouter.Close() // 随进程优雅退出
					dictM = dict.NewWithStore(gormstore.NewRoutedDictStore(
						st.DB, st.UserStore(), st.TenantStore(), tRouter))
				}
				sysM = sysconfig.NewWithStore(st.SysConfigStore())
				notM = notice.NewWithStore(st.NoticeStore())
				fileM = file.NewWithStore(st.FileStore(), localStorage)
				jobM = job.NewWithStore(a.Logger(), st.JobStore())
				finM = finance.NewWithStore(st.FinanceStore())
				docM = document.NewWithStore(st.DocumentStore())
				conM = contract.NewWithStore(st.ContractStore())
				wfM = workflow.NewWithStores(st.WorkflowStore(), st.WorkflowDefStore())
				ragFiles = &ragFileSource{store: fileM.Store(), storage: localStorage}
				ragM = rag.NewWithStore(st.RagStore(), ragFiles, ragEmb)
				safetyM = aisafety.NewWithStore(st.AISafetyStore())
			case "postgres", "opengauss":
				// openGauss 走 postgres 协议方言；驱动（gorm.io/driver/
				// postgres 或信创社区驱动）接线后在此 OpenDB 注入即可，
				// 9 模块 store 与 sqlite 分支共用同一 OpenDB 迁移路径。
				return fmt.Errorf("storage %s: driver wiring pending (use sqlite/memory for now)", cfg.Storage)
			default: // memory（测试/演示可退回的零依赖路径）
				idm = identity.New()
				tnt = tenant.New()
				adm = audit.New()
				orgM = org.New()
				dictM = dict.New()
				sysM = sysconfig.New()
				notM = notice.New()
				fileM = file.New(localStorage)
				jobM = job.New(a.Logger())
				wfM = workflow.New()
				finM = finance.New()
				docM = document.New()
				conM = contract.New()
				ragFiles = &ragFileSource{store: fileM.Store(), storage: localStorage}
				ragM = rag.New(ragFiles, ragEmb)
				safetyM = aisafety.New()
			}

			// 内容安全审核器注入网关（两路径均保留审核能力：
			// 持久化路径词库/策略跨重启延续，内存路径随进程）。
			gw.SetInspector(safetyM.Inspector())

			// OpenAI 兼容提供方装配（私有化一等公民）：
			// BASALT_AI_PROVIDER_URL/NAME/KEY，如 vLLM/Ollama/Qwen/GLM 端点。
			if u := os.Getenv("BASALT_AI_PROVIDER_URL"); u != "" {
				gw.RegisterProvider(os.Getenv("BASALT_AI_PROVIDER_NAME"), u, os.Getenv("BASALT_AI_PROVIDER_KEY"))
			}
			for _, m := range []app.Module{
				idm, tnt, adm,
				orgM, dictM, sysM, notM,
				fileM, jobM,
				wfM, finM, docM, conM, gw, ragM, safetyM,
			} {
				if err := a.Registry().Register(m); err != nil {
					return err
				}
			}
			return a.Run(context.Background())
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "", "HTTP listen address (env BASALT_ADDR, default \":8080\")")
	cmd.Flags().StringVar(&env, "env", "", "dev|test|staging|prod (env BASALT_ENV, default \"dev\")")
	return cmd
}

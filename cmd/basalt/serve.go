package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/basalt-go/basalt/console"
	"github.com/basalt-go/basalt/pkg/ai/gateway"
	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/config"
	"github.com/basalt-go/basalt/pkg/kernel/crypto"
	"github.com/basalt-go/basalt/pkg/kernel/web"
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

			// 存储装配：memory（默认零依赖）或 sqlite（持久化：
			// 身份/租户/审计链/配额全部落盘，重启延续）。
			var idm *identity.Module
			var tnt *tenant.Module
			var adm *audit.Module
			var gw *gateway.Module = gateway.New()
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
			default: // memory
				idm = identity.New()
				tnt = tenant.New()
				adm = audit.New()
			}

			// OpenAI 兼容提供方装配（私有化一等公民）：
			// BASALT_AI_PROVIDER_URL/KEY/MODEL，如 vLLM/Ollama/Qwen/GLM 端点。
			if u := os.Getenv("BASALT_AI_PROVIDER_URL"); u != "" {
				gw.RegisterProvider(os.Getenv("BASALT_AI_PROVIDER_NAME"), u, os.Getenv("BASALT_AI_PROVIDER_KEY"))
			}
			uploadDir := os.Getenv("BASALT_UPLOAD_DIR")
			if uploadDir == "" {
				uploadDir = "uploads"
			}
			localStorage, err := file.NewLocalStorage(uploadDir)
			if err != nil {
				return fmt.Errorf("upload dir: %w", err)
			}
			for _, m := range []app.Module{
				idm, tnt, adm,
				org.New(), dict.New(), sysconfig.New(), notice.New(),
				file.New(localStorage), job.New(a.Logger()),
				workflow.New(), finance.New(), document.New(), contract.New(), gw,
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

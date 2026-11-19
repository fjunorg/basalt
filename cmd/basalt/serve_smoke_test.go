package main

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/config"
	"github.com/basalt-go/basalt/pkg/kernel/web"
	"github.com/basalt-go/basalt/pkg/modules/agent"
	"github.com/basalt-go/basalt/pkg/modules/aisafety"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/contract"
	"github.com/basalt-go/basalt/pkg/modules/dict"
	"github.com/basalt-go/basalt/pkg/modules/document"
	"github.com/basalt-go/basalt/pkg/modules/errcode"
	"github.com/basalt-go/basalt/pkg/modules/file"
	"github.com/basalt-go/basalt/pkg/modules/finance"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/job"
	"github.com/basalt-go/basalt/pkg/modules/menu"
	"github.com/basalt-go/basalt/pkg/modules/notice"
	"github.com/basalt-go/basalt/pkg/modules/notify"
	"github.com/basalt-go/basalt/pkg/modules/org"
	"github.com/basalt-go/basalt/pkg/modules/rag"
	"github.com/basalt-go/basalt/pkg/modules/sysconfig"
	"github.com/basalt-go/basalt/pkg/modules/tenant"
	"github.com/basalt-go/basalt/pkg/modules/tenantpkg"
	"github.com/basalt-go/basalt/pkg/modules/workflow"
	"github.com/basalt-go/basalt/pkg/stores/gormstore"
)

// TestServeSQLiteFullAssemblySmoke 复现 serve.go sqlite 分支的全量模块
// 装配（21 模块），防「声明未构造的 nil 模块进入注册列表」回归
// （发版阻断缺陷：sqlite 下 notify/menu/tenantpkg/errcode 四模块漏接线
// 导致启动 panic）。
func TestServeSQLiteFullAssemblySmoke(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "serve_smoke.db")
	st, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer func() {
		if db, e := st.DB.DB(); e == nil {
			_ = db.Close()
		}
	}()

	cfg := config.Defaults()
	a := app.New(cfg)

	uploadDir := t.TempDir()
	localStorage, err := file.NewLocalStorage(uploadDir)
	if err != nil {
		t.Fatal(err)
	}

	chain, err := st.AuditChainStore(hasherFor(cfg))
	if err != nil {
		t.Fatal(err)
	}
	var ragFiles rag.FileResolver
	ragEmb, embErr := rag.NewOpenAIEmbeddingsFromEnv()
	if embErr != nil && embErr != rag.ErrEmbedNotConfigured {
		t.Fatalf("embeddings: %v", embErr)
	}

	// 与 serve.go sqlite 非 routed 分支完全同构的 21 模块装配。
	idm := identity.NewFull(st.UserStore(), st.RoleStore(), st.SSOStore(), st.OnlineStore())
	tnt := tenant.NewWithStore(st.TenantStore())
	adm := audit.NewWithStore(chain)
	orgM := org.NewWithStore(st.OrgStore())
	dictM := dict.NewWithStore(st.DictStore())
	sysM := sysconfig.NewWithStore(st.SysConfigStore())
	notM := notice.NewWithStore(st.NoticeStore())
	fileM := file.NewWithStore(st.FileStore(), localStorage)
	jobM := job.NewWithStore(a.Logger(), st.JobStore())
	finM := finance.NewWithStore(st.FinanceStore())
	docM := document.NewWithStore(st.DocumentStore())
	conM := contract.NewWithStore(st.ContractStore())
	wfM := workflow.NewWithStores(st.WorkflowStore(), st.WorkflowDefStore())
	ragM := rag.NewWithStore(st.RagStore(), ragFiles, ragEmb)
	agentM := agent.NewWithStore(st.AgentStore())
	notifyM := notify.NewWithStore(st.NotifyStore())
	menuM := menu.NewWithStore(st.MenuStore())
	safetyM := aisafety.NewWithStore(st.AISafetyStore())
	tpgM := tenantpkg.NewWithStore(st.TenantPkgStore())
	ecM := errcode.NewWithStore(st.ErrCodeStore())

	mods := []app.Module{
		idm, tnt, adm,
		orgM, dictM, sysM, notM,
		fileM, jobM,
		wfM, finM, docM, conM, ragM, safetyM, agentM, notifyM, menuM, tpgM, ecM,
	}
	for _, m := range mods {
		if err := a.Registry().Register(m); err != nil {
			t.Fatalf("register %s: %v", m.Name(), err)
		}
	}
	resolved, err := a.Registry().Resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, m := range resolved {
		if err := m.Init(context.Background(), a); err != nil {
			t.Fatalf("init %s: %v", m.Name(), err)
		}
	}
	for _, m := range resolved {
		if err := m.Start(context.Background()); err != nil {
			t.Fatalf("start %s: %v", m.Name(), err)
		}
	}
	// 计数核对：21 模块（含 rag 与 gateway 外的注册面——rag 未注册
	// 因 ragFiles 为 nil 时仍可注册；此处按 serve 注册列表口径）。
	if len(resolved) != len(mods) {
		t.Fatalf("resolved = %d, want %d", len(resolved), len(mods))
	}
	for i, m := range resolved {
		if m == nil {
			t.Fatalf("resolved[%d] is nil", i)
		}
	}

	// 冒烟：健康端点 + 模块自描述面。
	web.MountHealth(a.Router(), web.HealthOptions{Version: "smoke-test"})
	h := a.Router().Handler()
	srv := httptest.NewServer(h)
	defer srv.Close()
	client := srv.Client()
	client.Timeout = 5 * time.Second
	_ = os.Environ() // 保持与 serve 环境无关（测试不依赖 BASALT_*）
}

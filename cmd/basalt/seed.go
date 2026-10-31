// basalt seed --demo：演示数据集初始化（幂等）。
//
// 设计：固定 demo-* 语义 ID；每类数据以自身首条存在性为标记跳过，
// 二次执行数据量不变。prod 环境拒绝（--force 显式覆盖）；仅 sqlite
// 持久化有效（memory 无意义，直接提示）。密码统一 Demo@2026。
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/bcrypt"

	"github.com/basalt-go/basalt/pkg/kernel/config"
	"github.com/basalt-go/basalt/pkg/modules/agent"
	"github.com/basalt-go/basalt/pkg/modules/contract"
	"github.com/basalt-go/basalt/pkg/modules/dict"
	"github.com/basalt-go/basalt/pkg/modules/document"
	"github.com/basalt-go/basalt/pkg/modules/finance"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/menu"
	"github.com/basalt-go/basalt/pkg/modules/notice"
	"github.com/basalt-go/basalt/pkg/modules/notify"
	"github.com/basalt-go/basalt/pkg/modules/org"
	"github.com/basalt-go/basalt/pkg/modules/rag"
	"github.com/basalt-go/basalt/pkg/modules/tenant"
	"github.com/basalt-go/basalt/pkg/modules/tenantpkg"
	"github.com/basalt-go/basalt/pkg/modules/workflow"
	"github.com/basalt-go/basalt/pkg/stores/gormstore"
)

// demoPass 是全部演示账号的统一口令（README 与完成说明公开）。
const demoPass = "Demo@2026"

func newSeedCmd() *cobra.Command {
	var demo, force bool
	cmd := &cobra.Command{
		Use:   "seed",
		Short: "Data factory utilities (--demo: idempotent demo dataset)",
		RunE: func(c *cobra.Command, _ []string) error {
			if !demo {
				return c.Usage()
			}
			cfg := config.Load()
			if cfg.Env == "prod" && !force {
				return fmt.Errorf("refusing to seed demo data in prod env (set BASALT_ENV=dev or pass --force to override)")
			}
			if cfg.Storage != "sqlite" {
				return fmt.Errorf("seed requires sqlite storage (BASALT_STORAGE=sqlite); memory would be discarded on exit")
			}
			dsn := cfg.DBDSN
			if dsn == "" {
				dsn = "basalt.db"
			}
			st, err := gormstore.Open(dsn)
			if err != nil {
				return fmt.Errorf("open storage %s: %w", dsn, err)
			}
			ctx := context.Background()
			report := seedDemo(ctx, st)
			fmt.Println("demo dataset seeded into", dsn)
			for _, line := range report {
				fmt.Println(" -", line)
			}
			fmt.Printf("test accounts password: %s\n", demoPass)
			return nil
		},
	}
	cmd.Flags().BoolVar(&demo, "demo", false, "seed idempotent demo dataset")
	cmd.Flags().BoolVar(&force, "force", false, "allow seeding in prod env (dangerous)")
	return cmd
}

// seedDemo 灌入演示数据集，返回逐类摘要（含 skipped 标记）。
func seedDemo(ctx context.Context, st *gormstore.Stores) []string {
	var report []string
	now := time.Now().UTC()
	track := func(name string, n int, skipped bool) {
		if skipped {
			report = append(report, fmt.Sprintf("%s: skipped (already seeded)", name))
		} else {
			report = append(report, fmt.Sprintf("%s: %d items", name, n))
		}
	}

	// ---- 菜单种子（模块 Start 幂等播种，空库时补齐）----
	_ = menu.NewWithStore(st.MenuStore()).Start(ctx)
	report = append(report, "menu: ensured (module seeding)")

	// ---- 组织：三级部门树 + 岗位 ----
	if _, err := st.OrgStore().ListDepts(ctx); err == nil {
		depts, _ := st.OrgStore().ListDepts(ctx)
		if len(depts) > 0 {
			track("org(depts+posts)", 0, true)
		} else {
			depts := []org.Dept{
				{ID: "demo-d-hq", Name: "玄武科技", Sort: 1, Status: "active", CreatedAt: now},
				{ID: "demo-d-tech", ParentID: "demo-d-hq", Name: "技术部", Sort: 1, Status: "active", CreatedAt: now},
				{ID: "demo-d-mkt", ParentID: "demo-d-hq", Name: "市场部", Sort: 2, Status: "active", CreatedAt: now},
				{ID: "demo-d-plat", ParentID: "demo-d-tech", Name: "平台组", Sort: 1, Status: "active", CreatedAt: now},
			}
			posts := []org.Post{
				{ID: "demo-p-ceo", Code: "ceo", Name: "总经理", Sort: 1, Status: "active", CreatedAt: now},
				{ID: "demo-p-dev", Code: "dev", Name: "研发工程师", Sort: 2, Status: "active", CreatedAt: now},
				{ID: "demo-p-sale", Code: "sale", Name: "销售", Sort: 3, Status: "active", CreatedAt: now},
			}
			for _, d := range depts {
				_ = st.OrgStore().CreateDept(ctx, d)
			}
			for _, p := range posts {
				_ = st.OrgStore().CreatePost(ctx, p)
			}
			track("org(depts+posts)", len(depts)+len(posts), false)
		}
	}

	// ---- 角色：数据权限各档示例 ----
	if roles, _ := st.RoleStore().List(ctx); len(roles) <= 1 { // 仅内置 admin
		hash, _ := bcrypt.GenerateFromPassword([]byte(demoPass), bcrypt.MinCost)
		scopeRoles := []identity.Role{
			{ID: "demo-r-all", Code: "demo-scope-all", Name: "示例·全部数据", Permissions: []string{"org:dept:list", "finance:expense:list"}, DataScope: "all"},
			{ID: "demo-r-dept", Code: "demo-scope-dept", Name: "示例·本部门", Permissions: []string{"org:dept:list"}, DataScope: "dept"},
			{ID: "demo-r-self", Code: "demo-scope-self", Name: "示例·仅本人", Permissions: []string{"finance:expense:apply"}, DataScope: "self"},
			{ID: "demo-r-viewer", Code: "demo-viewer", Name: "示例·访客", Permissions: []string{"notice:list", "dict:type:list"}},
		}
		for _, r := range scopeRoles {
			_ = st.RoleStore().Create(ctx, r)
		}
		track("roles(data-scope demo)", len(scopeRoles), false)
		// ---- 用户：跨部门/角色/租户 ----
		users := []identity.User{
			{ID: "demo-u-ceo", Username: "demo-ceo", Status: identity.StatusActive, RoleIDs: []string{"demo-r-all"}, DeptID: "demo-d-hq", PasswordHash: string(hash)},
			{ID: "demo-u-dev", Username: "demo-dev", Status: identity.StatusActive, RoleIDs: []string{"demo-r-dept"}, DeptID: "demo-d-tech", PasswordHash: string(hash)},
			{ID: "demo-u-sale", Username: "demo-sale", Status: identity.StatusActive, RoleIDs: []string{"demo-r-self"}, DeptID: "demo-d-mkt", PasswordHash: string(hash)},
			{ID: "demo-u-view", Username: "demo-viewer", Status: identity.StatusActive, RoleIDs: []string{"demo-r-viewer"}, PasswordHash: string(hash)},
			{ID: "demo-u-fin", Username: "demo-fin", Status: identity.StatusActive, RoleIDs: []string{"demo-r-self"}, DeptID: "demo-d-plat", PasswordHash: string(hash)},
		}
		for _, u := range users {
			_ = st.UserStore().Create(ctx, u)
		}
		track("users", len(users), false)
	} else {
		track("roles+users", 0, true)
	}

	// ---- 字典 ----
	if ts, _ := st.DictStore().ListTypes(ctx); len(ts) == 0 {
		types := []dict.Type{
			{ID: "demo-t-gender", Code: "gender", Name: "性别", Status: "active", CreatedAt: now},
			{ID: "demo-t-bill", Code: "bill_status", Name: "单据状态", Status: "active", CreatedAt: now},
			{ID: "demo-t-sec", Code: "doc_security", Name: "公文密级", Status: "active", CreatedAt: now},
		}
		datas := []dict.Data{
			{ID: "demo-gd-1", TypeCode: "gender", Label: "男", Value: "M", Sort: 1, Status: "active", CreatedAt: now},
			{ID: "demo-gd-2", TypeCode: "gender", Label: "女", Value: "F", Sort: 2, Status: "active", CreatedAt: now},
			{ID: "demo-bd-1", TypeCode: "bill_status", Label: "草稿", Value: "draft", Sort: 1, Status: "active", CreatedAt: now},
			{ID: "demo-bd-2", TypeCode: "bill_status", Label: "已提交", Value: "submitted", Sort: 2, Status: "active", CreatedAt: now},
			{ID: "demo-bd-3", TypeCode: "bill_status", Label: "已支付", Value: "paid", Sort: 3, Status: "active", CreatedAt: now},
			{ID: "demo-sd-1", TypeCode: "doc_security", Label: "普通", Value: "普通", Sort: 1, Status: "active", CreatedAt: now},
			{ID: "demo-sd-2", TypeCode: "doc_security", Label: "秘密", Value: "秘密", Sort: 2, Status: "active", CreatedAt: now},
		}
		for _, t := range types {
			_ = st.DictStore().CreateType(ctx, t)
		}
		for _, d := range datas {
			_ = st.DictStore().CreateData(ctx, d)
		}
		track("dict(types+datas)", len(types)+len(datas), false)
	} else {
		track("dict", 0, true)
	}

	// ---- 公告：多状态 ----
	if ns, _ := st.NoticeStore().List(ctx, false); len(ns) == 0 {
		pub := now.Add(-2 * time.Hour)
		notices := []notice.Notice{
			{ID: "demo-n-1", Title: "玄武平台 v1.6 发布", Content: "八项 P0 能力全部交付，欢迎体验。", Type: "announcement", Status: notice.StatusPublished, CreatorID: "demo-u-ceo", CreatedAt: pub, PublishedAt: &pub},
			{ID: "demo-n-2", Title: "系统维护通知（草稿）", Content: "本周末 22:00-24:00 计划维护。", Type: "notice", Status: notice.StatusDraft, CreatorID: "demo-u-ceo", CreatedAt: now},
			{ID: "demo-n-3", Title: "旧版公告（已撤回）", Content: "已被新版替代。", Type: "notice", Status: notice.StatusRevoked, CreatorID: "demo-u-ceo", CreatedAt: now.Add(-24 * time.Hour)},
		}
		for _, n := range notices {
			_ = st.NoticeStore().Create(ctx, n)
		}
		track("notices", len(notices), false)
	} else {
		track("notices", 0, true)
	}

	// ---- 租户 + 套餐 + 绑定 ----
	_ = st.TenantStore().Create(ctx, tenant.Tenant{ID: "demo-tn-gov", Code: "demo-gov", Name: "示例政府客户",
		Status: "active", IsolationMode: tenant.IsolationRow, CreatedAt: now})
	_ = st.TenantPkgStore().CreatePkg(ctx, tenantpkg.Package{ID: "demo-pkg-basic", Name: "基础版",
		Status: "active", MenuIDs: []string{"m-overview", "m-approvals"},
		Perms:  []string{"notice:list", "dict:type:list", "workflow:create"},
		Remark: "演示套餐：工作台 + 基础权限", CreatedAt: now})
	_ = st.TenantPkgStore().Bind(ctx, "demo-tn-gov", "demo-pkg-basic", now)
	// 租户用户（挂租户）
	hash, _ := bcrypt.GenerateFromPassword([]byte(demoPass), bcrypt.MinCost)
	_ = st.UserStore().Create(ctx, identity.User{ID: "demo-u-tenant", Username: "demo-tenant",
		Status: identity.StatusActive, TenantID: "demo-tn-gov", PasswordHash: string(hash)})
	report = append(report, "tenant+package+bind: 1 tenant, 1 pkg, 1 tenant-user (idempotent upserts)")

	// ---- 财务：多状态单据 ----
	if es, _ := st.FinanceStore().ListExpenses(ctx, ""); len(es) == 0 {
		expenses := []finance.Expense{
			{ID: "demo-e-1", BusinessKey: "expense-demo-e-1", ApplicantID: "demo-u-fin", DeptID: "demo-d-plat", Amount: 128000,
				Reason: "平台组季度云资源采购", Items: []finance.ExpenseItem{{Name: "云主机", Amount: 98000}, {Name: "对象存储", Amount: 30000}},
				Status: finance.ExpDraft, CreatedAt: now},
			{ID: "demo-e-2", BusinessKey: "expense-demo-e-2", ApplicantID: "demo-u-sale", DeptID: "demo-d-mkt", Amount: 56000,
				Reason: "华南区客户答谢会", Items: []finance.ExpenseItem{{Name: "场地", Amount: 36000}, {Name: "物料", Amount: 20000}},
				Status: finance.ExpSubmitted, CreatedAt: now.Add(-2 * time.Hour)},
			{ID: "demo-e-3", BusinessKey: "expense-demo-e-3", ApplicantID: "demo-u-ceo", DeptID: "demo-d-hq", Amount: 8800,
				Reason: "标准委员会差旅", Items: []finance.ExpenseItem{{Name: "高铁往返", Amount: 8800}},
				Status: finance.ExpApproved, CreatedAt: now.Add(-24 * time.Hour)},
		}
		_ = st.FinanceStore().CreateInvoice(ctx, finance.Invoice{ID: "demo-iv-1", Code: "INV-2026-DEMO-001",
			Title: "玄武科技", TaxNo: "91330100DEMO", Amount: 128000, Date: now, CreatedAt: now})
		_ = st.FinanceStore().SetBudget(ctx, finance.Budget{DeptID: "demo-d-plat", Period: now.Format("2006-01"), Limit: 5000000, Used: 128000, Updated: now})
		_ = st.FinanceStore().CreatePayment(ctx, finance.Payment{ID: "demo-pay-1", ExpenseID: "demo-e-3",
			Payee: "张三", Amount: 8800, Status: "paid", CreatedAt: now})
		for _, e := range expenses {
			_ = st.FinanceStore().CreateExpense(ctx, e)
		}
		track("finance(expenses+invoice+budget+payment)", len(expenses)+3, false)
	} else {
		track("finance", 0, true)
	}

	// ---- 公文 / 合同：多状态含在途 ----
	if ds, _ := st.DocumentStore().List(ctx, ""); len(ds) == 0 {
		docs := []document.Doc{
			{ID: "demo-doc-1", DocNum: "玄武〔2026〕demo-1号", Type: "outbound", Title: "关于开展数据治理专项行动的通知",
				Security: document.SecPublic, Urgency: "平急", Correspond: "各部门", Status: document.StDraft, CreatorID: "demo-u-ceo", CreatedAt: now},
			{ID: "demo-doc-2", DocNum: "玄武〔2026〕demo-2号", Type: "inbound", Title: "上级单位转发文件",
				Security: document.SecSecret, Urgency: "加急", Status: document.StCirculating, CreatorID: "demo-u-dev", CreatedAt: now.Add(-3 * time.Hour)},
			{ID: "demo-doc-3", DocNum: "玄武〔2026〕demo-3号", Type: "outbound", Title: "年度总结报告",
				Security: document.SecPublic, Status: document.StApproved, CreatorID: "demo-u-ceo", CreatedAt: now.Add(-48 * time.Hour)},
		}
		for _, d := range docs {
			_ = st.DocumentStore().Create(ctx, d)
		}
		track("documents", len(docs), false)
	} else {
		track("documents", 0, true)
	}
	if cs, _ := st.ContractStore().List(ctx, ""); len(cs) == 0 {
		contracts := []contract.Contract{
			{ID: "demo-c-1", ContractNum: "HT-DEMO-001", Title: "政务云平台运维服务合同", Party: "某某大数据局",
				Amount: 98000000, SignDate: now.AddDate(0, -1, 0), ExpireDate: now.AddDate(0, 11, 0),
				Status: contract.StActive, CreatorID: "demo-u-ceo", CreatedAt: now.AddDate(0, -1, 0)},
			{ID: "demo-c-2", ContractNum: "HT-DEMO-002", Title: "办公设备采购合同", Party: "某某科技有限公司",
				Amount: 25800000, SignDate: now.AddDate(0, 0, -10), ExpireDate: now.AddDate(0, 0, 20),
				Status: contract.StReviewing, CreatorID: "demo-u-sale", CreatedAt: now.AddDate(0, 0, -12)},
			{ID: "demo-c-3", ContractNum: "HT-DEMO-003", Title: "咨询框架协议", Party: "某某研究院",
				Amount: 6600000, SignDate: now.AddDate(-1, 0, 0), ExpireDate: now.AddDate(0, 1, 0),
				Status: contract.StDraft, CreatorID: "demo-u-dev", CreatedAt: now},
		}
		for _, c := range contracts {
			_ = st.ContractStore().Create(ctx, c)
		}
		track("contracts", len(contracts), false)
	} else {
		track("contracts", 0, true)
	}

	// ---- RAG：示例知识库 + 文本文档（向量化跳过：seed 不依赖
	// embedding 端点，检索就绪度以切片入库为准，向量列留空）----
	if kbs, _ := st.RagStore().ListKBs(ctx); len(kbs) == 0 {
		_ = st.RagStore().CreateKB(ctx, rag.KnowledgeBase{ID: "demo-kb-1", Name: "玄武平台产品手册", OwnerID: "demo-u-ceo", CreatedAt: now})
		_ = st.RagStore().CreateDoc(ctx, rag.Document{ID: "demo-rd-1", KBID: "demo-kb-1", Name: "快速开始",
			SourceType: "text", Content: "玄武平台是纯开源、AI 原生的后端平台框架。三步完成快速开始：配置环境变量、启动 serve、登录 Console。", Status: "registered", CreatorID: "demo-u-ceo", CreatedAt: now})
		_ = st.RagStore().CreateDoc(ctx, rag.Document{ID: "demo-rd-2", KBID: "demo-kb-1", Name: "权限体系说明",
			SourceType: "text", Content: "权限体系基于 RBAC：用户绑定角色、角色聚合权限点，支持通配规则与部门级数据权限五档范围。", Status: "registered", CreatorID: "demo-u-ceo", CreatedAt: now})
		report = append(report, "rag: 1 KB + 2 text docs (embedding skipped: no external endpoint dependency in seed)")
	} else {
		track("rag", 0, true)
	}

	// ---- Agent 定义 ----
	if defs, _ := st.AgentStore().ListDefs(ctx); len(defs) == 0 {
		_ = st.AgentStore().CreateDef(ctx, agent.Definition{ID: "demo-ag-1", Name: "行政助手",
			Description: "内部制度问答", SystemPrompt: "你是玄武平台行政助手，简洁准确地回答公司制度问题。",
			Model: "gpt-4o-mini", Temperature: 0.3, MaxToolRounds: 4, CreatedAt: now})
		_ = st.AgentStore().CreateDef(ctx, agent.Definition{ID: "demo-ag-2", Name: "合同初审",
			Description: "提取合同关键条款", SystemPrompt: "你是法务助手，提取并汇总合同关键条款与风险点。",
			Model: "gpt-4o", Temperature: 0.1, MaxToolRounds: 2, CreatedAt: now})
		track("agent-defs", 2, false)
	} else {
		track("agent-defs", 0, true)
	}

	// ---- 通知模板（三渠道）----
	if ts, _ := st.NotifyStore().ListTemplates(ctx); len(ts) == 0 {
		templates := []notify.Template{
			{ID: "demo-nt-sms", Code: "demo-sms-code", Channel: notify.ChannelSMS, Name: "验证码短信",
				Content: `{"code":"{{code}}"}`, Status: "active", CreatedAt: now},
			{ID: "demo-nt-mail", Code: "demo-mail-welcome", Channel: notify.ChannelEmail, Name: "欢迎邮件",
				Subject: "欢迎加入玄武平台", Content: "{{name}} 您好，您的账号已开通，请联系管理员获取权限。", Status: "active", CreatedAt: now},
			{ID: "demo-nt-inbox", Code: "demo-inbox-notice", Channel: notify.ChannelInbox, Name: "系统站内信",
				Subject: "{{title}}", Content: "{{content}}", Status: "active", CreatedAt: now},
		}
		for _, t := range templates {
			_ = st.NotifyStore().CreateTemplate(ctx, t)
		}
		track("notify-templates", len(templates), false)
	} else {
		track("notify-templates", 0, true)
	}

	// ---- 流程定义（演示审批流）----
	if defs, _ := st.WorkflowDefStore().ListDefs(ctx); len(defs) <= 2 {
		_ = st.WorkflowDefStore().CreateDef(ctx, workflow.Definition{
			ID: "demo-wf-exp", Key: "demo-expense", Name: "演示·差旅报销", Category: "demo", Status: "active",
			Steps: []workflow.Step{
				{Name: "部门负责人审批", Approvers: []string{"demo-u-dev"}, Mode: workflow.ModeCountersign},
				{Name: "财务复核", Approvers: []string{"demo-u-fin"}, Mode: workflow.ModeOrSign},
			},
		})
		report = append(report, "workflow-defs: 1 demo definition (2-step)")
	} else {
		track("workflow-defs", 0, true)
	}

	return report
}

// Package seccheck 是等保基线自查引擎：basalt secure --check 的
// 检查逻辑与三态判定（PASS/WARN/FAIL）。
//
// 全部外部依赖（环境变量、文件系统、数据库核验）经 Options 注入，
// 判定逻辑本身纯函数化——单测覆盖三态矩阵，不依赖真实环境。
package seccheck

import (
	"fmt"
	"strings"
)

// Status 检查结论三态。
type Status string

const (
	Pass Status = "PASS"
	Warn Status = "WARN"
	Fail Status = "FAIL"
)

// Result 是单个检查项结论。
type Result struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
	Advice string `json:"advice,omitempty"`
}

// Report 是一次自查的完整报告。
type Report struct {
	Results  []Result `json:"results"`
	Passed   int      `json:"passed"`
	Warned   int      `json:"warned"`
	Failed   int      `json:"failed"`
	ExitCode int      `json:"exit_code"` // 全过 0 / 有 WARN 1 / 有 FAIL 2
}

// Options 注入检查所需的全部外部状态（cmd 层从 env/文件系统收集）。
type Options struct {
	// 凭据与环境
	Env       string // dev|test|staging|prod
	JWTSecret string
	AdminPass string
	// 算法与合规
	CryptoAlgo   string
	SM2ChainSign string // BASALT_SM2_CHAIN_SIGN（"" 或 "on"）
	// 存储
	Storage string // memory|sqlite
	DBDSN   string
	// 上传目录
	UploadDir   string
	UploadProbe func(dir string) error // 可写性探针（MkdirAll+写删探针文件）
	// 租户隔离
	TenantRouting string // BASALT_TENANT_ROUTING（"" 或 "on"）
	// 密钥文件扫描
	EnvFileExists     bool // 工作目录存在 .env
	EnvFileHasSecrets bool // .env 内含非空密钥项
	GitignoreCovers   bool // .gitignore 已忽略 .env
	AIProviderKey     string
	SM2PrivateKey     string
	// 审计链核验（返回断点序号；0=链完整）
	ChainVerify func(dsn string) (int, error)
}

// 基线常量（与 config.Defaults / SECURITY.md 对齐）。
const (
	DefaultJWTSecret = "basalt-dev-secret"
	DefaultAdminPass = "Basalt@2026"
	EnvProd          = "prod"
)

// Run 执行全部检查项。
func Run(o Options) Report {
	r := Report{Results: []Result{}}
	add := func(res Result) {
		r.Results = append(r.Results, res)
		switch res.Status {
		case Pass:
			r.Passed++
		case Warn:
			r.Warned++
		case Fail:
			r.Failed++
		}
	}
	add(checkCredentials(o))
	add(checkCryptoAlgo(o))
	add(checkUploadDir(o))
	add(checkAuditChain(o))
	add(checkTenantIsolation(o))
	add(checkSecretsHygiene(o))
	switch {
	case r.Failed > 0:
		r.ExitCode = 2
	case r.Warned > 0:
		r.ExitCode = 1
	default:
		r.ExitCode = 0
	}
	return r
}

// checkCredentials prod 默认凭据拒绝（JWT_SECRET / BOOTSTRAP_ADMIN_PASS）。
func checkCredentials(o Options) Result {
	res := Result{Name: "default-credentials"}
	badJWT := o.JWTSecret == DefaultJWTSecret || o.JWTSecret == ""
	badPass := o.AdminPass == DefaultAdminPass || o.AdminPass == ""
	switch {
	case o.Env == EnvProd && (badJWT || badPass):
		res.Status = Fail
		res.Detail = fmt.Sprintf("prod 环境存在默认凭据（jwt_secret=%v, admin_pass=%v）",
			badJWT, badPass)
		res.Advice = "设置 BASALT_JWT_SECRET 与 BASALT_BOOTSTRAP_ADMIN_PASS 为强随机值（identity 模块 prod 下同样拒绝启动）"
	case badJWT || badPass:
		res.Status = Warn
		res.Detail = fmt.Sprintf("非 prod 环境使用默认凭据（jwt_secret=%v, admin_pass=%v）",
			badJWT, badPass)
		res.Advice = "上线前必须覆盖（参考 SECURITY.md 默认安全基线）"
	default:
		res.Status = Pass
		res.Detail = "凭据均已显式配置"
	}
	return res
}

// checkCryptoAlgo 加密算法配置合规性。
func checkCryptoAlgo(o Options) Result {
	res := Result{Name: "crypto-algo"}
	switch strings.ToLower(o.CryptoAlgo) {
	case "sm2":
		res.Status = Pass
		res.Detail = "国密套件（SM3 摘要 + SM2 签名）"
	case "sm3":
		res.Status = Pass
		res.Detail = "国密 SM3 摘要"
	case "", "sha256":
		res.Status = Warn
		res.Detail = "SHA-256（通用）——非国密合规路径"
		res.Advice = "信创/等保场景建议 BASALT_CRYPTO_ALGO=sm3 或 sm2"
	default:
		res.Status = Fail
		res.Detail = fmt.Sprintf("未知算法标识 %q", o.CryptoAlgo)
		res.Advice = "合法值：sha256 | sm3 | sm2"
	}
	// 附加提示：链尾签名开关（独立结论并入 detail）
	if o.Env == EnvProd && o.SM2ChainSign != "on" {
		if res.Status == Pass {
			res.Status = Warn
		}
		res.Detail += "；prod 未启用审计链尾签名（BASALT_SM2_CHAIN_SIGN=on）"
		res.Advice = strings.TrimSpace(res.Advice + " 等保防抵赖建议开启链尾签名")
	}
	return res
}

// checkUploadDir 上传目录可写性与路径安全。
func checkUploadDir(o Options) Result {
	res := Result{Name: "upload-dir"}
	if o.UploadDir == "" {
		o.UploadDir = "uploads"
	}
	if strings.Contains(o.UploadDir, "..") {
		res.Status = Fail
		res.Detail = fmt.Sprintf("上传目录含路径穿越片段：%q", o.UploadDir)
		res.Advice = "改用无 .. 的路径"
		return res
	}
	if !strings.HasPrefix(o.UploadDir, "/") {
		res.Status = Warn
		res.Detail = fmt.Sprintf("相对路径 %q（随工作目录漂移）", o.UploadDir)
		res.Advice = "容器/生产建议绝对路径并指向持久卷"
	} else {
		res.Status = Pass
		res.Detail = fmt.Sprintf("目录 %q", o.UploadDir)
	}
	if o.UploadProbe != nil {
		if err := o.UploadProbe(o.UploadDir); err != nil {
			res.Status = Fail
			res.Detail = fmt.Sprintf("目录不可写：%v", err)
			res.Advice = "检查权限与挂载"
			return res
		}
		res.Detail += "，可写"
	}
	return res
}

// checkAuditChain 审计链完整性（复用 audit verify）。
func checkAuditChain(o Options) Result {
	res := Result{Name: "audit-chain"}
	if o.Storage != "sqlite" {
		res.Status = Warn
		res.Detail = fmt.Sprintf("存储为 %s：审计链不落盘，无法离线核验", o.Storage)
		res.Advice = "等保场景建议 BASALT_STORAGE=sqlite（或 postgres 路线）"
		return res
	}
	if o.ChainVerify == nil {
		res.Status = Warn
		res.Detail = "核验器未注入（CLI 层接线缺失）"
		return res
	}
	breakAt, err := o.ChainVerify(o.DBDSN)
	switch {
	case err != nil:
		res.Status = Fail
		res.Detail = fmt.Sprintf("链核验执行失败：%v", err)
		res.Advice = "检查数据库连接与权限"
	case breakAt > 0:
		res.Status = Fail
		res.Detail = fmt.Sprintf("链在第 %d 条断裂（篡改或损坏）", breakAt)
		res.Advice = "以离线备份恢复，并核查 seq 断点前后事件"
	default:
		res.Status = Pass
		res.Detail = "全链哈希与 PrevHash 链接校验通过"
	}
	return res
}

// checkTenantIsolation 租户隔离开关状态与建议。
func checkTenantIsolation(o Options) Result {
	res := Result{Name: "tenant-isolation"}
	if o.TenantRouting == "on" {
		res.Status = Pass
		res.Detail = "dedicated-db 租户路由已启用（BASALT_TENANT_ROUTING=on）"
		return res
	}
	if o.Env == EnvProd {
		res.Status = Warn
		res.Detail = "prod 未启用租户专属库路由（默认 shared-db 行为）"
		res.Advice = "多租户 G 端建议按 docs/tenant-isolation.md 启用 dedicated-db；单租户部署可接受"
	} else {
		res.Status = Warn
		res.Detail = "租户专属库路由未启用（开发环境可接受）"
	}
	return res
}

// checkSecretsHygiene AI provider / SM2 密钥明文落库扫描。
func checkSecretsHygiene(o Options) Result {
	res := Result{Name: "secrets-hygiene"}
	switch {
	case o.EnvFileExists && o.EnvFileHasSecrets && !o.GitignoreCovers:
		res.Status = Fail
		res.Detail = "工作目录 .env 含明文密钥且未被 .gitignore 覆盖——有入库（git）风险"
		res.Advice = ".gitignore 增加 .env 条目；密钥迁移到部署平台密钥管理"
	case o.EnvFileExists && o.EnvFileHasSecrets:
		res.Status = Warn
		res.Detail = ".env 含明文密钥（已被 .gitignore 覆盖，不入库）"
		res.Advice = "生产建议改用环境注入或密钥服务，减少明文落盘面"
	default:
		res.Status = Pass
		if o.AIProviderKey != "" || o.SM2PrivateKey != "" {
			res.Detail = "密钥经环境变量注入（未发现明文 .env），不入数据库"
		} else {
			res.Detail = "未检测到已配置密钥（无明文暴露面）"
		}
	}
	// 弱密钥长度检查（env 注入路径）
	if len(o.AIProviderKey) > 0 && len(o.AIProviderKey) < 20 {
		if res.Status == Pass {
			res.Status = Warn
		}
		res.Detail += "；BASALT_AI_PROVIDER_KEY 长度不足 20（弱密钥）"
		res.Advice = strings.TrimSpace(res.Advice + " 更换长随机密钥")
	}
	return res
}

// Format 渲染人读报告（CLI 输出）。
func (r Report) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "basalt secure --check（等保基线自查）\n\n")
	for _, res := range r.Results {
		fmt.Fprintf(&b, "[%s] %-20s %s\n", res.Status, res.Name, res.Detail)
		if res.Advice != "" {
			fmt.Fprintf(&b, "       └─ 建议: %s\n", res.Advice)
		}
	}
	fmt.Fprintf(&b, "\n总结: %d PASS / %d WARN / %d FAIL（退出码 %d）\n",
		r.Passed, r.Warned, r.Failed, r.ExitCode)
	return b.String()
}

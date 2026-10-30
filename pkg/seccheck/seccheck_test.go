package seccheck

import (
	"errors"
	"strings"
	"testing"
)

// runOpts 全 PASS 的基线 Options（各用例按需覆盖单字段断言三态）。
func runOpts() Options {
	return Options{
		Env:           "prod",
		JWTSecret:     "strong-secret-value-not-default",
		AdminPass:     "Strong-Pass-Not-Default!",
		CryptoAlgo:    "sm2",
		SM2ChainSign:  "on",
		Storage:       "sqlite",
		DBDSN:         "/var/lib/basalt/basalt.db",
		UploadDir:     "/var/lib/basalt/uploads",
		UploadProbe:   func(string) error { return nil },
		TenantRouting: "on",
		AIProviderKey: "sk-0123456789abcdef0123456789abcdef",
		ChainVerify:   func(string) (int, error) { return 0, nil },
	}
}

func find(r Report, name string) Result {
	for _, res := range r.Results {
		if res.Name == name {
			return res
		}
	}
	return Result{}
}

// TestAllPass 基线全过：退出码 0。
func TestAllPass(t *testing.T) {
	r := Run(runOpts())
	if r.ExitCode != 0 || r.Failed != 0 || r.Warned != 0 {
		t.Fatalf("report = %d/%d/%d code=%d: %s", r.Passed, r.Warned, r.Failed, r.ExitCode, r.Format())
	}
}

// TestCredentialsThreeStates 默认凭据三态。
func TestCredentialsThreeStates(t *testing.T) {
	// prod + 默认值 → FAIL
	o := runOpts()
	o.JWTSecret = DefaultJWTSecret
	if res := find(Run(o), "default-credentials"); res.Status != Fail {
		t.Fatalf("prod default jwt = %s", res.Status)
	}
	// prod + 默认口令 → FAIL
	o = runOpts()
	o.AdminPass = DefaultAdminPass
	if res := find(Run(o), "default-credentials"); res.Status != Fail {
		t.Fatalf("prod default pass = %s", res.Status)
	}
	// dev + 默认值 → WARN（不 FAIL）
	o = runOpts()
	o.Env = "dev"
	o.JWTSecret = DefaultJWTSecret
	o.AdminPass = DefaultAdminPass
	if res := find(Run(o), "default-credentials"); res.Status != Warn {
		t.Fatalf("dev defaults = %s", res.Status)
	}
	// prod + 强凭据 → PASS
	if res := find(Run(runOpts()), "default-credentials"); res.Status != Pass {
		t.Fatalf("strong = %s", res.Status)
	}
}

// TestCryptoAlgoStates 算法三态与链签名附加提示。
func TestCryptoAlgoStates(t *testing.T) {
	o := runOpts()
	o.CryptoAlgo = "sha256"
	if res := find(Run(o), "crypto-algo"); res.Status != Warn {
		t.Fatalf("sha256 = %s", res.Status)
	}
	o.CryptoAlgo = "sm3"
	if res := find(Run(o), "crypto-algo"); res.Status != Pass {
		t.Fatalf("sm3 = %s", res.Status)
	}
	o.CryptoAlgo = "matrix99"
	if res := find(Run(o), "crypto-algo"); res.Status != Fail {
		t.Fatalf("unknown = %s", res.Status)
	}
	// prod + sm2 但链签名 off → WARN 且提示包含链尾签名
	o = runOpts()
	o.SM2ChainSign = ""
	res := find(Run(o), "crypto-algo")
	if res.Status != Warn || !strings.Contains(res.Detail, "链尾签名") {
		t.Fatalf("sign off = %+v", res)
	}
}

// TestUploadDirStates 上传目录：穿越 FAIL / 相对 WARN / 不可写 FAIL / 可写 PASS。
func TestUploadDirStates(t *testing.T) {
	o := runOpts()
	o.UploadDir = "/data/../etc"
	if res := find(Run(o), "upload-dir"); res.Status != Fail {
		t.Fatalf("traversal = %s", res.Status)
	}
	o = runOpts()
	o.UploadDir = "uploads" // 相对路径 → WARN
	if res := find(Run(o), "upload-dir"); res.Status != Warn {
		t.Fatalf("relative = %s", res.Status)
	}
	o = runOpts()
	o.UploadProbe = func(string) error { return errors.New("permission denied") }
	if res := find(Run(o), "upload-dir"); res.Status != Fail {
		t.Fatalf("unwritable = %s", res.Status)
	}
	o = runOpts()
	o.UploadDir = "uploads"
	o.UploadProbe = func(string) error { return nil }
	if res := find(Run(o), "upload-dir"); res.Status != Warn { // 仍 WARN（相对）
		t.Fatalf("relative writable = %s", res.Status)
	}
}

// TestAuditChainStates 链核验：非 sqlite WARN / 断链 FAIL / 错误 FAIL / 完整 PASS。
func TestAuditChainStates(t *testing.T) {
	o := runOpts()
	o.Storage = "memory"
	if res := find(Run(o), "audit-chain"); res.Status != Warn {
		t.Fatalf("memory = %s", res.Status)
	}
	o = runOpts()
	o.ChainVerify = func(string) (int, error) { return 7, nil }
	if res := find(Run(o), "audit-chain"); res.Status != Fail || !strings.Contains(res.Detail, "7") {
		t.Fatalf("broken = %+v", res)
	}
	o = runOpts()
	o.ChainVerify = func(string) (int, error) { return 0, errors.New("db: no such file") }
	if res := find(Run(o), "audit-chain"); res.Status != Fail {
		t.Fatalf("error = %s", res.Status)
	}
	if res := find(Run(runOpts()), "audit-chain"); res.Status != Pass {
		t.Fatalf("intact = %s", res.Status)
	}
}

// TestTenantIsolationStates 租户隔离：on PASS / off WARN。
func TestTenantIsolationStates(t *testing.T) {
	if res := find(Run(runOpts()), "tenant-isolation"); res.Status != Pass {
		t.Fatalf("on = %s", res.Status)
	}
	o := runOpts()
	o.TenantRouting = ""
	if res := find(Run(o), "tenant-isolation"); res.Status != Warn {
		t.Fatalf("off = %s", res.Status)
	}
}

// TestSecretsHygieneStates 密钥卫生：裸 .env FAIL / 覆盖 WARN / 干净 PASS / 弱密钥 WARN。
func TestSecretsHygieneStates(t *testing.T) {
	o := runOpts()
	o.EnvFileExists, o.EnvFileHasSecrets, o.GitignoreCovers = true, true, false
	if res := find(Run(o), "secrets-hygiene"); res.Status != Fail {
		t.Fatalf("exposed env = %s", res.Status)
	}
	o.GitignoreCovers = true
	if res := find(Run(o), "secrets-hygiene"); res.Status != Warn {
		t.Fatalf("covered env = %s", res.Status)
	}
	if res := find(Run(runOpts()), "secrets-hygiene"); res.Status != Pass {
		t.Fatalf("clean = %s", res.Status)
	}
	o = runOpts()
	o.AIProviderKey = "short-key" // 弱密钥
	if res := find(Run(o), "secrets-hygiene"); res.Status != Warn || !strings.Contains(res.Detail, "弱密钥") {
		t.Fatalf("weak key = %+v", res)
	}
}

// TestExitCodeSemantics 退出码语义：全过 0 / 有 WARN 1 / 有 FAIL 2。
func TestExitCodeSemantics(t *testing.T) {
	if r := Run(runOpts()); r.ExitCode != 0 {
		t.Fatalf("all pass = %d", r.ExitCode)
	}
	o := runOpts()
	o.CryptoAlgo = "sha256" // 单 WARN
	if r := Run(o); r.ExitCode != 1 {
		t.Fatalf("warn = %d", r.ExitCode)
	}
	o.JWTSecret = DefaultJWTSecret // 再加 FAIL → 2
	if r := Run(o); r.ExitCode != 2 {
		t.Fatalf("fail = %d", r.ExitCode)
	}
}

// TestFormatIncludesSummary 渲染含逐项与总结。
func TestFormatIncludesSummary(t *testing.T) {
	out := Run(runOpts()).Format()
	for _, want := range []string{"[PASS]", "default-credentials", "总结", "退出码 0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("format missing %q:\n%s", want, out)
		}
	}
}

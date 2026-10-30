package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basalt-go/basalt/pkg/kernel/config"
	"github.com/basalt-go/basalt/pkg/seccheck"
	"github.com/basalt-go/basalt/pkg/stores/gormstore"
)

// newSecureCmd 等保基线自查：basalt secure --check。
// 退出码语义：全过 0 / 有 WARN 1 / 有 FAIL 2（CI 与脚本可直接消费）。
func newSecureCmd() *cobra.Command {
	var runCheck bool
	cmd := &cobra.Command{
		Use:   "secure",
		Short: "Security baseline utilities (--check: MLPS self-check)",
		RunE: func(c *cobra.Command, _ []string) error {
			if !runCheck {
				return c.Usage()
			}
			report := seccheck.Run(gatherOptions())
			fmt.Print(report.Format())
			// 退出码语义（0/1/2）供 CI 与脚本消费——cobra 成功路径
			// 固定 exit 0，故此处直接以报告码退出。
			os.Exit(report.ExitCode)
			return nil
		},
	}
	cmd.Flags().BoolVar(&runCheck, "check", false, "run MLPS (等保) baseline self-check")
	return cmd
}

// gatherOptions 从真实环境收集检查输入（env / 文件系统 / 数据库）。
func gatherOptions() seccheck.Options {
	cfg := config.Load()
	o := seccheck.Options{
		Env:           cfg.Env,
		JWTSecret:     cfg.JWTSecret,
		AdminPass:     cfg.BootstrapAdminPassword,
		CryptoAlgo:    cfg.CryptoAlgo,
		SM2ChainSign:  os.Getenv("BASALT_SM2_CHAIN_SIGN"),
		Storage:       cfg.Storage,
		DBDSN:         cfg.DBDSN,
		UploadDir:     os.Getenv("BASALT_UPLOAD_DIR"),
		UploadProbe:   probeUploadDir,
		TenantRouting: os.Getenv("BASALT_TENANT_ROUTING"),
		AIProviderKey: os.Getenv("BASALT_AI_PROVIDER_KEY"),
		SM2PrivateKey: os.Getenv("BASALT_SM2_PRIVATE_KEY"),
		SSOSocialKey:  os.Getenv("BASALT_SSO_DINGTALK_CLIENT_SECRET"),
		ChainVerify:   verifyAuditChain(cfg),
	}
	// .env 明文密钥扫描
	o.EnvFileExists, o.EnvFileHasSecrets = scanEnvFile(".env")
	o.GitignoreCovers = gitignoreCoversEnv(".gitignore")
	return o
}

// probeUploadDir 可写性探针：建目录 + 写删探针文件。
func probeUploadDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	probe := filepath.Join(dir, ".seccheck-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		return err
	}
	return os.Remove(probe)
}

// verifyAuditChain 打开 sqlite 审计链执行完整性核验（复用 audit verify）。
func verifyAuditChain(cfg *config.Config) func(dsn string) (int, error) {
	return func(dsn string) (int, error) {
		if dsn == "" {
			dsn = "basalt.db"
		}
		st, err := gormstore.Open(dsn)
		if err != nil {
			return 0, fmt.Errorf("open %s: %w", dsn, err)
		}
		if sqlDB, err := st.DB.DB(); err == nil {
			defer sqlDB.Close()
		}
		chain, err := st.AuditChainStore(hasherFor(cfg))
		if err != nil {
			return 0, err
		}
		return chain.Verify(context.Background())
	}
}

// scanEnvFile 检查 .env 是否存在且含非空密钥项。
func scanEnvFile(path string) (exists, hasSecrets bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, false
	}
	exists = true
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		for _, key := range []string{"BASALT_AI_PROVIDER_KEY", "BASALT_SM2_PRIVATE_KEY", "BASALT_JWT_SECRET", "BASALT_SSO_DINGTALK_CLIENT_SECRET"} {
			if strings.HasPrefix(line, key+"=") && strings.TrimSpace(strings.TrimPrefix(line, key+"=")) != "" {
				hasSecrets = true
			}
		}
	}
	return exists, hasSecrets
}

// gitignoreCoversEnv .gitignore 是否忽略 .env。
func gitignoreCoversEnv(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if t == ".env" || t == ".env.*" || t == ".env*" {
			return true
		}
	}
	return false
}

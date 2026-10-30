// Package config 提供框架配置。
//
// 当前支持：内置默认值 + BASALT_ 前缀环境变量覆盖。
// 计划：koanf 驱动的多源加载（文件/配置中心），接口保持不变。
package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	// Name 应用名，用于日志与监控标识。
	Name string
	// Addr HTTP 监听地址。
	Addr string
	// Env 运行环境：dev / test / staging / prod。
	Env string
	// InitTimeout 全部模块 Init 阶段的总预算。
	InitTimeout time.Duration
	// ShutdownTimeout 优雅关闭的总预算（覆盖全部模块 Stop 与 HTTP drain）。
	ShutdownTimeout time.Duration
	// JWTSecret 身份模块令牌签名密钥。生产环境必须显式配置，
	// 否则 identity 模块 Init 拒绝启动（对应 SECURITY.md 默认安全基线）。
	JWTSecret string
	// TokenTTL 访问令牌有效期。
	TokenTTL time.Duration
	// OtelEndpoint OTLP/HTTP 遥测导出地址（如 http://otel-col:4318）。
	// 为空时可观测性全部 no-op（私有化无外部依赖默认值）。
	OtelEndpoint string
	// CryptoAlgo 内核摘要算法：sm3（国密，信创推荐）| sha256（默认）。
	// 影响审计链哈希等能力。
	CryptoAlgo string
	// Storage 持久化后端：memory（默认，零依赖）| sqlite。
	// sqlite 时各模块切换到 GORM 持久化实现（审计链/配额/身份/租户）。
	Storage string
	// DBDSN 数据库连接串（sqlite 文件路径，如 /var/lib/basalt/basalt.db）。
	DBDSN string
	// BootstrapAdminUsername 首启引导管理员用户名。
	BootstrapAdminUsername string
	// BootstrapAdminPassword 首启引导管理员口令。生产环境必须显式配置
	// 且不得等于默认值（与 JWTSecret 同一条 SECURITY.md 基线）。
	BootstrapAdminPassword string
	// SSOAuthCodeTTL OAuth2 授权码有效期（默认 5 分钟，≤10 分钟为佳）。
	SSOAuthCodeTTL time.Duration
	// SSORefreshTTL OAuth2 refresh token 有效期（默认 14 天，轮换式）。
	SSORefreshTTL time.Duration
	// Modules 模块级配置表，键为模块名。当前仅透传，强类型化随各模块推进。
	Modules map[string]map[string]string
}

func Defaults() *Config {
	return &Config{
		Name:            "basalt",
		Addr:            ":8080",
		Env:             "dev",
		InitTimeout:     30 * time.Second,
		ShutdownTimeout: 15 * time.Second,
		JWTSecret:       "basalt-dev-secret", // 仅 dev/test 可用，prod 强制覆盖
		TokenTTL:        2 * time.Hour,
		Storage:         "memory",
		// 引导管理员默认口令仅限 dev/test；prod 必须显式覆盖。
		BootstrapAdminUsername: "admin",
		BootstrapAdminPassword: "Basalt@2026",
		Modules:                map[string]map[string]string{},
	}
}

// Load 以默认值为底，应用 BASALT_ 前缀环境变量覆盖。
func Load() *Config {
	c := Defaults()
	for _, e := range []struct {
		env string
		dst *string
	}{
		{"BASALT_ADDR", &c.Addr},
		{"BASALT_ENV", &c.Env},
		{"BASALT_JWT_SECRET", &c.JWTSecret},
		{"BASALT_OTEL_ENDPOINT", &c.OtelEndpoint},
		{"BASALT_CRYPTO_ALGO", &c.CryptoAlgo},
		{"BASALT_STORAGE", &c.Storage},
		{"BASALT_DB_DSN", &c.DBDSN},
		{"BASALT_BOOTSTRAP_ADMIN_USER", &c.BootstrapAdminUsername},
		{"BASALT_BOOTSTRAP_ADMIN_PASS", &c.BootstrapAdminPassword},
	} {
		if v := os.Getenv(e.env); v != "" {
			*e.dst = v
		}
	}
	if v := os.Getenv("BASALT_SHUTDOWN_TIMEOUT_SEC"); v != "" {
		if sec, err := strconv.Atoi(v); err == nil && sec > 0 {
			c.ShutdownTimeout = time.Duration(sec) * time.Second
		}
	}
	if v := os.Getenv("BASALT_TOKEN_TTL_MIN"); v != "" {
		if min, err := strconv.Atoi(v); err == nil && min > 0 {
			c.TokenTTL = time.Duration(min) * time.Minute
		}
	}
	if v := os.Getenv("BASALT_SSO_CODE_TTL_SEC"); v != "" {
		if sec, err := strconv.Atoi(v); err == nil && sec > 0 {
			c.SSOAuthCodeTTL = time.Duration(sec) * time.Second
		}
	}
	if v := os.Getenv("BASALT_SSO_REFRESH_TTL_HOUR"); v != "" {
		if hr, err := strconv.Atoi(v); err == nil && hr > 0 {
			c.SSORefreshTTL = time.Duration(hr) * time.Hour
		}
	}
	return c
}

// IsProd 报告当前是否为生产环境。
func (c *Config) IsProd() bool { return c.Env == "prod" }

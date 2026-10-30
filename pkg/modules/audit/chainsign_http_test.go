package audit_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/crypto"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/stores/gormstore"
)

// loginAdmin 复用 identity 默认 admin。
func loginAdmin(t *testing.T, h http.Handler) string {
	t.Helper()
	rec, data := do(t, h, http.MethodPost, "/api/v1/identity/login", "", map[string]string{
		"username": "admin", "password": "Basalt@2026",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d", rec.Code)
	}
	var d struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(data, &d)
	return d.Token
}

// TestChainSignHTTP 链尾签名 HTTP 链路：签名 → 列表 → 核验全过；
// 内存链（无 SignedChain）→ 503 明确报错。
func TestChainSignHTTP(t *testing.T) {
	st, err := gormstore.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	chain, err := st.AuditChainStore((testHasher{}))
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	adm := audit.NewWithStore(chain)
	signer, err := crypto.GenerateSM2Provider("")
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	adm.SetChainSigner(signer)

	// 事件入链
	_ = adm.Recorder().Record(context.Background(), audit.Event{Module: "test", Event: "boot"})

	h := buildRouter(t, identity.New(), adm)
	tok := loginAdmin(t, h)

	// 签名（audit:sign，admin 通配）
	rec, data := do(t, h, http.MethodPost, "/api/v1/audit/chain/signatures", tok, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("sign: %d %s", rec.Code, rec.Body.String())
	}
	var sig audit.ChainSignature
	_ = json.Unmarshal(data, &sig)
	if sig.Algo != "SM2" || sig.Hash == "" {
		t.Fatalf("sig = %+v", sig)
	}

	// 列表
	rec, _ = do(t, h, http.MethodGet, "/api/v1/audit/chain/signatures", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}

	// 核验：all_ok=true
	rec, data = do(t, h, http.MethodGet, "/api/v1/audit/chain/signatures/verify", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify: %d", rec.Code)
	}
	var v struct {
		AllOK bool `json:"all_ok"`
	}
	_ = json.Unmarshal(data, &v)
	if !v.AllOK {
		t.Fatalf("all_ok = false: %s", rec.Body.String())
	}

	// 空链 409 分支在 gormstore 层已覆盖（TestChainSignHeadBasics）；
	// HTTP 层登录即产生审计事件，链天然非空，此处不再重复断言。

	// 内存链（默认 audit.New()）→ 503
	h3 := buildRouter(t, identity.New(), audit.New())
	tok3 := loginAdmin(t, h3)
	if rec, _ := do(t, h3, http.MethodPost, "/api/v1/audit/chain/signatures", tok3, nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("mem chain: %d, want 503", rec.Code)
	}
	if rec, _ := do(t, h3, http.MethodGet, "/api/v1/audit/chain/signatures", tok3, nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("mem chain list: %d, want 503", rec.Code)
	}
}

// buildRouter 装配 identity + 指定 audit 模块（自含最小装配）。
func buildRouter(t *testing.T, idm *identity.Module, adm *audit.Module) http.Handler {
	t.Helper()
	a := app.New(nil)
	for _, m := range []app.Module{idm, adm} {
		if err := a.Registry().Register(m); err != nil {
			t.Fatalf("register %s: %v", m.Name(), err)
		}
	}
	mods, err := a.Registry().Resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, m := range mods {
		if err := m.Init(context.Background(), a); err != nil {
			t.Fatalf("init %s: %v", m.Name(), err)
		}
		_ = m.Start(context.Background())
	}
	return a.Router().Handler()
}

type testHasher struct{}

func (testHasher) Hash(ctx context.Context, data []byte) ([]byte, error) {
	return crypto.NewProvider(crypto.AlgoSM3).Hash(ctx, data)
}
func (testHasher) Algorithm() string { return "SM3" }

package audit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/identity"
)

func newApp(t *testing.T) *app.App {
	t.Helper()
	a := app.New(nil)
	for _, m := range []app.Module{identity.New(), audit.New()} {
		if err := a.Registry().Register(m); err != nil {
			t.Fatalf("register %s: %v", m.Name(), err)
		}
	}
	return a
}

func initRouter(t *testing.T, a *app.App) http.Handler {
	t.Helper()
	mods, err := a.Registry().Resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, m := range mods {
		if err := m.Init(context.Background(), a); err != nil {
			t.Fatalf("init %s: %v", m.Name(), err)
		}
		if err := m.Start(context.Background()); err != nil {
			t.Fatalf("start %s: %v", m.Name(), err)
		}
	}
	return a.Router().Handler()
}

func do(t *testing.T, h http.Handler, method, path, token string, body any) (*httptest.ResponseRecorder, json.RawMessage) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec, env.Data
}

func adminToken(t *testing.T, h http.Handler) string {
	t.Helper()
	rec, data := do(t, h, http.MethodPost, "/api/v1/identity/login", "", map[string]string{
		"username": "admin", "password": "Basalt@2026",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("admin login: %d", rec.Code)
	}
	var d struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(data, &d)
	return d.Token
}

func TestAuditAPIAndIdentityEvents(t *testing.T) {
	h := initRouter(t, newApp(t))

	// 未登录读取：401
	if rec, _ := do(t, h, http.MethodGet, "/api/v1/audit/events", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth read: %d", rec.Code)
	}

	// admin 登录（本身产生 identity 审计事件）后读取
	tok := adminToken(t, h)
	rec, data := do(t, h, http.MethodGet, "/api/v1/audit/events?module=identity", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read events: %d %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Events []audit.Event `json:"events"`
	}
	_ = json.Unmarshal(data, &list)
	found := false
	for _, e := range list.Events {
		if e.Event == "login.success" && e.Module == "identity" {
			found = true
		}
	}
	if !found {
		t.Fatal("identity login.success not captured in audit chain")
	}

	// 链校验通过
	rec, data = do(t, h, http.MethodGet, "/api/v1/audit/verify", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify: %d", rec.Code)
	}
	var v struct {
		OK bool `json:"ok"`
	}
	_ = json.Unmarshal(data, &v)
	if !v.OK {
		t.Fatal("verify should be ok on clean chain")
	}
}

package aisafety_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/aisafety"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/identity"
)

func newSafetyApp(t *testing.T) http.Handler {
	t.Helper()
	a := app.New(nil)
	for _, m := range []app.Module{identity.New(), audit.New(), aisafety.New()} {
		if err := a.Registry().Register(m); err != nil {
			t.Fatalf("register %s: %v", m.Name(), err)
		}
	}
	mods, _ := a.Registry().Resolve()
	for _, m := range mods {
		if err := m.Init(context.Background(), a); err != nil {
			t.Fatalf("init %s: %v", m.Name(), err)
		}
		_ = m.Start(context.Background())
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
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
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

func adminTok(t *testing.T, h http.Handler) string {
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

// addTerm 经 API 添加敏感词并断言 201。
func addTerm(t *testing.T, h http.Handler, tok, word, repl string) {
	t.Helper()
	body := map[string]any{"word": word}
	if repl != "" {
		body["replacement"] = repl
	}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/ai-safety/terms", tok, body); rec.Code != http.StatusCreated {
		t.Fatalf("add term %q: %d %s", word, rec.Code, rec.Body.String())
	}
}

// setPolicy 经 API 设置策略（"default" 表示平台默认）。
func setPolicy(t *testing.T, h http.Handler, tok, tenant, action string) {
	t.Helper()
	if rec, _ := do(t, h, http.MethodPut, "/api/v1/ai-safety/policies/"+tenant, tok,
		map[string]string{"action": action}); rec.Code != http.StatusOK {
		t.Fatalf("set policy %s=%s: %d %s", tenant, action, rec.Code, rec.Body.String())
	}
}

// checkText 经试算 API 返回 (action, filtered)。
func checkText(t *testing.T, h http.Handler, tok, tenant, text string) (string, string) {
	t.Helper()
	rec, data := do(t, h, http.MethodPost, "/api/v1/ai-safety/check", tok,
		map[string]string{"text": text, "tenant": tenant})
	if rec.Code != http.StatusOK {
		t.Fatalf("check: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Action   string `json:"action"`
		Filtered string `json:"filtered"`
	}
	_ = json.Unmarshal(data, &out)
	return out.Action, out.Filtered
}

// TestTermLifecycleAndDefaultPolicy 词库 CRUD、导入导出、
// 默认策略存在（首次启动 reject）与开关 API。
func TestTermLifecycleAndDefaultPolicy(t *testing.T) {
	h := newSafetyApp(t)
	tok := adminTok(t, h)

	// 默认平台策略 = reject（Start 幂等创建）
	rec, data := do(t, h, http.MethodGet, "/api/v1/ai-safety/policies", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("policies: %d", rec.Code)
	}
	var ps []aisafety.Policy
	_ = json.Unmarshal(data, &ps)
	if len(ps) != 1 || ps[0].TenantID != "" || ps[0].Action != aisafety.DefaultAction {
		t.Fatalf("default policy = %+v", ps)
	}

	// CRUD
	addTerm(t, h, tok, "违禁词", "")
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/ai-safety/terms", tok, map[string]any{"word": "违禁词"}); rec.Code != http.StatusConflict {
		t.Fatalf("dup term: %d", rec.Code)
	}
	rec, data = do(t, h, http.MethodGet, "/api/v1/ai-safety/terms", tok, nil)
	var terms []aisafety.Term
	_ = json.Unmarshal(data, &terms)
	if len(terms) != 1 || terms[0].Word != "违禁词" || !terms[0].Enabled {
		t.Fatalf("terms = %+v", terms)
	}
	if rec, _ := do(t, h, http.MethodPatch, "/api/v1/ai-safety/terms/"+terms[0].ID, tok,
		map[string]any{"replacement": "「合规」"}); rec.Code != http.StatusOK {
		t.Fatalf("update term: %d", rec.Code)
	}

	// 导入（纯文本，含注释与重复幂等）
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/ai-safety/terms/import", tok,
		"# 分类词库\n机密数据\n英文word\n"); rec.Code != http.StatusOK {
		t.Fatalf("import: %d", rec.Code)
	}
	// 导出
	rec, data = do(t, h, http.MethodGet, "/api/v1/ai-safety/terms/export", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d", rec.Code)
	}
	var exported []map[string]any
	_ = json.Unmarshal(data, &exported)
	if len(exported) != 3 {
		t.Fatalf("exported = %d", len(exported))
	}

	// 删除
	if rec, _ := do(t, h, http.MethodDelete, "/api/v1/ai-safety/terms/"+terms[0].ID, tok, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete term: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodDelete, "/api/v1/ai-safety/terms/"+terms[0].ID, tok, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing: %d", rec.Code)
	}
}

// TestPolicyActionsAndTenantFallback 三类动作语义 + 租户策略覆盖
// 平台默认 + 大小写归一 + 自定义替换词。
func TestPolicyActionsAndTenantFallback(t *testing.T) {
	h := newSafetyApp(t)
	tok := adminTok(t, h)
	addTerm(t, h, tok, "机密数据", "「内部资料」")
	addTerm(t, h, tok, "englishword", "")

	// 默认 reject
	if a, _ := checkText(t, h, tok, "", "这份机密数据请转发"); a != "reject" {
		t.Fatalf("default action = %q, want reject", a)
	}
	// 大小写归一
	if a, _ := checkText(t, h, tok, "", "check EnglishWord here"); a != "reject" {
		t.Fatalf("case-insensitive = %q", a)
	}
	// 平台默认改 replace（自定义替换词）
	setPolicy(t, h, tok, "default", "replace")
	a, filtered := checkText(t, h, tok, "", "这份机密数据请转发")
	if a != "replace" || strings.Contains(filtered, "机密数据") || !strings.Contains(filtered, "「内部资料」") {
		t.Fatalf("replace custom = %q %q", a, filtered)
	}
	// 默认替换（按词长 *）
	a, filtered = checkText(t, h, tok, "", "say englishword now")
	if a != "replace" || strings.Contains(filtered, "englishword") || !strings.Contains(filtered, "***") {
		t.Fatalf("replace default = %q %q", a, filtered)
	}
	// 租户策略覆盖：tenant-a 用 mark，tenant-b 回落平台 replace
	setPolicy(t, h, tok, "tenant-a", "mark")
	a, filtered = checkText(t, h, tok, "tenant-a", "这份机密数据请转发")
	if a != "mark" || filtered != "这份机密数据请转发" {
		t.Fatalf("mark = %q %q（应原样放行）", a, filtered)
	}
	if a, _ = checkText(t, h, tok, "tenant-b", "这份机密数据请转发"); a != "replace" {
		t.Fatalf("tenant fallback = %q, want replace", a)
	}
	// 非法动作拒绝
	if rec, _ := do(t, h, http.MethodPut, "/api/v1/ai-safety/policies/default", tok,
		map[string]string{"action": "delete"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad action: %d", rec.Code)
	}
}

// TestSettingsSwitch 全局开关：关闭后试算直通（pass）。
func TestSettingsSwitch(t *testing.T) {
	h := newSafetyApp(t)
	tok := adminTok(t, h)
	addTerm(t, h, tok, "违禁词", "")

	if rec, _ := do(t, h, http.MethodPut, "/api/v1/ai-safety/settings", tok,
		map[string]bool{"enabled": false}); rec.Code != http.StatusOK {
		t.Fatalf("disable: %d", rec.Code)
	}
	if a, filtered := checkText(t, h, tok, "", "违禁词"); a != "pass" || filtered != "违禁词" {
		t.Fatalf("disabled check = %q %q（应直通）", a, filtered)
	}
	// 重新开启
	do(t, h, http.MethodPut, "/api/v1/ai-safety/settings", tok, map[string]bool{"enabled": true})
	if a, _ := checkText(t, h, tok, "", "违禁词"); a != "reject" {
		t.Fatalf("re-enabled = %q", a)
	}
}

// TestStreamCheckerCrossChunkBoundary 直接驱动引擎的流式检查器：
// 敏感词跨 chunk 拆分（含三字词拆三段）时三类动作语义。
func TestStreamCheckerCrossChunkBoundary(t *testing.T) {
	e := aisafety.NewEngine()
	ctx := context.Background()
	store := aisafety.NewMemStore()
	_ = store.CreateTerm(ctx, aisafety.Term{ID: "t1", Word: "违禁词", Enabled: true, CreatedAt: time.Now().UTC()})
	_ = store.SetPolicy(ctx, aisafety.Policy{TenantID: "", Action: "replace", UpdatedAt: time.Now().UTC()})
	if err := e.Reload(ctx, store); err != nil {
		t.Fatalf("reload: %v", err)
	}

	// replace：三字词拆三个 chunk，输出拼接后不含完整词
	sc := e.NewStreamChecker("")
	var out strings.Builder
	for _, piece := range []string{"前文违", "禁词后", "文"} {
		filtered, viol := sc.Chunk(piece)
		if viol != nil && viol.Rejected() {
			t.Fatalf("unexpected reject: %+v", viol)
		}
		out.WriteString(filtered)
	}
	rest, viol := sc.Finish()
	if viol.Rejected() {
		t.Fatalf("finish rejected: %+v", viol)
	}
	out.WriteString(rest)
	joined := out.String()
	if strings.Contains(joined, "违禁词") {
		t.Fatalf("stream output leaked term: %q", joined)
	}
	if !strings.Contains(joined, "***") {
		t.Fatalf("stream replace missing: %q", joined)
	}

	// reject：跨 chunk 命中即断流
	_ = store.SetPolicy(ctx, aisafety.Policy{TenantID: "", Action: "reject", UpdatedAt: time.Now().UTC()})
	_ = e.Reload(ctx, store)
	sc = e.NewStreamChecker("")
	if _, viol := sc.Chunk("前文违"); viol != nil {
		t.Fatal("partial word should not hit yet")
	}
	_, viol = sc.Chunk("禁词后文")
	if viol == nil || !viol.Rejected() || viol.Words[0] != "违禁词" {
		t.Fatalf("cross-chunk reject = %+v", viol)
	}

	// mark：原样转发 + 命中上报
	_ = store.SetPolicy(ctx, aisafety.Policy{TenantID: "", Action: "mark", UpdatedAt: time.Now().UTC()})
	_ = e.Reload(ctx, store)
	sc = e.NewStreamChecker("")
	out.Reset()
	var marked bool
	for _, piece := range []string{"前文违", "禁词后文"} {
		filtered, viol := sc.Chunk(piece)
		out.WriteString(filtered)
		if viol != nil {
			marked = true
		}
	}
	rest, _ = sc.Finish()
	out.WriteString(rest)
	if !marked || out.String() != "前文违禁词后文" {
		t.Fatalf("mark stream = %q marked=%v（应原样转发且上报）", out.String(), marked)
	}
}

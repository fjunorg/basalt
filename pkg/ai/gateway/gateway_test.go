package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/basalt-go/basalt/pkg/ai/gateway"
	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/tenant"
)

// fakeOpenAI 模拟 OpenAI 兼容上游。
func fakeOpenAI(t *testing.T, totalTokens int64, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": "pong"}},
			},
			"usage": map[string]int64{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": totalTokens},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeOpenAISSE 模拟流式上游：SSE chunks + 末尾 usage 块。
func fakeOpenAISSE(t *testing.T, totalTokens int64, gotStream *bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stream        bool `json:"stream"`
			StreamOptions struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if gotStream != nil {
			*gotStream = body.Stream && body.StreamOptions.IncludeUsage
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		for _, chunk := range []string{
			`data: {"choices":[{"delta":{"content":"你"}}]}`,
			`data: {"choices":[{"delta":{"content":"好"}}]}`,
			`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":8,"total_tokens":` + fmt.Sprint(totalTokens) + `}}`,
			"data: [DONE]",
		} {
			_, _ = io.WriteString(w, chunk+"\n\n")
			fl.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newApp(t *testing.T, gw *gateway.Module) *app.App {
	t.Helper()
	a := app.New(nil)
	for _, m := range []app.Module{identity.New(), tenant.New(), audit.New(), gw} {
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
		_ = m.Init(context.Background(), a)
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

func chatBody() map[string]any {
	return map[string]any{
		"model":    "test-model",
		"messages": []map[string]string{{"role": "user", "content": "ping"}},
	}
}

func TestChatHappyPathWithQuotaAndAudit(t *testing.T) {
	up := fakeOpenAI(t, 15, http.StatusOK)
	gw := gateway.New().RegisterProvider("fake", up.URL, "test-key")
	h := initRouter(t, newApp(t, gw))
	tok := adminToken(t, h)

	// 两次调用共 30 tokens（admin 是平台级用户 → subject=platform）
	for i := 0; i < 2; i++ {
		rec, data := do(t, h, http.MethodPost, "/api/v1/ai/chat/completions", tok, chatBody())
		if rec.Code != http.StatusOK {
			t.Fatalf("chat %d: %d %s", i, rec.Code, rec.Body.String())
		}
		var out struct {
			Usage struct {
				TotalTokens int64 `json:"total_tokens"`
			} `json:"usage"`
		}
		_ = json.Unmarshal(data, &out)
		if out.Usage.TotalTokens != 15 {
			t.Fatalf("total tokens = %d, want 15", out.Usage.TotalTokens)
		}
	}

	// 用量记账
	rec, data := do(t, h, http.MethodGet, "/api/v1/ai/quota/platform", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("quota: %d", rec.Code)
	}
	var q struct {
		Used  int64 `json:"used"`
		Limit int64 `json:"limit"`
	}
	_ = json.Unmarshal(data, &q)
	if q.Used != 30 {
		t.Fatalf("used = %d, want 30", q.Used)
	}

	// AI 调用进入审计链
	rec, data = do(t, h, http.MethodGet, "/api/v1/audit/events?module=ai-gateway", tok, nil)
	var list struct {
		Events []audit.Event `json:"events"`
	}
	_ = json.Unmarshal(data, &list)
	if len(list.Events) < 2 {
		t.Fatalf("ai-gateway audit events = %d, want >= 2", len(list.Events))
	}
	if list.Events[0].Event != "chat.ok" {
		t.Fatalf("latest event = %q, want chat.ok", list.Events[0].Event)
	}
}

func TestQuotaExceededReturns429(t *testing.T) {
	up := fakeOpenAI(t, 15, http.StatusOK)
	gw := gateway.New().RegisterProvider("fake", up.URL, "")
	h := initRouter(t, newApp(t, gw))
	tok := adminToken(t, h)

	// platform 限额压到 15：第一次调用恰好用满（total_tokens=15），
	// 第二次预检（used 15 + 1 > 15）拒绝。
	if rec, _ := do(t, h, http.MethodPut, "/api/v1/ai/quota/platform", tok, map[string]int64{"limit": 15}); rec.Code != http.StatusOK {
		t.Fatalf("set quota: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/ai/chat/completions", tok, chatBody()); rec.Code != http.StatusOK {
		t.Fatalf("first chat: %d", rec.Code)
	}
	rec, _ := do(t, h, http.MethodPost, "/api/v1/ai/chat/completions", tok, chatBody())
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over-quota chat: %d, want 429", rec.Code)
	}
}

func TestUpstreamErrorReturns502(t *testing.T) {
	up := fakeOpenAI(t, 0, http.StatusInternalServerError)
	gw := gateway.New().RegisterProvider("fake", up.URL, "")
	h := initRouter(t, newApp(t, gw))
	tok := adminToken(t, h)

	if rec, _ := do(t, h, http.MethodPost, "/api/v1/ai/chat/completions", tok, chatBody()); rec.Code != http.StatusBadGateway {
		t.Fatalf("upstream 500: %d, want 502", rec.Code)
	}
}

func TestNoProviderReturns503(t *testing.T) {
	h := initRouter(t, newApp(t, gateway.New()))
	tok := adminToken(t, h)

	if rec, _ := do(t, h, http.MethodPost, "/api/v1/ai/chat/completions", tok, chatBody()); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no provider: %d, want 503", rec.Code)
	}
	// 认证仍必需
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/ai/chat/completions", "", chatBody()); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth chat: %d, want 401", rec.Code)
	}
}

func TestProviderSelectionAndList(t *testing.T) {
	up := fakeOpenAI(t, 7, http.StatusOK)
	gw := gateway.New().RegisterProvider("primary", up.URL, "").RegisterProvider("secondary", up.URL, "")
	h := initRouter(t, newApp(t, gw))
	tok := adminToken(t, h)

	rec, data := do(t, h, http.MethodGet, "/api/v1/ai/providers", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("providers: %d", rec.Code)
	}
	var p struct {
		Providers []string `json:"providers"`
		Default   string   `json:"default"`
	}
	_ = json.Unmarshal(data, &p)
	if len(p.Providers) != 2 || p.Default != "primary" {
		t.Fatalf("providers = %v default %q", p.Providers, p.Default)
	}

	// provider/model 形式显式选择
	body := chatBody()
	body["model"] = "secondary/test-model"
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/ai/chat/completions", tok, body); rec.Code != http.StatusOK {
		t.Fatalf("explicit provider chat: %d", rec.Code)
	}
}

// TestStreamChatWithUsageAccounting 流式端到端：SSE 透传、
// include_usage 注入上游、用量入账、审计事件。
func TestStreamChatWithUsageAccounting(t *testing.T) {
	var upstreamGotStreamOptions bool
	up := fakeOpenAISSE(t, 25, &upstreamGotStreamOptions)
	gw := gateway.New().RegisterProvider("fake", up.URL, "")
	h := initRouter(t, newApp(t, gw))
	tok := adminToken(t, h)

	body := chatBody()
	body["stream"] = true
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/chat/completions", func() io.Reader {
		b, _ := json.Marshal(body)
		return bytes.NewReader(b)
	}())
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("stream chat: %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}
	out := rec.Body.String()
	// 增量分片：两个 delta chunk 分别携带 "你" 与 "好"。
	for _, want := range []string{`"content":"你"`, `"content":"好"`, `"total_tokens":25`, "data: [DONE]"} {
		if !bytes.Contains([]byte(out), []byte(want)) {
			t.Fatalf("stream output missing %q:\n%s", want, out)
		}
	}
	if !upstreamGotStreamOptions {
		t.Fatal("gateway must inject stream_options.include_usage for accounting")
	}

	// 用量入账
	rec, data := do(t, h, http.MethodGet, "/api/v1/ai/quota/platform", tok, nil)
	var q struct {
		Used int64 `json:"used"`
	}
	_ = json.Unmarshal(data, &q)
	if q.Used != 25 {
		t.Fatalf("stream usage = %d, want 25", q.Used)
	}

	// 审计事件
	rec, data = do(t, h, http.MethodGet, "/api/v1/audit/events?module=ai-gateway", tok, nil)
	var list struct {
		Events []audit.Event `json:"events"`
	}
	_ = json.Unmarshal(data, &list)
	if len(list.Events) == 0 || list.Events[0].Event != "chat.stream.ok" {
		t.Fatalf("stream audit event = %+v", list.Events)
	}
}

package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/basalt-go/basalt/pkg/ai/gateway"
	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/aisafety"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/tenant"
)

// newSafetyGateway 装配 identity+tenant+audit+aisafety+gateway，
// 返回注入了审核器的网关与 app。
func newSafetyGateway(t *testing.T, gw *gateway.Module) http.Handler {
	t.Helper()
	safety := aisafety.New()
	a := app.New(nil)
	for _, m := range []app.Module{identity.New(), tenant.New(), audit.New(), safety, gw} {
		if err := a.Registry().Register(m); err != nil {
			t.Fatalf("register %s: %v", m.Name(), err)
		}
	}
	gw.SetInspector(safety.Inspector())
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

// safetySetup 登录并导入敏感词、设置平台策略。
func safetySetup(t *testing.T, h http.Handler, word, action string) string {
	t.Helper()
	tok := adminToken(t, h)
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/ai-safety/terms", tok, map[string]string{"word": word}); rec.Code != http.StatusCreated {
		t.Fatalf("add term: %d %s", rec.Code, rec.Body.String())
	}
	if rec, _ := do(t, h, http.MethodPut, "/api/v1/ai-safety/policies/default", tok, map[string]string{"action": action}); rec.Code != http.StatusOK {
		t.Fatalf("set policy: %d %s", rec.Code, rec.Body.String())
	}
	return tok
}

// fakeOpenAICustom 可配置回复内容的非流式上游；捕获收到的消息供断言。
func fakeOpenAICustom(t *testing.T, reply string, gotContent *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []gateway.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if gotContent != nil {
			for _, m := range body.Messages {
				*gotContent = append(*gotContent, m.Content)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": reply}},
			},
			"usage": map[string]int64{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeOpenAISSEChunks 可配置 delta 分片的流式上游（敏感词可跨 chunk 拆分）。
func fakeOpenAISSEChunks(t *testing.T, chunks []string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, c := range chunks {
			data, _ := json.Marshal(map[string]any{
				"choices": []any{map[string]any{"delta": map[string]string{"content": c}}},
			})
			_, _ = io.WriteString(w, "data: "+string(data)+"\n\n")
			fl.Flush()
		}
		_, _ = io.WriteString(w, `data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":8,"total_tokens":18}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func streamChat(t *testing.T, h http.Handler, tok string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"model": "test-model", "stream": true,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// TestSafetyInputReject 默认 reject：输入命中 → 422 拒绝，上游未被调用。
func TestSafetyInputReject(t *testing.T) {
	up := fakeOpenAICustom(t, "pong", nil)
	gw := gateway.New().RegisterProvider("fake", up.URL, "")
	h := newSafetyGateway(t, gw)
	tok := safetySetup(t, h, "机密数据", "reject")

	if rec, _ := do(t, h, http.MethodPost, "/api/v1/ai/chat/completions", tok,
		map[string]any{"model": "m", "messages": []map[string]string{{"role": "user", "content": "请描述机密数据"}}}); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "safety.input_rejected") {
		t.Fatalf("input reject: %d %s", rec.Code, rec.Body.String())
	}
	// 审计链含 safety.input.reject
	_, data := do(t, h, http.MethodGet, "/api/v1/audit/events?module=ai-gateway&limit=5", tok, nil)
	var list struct {
		Events []audit.Event `json:"events"`
	}
	_ = json.Unmarshal(data, &list)
	found := false
	for _, e := range list.Events {
		if e.Event == "safety.input.reject" {
			found = true
		}
	}
	if !found {
		t.Fatalf("safety audit missing: %+v", list.Events)
	}
}

// TestSafetyInputReplace 输入替换：上游收到改写后的消息。
func TestSafetyInputReplace(t *testing.T) {
	var got []string
	up := fakeOpenAICustom(t, "ok", &got)
	gw := gateway.New().RegisterProvider("fake", up.URL, "")
	h := newSafetyGateway(t, gw)
	tok := safetySetup(t, h, "机密数据", "replace")

	if rec, _ := do(t, h, http.MethodPost, "/api/v1/ai/chat/completions", tok,
		map[string]any{"model": "m", "messages": []map[string]string{{"role": "user", "content": "请描述机密数据细节"}}}); rec.Code != http.StatusOK {
		t.Fatalf("input replace: %d %s", rec.Code, rec.Body.String())
	}
	if len(got) == 0 || strings.Contains(got[0], "机密数据") || !strings.Contains(got[0], "***") {
		t.Fatalf("upstream got = %v（应已替换）", got)
	}
}

// TestSafetyOutputReplaceNonStream 非流式输出替换：下发内容已脱敏。
func TestSafetyOutputReplaceNonStream(t *testing.T) {
	up := fakeOpenAICustom(t, "这是机密数据的说明", nil)
	gw := gateway.New().RegisterProvider("fake", up.URL, "")
	h := newSafetyGateway(t, gw)
	tok := safetySetup(t, h, "机密数据", "replace")

	rec, data := do(t, h, http.MethodPost, "/api/v1/ai/chat/completions", tok, chatBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("output replace: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(string(data), "机密数据") {
		t.Fatalf("output leaked: %s", data)
	}
	if !strings.Contains(string(data), "***") {
		t.Fatalf("output not replaced: %s", data)
	}
}

// TestSafetyStreamCrossChunkReplace SSE 输出侧：敏感词跨 chunk 拆分
//（"机密" + "数据"），替换后下发内容不含完整词。
func TestSafetyStreamCrossChunkReplace(t *testing.T) {
	up := fakeOpenAISSEChunks(t, []string{"回答：机", "密数据与", "其它内容"})
	gw := gateway.New().RegisterProvider("fake", up.URL, "")
	h := newSafetyGateway(t, gw)
	tok := safetySetup(t, h, "机密数据", "replace")

	code, out := streamChat(t, h, tok)
	if code != http.StatusOK {
		t.Fatalf("stream: %d %s", code, out)
	}
	if strings.Contains(out, "机密数据") {
		t.Fatalf("stream leaked term:\n%s", out)
	}
	if !strings.Contains(out, "***") {
		t.Fatalf("stream replace missing:\n%s", out)
	}
	// 窗口缓冲把末段拆为独立 delta（Finish 冲洗），断言拼接语义
	if !strings.Contains(out, "data: [DONE]") || !strings.Contains(out, "它内容") {
		t.Fatalf("stream incomplete:\n%s", out)
	}
}

// TestSafetyStreamReject SSE 输出侧 reject：error 事件下发并断流
//（无 [DONE]）。
func TestSafetyStreamReject(t *testing.T) {
	up := fakeOpenAISSEChunks(t, []string{"正常内容，", "然后机密数据", "曝光"})
	gw := gateway.New().RegisterProvider("fake", up.URL, "")
	h := newSafetyGateway(t, gw)
	tok := safetySetup(t, h, "机密数据", "reject")

	code, out := streamChat(t, h, tok)
	if code != http.StatusOK { // SSE 已 200 开头，拒绝以 error 事件表达
		t.Fatalf("stream reject code: %d %s", code, out)
	}
	if !strings.Contains(out, "safety.output_rejected") {
		t.Fatalf("error event missing:\n%s", out)
	}
	if strings.Contains(out, "data: [DONE]") {
		t.Fatalf("rejected stream must stop before [DONE]:\n%s", out)
	}
}

// TestSafetyMarkPassthrough mark：内容原样透传（含命中词）+ 审计标记。
func TestSafetyMarkPassthrough(t *testing.T) {
	up := fakeOpenAICustom(t, "内含机密数据", nil)
	gw := gateway.New().RegisterProvider("fake", up.URL, "")
	h := newSafetyGateway(t, gw)
	tok := safetySetup(t, h, "机密数据", "mark")

	rec, data := do(t, h, http.MethodPost, "/api/v1/ai/chat/completions", tok, chatBody())
	if rec.Code != http.StatusOK || !strings.Contains(string(data), "机密数据") {
		t.Fatalf("mark passthrough: %d %s", rec.Code, data)
	}
	rec, data = do(t, h, http.MethodGet, "/api/v1/audit/events?module=ai-gateway&limit=5", tok, nil)
	var list struct {
		Events []audit.Event `json:"events"`
	}
	_ = json.Unmarshal(data, &list)
	found := false
	for _, e := range list.Events {
		if e.Event == "safety.output.mark" {
			found = true
		}
	}
	if !found {
		t.Fatalf("mark audit missing: %+v", list.Events)
	}
}

// TestSafetyDisabledZeroOverhead 开关关闭：输入命中也直通（策略 reject 不生效）。
func TestSafetyDisabledZeroOverhead(t *testing.T) {
	up := fakeOpenAICustom(t, "pong", nil)
	gw := gateway.New().RegisterProvider("fake", up.URL, "")
	h := newSafetyGateway(t, gw)
	tok := safetySetup(t, h, "机密数据", "reject")
	// 关闭全局开关
	if rec, _ := do(t, h, http.MethodPut, "/api/v1/ai-safety/settings", tok, map[string]bool{"enabled": false}); rec.Code != http.StatusOK {
		t.Fatalf("disable: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/ai/chat/completions", tok,
		map[string]any{"model": "m", "messages": []map[string]string{{"role": "user", "content": "机密数据直通测试"}}}); rec.Code != http.StatusOK {
		t.Fatalf("disabled passthrough: %d %s", rec.Code, rec.Body.String())
	}
}

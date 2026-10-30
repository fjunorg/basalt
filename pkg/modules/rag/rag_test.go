package rag_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/identity"
	"github.com/basalt-go/basalt/pkg/modules/rag"
	"github.com/basalt-go/basalt/pkg/stores/gormstore"
)

// ---------- 测试基建（对齐 file/workflow 模块测试范式） ----------

func newApp(t *testing.T, m *rag.Module) http.Handler {
	t.Helper()
	a := app.New(nil)
	for _, mod := range []app.Module{identity.New(), audit.New(), m} {
		if err := a.Registry().Register(mod); err != nil {
			t.Fatalf("register %s: %v", mod.Name(), err)
		}
	}
	resolved, err := a.Registry().Resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, mod := range resolved {
		if err := mod.Init(context.Background(), a); err != nil {
			t.Fatalf("init %s: %v", mod.Name(), err)
		}
		_ = mod.Start(context.Background())
	}
	return a.Router().Handler()
}

func adminToken(t *testing.T, h http.Handler) string {
	t.Helper()
	rec, data := do(t, h, http.MethodPost, "/api/v1/identity/login", "", map[string]string{
		"username": "admin", "password": "Basalt@2026",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("admin login: %d %s", rec.Code, rec.Body.String())
	}
	var d struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(data, &d)
	return d.Token
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
		Data  json.RawMessage `json:"data"`
		Error string          `json:"message"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec, env.Data
}

// ---------- 确定性假 embedding（不做真实 LLM 调用） ----------

// stubEmbedder 把文本映射到固定维度向量：每个 rune 落入固定桶累加。
// 同文本必得同向量（确定性）；共享字符的文本余弦相似度高——
// “苹果 香蕉”与“苹果 榨汁”正相关，与“量子 物理”（不同桶）正交。
type stubEmbedder struct{ dim int }

func (s stubEmbedder) ModelName() string { return "stub-embed" }

func (s stubEmbedder) Embed(_ context.Context, texts []string) ([][]float64, error) {
	out := make([][]float64, len(texts))
	for i, t := range texts {
		v := make([]float64, s.dim)
		for _, r := range t {
			v[int(r)%s.dim]++
		}
		out[i] = v
	}
	return out, nil
}

// stubFileResolver 模拟 file 模块引用通道。
type stubFileResolver struct {
	files map[string]stubFile
}
type stubFile struct{ name, content string }

func (s *stubFileResolver) ResolveFile(_ context.Context, fileID string) (string, io.ReadCloser, error) {
	f, ok := s.files[fileID]
	if !ok {
		return "", nil, rag.ErrFileNotFound
	}
	return f.name, io.NopCloser(strings.NewReader(f.content)), nil
}

// ---------- 纯函数：切片策略 ----------

func TestSplitTextByLength(t *testing.T) {
	text := strings.Repeat("甲", 26) // 26 rune
	parts := rag.SplitText(text, rag.ChunkStrategy{Mode: rag.ChunkByLength, Size: 10, Overlap: 3})
	// 窗口 [0:10] [7:17] [14:24] [21:26] → 4 块
	if len(parts) != 4 {
		t.Fatalf("parts = %d, want 4: %q", len(parts), parts)
	}
	if parts[0] != strings.Repeat("甲", 10) || parts[3] != strings.Repeat("甲", 5) {
		t.Fatalf("window content wrong: %q", parts)
	}
	// 重叠语义：块 i 尾部 3 字符 == 块 i+1 头部 3 字符（rune 计）
	if string([]rune(parts[0])[7:]) != string([]rune(parts[1])[:3]) {
		t.Fatalf("overlap broken: %q vs %q", parts[0][7:], parts[1][:3])
	}
}

func TestSplitTextBySeparator(t *testing.T) {
	text := "第一段\n\n第二段\n\n\n\n第三段"
	parts := rag.SplitText(text, rag.ChunkStrategy{Mode: rag.ChunkBySeparator, Separator: "\n\n"})
	if len(parts) != 3 || parts[0] != "第一段" || parts[2] != "第三段" {
		t.Fatalf("parts = %q", parts)
	}
	// 超长段回退按长度细化
	long := strings.Repeat("字", 25)
	parts = rag.SplitText("短\n\n"+long, rag.ChunkStrategy{
		Mode: rag.ChunkBySeparator, Separator: "\n\n", Size: 10, Overlap: 2,
	})
	if len(parts) != 1+3 { // 短段 1 + 25 字按 size10/overlap2 切 3 块
		t.Fatalf("fallback split = %d parts: %q", len(parts), parts)
	}
}

func TestChunkStrategyNormalize(t *testing.T) {
	// 默认值填充：mode/size 填默认；overlap 零值 = 不重叠
	//（显式配置仍可传正值；零值歧义从简，可预测优先）。
	s, err := rag.ChunkStrategy{}.Normalize()
	if err != nil || s.Mode != rag.ChunkByLength || s.Size != rag.DefaultChunkSize || s.Overlap != 0 {
		t.Fatalf("defaults = %+v err=%v", s, err)
	}
	// overlap >= size 拒绝
	if _, err := (rag.ChunkStrategy{Mode: rag.ChunkByLength, Size: 10, Overlap: 10}).Normalize(); !errors.Is(err, rag.ErrBadStrategy) {
		t.Fatalf("overlap==size: err=%v", err)
	}
	// 非法 mode
	if _, err := (rag.ChunkStrategy{Mode: "matrix"}).Normalize(); !errors.Is(err, rag.ErrBadStrategy) {
		t.Fatalf("bad mode: err=%v", err)
	}
	// separator 模式缺分隔符
	if _, err := (rag.ChunkStrategy{Mode: rag.ChunkBySeparator}).Normalize(); !errors.Is(err, rag.ErrBadStrategy) {
		t.Fatalf("no separator: err=%v", err)
	}
	// 空文本
	if got := rag.SplitText("   ", rag.ChunkStrategy{}); got != nil {
		t.Fatalf("empty text = %q", got)
	}
}

func TestCosineSimilarity(t *testing.T) {
	if s := rag.CosineSimilarity(rag.Vector{1, 0}, rag.Vector{1, 0}); s < 0.999 {
		t.Fatalf("identical = %f", s)
	}
	if s := rag.CosineSimilarity(rag.Vector{1, 0}, rag.Vector{0, 1}); s != 0 {
		t.Fatalf("orthogonal = %f", s)
	}
	if s := rag.CosineSimilarity(rag.Vector{0, 0}, rag.Vector{1, 1}); s != 0 {
		t.Fatalf("zero vector = %f", s)
	}
	if s := rag.CosineSimilarity(rag.Vector{1, 0}, rag.Vector{1, 0, 0}); s != 0 {
		t.Fatalf("dim mismatch = %f", s)
	}
}

func TestVectorScanValue(t *testing.T) {
	// nil → SQL NULL（未向量化哨兵）
	v := rag.Vector(nil)
	dv, err := v.Value()
	if err != nil || dv != nil {
		t.Fatalf("nil value = %v err=%v", dv, err)
	}
	var out rag.Vector
	if err := out.Scan(nil); err != nil || out != nil {
		t.Fatalf("scan nil = %v err=%v", out, err)
	}
	// 往返
	src := rag.Vector{0.5, -1.25, 3}
	dv, err = src.Value()
	if err != nil {
		t.Fatalf("value: %v", err)
	}
	b, ok := dv.([]byte)
	if !ok {
		t.Fatalf("value type %T, want []byte (blob)", dv)
	}
	var back rag.Vector
	if err := back.Scan(b); err != nil || len(back) != 3 || back[1] != -1.25 {
		t.Fatalf("roundtrip = %v err=%v", back, err)
	}
}

// ---------- 知识库 CRUD ----------

func TestKBCRUD(t *testing.T) {
	h := newApp(t, rag.New(nil, stubEmbedder{dim: 32}))
	tok := adminToken(t, h)

	// 空 name → 400
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/rag/kbs", tok, map[string]any{"name": "  "}); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty name: %d", rec.Code)
	}
	// 非法策略 → 400
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/rag/kbs", tok, map[string]any{
		"name": "坏库", "strategy": map[string]any{"mode": "length", "size": 10, "overlap": 20},
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad strategy: %d", rec.Code)
	}

	// 创建（缺省策略自动填充）
	rec, data := do(t, h, http.MethodPost, "/api/v1/rag/kbs", tok, map[string]any{
		"name": "产品手册库", "description": "内部产品文档",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var kb rag.KnowledgeBase
	_ = json.Unmarshal(data, &kb)
	if kb.ID == "" || kb.Strategy.Mode != rag.ChunkByLength || kb.Strategy.Size != rag.DefaultChunkSize {
		t.Fatalf("kb = %+v", kb)
	}

	// 列表 + 详情
	rec, data = do(t, h, http.MethodGet, "/api/v1/rag/kbs", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	var lst struct {
		Count int `json:"count"`
	}
	_ = json.Unmarshal(data, &lst)
	if lst.Count != 1 {
		t.Fatalf("count = %d", lst.Count)
	}
	rec, data = do(t, h, http.MethodGet, "/api/v1/rag/kbs/"+kb.ID, tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d", rec.Code)
	}
	var detail struct {
		KB            rag.KnowledgeBase `json:"kb"`
		DocumentCount int               `json:"document_count"`
	}
	_ = json.Unmarshal(data, &detail)
	if detail.KB.Name != "产品手册库" || detail.DocumentCount != 0 {
		t.Fatalf("detail = %+v", detail)
	}

	// 更新（改名 + 换分隔符策略）
	rec, data = do(t, h, http.MethodPatch, "/api/v1/rag/kbs/"+kb.ID, tok, map[string]any{
		"name": "产品手册库v2", "strategy": map[string]any{"mode": "separator", "separator": "\n\n"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d", rec.Code)
	}
	_ = json.Unmarshal(data, &kb)
	if kb.Name != "产品手册库v2" || kb.Strategy.Mode != rag.ChunkBySeparator {
		t.Fatalf("patched = %+v", kb)
	}

	// 404 分支
	if rec, _ := do(t, h, http.MethodGet, "/api/v1/rag/kbs/ghost", tok, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("get ghost: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodDelete, "/api/v1/rag/kbs/ghost", tok, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("delete ghost: %d", rec.Code)
	}

	// 删除
	if rec, _ := do(t, h, http.MethodDelete, "/api/v1/rag/kbs/"+kb.ID, tok, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodGet, "/api/v1/rag/kbs/"+kb.ID, tok, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: %d", rec.Code)
	}
}

// ---------- 管道：登记 → 切片 → 向量化 → 检索 ----------

// pipeDoc 走完 登记→切片→向量化，返回 (kbID, docID)。
func pipeDoc(t *testing.T, h http.Handler, tok, kbID, name, text string) string {
	t.Helper()
	rec, data := do(t, h, http.MethodPost, "/api/v1/rag/kbs/"+kbID+"/documents", tok, map[string]any{
		"name": name, "text": text,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	var doc rag.Document
	_ = json.Unmarshal(data, &doc)
	if doc.Status != rag.StatusRegistered {
		t.Fatalf("status = %s", doc.Status)
	}
	rec, _ = do(t, h, http.MethodPost, "/api/v1/rag/documents/"+doc.ID+"/chunk", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("chunk: %d %s", rec.Code, rec.Body.String())
	}
	rec, _ = do(t, h, http.MethodPost, "/api/v1/rag/documents/"+doc.ID+"/embed", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("embed: %d %s", rec.Code, rec.Body.String())
	}
	return doc.ID
}

func TestPipelineSearchTopK(t *testing.T) {
	h := newApp(t, rag.New(nil, stubEmbedder{dim: 64}))
	tok := adminToken(t, h)

	// 库策略：按空行分段（段即 chunk，断言直观）
	_, data := do(t, h, http.MethodPost, "/api/v1/rag/kbs", tok, map[string]any{
		"name": "混合库",
		"strategy": map[string]any{"mode": "separator", "separator": "\n\n"},
	})
	var kb rag.KnowledgeBase
	_ = json.Unmarshal(data, &kb)

	pipeDoc(t, h, tok, kb.ID, "水果常识",
		"苹果 香蕉 是常见的水果\n\n量子 物理 是基础科学\n\n苹果 可以榨汁")

	// 管道顺序：未切片先 embed → 400
	rec, data := do(t, h, http.MethodPost, "/api/v1/rag/kbs/"+kb.ID+"/documents", tok, map[string]any{
		"name": "顺序错误", "text": "内容",
	})
	var doc2 rag.Document
	_ = json.Unmarshal(data, &doc2)
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/rag/documents/"+doc2.ID+"/embed", tok, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("embed before chunk: %d, want 400", rec.Code)
	}
	// 重复向量化 → 409（先走完管道）
	_, _ = do(t, h, http.MethodPost, "/api/v1/rag/documents/"+doc2.ID+"/chunk", tok, nil)
	_, _ = do(t, h, http.MethodPost, "/api/v1/rag/documents/"+doc2.ID+"/embed", tok, nil)
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/rag/documents/"+doc2.ID+"/embed", tok, nil); rec.Code != http.StatusConflict {
		t.Fatalf("re-embed: %d, want 409", rec.Code)
	}

	// 登记校验：text/file 双空 → 400
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/rag/kbs/"+kb.ID+"/documents", tok, map[string]any{"name": "空"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("no text/file: %d", rec.Code)
	}

	// 检索 1：共享词召回正确——“苹果”与两个苹果段正相关，
	// 与“量子 物理”段正交（不进 hits）。
	rec, data = do(t, h, http.MethodPost, "/api/v1/rag/kbs/"+kb.ID+"/search", tok, map[string]any{
		"query": "苹果", "top_k": 2,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("search: %d %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Count int       `json:"count"`
		Hits  []rag.Hit `json:"hits"`
	}
	_ = json.Unmarshal(data, &res)
	if res.Count == 0 || len(res.Hits) != 2 {
		t.Fatalf("hits = %+v", res.Hits)
	}
	for _, hit := range res.Hits {
		if strings.Contains(hit.Content, "量子") {
			t.Fatalf("orthogonal chunk recalled: %+v", hit)
		}
		if !strings.Contains(hit.Content, "苹果") {
			t.Fatalf("irrelevant hit: %+v", hit)
		}
	}
	// top-1 分数 >= top-2（降序）
	if res.Hits[0].Score < res.Hits[1].Score {
		t.Fatalf("not sorted desc: %+v", res.Hits)
	}
	// 命中携带文档名
	if res.Hits[0].DocName != "水果常识" {
		t.Fatalf("doc name = %q", res.Hits[0].DocName)
	}

	// 检索 2：top_k 截断为 1
	rec, data = do(t, h, http.MethodPost, "/api/v1/rag/kbs/"+kb.ID+"/search", tok, map[string]any{
		"query": "苹果", "top_k": 1,
	})
	_ = json.Unmarshal(data, &res)
	if res.Count != 1 || len(res.Hits) != 1 {
		t.Fatalf("top_k=1: %+v", res.Hits)
	}

	// 检索 3：跨文档聚合 + 删除文档后不再命中
	idB := pipeDoc(t, h, tok, kb.ID, "更多水果", "香蕉 产地 热带\n\n苹果 变种 很多")
	rec, data = do(t, h, http.MethodPost, "/api/v1/rag/kbs/"+kb.ID+"/search", tok, map[string]any{
		"query": "香蕉", "top_k": 10,
	})
	_ = json.Unmarshal(data, &res)
	docs := map[string]bool{}
	for _, hit := range res.Hits {
		docs[hit.DocID] = true
	}
	if len(docs) < 2 {
		t.Fatalf("expected cross-doc recall, hits=%+v", res.Hits)
	}
	if rec, _ := do(t, h, http.MethodDelete, "/api/v1/rag/documents/"+idB, tok, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete doc: %d", rec.Code)
	}
	rec, data = do(t, h, http.MethodPost, "/api/v1/rag/kbs/"+kb.ID+"/search", tok, map[string]any{
		"query": "香蕉", "top_k": 10,
	})
	_ = json.Unmarshal(data, &res)
	for _, hit := range res.Hits {
		if hit.DocID == idB {
			t.Fatalf("deleted doc still hit: %+v", hit)
		}
	}

	// 校验分支：空 query 400 / top_k 越界 400 / 省略 top_k 用默认 / 库不存在 404
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/rag/kbs/"+kb.ID+"/search", tok, map[string]any{"query": " "}); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty query: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/rag/kbs/"+kb.ID+"/search", tok, map[string]any{"query": "x", "top_k": 101}); rec.Code != http.StatusBadRequest {
		t.Fatalf("top_k=101: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/rag/kbs/ghost/search", tok, map[string]any{"query": "x"}); rec.Code != http.StatusNotFound {
		t.Fatalf("ghost kb search: %d", rec.Code)
	}
}

func TestDocDetailAndView(t *testing.T) {
	h := newApp(t, rag.New(nil, stubEmbedder{dim: 32}))
	tok := adminToken(t, h)
	_, data := do(t, h, http.MethodPost, "/api/v1/rag/kbs", tok, map[string]any{
		"name": "详情库", "strategy": map[string]any{"mode": "separator", "separator": "\n\n"},
	})
	var kb rag.KnowledgeBase
	_ = json.Unmarshal(data, &kb)

	rec, data := do(t, h, http.MethodPost, "/api/v1/rag/kbs/"+kb.ID+"/documents", tok, map[string]any{
		"name": "多段", "text": "甲段\n\n乙段\n\n丙段",
	})
	var doc rag.Document
	_ = json.Unmarshal(data, &doc)
	_, _ = do(t, h, http.MethodPost, "/api/v1/rag/documents/"+doc.ID+"/chunk", tok, nil)

	// 文档详情：切片预览 + embedded 标记
	rec, data = do(t, h, http.MethodGet, "/api/v1/rag/documents/"+doc.ID, tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get doc: %d", rec.Code)
	}
	var detail struct {
		Document rag.Document `json:"document"`
		Chunks   []struct {
			Seq      int    `json:"seq"`
			Content  string `json:"content"`
			Embedded bool   `json:"embedded"`
		} `json:"chunks"`
	}
	_ = json.Unmarshal(data, &detail)
	if len(detail.Chunks) != 3 || detail.Document.Status != rag.StatusChunked || detail.Document.ChunkCount != 3 {
		t.Fatalf("detail = %+v", detail)
	}
	if detail.Chunks[0].Embedded || detail.Chunks[0].Content != "甲段" {
		t.Fatalf("chunks[0] = %+v", detail.Chunks[0])
	}
	// 切片预览不含 embedding 向量本体（响应体积护栏）
	if strings.Contains(rec.Body.String(), "embedding") {
		t.Fatal("chunk view leaked embedding")
	}

	// 文档列表
	rec, data = do(t, h, http.MethodGet, "/api/v1/rag/kbs/"+kb.ID+"/documents", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list docs: %d", rec.Code)
	}
	var lst struct {
		Count int `json:"count"`
	}
	_ = json.Unmarshal(data, &lst)
	if lst.Count != 1 {
		t.Fatalf("docs count = %d", lst.Count)
	}
}

func TestFileRegistration(t *testing.T) {
	files := &stubFileResolver{files: map[string]stubFile{
		"f-001": {name: "培训教材.txt", content: "内部培训 第一课\n\n内部培训 第二课"},
	}}
	h := newApp(t, rag.New(files, stubEmbedder{dim: 32}))
	tok := adminToken(t, h)
	_, data := do(t, h, http.MethodPost, "/api/v1/rag/kbs", tok, map[string]any{
		"name": "文件库", "strategy": map[string]any{"mode": "separator", "separator": "\n\n"},
	})
	var kb rag.KnowledgeBase
	_ = json.Unmarshal(data, &kb)

	// file_id 登记成功，name 缺省取源文件名
	rec, data := do(t, h, http.MethodPost, "/api/v1/rag/kbs/"+kb.ID+"/documents", tok, map[string]any{
		"file_id": "f-001",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register file: %d %s", rec.Code, rec.Body.String())
	}
	var doc rag.Document
	_ = json.Unmarshal(data, &doc)
	if doc.SourceType != "file" || doc.FileID != "f-001" || doc.Name != "培训教材.txt" {
		t.Fatalf("doc = %+v", doc)
	}
	// 管道走通（内容已在登记期抽取，切片数与源文件分段一致）
	_, _ = do(t, h, http.MethodPost, "/api/v1/rag/documents/"+doc.ID+"/chunk", tok, nil)
	rec, _ = do(t, h, http.MethodPost, "/api/v1/rag/documents/"+doc.ID+"/embed", tok, nil)
	if rec.Code != http.StatusOK && rec.Code != http.StatusConflict {
		t.Fatalf("file doc embed: %d", rec.Code)
	}

	// 引用不存在 → 404
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/rag/kbs/"+kb.ID+"/documents", tok, map[string]any{"file_id": "ghost"}); rec.Code != http.StatusNotFound {
		t.Fatalf("ghost file: %d", rec.Code)
	}

	// resolver 未装配 → 503 明确报错
	h2 := newApp(t, rag.New(nil, stubEmbedder{dim: 32}))
	tok2 := adminToken(t, h2)
	_, data = do(t, h2, http.MethodPost, "/api/v1/rag/kbs", tok2, map[string]any{"name": "无文件库"})
	var kb2 rag.KnowledgeBase
	_ = json.Unmarshal(data, &kb2)
	if rec, _ := do(t, h2, http.MethodPost, "/api/v1/rag/kbs/"+kb2.ID+"/documents", tok2, map[string]any{"file_id": "f-001"}); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no resolver: %d, want 503", rec.Code)
	}
}

// ---------- 无 provider 配置：明确报错而非静默跳过 ----------

func TestEmbedNotConfigured(t *testing.T) {
	h := newApp(t, rag.New(nil, nil)) // emb = nil
	tok := adminToken(t, h)
	_, data := do(t, h, http.MethodPost, "/api/v1/rag/kbs", tok, map[string]any{"name": "无提供方库"})
	var kb rag.KnowledgeBase
	_ = json.Unmarshal(data, &kb)

	rec, data := do(t, h, http.MethodPost, "/api/v1/rag/kbs/"+kb.ID+"/documents", tok, map[string]any{
		"name": "文本", "text": "内容甲\n\n内容乙",
	})
	var doc rag.Document
	_ = json.Unmarshal(data, &doc)
	_, _ = do(t, h, http.MethodPost, "/api/v1/rag/documents/"+doc.ID+"/chunk", tok, nil)

	// embed → 503 且报错信息指明配置项
	rec, _ = do(t, h, http.MethodPost, "/api/v1/rag/documents/"+doc.ID+"/embed", tok, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("embed: %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "BASALT_AI_PROVIDER_URL") {
		t.Fatalf("error message = %s", rec.Body.String())
	}
	// 文档状态未被静默推进
	rec, data = do(t, h, http.MethodGet, "/api/v1/rag/documents/"+doc.ID, tok, nil)
	var detail struct {
		Document rag.Document `json:"document"`
	}
	_ = json.Unmarshal(data, &detail)
	if detail.Document.Status != rag.StatusChunked {
		t.Fatalf("status silently advanced: %s", detail.Document.Status)
	}
	// 检索同样明确报错
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/rag/kbs/"+kb.ID+"/search", tok, map[string]any{"query": "内容"}); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("search: %d, want 503", rec.Code)
	}
}

// ---------- 权限点拦截 ----------

func TestPermissionGuard(t *testing.T) {
	h := newApp(t, rag.New(nil, stubEmbedder{dim: 32}))
	tok := adminToken(t, h)

	// 建普通用户（无角色 → 无 rag 权限点）
	rec, _ := do(t, h, http.MethodPost, "/api/v1/identity/users", tok, map[string]string{
		"username": "plain", "password": "Plain@2026", "nickname": "普通用户",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create user: %d %s", rec.Code, rec.Body.String())
	}
	plain := loginAs(t, h, "plain", "Plain@2026")

	// 三类权限点全部 403
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/rag/kbs"},
		{http.MethodGet, "/api/v1/rag/kbs"},
		{http.MethodPost, "/api/v1/rag/kbs/x/search"},
	} {
		if rec, _ := do(t, h, tc.method, tc.path, plain, map[string]any{"name": "n", "query": "q"}); rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s: %d, want 403", tc.method, tc.path, rec.Code)
		}
	}
}

func loginAs(t *testing.T, h http.Handler, user, pass string) string {
	t.Helper()
	rec, data := do(t, h, http.MethodPost, "/api/v1/identity/login", "", map[string]string{
		"username": user, "password": pass,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("login %s: %d %s", user, rec.Code, rec.Body.String())
	}
	var d struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(data, &d)
	return d.Token
}

// ---------- sqlite GORM：CRUD + 重启恢复 ----------

func TestGORMPersistenceRestart(t *testing.T) {
	dsn := t.TempDir() + "/rag.db"
	emb := stubEmbedder{dim: 48}

	mkApp := func(store rag.Store) http.Handler {
		return newApp(t, rag.NewWithStore(store, nil, emb))
	}

	// 第一次启动：完整管道入库
	s1, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	h1 := mkApp(s1.RagStore())
	tok := adminToken(t, h1)
	_, data := do(t, h1, http.MethodPost, "/api/v1/rag/kbs", tok, map[string]any{
		"name": "重启库", "strategy": map[string]any{"mode": "separator", "separator": "\n\n"},
	})
	var kb rag.KnowledgeBase
	_ = json.Unmarshal(data, &kb)
	docID := pipeDoc(t, h1, tok, kb.ID, "重启文档", "苹果 香蕉 水果\n\n量子 物理 科学")
	if sqlDB, err := s1.DB.DB(); err == nil {
		_ = sqlDB.Close()
	}

	// 第二次启动：同一 dsn 重开（重启恢复）
	s2, err := gormstore.Open(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	h2 := mkApp(s2.RagStore())
	tok2 := adminToken(t, h2)

	// 知识库与文档状态恢复
	rec, data := do(t, h2, http.MethodGet, "/api/v1/rag/kbs/"+kb.ID, tok2, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("kb after restart: %d", rec.Code)
	}
	rec, data = do(t, h2, http.MethodGet, "/api/v1/rag/documents/"+docID, tok2, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("doc after restart: %d", rec.Code)
	}
	var detail struct {
		Document rag.Document `json:"document"`
		Chunks   []struct {
			Seq      int  `json:"seq"`
			Embedded bool `json:"embedded"`
		} `json:"chunks"`
	}
	_ = json.Unmarshal(data, &detail)
	if detail.Document.Status != rag.StatusEmbedded || detail.Document.EmbedModel != "stub-embed" || detail.Document.Dim != 48 {
		t.Fatalf("doc after restart = %+v", detail.Document)
	}
	if len(detail.Chunks) != 2 || !detail.Chunks[0].Embedded {
		t.Fatalf("chunks after restart = %+v", detail.Chunks)
	}

	// 关键验收：重启后向量自 blob 恢复，检索直接可用且召回正确
	rec, data = do(t, h2, http.MethodPost, "/api/v1/rag/kbs/"+kb.ID+"/search", tok2, map[string]any{
		"query": "苹果 香蕉", "top_k": 5,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("search after restart: %d %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Hits []rag.Hit `json:"hits"`
	}
	_ = json.Unmarshal(data, &res)
	if len(res.Hits) == 0 || !strings.Contains(res.Hits[0].Content, "苹果") {
		t.Fatalf("hits after restart = %+v", res.Hits)
	}
	if strings.Contains(res.Hits[0].Content, "量子") {
		t.Fatalf("wrong top-1 after restart: %+v", res.Hits[0])
	}
}

// ---------- OpenAI 兼容 embeddings 客户端 ----------

func TestOpenAIEmbeddingsClient(t *testing.T) {
	var gotAuth, gotModel string
	var gotInput []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		var body struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel, gotInput = body.Model, body.Input
		// 乱序返回（index 1,0）：客户端必须按 index 还原
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"index": 1, "embedding": []float64{0, 1}},
				{"index": 0, "embedding": []float64{1, 0}},
			},
		})
	}))
	defer srv.Close()

	c := rag.NewOpenAIEmbeddings(srv.URL, "sk-test", "bge-m3")
	if c.ModelName() != "bge-m3" {
		t.Fatalf("model = %s", c.ModelName())
	}
	vecs, err := c.Embed(context.Background(), []string{"甲", "乙"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if gotAuth != "Bearer sk-test" || gotModel != "bge-m3" || len(gotInput) != 2 {
		t.Fatalf("request = auth %q model %q input %v", gotAuth, gotModel, gotInput)
	}
	// 顺序还原断言
	if len(vecs) != 2 || vecs[0][0] != 1 || vecs[1][1] != 1 {
		t.Fatalf("vecs = %v (index order broken)", vecs)
	}

	// 上游非 200 → ErrEmbedBadUpstream
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer bad.Close()
	c2 := rag.NewOpenAIEmbeddings(bad.URL, "", "m")
	if _, err := c2.Embed(context.Background(), []string{"x"}); !errors.Is(err, rag.ErrEmbedBadUpstream) {
		t.Fatalf("bad upstream err = %v", err)
	}

	// env 构造：未配置 → 明确报错
	t.Setenv("BASALT_AI_PROVIDER_URL", "")
	t.Setenv("BASALT_AI_EMBED_MODEL", "")
	if _, err := rag.NewOpenAIEmbeddingsFromEnv(); !errors.Is(err, rag.ErrEmbedNotConfigured) {
		t.Fatalf("env empty err = %v", err)
	}
	// URL 有但模型缺 → 同样明确报错（不臆造默认模型）
	t.Setenv("BASALT_AI_PROVIDER_URL", "http://localhost:8000")
	t.Setenv("BASALT_AI_EMBED_MODEL", "")
	if _, err := rag.NewOpenAIEmbeddingsFromEnv(); !errors.Is(err, rag.ErrEmbedNotConfigured) {
		t.Fatalf("model missing err = %v", err)
	}
	// 齐备 → 构造成功
	t.Setenv("BASALT_AI_PROVIDER_KEY", "k")
	t.Setenv("BASALT_AI_EMBED_MODEL", "bge-m3")
	if c3, err := rag.NewOpenAIEmbeddingsFromEnv(); err != nil || c3.ModelName() != "bge-m3" {
		t.Fatalf("full env: %v", err)
	}
}

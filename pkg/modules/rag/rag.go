// Package rag 是 L2 知识库与检索增强模块：知识库 → 文档 → 切片 →
// 向量化 → 相似检索的完整 RAG 管道。
//
// 设计边界（docs/03 P0 缺口补齐）：
//   - 向量存储起步方案：embedding 以 blob 落 sqlite（与业务数据同一
//     恢复面，重启后检索直接可用），检索在内存做余弦相似度排序；
//     检索路径经 VectorSearcher 接口收敛，专用向量库（milvus 等）
//     后续经此缝注入，模块层零改动。
//   - 向量化客户端自含于本包（OpenAI 兼容 /v1/embeddings），复用
//     BASALT_AI_PROVIDER_* 环境变量族；无 provider 配置时向量化
//     步骤明确报错而非静默跳过。
//   - 文档登记双通道：直接文本输入，或引用 file 模块上传的文件
//     （经 FileResolver 抽象，file 包零改动）。
package rag

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/crypto/randutil"
	"github.com/basalt-go/basalt/pkg/kernel/web"
	"github.com/basalt-go/basalt/pkg/modules/audit"
	"github.com/basalt-go/basalt/pkg/modules/identity"
)

const Version = "0.1.0"

// 权限点。
const (
	PermKBManage  = "rag:kb:manage"  // 知识库建/改/删
	PermDocManage = "rag:doc:manage" // 文档登记/切片/向量化/删除
	PermRead      = "rag:read"       // 列表/详情/检索
)

// 管道状态。
const (
	StatusRegistered = "registered" // 已登记（原文在手，未切片）
	StatusChunked    = "chunked"    // 已切片（未向量化）
	StatusEmbedded   = "embedded"   // 已向量化（可检索）
)

var (
	ErrNotFound     = errors.New("rag: not found")
	ErrNoText       = errors.New("rag: text or file_id required")
	ErrBadStrategy  = errors.New("rag: invalid chunk strategy")
	ErrNotChunked   = errors.New("rag: chunk the document before embedding")
	ErrAlreadyDone  = errors.New("rag: document already embedded")
	ErrFileUnavail  = errors.New("rag: file resolver unavailable")
	ErrFileNotFound = errors.New("rag: source file not found")
	ErrEmptyKB      = errors.New("rag: knowledge base name required")
	ErrBadTopK      = errors.New("rag: top_k must be 1..100")
	ErrEmptyQuery   = errors.New("rag: query required")
)

// ---------- 向量序列化 ----------

// Vector 是一个切片向量。经 driver.Valuer/sql.Scanner 以 JSON 字节
// 落 sqlite BLOB——向量随业务数据同库持久化，重启后无需重建索引
// 即可检索；nil 与空向量语义不同（nil = 未向量化）。
type Vector []float64

func (v Vector) Value() (driver.Value, error) {
	if v == nil {
		return nil, nil // SQL NULL：未向量化
	}
	return json.Marshal([]float64(v))
}

func (v *Vector) Scan(src any) error {
	if src == nil {
		*v = nil
		return nil
	}
	var b []byte
	switch s := src.(type) {
	case []byte:
		b = s
	case string:
		b = []byte(s)
	default:
		return fmt.Errorf("rag: vector scan from %T", src)
	}
	return json.Unmarshal(b, (*[]float64)(v))
}

// GormDataType 声明列类型为 blob。
func (Vector) GormDataType() string { return "blob" }

// ---------- 切片策略 ----------

const (
	ChunkByLength    = "length"    // 按长度滑窗（rune 计）
	ChunkBySeparator = "separator" // 按分隔符切（超长段回退按长度）
)

// 默认切片策略：500 字/块，重叠 50，兼顾语义连贯与召回粒度。
const (
	DefaultChunkSize    = 500
	DefaultChunkOverlap = 50
)

// ChunkStrategy 是可配置的切片参数（挂在知识库上，随库生效）。
type ChunkStrategy struct {
	Mode      string `json:"mode"`                // length | separator
	Size      int    `json:"size"`                // length：目标块长（rune）
	Overlap   int    `json:"overlap"`             // length：相邻块重叠（rune）
	Separator string `json:"separator,omitempty"` // separator：分隔符
}

// Normalize 填默认值并校验（mode 非空默认 length）。
func (s ChunkStrategy) Normalize() (ChunkStrategy, error) {
	if s.Mode == "" {
		s.Mode = ChunkByLength
	}
	switch s.Mode {
	case ChunkByLength:
		if s.Size <= 0 {
			s.Size = DefaultChunkSize
		}
		if s.Overlap < 0 {
			s.Overlap = 0
		}
		if s.Overlap >= s.Size {
			return ChunkStrategy{}, fmt.Errorf("%w: overlap (%d) must be < size (%d)", ErrBadStrategy, s.Overlap, s.Size)
		}
	case ChunkBySeparator:
		if s.Separator == "" {
			return ChunkStrategy{}, fmt.Errorf("%w: separator required in separator mode", ErrBadStrategy)
		}
		// Size/Overlap 是超长段回退滑窗的护栏参数，同样填默认。
		if s.Size <= 0 {
			s.Size = DefaultChunkSize
		}
		if s.Overlap < 0 {
			s.Overlap = 0
		}
		if s.Overlap >= s.Size {
			return ChunkStrategy{}, fmt.Errorf("%w: overlap (%d) must be < size (%d)", ErrBadStrategy, s.Overlap, s.Size)
		}
	default:
		return ChunkStrategy{}, fmt.Errorf("%w: mode %q (want length|separator)", ErrBadStrategy, s.Mode)
	}
	return s, nil
}

// SplitText 按策略切分文本（纯函数，切片语义单一事实来源）。
func SplitText(text string, s ChunkStrategy) []string {
	s, err := s.Normalize()
	if err != nil {
		return nil
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var parts []string
	switch s.Mode {
	case ChunkBySeparator:
		for _, p := range strings.Split(text, s.Separator) {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			parts = append(parts, p)
		}
	default: // ChunkByLength
		parts = []string{text}
	}
	// 统一长度护栏：任何模式下超过 Size 的段再按长度滑窗细化，
	// 防止单条超长切片撑爆 embedding 输入。
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		r := []rune(p)
		if len(r) <= s.Size {
			out = append(out, string(r))
			continue
		}
		step := s.Size - s.Overlap
		if step <= 0 { // 防御：护栏参数异常时退化为无重叠滑窗，绝不零步进
			step = s.Size
		}
		for i := 0; i < len(r); i += step {
			j := i + s.Size
			if j > len(r) {
				j = len(r)
			}
			out = append(out, string(r[i:j]))
			if j == len(r) {
				break
			}
		}
	}
	return out
}

// ---------- 实体 ----------

// KnowledgeBase 是知识库（切片策略挂在库上，库内文档统一生效）。
type KnowledgeBase struct {
	ID          string        `json:"id" gorm:"primaryKey;size:40"`
	Name        string        `json:"name" gorm:"size:128"`
	Description string        `json:"description,omitempty" gorm:"size:255"`
	Strategy    ChunkStrategy `json:"strategy" gorm:"serializer:json"`
	OwnerID     string        `json:"owner_id" gorm:"size:40;index"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

// Document 是登记进知识库的文档。SourceType=text 时原文在 Content；
// sourceType=file 时 Content 保存登记期抽取的文本（登记即抽取，
// 检索管道不再依赖文件在位）。
type Document struct {
	ID         string    `json:"id" gorm:"primaryKey;size:40"`
	KBID       string    `json:"kb_id" gorm:"size:40;index"`
	Name       string    `json:"name" gorm:"size:255"`
	SourceType string    `json:"source_type" gorm:"size:16"` // text | file
	FileID     string    `json:"file_id,omitempty" gorm:"size:40"`
	Content    string    `json:"content,omitempty" gorm:"type:text"`
	Status     string    `json:"status" gorm:"size:16;index"` // registered|chunked|embedded
	ChunkCount int       `json:"chunk_count"`
	EmbedModel string    `json:"embed_model,omitempty" gorm:"size:128"`
	Dim        int       `json:"dim,omitempty"` // 向量维度（0 = 未向量化）
	CreatorID  string    `json:"creator_id" gorm:"size:40;index"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Chunk 是文档的一个切片。Embedding 为 nil 表示未向量化。
type Chunk struct {
	ID        string    `json:"id" gorm:"primaryKey;size:40"`
	KBID      string    `json:"kb_id" gorm:"size:40;index"`
	DocID     string    `json:"doc_id" gorm:"size:40;index"`
	Seq       int       `json:"seq"`
	Content   string    `json:"content" gorm:"type:text"`
	Embedding Vector    `json:"-" gorm:"type:blob"`
	CreatedAt time.Time `json:"created_at"`
}

// Hit 是一次检索命中。
type Hit struct {
	ChunkID string  `json:"chunk_id"`
	DocID   string  `json:"doc_id"`
	DocName string  `json:"doc_name"`
	Seq     int     `json:"seq"`
	Content string  `json:"content"`
	Score   float64 `json:"score"` // 余弦相似度 [-1,1]
}

// ---------- 持久化抽象 ----------

// Store 抽象知识库/文档/切片持久化（GORM 落盘或内存实现）。
type Store interface {
	CreateKB(ctx context.Context, kb KnowledgeBase) error
	GetKB(ctx context.Context, id string) (KnowledgeBase, error)
	ListKBs(ctx context.Context) ([]KnowledgeBase, error)
	UpdateKB(ctx context.Context, kb KnowledgeBase) error
	DeleteKB(ctx context.Context, id string) error // 级联删文档与切片

	CreateDoc(ctx context.Context, d Document) error
	GetDoc(ctx context.Context, id string) (Document, error)
	ListDocs(ctx context.Context, kbID string) ([]Document, error)
	UpdateDoc(ctx context.Context, d Document) error
	DeleteDoc(ctx context.Context, id string) error // 级联删切片

	ReplaceChunks(ctx context.Context, chunks []Chunk) error // 重切先清旧
	ListChunks(ctx context.Context, docID string) ([]Chunk, error)
	// VectorsOfKB 返回库内全部已向量化切片（检索路径；专用向量库
	// 注入后本方法退出检索热路径）。
	VectorsOfKB(ctx context.Context, kbID string) ([]Chunk, error)
}

// FileResolver 抽象“引用 file 模块文件”的读取通道（serve 装配处
// 用 file.Storage 适配，file 包零改动）。
type FileResolver interface {
	ResolveFile(ctx context.Context, fileID string) (name string, r io.ReadCloser, err error)
}

// ---------- 内存实现（测试/演示零依赖路径） ----------

type MemStore struct {
	mu   sync.RWMutex
	kbs  map[string]KnowledgeBase
	docs map[string]Document
	ck   map[string][]Chunk // docID -> chunks
}

func NewMemStore() *MemStore {
	return &MemStore{kbs: map[string]KnowledgeBase{}, docs: map[string]Document{}, ck: map[string][]Chunk{}}
}

func (s *MemStore) CreateKB(_ context.Context, kb KnowledgeBase) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.kbs[kb.ID]; dup {
		return ErrNotFound
	}
	s.kbs[kb.ID] = kb
	return nil
}

func (s *MemStore) GetKB(_ context.Context, id string) (KnowledgeBase, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	kb, ok := s.kbs[id]
	if !ok {
		return KnowledgeBase{}, ErrNotFound
	}
	return kb, nil
}

func (s *MemStore) ListKBs(_ context.Context) ([]KnowledgeBase, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]KnowledgeBase, 0, len(s.kbs))
	for _, kb := range s.kbs {
		out = append(out, kb)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *MemStore) UpdateKB(_ context.Context, kb KnowledgeBase) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.kbs[kb.ID]; !ok {
		return ErrNotFound
	}
	s.kbs[kb.ID] = kb
	return nil
}

func (s *MemStore) DeleteKB(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.kbs[id]; !ok {
		return ErrNotFound
	}
	delete(s.kbs, id)
	for did, d := range s.docs {
		if d.KBID == id {
			delete(s.docs, did)
			delete(s.ck, did)
		}
	}
	return nil
}

func (s *MemStore) CreateDoc(_ context.Context, d Document) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.docs[d.ID]; dup {
		return ErrNotFound
	}
	s.docs[d.ID] = d
	return nil
}

func (s *MemStore) GetDoc(_ context.Context, id string) (Document, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.docs[id]
	if !ok {
		return Document{}, ErrNotFound
	}
	return d, nil
}

func (s *MemStore) ListDocs(_ context.Context, kbID string) ([]Document, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Document
	for _, d := range s.docs {
		if d.KBID == kbID {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *MemStore) UpdateDoc(_ context.Context, d Document) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.docs[d.ID]; !ok {
		return ErrNotFound
	}
	s.docs[d.ID] = d
	return nil
}

func (s *MemStore) DeleteDoc(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.docs[id]; !ok {
		return ErrNotFound
	}
	delete(s.docs, id)
	delete(s.ck, id)
	return nil
}

func (s *MemStore) ReplaceChunks(_ context.Context, chunks []Chunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(chunks) == 0 {
		return nil
	}
	did := chunks[0].DocID
	for _, c := range chunks {
		if c.DocID != did {
			return errors.New("rag: replace chunks spans multiple documents")
		}
	}
	s.ck[did] = chunks
	return nil
}

func (s *MemStore) ListChunks(_ context.Context, docID string) ([]Chunk, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Chunk, len(s.ck[docID]))
	copy(out, s.ck[docID])
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

func (s *MemStore) VectorsOfKB(_ context.Context, kbID string) ([]Chunk, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Chunk
	for _, chunks := range s.ck {
		for _, c := range chunks {
			if c.KBID == kbID && c.Embedding != nil {
				out = append(out, c)
			}
		}
	}
	return out, nil
}

// ---------- 相似度 ----------

// CosineSimilarity 余弦相似度；零向量约定为 0（未向量化切片不参与
// 排序，维度不一致同样返回 0——跨模型向量混库属配置错误，宁可无
// 召回不可错召回）。
func CosineSimilarity(a, b Vector) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// ---------- 检索缝：向量检索器 ----------

// VectorSearcher 收敛检索热路径，是专用向量库（milvus/pgvector 等）
// 的注入缝：起步实现 cosineSearcher（向量随行读出后内存余弦排序），
// 外部向量库就绪后经 SetSearcher 整体替换，模块层与 API 层零改动。
type VectorSearcher interface {
	Search(ctx context.Context, store Store, kbID string, query Vector, topK int) ([]Hit, error)
}

// cosineSearcher 是起步检索实现：已向量化切片全量载入 → 余弦排序
// → 截 topK（规模上限随专用向量库注入解除）。
type cosineSearcher struct{}

func (cosineSearcher) Search(ctx context.Context, store Store, kbID string, query Vector, topK int) ([]Hit, error) {
	chunks, err := store.VectorsOfKB(ctx, kbID)
	if err != nil {
		return nil, err
	}
	docNames := map[string]string{}
	for _, c := range chunks {
		if _, ok := docNames[c.DocID]; !ok {
			if d, err := store.GetDoc(ctx, c.DocID); err == nil {
				docNames[d.ID] = d.Name
			}
		}
	}
	hits := make([]Hit, 0, len(chunks))
	for _, c := range chunks {
		if s := CosineSimilarity(query, c.Embedding); s > 0 {
			hits = append(hits, Hit{
				ChunkID: c.ID, DocID: c.DocID, DocName: docNames[c.DocID],
				Seq: c.Seq, Content: c.Content, Score: s,
			})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].ChunkID < hits[j].ChunkID // 稳定并列
	})
	if len(hits) > topK {
		hits = hits[:topK]
	}
	return hits, nil
}

// ---------- 模块 ----------

type Module struct {
	store    Store
	files    FileResolver
	emb      EmbeddingClient
	searcher VectorSearcher // 检索缝：默认内存余弦，可注入专用向量库
	facade   *identity.Facade
	recorder audit.Recorder
}

// New 纯内存装配（files/emb 可为 nil，对应能力在请求期明确报错）。
func New(files FileResolver, emb EmbeddingClient) *Module {
	return &Module{store: NewMemStore(), files: files, emb: emb, searcher: cosineSearcher{}}
}

// NewWithStore 注入持久化实现（GORM 落盘、重启恢复）。
func NewWithStore(store Store, files FileResolver, emb EmbeddingClient) *Module {
	return &Module{store: store, files: files, emb: emb, searcher: cosineSearcher{}}
}

// SetEmbeddingClient 运行期注入/替换向量化客户端（装配缝：测试
// stub 与专用向量服务都走这里或构造参数，二者等效）。
func (m *Module) SetEmbeddingClient(emb EmbeddingClient) { m.emb = emb }

// SetSearcher 注入检索实现（专用向量库接入缝）。
func (m *Module) SetSearcher(s VectorSearcher) { m.searcher = s }

func (*Module) Name() string           { return "rag" }
func (*Module) Version() string        { return Version }
func (*Module) Dependencies() []string { return []string{"identity", "audit"} }

func (*Module) Describe() app.ModuleInfo {
	return app.ModuleInfo{Name: "rag", Version: Version, Permissions: []string{
		PermKBManage, PermDocManage, PermRead,
	}}
}

func (m *Module) Init(_ context.Context, a *app.App) error {
	idm, ok := a.Registry().MustGet("identity").(*identity.Module)
	if !ok {
		return errors.New("rag: identity module not available")
	}
	m.facade = idm.Facade()
	if adm, ok := a.Registry().MustGet("audit").(*audit.Module); ok {
		m.recorder = adm.Recorder()
	}

	auth := m.facade.Auth
	req := m.facade.Require
	g := a.Router().Group("/api/v1/rag")

	// 知识库
	g.Handle(http.MethodPost, "/kbs", auth(req(PermKBManage, m.createKB)))
	g.Handle(http.MethodGet, "/kbs", auth(req(PermRead, m.listKBs)))
	g.Handle(http.MethodGet, "/kbs/{id}", auth(req(PermRead, m.getKB)))
	g.Handle(http.MethodPatch, "/kbs/{id}", auth(req(PermKBManage, m.updateKB)))
	g.Handle(http.MethodDelete, "/kbs/{id}", auth(req(PermKBManage, m.deleteKB)))
	// 文档
	g.Handle(http.MethodPost, "/kbs/{id}/documents", auth(req(PermDocManage, m.registerDoc)))
	g.Handle(http.MethodGet, "/kbs/{id}/documents", auth(req(PermRead, m.listDocs)))
	g.Handle(http.MethodGet, "/documents/{id}", auth(req(PermRead, m.getDoc)))
	g.Handle(http.MethodDelete, "/documents/{id}", auth(req(PermDocManage, m.deleteDoc)))
	// 管道动作
	g.Handle(http.MethodPost, "/documents/{id}/chunk", auth(req(PermDocManage, m.chunkDoc)))
	g.Handle(http.MethodPost, "/documents/{id}/embed", auth(req(PermDocManage, m.embedDoc)))
	// 检索
	g.Handle(http.MethodPost, "/kbs/{id}/search", auth(req(PermRead, m.search)))
	return nil
}

func (*Module) Start(context.Context) error { return nil }
func (*Module) Stop(context.Context) error  { return nil }

// Store 供装配层与后续 chat-RAG 集成读取。
func (m *Module) Store() Store { return m.store }

// ---------- 知识库 handler ----------

func (m *Module) createKB(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string        `json:"name"`
		Description string        `json:"description"`
		Strategy    ChunkStrategy `json:"strategy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return
	}
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		web.Err(w, http.StatusBadRequest, ErrEmptyKB.Error())
		return
	}
	strategy, err := req.Strategy.Normalize()
	if err != nil {
		web.Err(w, http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now().UTC()
	kb := KnowledgeBase{
		ID: newID(), Name: strings.TrimSpace(req.Name), Description: req.Description,
		Strategy: strategy, OwnerID: u.ID, CreatedAt: now, UpdatedAt: now,
	}
	if err := m.store.CreateKB(r.Context(), kb); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	m.record(r.Context(), u.ID, "kb.created", kb.ID, kb.Name)
	web.Created(w, kb)
}

func (m *Module) listKBs(w http.ResponseWriter, r *http.Request) {
	kbs, err := m.store.ListKBs(r.Context())
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, map[string]any{"count": len(kbs), "items": kbs})
}

func (m *Module) getKB(w http.ResponseWriter, r *http.Request) {
	kb, err := m.store.GetKB(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			web.Err(w, http.StatusNotFound, "knowledge base not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	docs, err := m.store.ListDocs(r.Context(), kb.ID)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, map[string]any{"kb": kb, "document_count": len(docs)})
}

func (m *Module) updateKB(w http.ResponseWriter, r *http.Request) {
	kb, err := m.store.GetKB(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			web.Err(w, http.StatusNotFound, "knowledge base not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	var req struct {
		Name        *string        `json:"name"`
		Description *string        `json:"description"`
		Strategy    *ChunkStrategy `json:"strategy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if req.Name != nil {
		if strings.TrimSpace(*req.Name) == "" {
			web.Err(w, http.StatusBadRequest, ErrEmptyKB.Error())
			return
		}
		kb.Name = strings.TrimSpace(*req.Name)
	}
	if req.Description != nil {
		kb.Description = *req.Description
	}
	if req.Strategy != nil {
		s, err := req.Strategy.Normalize()
		if err != nil {
			web.Err(w, http.StatusBadRequest, err.Error())
			return
		}
		kb.Strategy = s
	}
	kb.UpdatedAt = time.Now().UTC()
	if err := m.store.UpdateKB(r.Context(), kb); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, kb)
}

func (m *Module) deleteKB(w http.ResponseWriter, r *http.Request) {
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := m.store.DeleteKB(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, ErrNotFound) {
			web.Err(w, http.StatusNotFound, "knowledge base not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	m.record(r.Context(), u.ID, "kb.deleted", r.PathValue("id"), "")
	web.OK(w, nil)
}

// ---------- 文档 handler ----------

func (m *Module) registerDoc(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Text   string `json:"text"`
		FileID string `json:"file_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return
	}
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	kb, err := m.store.GetKB(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			web.Err(w, http.StatusNotFound, "knowledge base not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}

	doc := Document{
		ID: newID(), KBID: kb.ID, Status: StatusRegistered,
		CreatorID: u.ID, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	switch {
	case req.FileID != "":
		if m.files == nil {
			web.Err(w, http.StatusServiceUnavailable, ErrFileUnavail.Error())
			return
		}
		name, rc, err := m.files.ResolveFile(r.Context(), req.FileID)
		if err != nil {
			if errors.Is(err, ErrFileNotFound) {
				web.Err(w, http.StatusNotFound, "source file not found")
				return
			}
			web.Err(w, http.StatusBadGateway, "source file read failed")
			return
		}
		defer rc.Close()
		b, err := io.ReadAll(io.LimitReader(rc, maxSourceBytes))
		if err != nil {
			web.Err(w, http.StatusBadGateway, "source file read failed")
			return
		}
		doc.SourceType = "file"
		doc.FileID = req.FileID
		doc.Name = req.Name
		if doc.Name == "" {
			doc.Name = name
		}
		doc.Content = string(b)
	case req.Text != "":
		doc.SourceType = "text"
		doc.Name = req.Name
		doc.Content = req.Text
	default:
		web.Err(w, http.StatusBadRequest, ErrNoText.Error())
		return
	}
	if strings.TrimSpace(doc.Name) == "" {
		web.Err(w, http.StatusBadRequest, "document name required")
		return
	}
	if strings.TrimSpace(doc.Content) == "" {
		web.Err(w, http.StatusBadRequest, "document content is empty")
		return
	}
	if err := m.store.CreateDoc(r.Context(), doc); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	m.record(r.Context(), u.ID, "doc.registered", doc.ID, doc.Name)
	web.Created(w, doc)
}

func (m *Module) listDocs(w http.ResponseWriter, r *http.Request) {
	if _, err := m.store.GetKB(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, ErrNotFound) {
			web.Err(w, http.StatusNotFound, "knowledge base not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	docs, err := m.store.ListDocs(r.Context(), r.PathValue("id"))
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, map[string]any{"count": len(docs), "items": docs})
}

func (m *Module) getDoc(w http.ResponseWriter, r *http.Request) {
	doc, err := m.store.GetDoc(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			web.Err(w, http.StatusNotFound, "document not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	chunks, err := m.store.ListChunks(r.Context(), doc.ID)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	type chunkView struct {
		ChunkID  string `json:"chunk_id"`
		Seq      int    `json:"seq"`
		Content  string `json:"content"`
		Embedded bool   `json:"embedded"`
	}
	views := make([]chunkView, 0, len(chunks))
	for _, c := range chunks {
		views = append(views, chunkView{ChunkID: c.ID, Seq: c.Seq, Content: c.Content, Embedded: c.Embedding != nil})
	}
	web.OK(w, map[string]any{"document": doc, "chunks": views})
}

func (m *Module) deleteDoc(w http.ResponseWriter, r *http.Request) {
	u, err := m.facade.User(r)
	if err != nil {
		web.Err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := m.store.DeleteDoc(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, ErrNotFound) {
			web.Err(w, http.StatusNotFound, "document not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	m.record(r.Context(), u.ID, "doc.deleted", r.PathValue("id"), "")
	web.OK(w, nil)
}

// ---------- 管道 handler ----------

// maxSourceBytes 文档原文上限（与 file 模块上传上限 32MB 对齐）。
const maxSourceBytes = 32 << 20

// chunkDoc 切片：按所属知识库的策略把原文切成 Chunk（重切替换旧片）。
func (m *Module) chunkDoc(w http.ResponseWriter, r *http.Request) {
	doc, err := m.store.GetDoc(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			web.Err(w, http.StatusNotFound, "document not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	kb, err := m.store.GetKB(r.Context(), doc.KBID)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	parts := SplitText(doc.Content, kb.Strategy)
	if len(parts) == 0 {
		web.Err(w, http.StatusBadRequest, "document content is empty")
		return
	}
	now := time.Now().UTC()
	chunks := make([]Chunk, len(parts))
	for i, p := range parts {
		chunks[i] = Chunk{ID: newID(), KBID: doc.KBID, DocID: doc.ID, Seq: i, Content: p, CreatedAt: now}
	}
	if err := m.store.ReplaceChunks(r.Context(), chunks); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	doc.Status = StatusChunked
	doc.ChunkCount = len(chunks)
	doc.UpdatedAt = now
	if err := m.store.UpdateDoc(r.Context(), doc); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	u, _ := m.facade.User(r)
	m.record(r.Context(), u.ID, "doc.chunked", doc.ID, fmt.Sprintf("%d chunks", len(chunks)))
	web.OK(w, map[string]any{"document": doc, "chunk_count": len(chunks)})
}

// embedDoc 向量化：把该文档全部切片批量送 EmbeddingClient，向量
// 随 Chunk 落库。无 provider 配置明确报错（503），不静默跳过。
func (m *Module) embedDoc(w http.ResponseWriter, r *http.Request) {
	doc, err := m.store.GetDoc(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			web.Err(w, http.StatusNotFound, "document not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	if doc.Status == StatusEmbedded {
		web.Err(w, http.StatusConflict, ErrAlreadyDone.Error())
		return
	}
	if doc.Status != StatusChunked {
		web.Err(w, http.StatusBadRequest, ErrNotChunked.Error())
		return
	}
	if m.emb == nil {
		web.Err(w, http.StatusServiceUnavailable, ErrEmbedNotConfigured.Error())
		return
	}
	chunks, err := m.store.ListChunks(r.Context(), doc.ID)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	if len(chunks) == 0 {
		web.Err(w, http.StatusBadRequest, ErrNotChunked.Error())
		return
	}
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Content
	}
	vecs, err := m.emb.Embed(r.Context(), texts)
	if err != nil {
		if errors.Is(err, ErrEmbedNotConfigured) {
			web.Err(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		if errors.Is(err, ErrEmbedBadUpstream) {
			web.Err(w, http.StatusBadGateway, err.Error())
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	if len(vecs) != len(chunks) {
		web.Err(w, http.StatusInternalServerError, "embedding count mismatch")
		return
	}
	now := time.Now().UTC()
	for i := range chunks {
		chunks[i].Embedding = Vector(vecs[i])
	}
	if err := m.store.ReplaceChunks(r.Context(), chunks); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	doc.Status = StatusEmbedded
	doc.EmbedModel = m.emb.ModelName()
	doc.Dim = len(vecs[0])
	doc.UpdatedAt = now
	if err := m.store.UpdateDoc(r.Context(), doc); err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	u, _ := m.facade.User(r)
	m.record(r.Context(), u.ID, "doc.embedded", doc.ID, doc.EmbedModel)
	web.OK(w, doc)
}

// ---------- 检索 handler ----------

// maxTopK / 默认 top-k。
const (
	defaultTopK = 5
	maxTopK     = 100
)

func (m *Module) search(w http.ResponseWriter, r *http.Request) {
	kbID := r.PathValue("id")
	if _, err := m.store.GetKB(r.Context(), kbID); err != nil {
		if errors.Is(err, ErrNotFound) {
			web.Err(w, http.StatusNotFound, "knowledge base not found")
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	var req struct {
		Query string `json:"query"`
		TopK  int    `json:"top_k"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		web.Err(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		web.Err(w, http.StatusBadRequest, ErrEmptyQuery.Error())
		return
	}
	topK := req.TopK
	if topK == 0 {
		topK = defaultTopK
	}
	if topK < 1 || topK > maxTopK {
		web.Err(w, http.StatusBadRequest, ErrBadTopK.Error())
		return
	}
	if m.emb == nil {
		web.Err(w, http.StatusServiceUnavailable, ErrEmbedNotConfigured.Error())
		return
	}
	qv, err := m.emb.Embed(r.Context(), []string{req.Query})
	if err != nil {
		if errors.Is(err, ErrEmbedNotConfigured) {
			web.Err(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		if errors.Is(err, ErrEmbedBadUpstream) {
			web.Err(w, http.StatusBadGateway, err.Error())
			return
		}
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	hits, err := m.Search(r.Context(), kbID, Vector(qv[0]), topK)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	if hits == nil {
		hits = []Hit{}
	}
	web.OK(w, map[string]any{"count": len(hits), "hits": hits})
}

// Search 检索入口：委托 searcher（默认内存余弦起步实现，专用向量
// 库经 SetSearcher 注入后此方法签名与调用方零改动）。
func (m *Module) Search(ctx context.Context, kbID string, query Vector, topK int) ([]Hit, error) {
	if m.searcher == nil {
		m.searcher = cosineSearcher{}
	}
	return m.searcher.Search(ctx, m.store, kbID, query, topK)
}

// SearchText 文本检索入口（内部向量化后走 Search）：供 agent 检索
// 工具等模块内调用方使用——它们只持文本，不感知 embedding 客户端。
// embedding 未配置时返回 ErrEmbedNotConfigured。
func (m *Module) SearchText(ctx context.Context, kbID, query string, topK int) ([]Hit, error) {
	if m.emb == nil {
		return nil, ErrEmbedNotConfigured
	}
	if _, err := m.store.GetKB(ctx, kbID); err != nil {
		return nil, err
	}
	qv, err := m.emb.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	if len(qv) == 0 {
		return nil, errors.New("rag: empty embedding for query")
	}
	return m.Search(ctx, kbID, Vector(qv[0]), topK)
}

// Embedder 暴露向量化客户端装配状态（agent 等调用方预检用）。
func (m *Module) Embedder() EmbeddingClient { return m.emb }

// ---------- 工具 ----------

// modRAG 审计模块标识（audit.ModXxx 常量表由各模块自治维护，避免
// 并行迭代期共享文件冲突）。
const modRAG = "rag"

func (m *Module) record(ctx context.Context, actor, event, resource, detail string) {
	if m.recorder == nil {
		return
	}
	detailJSON, _ := json.Marshal(map[string]string{"resource": resource, "detail": detail})
	_ = m.recorder.Record(ctx, audit.Event{
		Module: modRAG, Event: event, ActorID: actor,
		Resource: resource, Detail: detailJSON, Time: time.Now().UTC(),
	})
}

func newID() string { return randutil.Hex16() }

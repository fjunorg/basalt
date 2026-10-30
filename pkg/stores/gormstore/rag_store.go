package gormstore

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/basalt-go/basalt/pkg/modules/rag"
)

// RagStore 实现 rag.Store（GORM，任意方言）。
//
// embedding 以 blob 列随 Chunk 行落库：重启后 VectorsOfKB 直接从
// 行内 blob 还原向量参与检索（内存余弦），无需外部索引重建——
// 这是"sqlite blob + 内存余弦"起步方案的持久化半场。
type RagStore struct{ db *gorm.DB }

// 编译期接口对齐检查。
var _ rag.Store = (*RagStore)(nil)

func (s *RagStore) CreateKB(_ context.Context, kb rag.KnowledgeBase) error {
	return s.db.Create(&kb).Error
}

func (s *RagStore) GetKB(_ context.Context, id string) (rag.KnowledgeBase, error) {
	var kb rag.KnowledgeBase
	err := s.db.First(&kb, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return rag.KnowledgeBase{}, rag.ErrNotFound
	}
	return kb, err
}

func (s *RagStore) ListKBs(_ context.Context) ([]rag.KnowledgeBase, error) {
	var out []rag.KnowledgeBase
	err := s.db.Order("created_at ASC").Find(&out).Error
	return out, err
}

func (s *RagStore) UpdateKB(_ context.Context, kb rag.KnowledgeBase) error {
	res := s.db.Model(&rag.KnowledgeBase{}).Where("id = ?", kb.ID).Updates(map[string]any{
		"name":        kb.Name,
		"description": kb.Description,
		"strategy":    kb.Strategy,
		"updated_at":  kb.UpdatedAt,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return rag.ErrNotFound
	}
	return nil
}

// DeleteKB 级联删除库内文档与切片（库-文档-切片为强从属树）。
func (s *RagStore) DeleteKB(_ context.Context, id string) error {
	res := s.db.Where("id = ?", id).Delete(&rag.KnowledgeBase{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return rag.ErrNotFound
	}
	if err := s.db.Where("kb_id = ?", id).Delete(&rag.Document{}).Error; err != nil {
		return err
	}
	return s.db.Where("kb_id = ?", id).Delete(&rag.Chunk{}).Error
}

func (s *RagStore) CreateDoc(_ context.Context, d rag.Document) error {
	return s.db.Create(&d).Error
}

func (s *RagStore) GetDoc(_ context.Context, id string) (rag.Document, error) {
	var d rag.Document
	err := s.db.First(&d, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return rag.Document{}, rag.ErrNotFound
	}
	return d, err
}

func (s *RagStore) ListDocs(_ context.Context, kbID string) ([]rag.Document, error) {
	var out []rag.Document
	err := s.db.Where("kb_id = ?", kbID).Order("created_at ASC").Find(&out).Error
	return out, err
}

func (s *RagStore) UpdateDoc(_ context.Context, d rag.Document) error {
	res := s.db.Model(&rag.Document{}).Where("id = ?", d.ID).Updates(map[string]any{
		"status":      d.Status,
		"chunk_count": d.ChunkCount,
		"embed_model": d.EmbedModel,
		"dim":         d.Dim,
		"updated_at":  d.UpdatedAt,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return rag.ErrNotFound
	}
	return nil
}

// DeleteDoc 级联删除文档切片。
func (s *RagStore) DeleteDoc(_ context.Context, id string) error {
	res := s.db.Where("id = ?", id).Delete(&rag.Document{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return rag.ErrNotFound
	}
	return s.db.Where("doc_id = ?", id).Delete(&rag.Chunk{}).Error
}

// ReplaceChunks 重切替换：单事务内先清该文档旧切片再写新切片。
// 向量化复用同一路径（读出-改向量-整体回写），语义单一。
func (s *RagStore) ReplaceChunks(ctx context.Context, chunks []rag.Chunk) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(chunks) == 0 {
			return nil
		}
		did := chunks[0].DocID
		for _, c := range chunks {
			if c.DocID != did {
				return errors.New("rag: replace chunks spans multiple documents")
			}
		}
		if err := tx.Where("doc_id = ?", did).Delete(&rag.Chunk{}).Error; err != nil {
			return err
		}
		return tx.Create(&chunks).Error
	})
}

func (s *RagStore) ListChunks(_ context.Context, docID string) ([]rag.Chunk, error) {
	var out []rag.Chunk
	err := s.db.Where("doc_id = ?", docID).Order("seq ASC").Find(&out).Error
	return out, err
}

// VectorsOfKB 只取已向量化切片（embedding blob 非空）。
func (s *RagStore) VectorsOfKB(_ context.Context, kbID string) ([]rag.Chunk, error) {
	var out []rag.Chunk
	err := s.db.Where("kb_id = ? AND embedding IS NOT NULL", kbID).Find(&out).Error
	return out, err
}

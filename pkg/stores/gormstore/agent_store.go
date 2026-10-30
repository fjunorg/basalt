package gormstore

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/basalt-go/basalt/pkg/modules/agent"
)

// AgentStore 实现 agent.Store（GORM，任意方言）。
//
// 定义/运行/消息三表落盘：重启后定义与历史对话完整恢复
// （运行中进程的重启不续跑——运行态随进程，记录态随存储）。
type AgentStore struct{ db *gorm.DB }

// 编译期接口对齐检查。
var _ agent.Store = (*AgentStore)(nil)

func (s *AgentStore) CreateDef(_ context.Context, d agent.Definition) error {
	return s.db.Create(&d).Error
}

func (s *AgentStore) GetDef(_ context.Context, id string) (agent.Definition, error) {
	var d agent.Definition
	err := s.db.First(&d, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return agent.Definition{}, agent.ErrNotFound
	}
	return d, err
}

func (s *AgentStore) ListDefs(_ context.Context) ([]agent.Definition, error) {
	var out []agent.Definition
	err := s.db.Order("created_at ASC").Find(&out).Error
	return out, err
}

func (s *AgentStore) UpdateDef(_ context.Context, d agent.Definition) error {
	res := s.db.Model(&agent.Definition{}).Where("id = ?", d.ID).Updates(map[string]any{
		"name":            d.Name,
		"description":     d.Description,
		"system_prompt":   d.SystemPrompt,
		"model":           d.Model,
		"tools":           d.Tools,
		"temperature":     d.Temperature,
		"max_tool_rounds": d.MaxToolRounds,
		"status":          d.Status,
		"updated_at":      d.UpdatedAt,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return agent.ErrNotFound
	}
	return nil
}

// DeleteDef 级联删除该定义的运行记录与消息历史（定义是强属主）。
func (s *AgentStore) DeleteDef(_ context.Context, id string) error {
	res := s.db.Where("id = ?", id).Delete(&agent.Definition{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return agent.ErrNotFound
	}
	if err := s.db.Where("agent_id = ?", id).Delete(&agent.Run{}).Error; err != nil {
		return err
	}
	return s.db.Where("run_id IN (SELECT id FROM agent_runs WHERE agent_id = ?)", id).
		Delete(&agent.RunMessage{}).Error
}

func (s *AgentStore) CreateRun(_ context.Context, r agent.Run) error {
	return s.db.Create(&r).Error
}

func (s *AgentStore) GetRun(_ context.Context, id string) (agent.Run, error) {
	var r agent.Run
	err := s.db.First(&r, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return agent.Run{}, agent.ErrNotFound
	}
	return r, err
}

func (s *AgentStore) UpdateRun(_ context.Context, r agent.Run) error {
	res := s.db.Model(&agent.Run{}).Where("id = ?", r.ID).Updates(map[string]any{
		"status":       r.Status,
		"output":       r.Output,
		"rounds":       r.Rounds,
		"total_tokens": r.TotalTokens,
		"error":        r.Error,
		"finished_at":  r.FinishedAt,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return agent.ErrNotFound
	}
	return nil
}

// ListRuns 按 started_at 倒序（最新在前，契约 §3.2）。
func (s *AgentStore) ListRuns(_ context.Context, agentID string) ([]agent.Run, error) {
	var out []agent.Run
	err := s.db.Where("agent_id = ?", agentID).Order("started_at DESC").Find(&out).Error
	return out, err
}

func (s *AgentStore) AppendMsg(_ context.Context, msg agent.RunMessage) error {
	return s.db.Create(&msg).Error
}

// ListMsgs 按 seq 升序（对话时序）。
func (s *AgentStore) ListMsgs(_ context.Context, runID string) ([]agent.RunMessage, error) {
	var out []agent.RunMessage
	err := s.db.Where("run_id = ?", runID).Order("seq ASC").Find(&out).Error
	return out, err
}

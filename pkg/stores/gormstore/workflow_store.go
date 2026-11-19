package gormstore

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/basalt-go/basalt/pkg/modules/workflow"
)

// WorkflowStore 实现 workflow.Store（GORM，任意方言）。
//
// 状态机（会签/或签推进、终态判定、acted 去重）复用 workflow 包
// 导出的 ApplyAction/ApplyDelegate 纯函数——与 MemStore 单一事实
// 来源；GORM 侧仅负责事务内的读-算-写与持久化。
//
// 重启恢复语义：实例行（含展开步骤与 StepIndex）与动作历史全部
// 落盘，重启后在途实例从任意中间态（会签部分通过、多步中间步）
// 继续推进；待办列表（ListTodo）由 status+当前步审批人+动作历史
// 现算恢复；已完结实例可查（ListAll/Get）。
type WorkflowStore struct{ db *gorm.DB }

// 编译期接口对齐检查。
var _ workflow.Store = (*WorkflowStore)(nil)

func (s *WorkflowStore) Create(ctx context.Context, in workflow.Instance) error {
	if err := workflow.PrepareInstance(&in); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Create(&in).Error
}

func (s *WorkflowStore) Get(ctx context.Context, id string) (workflow.Instance, []workflow.Action, error) {
	var in workflow.Instance
	if err := s.db.WithContext(ctx).First(&in, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return workflow.Instance{}, nil, workflow.ErrNotFound
		}
		return workflow.Instance{}, nil, err
	}
	var actions []workflow.Action
	if err := s.db.WithContext(ctx).Where("instance_id = ?", id).Order("seq").Find(&actions).Error; err != nil {
		return workflow.Instance{}, nil, err
	}
	if len(actions) == 0 {
		actions = nil // 与 MemStore 的空历史表示一致
	}
	return in, actions, nil
}

// Act 在单个事务内执行读-算-写：实例行与动作历史快照经共享
// 状态机推进后整体写回，动作以自增 Seq 追加（顺序即历史顺序）。
func (s *WorkflowStore) Act(ctx context.Context, instanceID, actorID, act, comment string) (workflow.Instance, error) {
	return s.ActBranch(ctx, instanceID, actorID, act, comment, -1)
}

// ActBranch 在指定并行分支上执行动作（branch<0 表示主流程步骤）。
func (s *WorkflowStore) ActBranch(ctx context.Context, instanceID, actorID, act, comment string, branch int) (workflow.Instance, error) {
	var out workflow.Instance
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var in workflow.Instance
		if err := tx.First(&in, "id = ?", instanceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return workflow.ErrNotFound
			}
			return err
		}
		actions, err := loadActions(tx, instanceID)
		if err != nil {
			return err
		}
		next, action, err := workflow.ApplyActionBranch(in, actions, actorID, act, comment, branch)
		if err != nil {
			return err
		}
		action.InstanceID = instanceID
		if err := saveInstance(tx, next); err != nil {
			return err
		}
		if err := tx.Create(&action).Error; err != nil {
			return err
		}
		out = next
		return nil
	})
	if err != nil {
		return workflow.Instance{}, err
	}
	return out, nil
}

// Delegate 同 Act：事务内读-算-写（转办重写当前步审批人名单）。
func (s *WorkflowStore) Delegate(ctx context.Context, instanceID, fromUserID, toUserID string) (workflow.Instance, error) {
	var out workflow.Instance
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var in workflow.Instance
		if err := tx.First(&in, "id = ?", instanceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return workflow.ErrNotFound
			}
			return err
		}
		next, action, err := workflow.ApplyDelegate(in, fromUserID, toUserID)
		if err != nil {
			return err
		}
		action.InstanceID = instanceID
		if err := saveInstance(tx, next); err != nil {
			return err
		}
		if err := tx.Create(&action).Error; err != nil {
			return err
		}
		out = next
		return nil
	})
	if err != nil {
		return workflow.Instance{}, err
	}
	return out, nil
}

// ListTodo 现算待办：pending 实例经当前步审批人过滤后，按动作
// 历史排除已批用户（审批人名单在 Steps json 列内，SQL 不下推，
// pending 集合量级可控——与 MemStore 全扫描语义等价）。
func (s *WorkflowStore) ListTodo(ctx context.Context, userID string) ([]workflow.Instance, error) {
	var pend []workflow.Instance
	if err := s.db.WithContext(ctx).Where("status = ?", workflow.StatusPending).
		Order("created_at DESC").Find(&pend).Error; err != nil {
		return nil, err
	}
	var out []workflow.Instance
	for i := range pend {
		in := pend[i]
		actions, err := loadActions(s.db, in.ID)
		if err != nil {
			return nil, err
		}
		if todo, _ := in.TodoFor(userID, actions); todo {
			out = append(out, in)
		}
	}
	return out, nil
}

func (s *WorkflowStore) ListByCreator(ctx context.Context, userID string) ([]workflow.Instance, error) {
	var out []workflow.Instance
	err := s.db.WithContext(ctx).Where("creator_id = ?", userID).
		Order("created_at DESC").Find(&out).Error
	return list(out), err
}

func (s *WorkflowStore) ListAll(ctx context.Context) ([]workflow.Instance, error) {
	var out []workflow.Instance
	err := s.db.WithContext(ctx).Order("created_at DESC").Find(&out).Error
	return list(out), err
}

func loadActions(db *gorm.DB, instanceID string) ([]workflow.Action, error) {
	var actions []workflow.Action
	if err := db.Where("instance_id = ?", instanceID).Order("seq").Find(&actions).Error; err != nil {
		return nil, err
	}
	return actions, nil
}

// saveInstance 全量写回（Steps/Approvers/Branches/Vars 走
// serializer:json 的 struct 写入路径；StepIndex/Status/FinishedAt
// 为状态机推进结果）。
func saveInstance(db *gorm.DB, in workflow.Instance) error {
	return db.Model(&workflow.Instance{}).Where("id = ?", in.ID).
		Select("BusinessKey", "Summary", "DefinitionID", "Steps", "StepIndex",
			"CreatorID", "Status", "FinishedAt", "Approvers", "Mode",
			"Round", "CCUserIDs", "Delegations", "Notify", "Vars", "Branches").
		Updates(in).Error
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}


// ---------- 定义 ----------

// WorkflowDefStore 实现 workflow.DefStore（GORM，任意方言）。
type WorkflowDefStore struct{ db *gorm.DB }

// 编译期接口对齐检查。
var _ workflow.DefStore = (*WorkflowDefStore)(nil)

func (s *WorkflowDefStore) CreateDef(ctx context.Context, d workflow.Definition) error {
	steps, err := workflow.NormalizeSteps(d.Steps)
	if err != nil {
		return err
	}
	d.Steps = steps
	if d.Status == "" {
		d.Status = "active"
	}
	var cnt int64
	if err := s.db.WithContext(ctx).Model(&workflow.Definition{}).Where("`key` = ?", d.Key).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return workflow.ErrDefKeyUsed
	}
	return s.db.WithContext(ctx).Create(&d).Error
}

func (s *WorkflowDefStore) UpdateDef(ctx context.Context, d workflow.Definition) error {
	steps, err := workflow.NormalizeSteps(d.Steps)
	if err != nil {
		return err
	}
	d.Steps = steps
	var cnt int64
	if err := s.db.WithContext(ctx).Model(&workflow.Definition{}).Where("`key` = ? AND id <> ?", d.Key, d.ID).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return workflow.ErrDefKeyUsed
	}
	res := s.db.WithContext(ctx).Model(&workflow.Definition{}).Where("id = ?", d.ID).
		Select("Key", "Name", "Category", "Steps", "Status").
		Updates(d)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return workflow.ErrDefNotFound
	}
	return nil
}

func (s *WorkflowDefStore) DeleteDef(ctx context.Context, id string) error {
	res := s.db.WithContext(ctx).Delete(&workflow.Definition{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return workflow.ErrDefNotFound
	}
	return nil
}

func (s *WorkflowDefStore) GetDef(ctx context.Context, id string) (workflow.Definition, error) {
	var d workflow.Definition
	err := s.db.WithContext(ctx).First(&d, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return workflow.Definition{}, workflow.ErrDefNotFound
	}
	return d, err
}

func (s *WorkflowDefStore) GetDefByKey(ctx context.Context, key string) (workflow.Definition, error) {
	var d workflow.Definition
	err := s.db.WithContext(ctx).First(&d, "`key` = ?", key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return workflow.Definition{}, workflow.ErrDefNotFound
	}
	return d, err
}

func (s *WorkflowDefStore) ListDefs(ctx context.Context) ([]workflow.Definition, error) {
	var out []workflow.Definition
	err := s.db.WithContext(ctx).Order("`key`").Find(&out).Error
	return list(out), err
}

// ---------- 六项操作扩展（事务内读-纯函数-写，与 MemStore 语义一致） ----------

// wfApply 通用事务模板（读实例-纯函数-写回+动作）。
func (s *WorkflowStore) wfApply(instanceID string, fn func(in workflow.Instance, actions []workflow.Action) (workflow.Instance, workflow.Action, error)) (workflow.Instance, error) {
	var out workflow.Instance
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var in workflow.Instance
		if err := tx.First(&in, "id = ?", instanceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return workflow.ErrNotFound
			}
			return err
		}
		actions, err := loadActions(tx, instanceID)
		if err != nil {
			return err
		}
		next, action, err := fn(in, actions)
		if err != nil {
			return err
		}
		action.InstanceID = instanceID
		if err := saveInstance(tx, next); err != nil {
			return err
		}
		if err := tx.Create(&action).Error; err != nil {
			return err
		}
		out = next
		return nil
	})
	if err != nil {
		return workflow.Instance{}, err
	}
	return out, nil
}

func (s *WorkflowStore) CC(ctx context.Context, instanceID, actorID string, userIDs []string) (workflow.Instance, error) {
	return s.wfApply(instanceID, func(in workflow.Instance, _ []workflow.Action) (workflow.Instance, workflow.Action, error) {
		return workflow.ApplyCC(in, actorID, userIDs)
	})
}

func (s *WorkflowStore) DelegateAct(ctx context.Context, instanceID, fromUserID, toUserID string) (workflow.Instance, error) {
	return s.wfApply(instanceID, func(in workflow.Instance, _ []workflow.Action) (workflow.Instance, workflow.Action, error) {
		return workflow.ApplyDelegateAct(in, fromUserID, toUserID)
	})
}

func (s *WorkflowStore) AddSign(ctx context.Context, instanceID, actorID, position string, approvers []string, mode string) (workflow.Instance, error) {
	return s.wfApply(instanceID, func(in workflow.Instance, _ []workflow.Action) (workflow.Instance, workflow.Action, error) {
		return workflow.ApplyAddSign(in, actorID, position, approvers, mode)
	})
}

func (s *WorkflowStore) RejectTo(ctx context.Context, instanceID, actorID string, targetStep int, comment string) (workflow.Instance, error) {
	return s.wfApply(instanceID, func(in workflow.Instance, actions []workflow.Action) (workflow.Instance, workflow.Action, error) {
		return workflow.ApplyRejectTo(in, actorID, targetStep, comment)
	})
}

// ListCC 被抄送列表（pending 与终态均可见）。
func (s *WorkflowStore) ListCC(ctx context.Context, userID string) ([]workflow.Instance, error) {
	var out []workflow.Instance
	// CCUserIDs 为 JSON 列，跨方言兼容用 LIKE 粗筛（"uid" 引号定界）
	err := s.db.WithContext(ctx).Where("\"cc_user_ids\" LIKE ?", "%\""+userID+"\"%").
		Order("created_at DESC").Find(&out).Error
	return list(out), err
}

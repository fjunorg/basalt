// 流转通知（v0.3）：审批流转各节点的通知钩子。
//
// Notifier 由 serve 装配层注入（包装 notify 模块的站内信/邮件/短信
// 通道），workflow 包不直接 import notify。零值（nil Notifier）
// 表示不启用通知，向后兼容。
package workflow

import "context"

// 通知事件类型。
const (
	NotifyStepEnter = "step_enter" // 进入某步 → 通知该步审批人
	NotifyApproved  = "approved"   // 终态通过 → 通知发起人 + 抄送人
	NotifyRejected  = "rejected"   // 终态驳回 → 通知发起人
	NotifyCanceled  = "canceled"   // 撤回 → 通知当前步审批人
	NotifyCC        = "cc"         // 抄送 → 通知被抄送人
)

// NotifyEvent 是一次流转通知的完整上下文。
type NotifyEvent struct {
	Type       string   // NotifyStepEnter 等
	InstanceID string   // 实例 ID
	Summary    string   // 实例摘要（通知标题用）
	BizKey     string   // 业务键
	StepName   string   // 当前步名（step_enter 用）
	StepIndex  int      // 当前步序号
	Recipients []string // 接收人用户 ID 列表
	CreatorID  string   // 发起人 ID
	ActorID    string   // 触发动作的人（审批人/撤回人）
	Comment    string   // 审批意见（驳回原因等）
}

// Notifier 是流转通知发送端，由装配层注入。
// 实现必须容错：通知失败不阻断流程流转。
type Notifier interface {
	Notify(ctx context.Context, ev NotifyEvent) error
}

// NotifySpec 是流程定义上的通知开关（哪些事件启用通知）。
// 零值全部关闭，向后兼容。
type NotifySpec struct {
	StepEnter bool `json:"step_enter"` // 进入某步时通知审批人
	Approved  bool `json:"approved"`   // 终态通过时通知发起人+抄送人
	Rejected  bool `json:"rejected"`   // 终态驳回时通知发起人
	Canceled  bool `json:"canceled"`   // 撤回时通知当前步审批人
	CC        bool `json:"cc"`         // 抄送时通知被抄送人
}

// AllNotify 全开（演示/快速启用）。
func AllNotify() NotifySpec {
	return NotifySpec{StepEnter: true, Approved: true, Rejected: true, Canceled: true, CC: true}
}

// shouldNotify 判断事件是否启用。
func (s NotifySpec) shouldNotify(eventType string) bool {
	switch eventType {
	case NotifyStepEnter:
		return s.StepEnter
	case NotifyApproved:
		return s.Approved
	case NotifyRejected:
		return s.Rejected
	case NotifyCanceled:
		return s.Canceled
	case NotifyCC:
		return s.CC
	}
	return false
}

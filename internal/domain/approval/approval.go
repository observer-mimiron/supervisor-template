// Package approval 定义副作用执行的人工审批合同。
//
// 本包只拥有审批状态转换，不调用 HTTP、模型、Tool 或持久化实现。
package approval

import (
	"errors"
	"time"
)

// Status 是审批请求的生命周期状态。
type Status string

const (
	Pending  Status = "pending"
	Approved Status = "approved"
	Rejected Status = "rejected"
	Expired  Status = "expired"
)

// Request 是一个副作用步骤的审批记录。
type Request struct {
	ApprovalID    string
	RunID         string
	StepID        string
	ActionSummary string
	Risk          string
	Status        Status
	Reviewer      string
	ReviewedAt    time.Time
}

// Decide 写入一次审批结果；重复提交只返回当前结果，不推进状态。
func (r *Request) Decide(status Status, reviewer string, reviewedAt time.Time) error {
	if r.Status != Pending {
		return nil
	}
	if status != Approved && status != Rejected && status != Expired {
		return errors.New("审批状态不合法")
	}
	if status != Expired && reviewer == "" {
		return errors.New("审批人不能为空")
	}
	r.Status = status
	r.Reviewer = reviewer
	r.ReviewedAt = reviewedAt
	return nil
}

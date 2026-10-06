// Package approval 定义副作用执行的人工审批合同。
//
// 本包只拥有审批状态转换，不调用 HTTP、模型、Tool 或持久化实现。
package approval

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"
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
//
// 审计需要能回答"谁批准了哪一次写入"，因此记录完整保留 RunID、StepID、ToolID、
// 动作指纹与摘要，以及决策人和决策时间（US2-4）。
type Request struct {
	ApprovalID string
	RunID      string
	StepID     string
	ToolID     string
	// ActionDigest 是被批准动作的指纹，由 ComputeActionDigest 计算。
	// 它把审批绑定到"这一步、这个 Tool、这组输入"；参数一旦变化，指纹随之变化，
	// 原批准不再匹配。空指纹表示无法绑定，调用方 MUST 按"未获批准"处理。
	ActionDigest  string
	ActionSummary string
	Risk          string
	Status        Status
	RequestedAt   time.Time
	Reviewer      string
	ReviewedAt    time.Time
}

// ComputeActionDigest 计算一个动作的规范化指纹，绑定 (stepID, toolID, input)。
//
// 这是纯函数、无 I/O，且只依赖标准库：判断"什么算同一个动作"是领域语义，
// 不是某个适配器的细节。
//
// 输入按键名排序后做**长度前缀**编码再哈希，因此 map 迭代顺序不影响结果，
// 且 {"a":"bc"} 与 {"ab":"c"} 不会碰撞。stepID 或 toolID 为空时返回空串——
// 调用方必须把空指纹视为"无法绑定"，不得用它匹配任何已有批准（fail-closed）。
func ComputeActionDigest(stepID, toolID string, input map[string]string) string {
	if stepID == "" || toolID == "" {
		return ""
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	writeField := func(value string) {
		builder.WriteString(strconv.Itoa(len(value)))
		builder.WriteByte(':')
		builder.WriteString(value)
		builder.WriteByte('\n')
	}
	writeField(stepID)
	writeField(toolID)
	writeField(strconv.Itoa(len(keys)))
	for _, key := range keys {
		writeField(key)
		writeField(input[key])
	}
	digest := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(digest[:])
}

// BindsAction 判断本记录是否恰好授权给定的那个动作。
//
// 三个条件缺一不可：处于 approved、指纹非空且相等。空指纹（无法绑定）一律不匹配。
func (r Request) BindsAction(digest string) bool {
	return r.Status == Approved && r.ActionDigest != "" && r.ActionDigest == digest
}

// ExpiredBy 判断待审批记录是否超过了配置的等待上限。
//
// 未设置 RequestedAt 的记录（例如更早版本写入的快照）视为不过期，避免把历史数据
// 误判成超时。
func (r Request) ExpiredBy(now time.Time, timeout time.Duration) bool {
	return r.Status == Pending && timeout > 0 && !r.RequestedAt.IsZero() &&
		now.Sub(r.RequestedAt) >= timeout
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

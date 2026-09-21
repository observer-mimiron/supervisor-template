// Package persistence 提供 v1 的内存 Repository 适配器。
//
// 本文件只保存最小请求和计划状态，不能替代领域状态所有权或提供跨进程恢复。
package persistence

import (
	"errors"
	"sync"

	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	"github.com/observer-mimiron/supervisor-template/internal/domain/approval"
	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
)

// MemoryRepository 保存单进程内的请求和计划快照。
type MemoryRepository struct {
	mu        sync.RWMutex
	requests  map[string]conversation.ExecutionRequest
	approvals map[string]approval.Request
	plans     map[string]agent.ExecutionPlan
}

// NewMemoryRepository 创建空的内存 Repository。
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{requests: make(map[string]conversation.ExecutionRequest), approvals: make(map[string]approval.Request), plans: make(map[string]agent.ExecutionPlan)}
}

// SaveRequest 保存一个请求；同一 run_id 不允许换绑另一个请求。
func (r *MemoryRepository) SaveRequest(request conversation.ExecutionRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.requests[request.RunID]; ok && existing != request {
		return errors.New("run_id 已绑定其他请求")
	}
	r.requests[request.RunID] = request
	return nil
}

// GetRequest 读取请求，不存在时返回 false。
func (r *MemoryRepository) GetRequest(runID string) (conversation.ExecutionRequest, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	request, ok := r.requests[runID]
	return request, ok
}

// SaveApproval 保存审批快照，供进程重启后的 resume 继续判断审批状态。
func (r *MemoryRepository) SaveApproval(request approval.Request) error {
	if request.RunID == "" || request.ApprovalID == "" {
		return errors.New("审批快照缺少必要字段")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.approvals[request.RunID]; ok && existing.Status != approval.Pending && existing.Status != request.Status {
		return errors.New("审批终态不可覆盖")
	}
	r.approvals[request.RunID] = request
	return nil
}

// GetApproval 读取某个 run 的审批快照。
func (r *MemoryRepository) GetApproval(runID string) (approval.Request, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	request, ok := r.approvals[runID]
	return request, ok
}

// SavePlan 保存计划；已完成步骤不能被后续快照回写为未完成。
func (r *MemoryRepository) SavePlan(plan agent.ExecutionPlan) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if previous, ok := r.plans[plan.RunID]; ok {
		if len(previous.Steps) != len(plan.Steps) {
			return errors.New("执行计划步骤数量不可改变")
		}
		for index, step := range previous.Steps {
			if step.Status == agent.StepSucceeded && plan.Steps[index].Status != agent.StepSucceeded {
				return errors.New("已完成步骤不可回写")
			}
		}
	}
	r.plans[plan.RunID] = clonePlan(plan)
	return nil
}

// GetPlan 读取计划，不存在时返回 false。
func (r *MemoryRepository) GetPlan(runID string) (agent.ExecutionPlan, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	plan, ok := r.plans[runID]
	return clonePlan(plan), ok
}

// clonePlan 复制切片和嵌套输入，避免调用方绕过 Repository 合同修改已保存快照。
func clonePlan(plan agent.ExecutionPlan) agent.ExecutionPlan {
	plan.Steps = append([]agent.PlanStep(nil), plan.Steps...)
	for index := range plan.Steps {
		plan.Steps[index].Input = cloneMap(plan.Steps[index].Input)
	}
	return plan
}

// cloneMap 复制可变输入，保证内存 Repository 的读写边界稳定。
func cloneMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

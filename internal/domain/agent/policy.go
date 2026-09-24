// Package agent 定义路由策略门控和能力注册合同。
//
// Policy Gate 只依据注册表与声明风险做确定性判断，不执行 Worker、Tool 或模型调用。
package agent

import (
	"errors"
	"fmt"

	"github.com/observer-mimiron/supervisor-template/internal/domain/operation"
	domaintool "github.com/observer-mimiron/supervisor-template/internal/domain/tool"
)

// PolicyGate 将候选决策限制到已注册 Worker 和 allow-listed Tool。
type PolicyGate struct {
	Workers map[string]operation.WorkerContract
	Tools   map[string]domaintool.Contract
}

// NewPolicyGate 创建不可改变权限上限的确定性策略门控。
func NewPolicyGate(workers []operation.WorkerContract, tools []domaintool.Contract) *PolicyGate {
	workerMap := make(map[string]operation.WorkerContract, len(workers))
	for _, worker := range workers {
		workerMap[worker.WorkerID] = worker
	}
	toolMap := make(map[string]domaintool.Contract, len(tools))
	for _, tool := range tools {
		toolMap[tool.ToolID] = tool
	}
	return &PolicyGate{Workers: workerMap, Tools: toolMap}
}

// Evaluate 把候选路由转换成允许执行的 ApprovedRoute。
func (p *PolicyGate) Evaluate(decision SupervisorDecision) (ApprovedRoute, error) {
	worker, ok := p.Workers[decision.WorkerID]
	if !ok {
		return ApprovedRoute{}, fmt.Errorf("Worker %q 未注册: %w", decision.WorkerID, errors.New(string(ErrorUnknownCapability)))
	}
	toolID := decision.Arguments["tool_id"]
	tool, ok := p.Tools[toolID]
	if !ok {
		return ApprovedRoute{}, fmt.Errorf("Tool %q 未注册: %w", toolID, errors.New(string(ErrorUnknownCapability)))
	}
	if !contains(worker.AllowedTools, toolID) {
		return ApprovedRoute{}, fmt.Errorf("Tool %q 不在 Worker %q allow-list: %w", toolID, worker.WorkerID, errors.New(string(ErrorPolicyDenied)))
	}
	if decision.Risk != RiskReadOnly && decision.Risk != RiskSideEffect {
		return ApprovedRoute{}, errors.New(string(ErrorPolicyDenied))
	}
	if tool.Risk == string(RiskSideEffect) && decision.Risk != RiskSideEffect {
		return ApprovedRoute{}, errors.New(string(ErrorPolicyDenied))
	}
	// A model may not escalate a read-only capability into a side effect.
	if decision.Risk == RiskSideEffect && tool.Risk != string(RiskSideEffect) {
		return ApprovedRoute{}, errors.New(string(ErrorPolicyDenied))
	}
	return ApprovedRoute{
		DecisionID:       decision.DecisionID,
		WorkerID:         worker.WorkerID,
		AllowedTools:     []string{toolID},
		ApprovalRequired: tool.Risk == string(RiskSideEffect) || tool.RequiresApproval,
		PolicyVersion:    "v1",
	}, nil
}

// contains 判断一个能力是否在静态白名单中。
func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

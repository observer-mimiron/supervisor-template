package examplebusiness

import (
	"strings"

	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
)

const (
	fixedAudienceQuery      = "{\"as_of\":\"2026-09-25\"}"
	fixedAudienceProjection = "{\"count\":4,\"customer_ids\":[\"cust-001\",\"cust-002\",\"cust-006\",\"cust-008\"],\"spend_365d_total\":6200}"
	fixedOrderQuery         = "{\"user_id\":1}"
	fixedOrderInsert        = "{\"user_id\":1,\"product_id\":1,\"quantity\":1,\"total_amount\":\"19.90\"}"
)

// FakeDecisionBuilder adapts the example business fixture to the generic LLM
// fake. All payloads remain owned by this business module.
func FakeDecisionBuilder(request conversation.ExecutionRequest, workerID, intent, toolID string, risk agent.Risk) agent.SupervisorDecision {
	message := strings.TrimSpace(request.Message)
	if workerID == "" {
		workerID, intent, risk = WorkerID, WorkerID, agent.RiskReadOnly
	}
	if toolID == "" {
		toolID = "not_registered"
	}
	if strings.Contains(message, "未知能力") {
		toolID = "not_registered"
	}
	toolMessage := message
	switch toolID {
	case ReadOnlyToolID:
		toolMessage = fixedAudienceQuery
	case SummaryToolID, SideEffectToolID:
		toolMessage = fixedAudienceProjection
	case MySQLQueryToolID:
		toolMessage = fixedOrderQuery
	case MySQLInsertToolID:
		toolMessage = fixedOrderInsert
	}
	decision := agent.SupervisorDecision{
		DecisionID: request.RunID + ":decision",
		WorkerID:   workerID,
		Intent:     intent,
		Arguments:  map[string]string{"tool_id": toolID, "message": toolMessage},
		Risk:       risk,
		Confidence: 1,
	}
	if isMixedApprovalExample(message) {
		// 只读 + 写入的混合计划：这是动作级审批的判定形状。第一步只读不需要审批，
		// 第二步副作用的输入会被第一步结果改写，因此只能在执行到它时才请求审批。
		first, firstOK := RouteFor(ReadOnlyToolID)
		second, secondOK := RouteFor(SideEffectToolID)
		if firstOK && secondOK {
			decision.Steps = []agent.CandidateStep{
				{WorkerID: first.WorkerID, Intent: first.Intent, Arguments: map[string]string{"tool_id": first.ToolID, "message": fixedAudienceQuery}, Risk: first.Risk},
				{WorkerID: second.WorkerID, Intent: second.Intent, Arguments: map[string]string{"tool_id": second.ToolID, "message": fixedAudienceProjection}, Risk: second.Risk},
			}
			decision.WorkerID, decision.Intent = first.WorkerID, first.Intent
			decision.Arguments, decision.Risk = decision.Steps[0].Arguments, decision.Steps[0].Risk
		}
	} else if isSerialExample(message) {
		first, firstOK := RouteFor(ReadOnlyToolID)
		second, secondOK := RouteFor(SummaryToolID)
		if firstOK && secondOK {
			decision.Steps = []agent.CandidateStep{
				{WorkerID: first.WorkerID, Intent: first.Intent, Arguments: map[string]string{"tool_id": first.ToolID, "message": fixedAudienceQuery}, Risk: first.Risk},
				{WorkerID: second.WorkerID, Intent: second.Intent, Arguments: map[string]string{"tool_id": second.ToolID, "message": fixedAudienceProjection}, Risk: second.Risk},
			}
			decision.WorkerID, decision.Intent = first.WorkerID, first.Intent
			decision.Arguments, decision.Risk = decision.Steps[0].Arguments, decision.Steps[0].Risk
		}
	}
	return decision
}

func isSerialExample(message string) bool {
	return (strings.Contains(message, "串行") || strings.Contains(message, "多步骤") || strings.Contains(message, "先") && strings.Contains(message, "再")) && !strings.Contains(message, "未知能力")
}

// isMixedApprovalExample 判断请求是否要求"先只读、后写入"的混合计划。
//
// 关键词特意避开 isSerialExample 的"串行/多步骤/先…再"，否则两种形状会互相抢占。
func isMixedApprovalExample(message string) bool {
	return strings.Contains(message, "混合") && !strings.Contains(message, "未知能力")
}

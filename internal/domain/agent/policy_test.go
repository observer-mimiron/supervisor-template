package agent

import (
	"strings"
	"testing"

	"github.com/observer-mimiron/supervisor-template/internal/domain/operation"
	domaintool "github.com/observer-mimiron/supervisor-template/internal/domain/tool"
)

// 已注册、但不在该 Worker allow-list 内的 Tool 必须被拒绝。
// 这是 Tool 越权的第一道闸：模型只能在该 Worker 被声明允许的能力范围内行动，
// 不能因为某个 Tool 恰好已注册就借用它。
func TestPolicyGateRejectsToolOutsideWorkerAllowList(t *testing.T) {
	gate := NewPolicyGate(
		[]operation.WorkerContract{{WorkerID: "user_analysis", AllowedTools: []string{"user_query"}}},
		[]domaintool.Contract{
			{ToolID: "user_query", Risk: string(RiskReadOnly)},
			{ToolID: "simulated_outreach", Risk: string(RiskSideEffect)},
		},
	)
	_, err := gate.Evaluate(SupervisorDecision{
		DecisionID: "d-allowlist", WorkerID: "user_analysis",
		Arguments: map[string]string{"tool_id": "simulated_outreach"}, Risk: RiskSideEffect,
	})
	if err == nil || !strings.Contains(err.Error(), "allow-list") {
		t.Fatalf("expected allow-list rejection for a registered tool outside the worker allow-list, got %v", err)
	}
}

func TestPolicyGateRejectsUnknownTool(t *testing.T) {
	gate := NewPolicyGate([]operation.WorkerContract{{WorkerID: "user_analysis", AllowedTools: []string{"user_query"}}}, nil)
	_, err := gate.Evaluate(SupervisorDecision{DecisionID: "d1", WorkerID: "user_analysis", Arguments: map[string]string{"tool_id": "missing"}, Risk: RiskReadOnly})
	if err == nil {
		t.Fatal("expected unknown tool rejection")
	}
}

func TestPolicyGateRequiresApprovalForSideEffect(t *testing.T) {
	gate := NewPolicyGate(
		[]operation.WorkerContract{{WorkerID: "user_analysis", AllowedTools: []string{"simulated_outreach"}}},
		[]domaintool.Contract{{ToolID: "simulated_outreach", Risk: string(RiskSideEffect), RequiresApproval: true}},
	)
	route, err := gate.Evaluate(SupervisorDecision{DecisionID: "d1", WorkerID: "user_analysis", Arguments: map[string]string{"tool_id": "simulated_outreach"}, Risk: RiskSideEffect})
	if err != nil {
		t.Fatal(err)
	}
	if !route.ApprovalRequired {
		t.Fatal("expected approval requirement")
	}
}

func TestPolicyGateRejectsModelEscalatingReadOnlyTool(t *testing.T) {
	gate := NewPolicyGate(
		[]operation.WorkerContract{{WorkerID: "user_analysis", AllowedTools: []string{"user_query"}}},
		[]domaintool.Contract{{ToolID: "user_query", Risk: string(RiskReadOnly)}},
	)
	_, err := gate.Evaluate(SupervisorDecision{
		DecisionID: "d-escalate", WorkerID: "user_analysis",
		Arguments: map[string]string{"tool_id": "user_query"}, Risk: RiskSideEffect,
	})
	if err == nil {
		t.Fatal("expected policy escalation rejection")
	}
}

func TestPolicyGateRejectsApprovalFloorWithoutSideEffectDeclaration(t *testing.T) {
	gate := NewPolicyGate(
		[]operation.WorkerContract{{WorkerID: "user_analysis", AllowedTools: []string{"user_query"}}},
		[]domaintool.Contract{{ToolID: "user_query", Risk: string(RiskReadOnly), RequiresApproval: true}},
	)
	route, err := gate.Evaluate(SupervisorDecision{
		DecisionID: "d-approval", WorkerID: "user_analysis",
		Arguments: map[string]string{"tool_id": "user_query"}, Risk: RiskReadOnly,
	})
	if err != nil || !route.ApprovalRequired {
		t.Fatalf("approval floor was lost: route=%#v err=%v", route, err)
	}
}

func TestPolicyGateCopiesRegistrationSlices(t *testing.T) {
	workers := []operation.WorkerContract{{WorkerID: "worker", AllowedTools: []string{"tool"}}}
	tools := []domaintool.Contract{{ToolID: "tool", Risk: string(RiskReadOnly), RequiredInputs: []string{"message"}}}
	gate := NewPolicyGate(workers, tools)
	workers[0].AllowedTools[0] = "tampered"
	tools[0].RequiredInputs[0] = "tampered"
	if _, err := gate.Evaluate(SupervisorDecision{DecisionID: "d-copy", WorkerID: "worker", Arguments: map[string]string{"tool_id": "tool"}, Risk: RiskReadOnly}); err != nil {
		t.Fatalf("registration mutation escaped constructor copy: %v", err)
	}
}

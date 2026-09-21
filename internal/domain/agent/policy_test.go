package agent

import (
	"testing"

	"github.com/observer-mimiron/supervisor-template/internal/domain/operation"
	domaintool "github.com/observer-mimiron/supervisor-template/internal/domain/tool"
)

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

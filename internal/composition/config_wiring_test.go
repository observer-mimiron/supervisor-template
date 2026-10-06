package composition

import (
	"testing"

	"github.com/observer-mimiron/supervisor-template/internal/config"
	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	"github.com/observer-mimiron/supervisor-template/internal/domain/operation"
	domaintool "github.com/observer-mimiron/supervisor-template/internal/domain/tool"
)

// TestSelectPolicyWorkersEnforcesSupervisorAllowList 证明 agent.supervisor.allowed_workers
// 是真实的运行期边界，而不只是启动期校验：名单外的 Worker 会在 Policy Gate 被拒绝。
//
// 这条保护在 fake provider 下不可达（路由表已被启动校验限制在名单内），但真实模型
// 可以自由返回 worker_id，因此必须由门控兜住。
func TestSelectPolicyWorkersEnforcesSupervisorAllowList(t *testing.T) {
	workers := []operation.WorkerContract{
		{WorkerID: "listed", AllowedTools: []string{"read_tool"}},
		{WorkerID: "unlisted", AllowedTools: []string{"read_tool"}},
	}
	tools := []domaintool.Contract{{ToolID: "read_tool", Risk: "read_only"}}

	gate := agent.NewPolicyGate(selectPolicyWorkers(workers, []string{"listed"}), tools)
	if _, err := gate.Evaluate(agent.SupervisorDecision{
		WorkerID:  "listed",
		Arguments: map[string]string{"tool_id": "read_tool"},
		Risk:      agent.RiskReadOnly,
	}); err != nil {
		t.Fatalf("listed worker must be routable: %v", err)
	}
	if _, err := gate.Evaluate(agent.SupervisorDecision{
		WorkerID:  "unlisted",
		Arguments: map[string]string{"tool_id": "read_tool"},
		Risk:      agent.RiskReadOnly,
	}); err == nil {
		t.Fatal("worker outside allowed_workers must be rejected by the policy gate")
	}
}

// TestSelectPolicyWorkersEmptyListKeepsEveryWorker 记录"留空即不收紧"的语义，
// 避免把空名单误读成"谁都不许调"。
func TestSelectPolicyWorkersEmptyListKeepsEveryWorker(t *testing.T) {
	workers := []operation.WorkerContract{{WorkerID: "a"}, {WorkerID: "b"}}
	if got := selectPolicyWorkers(workers, nil); len(got) != 2 {
		t.Fatalf("empty allow-list must not narrow the gate, got %d workers", len(got))
	}
}

// TestToolRetryLimitsResolveFromConfig 证明 [tools.*].max_retries 会被解析成运行期表，
// 未配置的 Tool 不出现在表中并回落到全局预算。
func TestToolRetryLimitsResolveFromConfig(t *testing.T) {
	zero := 0
	three := 3
	negative := -1
	limits := toolRetryLimits(map[string]config.ToolConfig{
		"write_tool": {MaxRetries: &zero},
		"slow_tool":  {MaxRetries: &three},
		"inherit":    {},
		"negative":   {MaxRetries: &negative},
	}, 2)

	if limits["write_tool"] != 0 {
		t.Fatalf("explicit zero must survive, got %d", limits["write_tool"])
	}
	if limits["slow_tool"] != 3 {
		t.Fatalf("explicit limit must survive, got %d", limits["slow_tool"])
	}
	if _, ok := limits["inherit"]; ok {
		t.Fatal("unset tools must not be listed so they fall back to the global budget")
	}
	if limits["negative"] != 2 {
		t.Fatalf("negative values mean fallback, got %d", limits["negative"])
	}
}

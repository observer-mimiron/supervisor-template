package llm

import (
	"context"
	"testing"

	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
)

func TestFakeSupervisorUsesConfiguredRoute(t *testing.T) {
	supervisor := NewFakeSupervisor(FakeRoute{
		WorkerID: "segment_worker",
		Intent:   "segment_users",
		Matches:  []string{"分群"},
		ToolID:   "segment_query",
		Risk:     agent.RiskReadOnly,
	})
	decision, err := supervisor.Decide(context.Background(), conversation.ExecutionRequest{
		RunID:   "run-config-route",
		Message: "请做用户分群",
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.WorkerID != "segment_worker" || decision.Intent != "segment_users" || decision.Arguments["tool_id"] != "segment_query" {
		t.Fatalf("configured route was ignored: %#v", decision)
	}
}

func TestFakeSupervisorRejectsEmptyMessage(t *testing.T) {
	_, err := NewFakeSupervisor().Decide(context.Background(), conversation.ExecutionRequest{RunID: "empty"})
	if err == nil {
		t.Fatal("expected empty message rejection")
	}
}

func TestFakeSupervisorCanExposeUnknownCapabilityForPolicyTest(t *testing.T) {
	decision, err := NewFakeSupervisor().Decide(context.Background(), conversation.ExecutionRequest{RunID: "unknown", Message: "未知能力"})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Arguments["tool_id"] != "not_registered" {
		t.Fatalf("tool id = %q", decision.Arguments["tool_id"])
	}
}

func TestFakeSupervisorRejectsArbitraryUnmatchedMessageAsUnknownRoute(t *testing.T) {
	decision, err := NewFakeSupervisor().Decide(context.Background(), conversation.ExecutionRequest{RunID: "unmatched", Message: "请帮我写一首诗"})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Arguments["tool_id"] != "not_registered" {
		t.Fatalf("unmatched message defaulted to tool %q", decision.Arguments["tool_id"])
	}
}

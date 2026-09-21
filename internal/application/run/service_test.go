package run

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/application"
	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
	"github.com/observer-mimiron/supervisor-template/internal/domain/operation"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/checkpoint"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/eventbus"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/llm"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/persistence"
	toolinfra "github.com/observer-mimiron/supervisor-template/internal/infrastructure/tool"
)

func newTestService() (*Service, *toolinfra.FakeRegistry) {
	repository := persistence.NewMemoryRepository()
	checkpoints := checkpoint.NewMemoryStore()
	events := eventbus.NewMemoryBus()
	tools := toolinfra.NewFakeRegistry()
	deps := application.Dependencies{
		Repository: repository,
		Checkpoint: checkpoints,
		EventBus:   events,
		Supervisor: llm.NewFakeSupervisor(),
		Policy: agent.NewPolicyGate([]operation.WorkerContract{{
			WorkerID:     "user_analysis",
			AllowedTools: []string{"user_query", "simulated_outreach"},
		}}, toolinfra.Contracts()),
		Tools: tools,
	}
	return NewService(deps), tools
}

func TestReadOnlyRunCompletesOnceAndReplaysTerminalEvents(t *testing.T) {
	service, _ := newTestService()
	runID, err := service.Start(context.Background(), request("run-read", "分析示例用户分群"))
	if err != nil {
		t.Fatal(err)
	}
	events := service.Events(runID)
	if terminalCount(events) != 1 || events[len(events)-1].Type != agent.Completed {
		t.Fatalf("unexpected terminal events: %#v", events)
	}
	originalCount := len(events)
	replayed, err := service.Resume(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != originalCount || terminalCount(replayed) != 1 {
		t.Fatalf("resume changed terminal event set: %#v", replayed)
	}
}

func TestApprovalBlocksSideEffectAndRepeatedResumeIsIdempotent(t *testing.T) {
	service, tools := newTestService()
	runID, err := service.Start(context.Background(), request("run-side-effect", "模拟触达示例用户"))
	if err != nil {
		t.Fatal(err)
	}
	if tools.OutreachCount() != 0 || !hasEvent(service.Events(runID), agent.ApprovalRequired) {
		t.Fatalf("side effect escaped approval: %#v", service.Events(runID))
	}
	if _, err := service.Resume(context.Background(), runID); err == nil {
		t.Fatal("expected approval requirement")
	}
	if err := service.Approve(context.Background(), runID, "approve", "operator-1"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := service.Resume(context.Background(), runID); err != nil {
			t.Fatal(err)
		}
	}
	if tools.OutreachCount() != 1 || terminalCount(service.Events(runID)) != 1 {
		t.Fatalf("idempotency failed: count=%d events=%#v", tools.OutreachCount(), service.Events(runID))
	}
}

func TestRejectedApprovalNeverCallsSideEffectTool(t *testing.T) {
	service, tools := newTestService()
	runID, err := service.Start(context.Background(), request("run-reject", "模拟触达示例用户"))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Approve(context.Background(), runID, "reject", "operator-1"); err != nil {
		t.Fatal(err)
	}
	events := service.Events(runID)
	if tools.OutreachCount() != 0 || terminalCount(events) != 1 || events[len(events)-1].Type != agent.Failed {
		t.Fatalf("rejected run is invalid: %#v", events)
	}
}

func TestUnknownCapabilityIsClassifiedAndNeverExecuted(t *testing.T) {
	service, tools := newTestService()
	runID, err := service.Start(context.Background(), request("run-unknown", "未知能力"))
	if err == nil || !strings.Contains(err.Error(), "未注册") {
		t.Fatalf("expected unknown capability error, got %v", err)
	}
	events := service.Events(runID)
	if tools.OutreachCount() != 0 || terminalCount(events) != 1 || events[len(events)-1].Type != agent.Failed {
		t.Fatalf("unknown capability was not classified: %#v", events)
	}
}

func TestCancelApprovalRunProducesCanceledTerminal(t *testing.T) {
	service, tools := newTestService()
	runID, err := service.Start(context.Background(), request("run-cancel", "模拟触达示例用户"))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Cancel(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	events := service.Events(runID)
	if tools.OutreachCount() != 0 || terminalCount(events) != 1 || events[len(events)-1].Type != agent.Canceled {
		t.Fatalf("cancel did not terminate safely: %#v", events)
	}
	if err := service.Cancel(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	if terminalCount(service.Events(runID)) != 1 {
		t.Fatal("repeated cancel added a terminal event")
	}
}

func TestToolTimeoutIsClassifiedAndDoesNotComplete(t *testing.T) {
	service, _ := newTestService()
	service.deps.Tools = blockingTool{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	runID, err := service.Start(ctx, request("run-timeout", "分析示例用户分群"))
	var runErr *Error
	if !errors.As(err, &runErr) || runErr.Code != agent.ErrorToolTimeout {
		t.Fatalf("expected TOOL_TIMEOUT, got %v", err)
	}
	events := service.Events(runID)
	if events[len(events)-1].Type != agent.Failed || hasEvent(events, agent.Completed) {
		t.Fatalf("timeout terminal is invalid: %#v", events)
	}
}

func TestSensitiveToolResultIsRejectedBeforeTextProjection(t *testing.T) {
	service, _ := newTestService()
	service.deps.Tools = fixedTool{result: "api_key=secret /home/service/.env"}
	runID, err := service.Start(context.Background(), request("run-sensitive", "分析示例用户分群"))
	var runErr *Error
	if !errors.As(err, &runErr) || runErr.Code != agent.ErrorInvalidOutput {
		t.Fatalf("expected INVALID_OUTPUT, got %v", err)
	}
	events := service.Events(runID)
	if events[len(events)-1].Type != agent.Failed || hasEvent(events, agent.Text) || hasEvent(events, agent.Completed) {
		t.Fatalf("sensitive output escaped guard: %#v", events)
	}
}

func TestExistingRunCannotBeReplayedAcrossConversations(t *testing.T) {
	service, _ := newTestService()
	if _, err := service.Start(context.Background(), request("run-owned", "分析示例用户分群")); err != nil {
		t.Fatal(err)
	}
	_, err := service.Start(context.Background(), conversation.ExecutionRequest{
		RunID:          "run-owned",
		ConversationID: "other-conversation",
		Message:        "分析示例用户分群",
	})
	var runErr *Error
	if !errors.As(err, &runErr) || runErr.Code != agent.ErrorRunNotResumable {
		t.Fatalf("expected ownership rejection, got %v", err)
	}
}

type fixedTool struct{ result string }

func (t fixedTool) Execute(context.Context, string, map[string]string, string) (string, error) {
	return t.result, nil
}

type blockingTool struct{}

func (blockingTool) Execute(ctx context.Context, _ string, _ map[string]string, _ string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func request(runID, message string) conversation.ExecutionRequest {
	return conversation.ExecutionRequest{RunID: runID, ConversationID: "demo", Message: message}
}

func terminalCount(events []agent.RunEvent) int {
	count := 0
	for _, event := range events {
		if agent.IsTerminal(eventbus.TerminalStatus(event.Type)) {
			count++
		}
	}
	return count
}

func hasEvent(events []agent.RunEvent, want agent.EventType) bool {
	for _, event := range events {
		if event.Type == want {
			return true
		}
	}
	return false
}

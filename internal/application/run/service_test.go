package run

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/application"
	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
	"github.com/observer-mimiron/supervisor-template/internal/domain/identity"
	"github.com/observer-mimiron/supervisor-template/internal/domain/operation"
	authinfra "github.com/observer-mimiron/supervisor-template/internal/infrastructure/auth"
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
		RunAuth: authinfra.OwnerRunAuthorizer{},
		Tools:   tools,
	}
	return NewService(deps), tools
}

func TestFileBackedServiceRestoresApprovalAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	newFileService := func() (*Service, *toolinfra.FakeRegistry) {
		repository, err := persistence.NewFileRepository(filepath.Join(dir, "runs"))
		if err != nil {
			t.Fatal(err)
		}
		checkpoints, err := checkpoint.NewFileStore(filepath.Join(dir, "checkpoints"))
		if err != nil {
			t.Fatal(err)
		}
		events, err := eventbus.NewFileBus(filepath.Join(dir, "events"))
		if err != nil {
			t.Fatal(err)
		}
		tools := toolinfra.NewFakeRegistry()
		deps := application.Dependencies{
			Repository: repository,
			Checkpoint: checkpoints,
			EventBus:   events,
			Supervisor: llm.NewFakeSupervisor(),
			Policy: agent.NewPolicyGate([]operation.WorkerContract{{
				WorkerID: "user_analysis", AllowedTools: []string{"user_query", "simulated_outreach"},
			}}, toolinfra.Contracts()),
			RunAuth: authinfra.OwnerRunAuthorizer{},
			Tools:   tools,
		}
		return NewService(deps), tools
	}
	first, _ := newFileService()
	runID, err := first.Start(context.Background(), request("run-restart", "模拟触达示例用户"))
	if err != nil {
		t.Fatal(err)
	}
	second, tools := newFileService()
	if _, err := second.Resume(context.Background(), testSubject(), runID); err == nil || !strings.Contains(err.Error(), "审批") {
		t.Fatalf("restart lost approval state: %v", err)
	}
	if err := second.Approve(context.Background(), testSubject(), runID, "approve"); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Resume(context.Background(), testSubject(), runID); err != nil {
		t.Fatal(err)
	}
	events := second.Events(runID)
	eventIDs := make(map[string]struct{}, len(events))
	for _, event := range events {
		if _, exists := eventIDs[event.EventID]; exists {
			t.Fatalf("restart resume reused event id %q: %#v", event.EventID, events)
		}
		eventIDs[event.EventID] = struct{}{}
	}
	if tools.OutreachCount() != 1 || terminalCount(events) != 1 {
		t.Fatalf("restart resume did not complete once: count=%d events=%#v", tools.OutreachCount(), second.Events(runID))
	}
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
	replayed, err := service.Resume(context.Background(), testSubject(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != originalCount || terminalCount(replayed) != 1 {
		t.Fatalf("resume changed terminal event set: %#v", replayed)
	}
}

func TestRunUsesConfiguredWorkerRunner(t *testing.T) {
	service, _ := newTestService()
	runner := &recordingRunner{result: "runner result"}
	service.deps.Runner = runner

	runID, err := service.Start(context.Background(), request("run-runner", "分析示例用户分群"))
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(runner.requests))
	}
	call := runner.requests[0]
	if call.RunID != runID || call.WorkerID != "user_analysis" || call.Intent != "user_analysis" || call.ToolID != "user_query" {
		t.Fatalf("unexpected worker request: %#v", call)
	}
	if call.IdempotencyKey == "" || call.Input["message"] == "" {
		t.Fatalf("worker request lost execution context: %#v", call)
	}
	events := service.Events(runID)
	if events[len(events)-1].Type != agent.Completed || events[len(events)-2].Data["content"] != "runner result" {
		t.Fatalf("runner result was not projected: %#v", events)
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
	if _, err := service.Resume(context.Background(), testSubject(), runID); err == nil {
		t.Fatal("expected approval requirement")
	}
	if err := service.Approve(context.Background(), testSubject(), runID, "approve"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := service.Resume(context.Background(), testSubject(), runID); err != nil {
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
	if err := service.Approve(context.Background(), testSubject(), runID, "reject"); err != nil {
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
	if err := service.Cancel(context.Background(), testSubject(), runID); err != nil {
		t.Fatal(err)
	}
	events := service.Events(runID)
	if tools.OutreachCount() != 0 || terminalCount(events) != 1 || events[len(events)-1].Type != agent.Canceled {
		t.Fatalf("cancel did not terminate safely: %#v", events)
	}
	if err := service.Cancel(context.Background(), testSubject(), runID); err != nil {
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
		Subject:        testSubject(),
		Message:        "分析示例用户分群",
	})
	var runErr *Error
	if !errors.As(err, &runErr) || runErr.Code != agent.ErrorRunNotResumable {
		t.Fatalf("expected ownership rejection, got %v", err)
	}
}

func TestRunAccessIsBoundToOwningSubject(t *testing.T) {
	service, tools := newTestService()
	runID, err := service.Start(context.Background(), request("run-owner", "模拟触达示例用户"))
	if err != nil {
		t.Fatal(err)
	}
	other := identity.Subject{TenantID: "other-tenant", SubjectID: "other-user"}
	replay := request(runID, "模拟触达示例用户")
	replay.Subject = other
	if _, err := service.Start(context.Background(), replay); !hasAccessDenied(err) {
		t.Fatalf("replay error = %v", err)
	}
	if err := service.Approve(context.Background(), other, runID, "approve"); !hasAccessDenied(err) {
		t.Fatalf("approval error = %v", err)
	}
	if _, err := service.Resume(context.Background(), other, runID); !hasAccessDenied(err) {
		t.Fatalf("resume error = %v", err)
	}
	if err := service.Cancel(context.Background(), other, runID); !hasAccessDenied(err) {
		t.Fatalf("cancel error = %v", err)
	}
	if tools.OutreachCount() != 0 || !hasEvent(service.Events(runID), agent.ApprovalRequired) {
		t.Fatalf("foreign subject changed run state: %#v", service.Events(runID))
	}
}

type fixedTool struct{ result string }

type recordingRunner struct {
	requests []application.WorkerRequest
	result   string
}

func (r *recordingRunner) Run(_ context.Context, request application.WorkerRequest) (application.WorkerResult, error) {
	r.requests = append(r.requests, request)
	return application.WorkerResult{Content: r.result}, nil
}

func (t fixedTool) Execute(context.Context, string, map[string]string, string) (string, error) {
	return t.result, nil
}

type blockingTool struct{}

func (blockingTool) Execute(ctx context.Context, _ string, _ map[string]string, _ string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func request(runID, message string) conversation.ExecutionRequest {
	return conversation.ExecutionRequest{RunID: runID, ConversationID: "demo", Subject: testSubject(), Message: message}
}

func testSubject() identity.Subject {
	return identity.Subject{TenantID: "test-tenant", SubjectID: "test-user"}
}

func hasAccessDenied(err error) bool {
	var runErr *Error
	return errors.As(err, &runErr) && runErr.Code == agent.ErrorAccessDenied
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

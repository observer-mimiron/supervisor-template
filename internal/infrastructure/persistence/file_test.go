package persistence

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	"github.com/observer-mimiron/supervisor-template/internal/domain/approval"
	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
)

func TestFileRepositoryRestoresRequestApprovalPlanAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	repo, err := NewFileRepository(dir)
	if err != nil {
		t.Fatal(err)
	}
	request := conversation.ExecutionRequest{RunID: "run-1", ConversationID: "conversation-1", Message: "hello", RequestedAt: time.Now()}
	if err := repo.SaveRequest(request); err != nil {
		t.Fatal(err)
	}
	approvalRequest := approval.Request{ApprovalID: "approval-1", RunID: request.RunID, StepID: "step-1", Status: approval.Pending}
	if err := repo.SaveApproval(approvalRequest); err != nil {
		t.Fatal(err)
	}
	plan, err := agent.NewExecutionPlan("plan-1", request.RunID, []agent.PlanStep{{StepID: "step-1", ToolID: "user_query", Status: agent.StepPending}}, 1, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SavePlan(plan); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewFileRepository(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok, err := restarted.LoadRequest(request.RunID); err != nil || !ok || got.Message != request.Message {
		t.Fatalf("request = %#v, %v, %v", got, ok, err)
	}
	if got, ok, err := restarted.LoadApproval(request.RunID); err != nil || !ok || got.Status != approval.Pending {
		t.Fatalf("approval = %#v, %v, %v", got, ok, err)
	}
	if got, ok, err := restarted.LoadPlan(request.RunID); err != nil || !ok || got.PlanID != plan.PlanID {
		t.Fatalf("plan = %#v, %v, %v", got, ok, err)
	}
	if files, err := filepath.Glob(filepath.Join(dir, ".run-*.tmp")); err != nil || len(files) != 0 {
		t.Fatalf("temporary files remain: %v, %v", files, err)
	}
}

func TestFileRepositoryRejectsCorruptAndUnsupportedVersion(t *testing.T) {
	dir := t.TempDir()
	repo, err := NewFileRepository(dir)
	if err != nil {
		t.Fatal(err)
	}
	request := conversation.ExecutionRequest{RunID: "run-1", ConversationID: "conversation-1", Message: "hello"}
	if err := repo.SaveRequest(request); err != nil {
		t.Fatal(err)
	}
	path := repo.path(request.RunID)
	for _, data := range []string{"{", `{"schema_version":99}`} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		_, ok, err := repo.LoadRequest(request.RunID)
		if ok || err == nil {
			t.Fatal("expected explicit persistence read error")
		}
		if !strings.Contains(err.Error(), "快照") {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

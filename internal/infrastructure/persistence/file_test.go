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

func TestFileRepositoryPersistsDurabilityFields(t *testing.T) {
	repo, err := NewFileRepository(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	updated := time.Unix(1700000000, 0).UTC()
	plan, err := agent.NewExecutionPlan("plan-fields", "run-fields", []agent.PlanStep{{
		StepID: "step-1", ToolID: "user_query", Status: agent.StepPending,
	}}, 1, updated.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	plan.ErrorClass = agent.ClassUnknownOutcome
	plan.UpdatedAt = updated
	plan.Steps[0].ErrorClass = agent.ClassUnknownOutcome
	plan.Steps[0].UpdatedAt = updated
	if err := repo.SavePlan(plan); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := repo.LoadPlan(plan.RunID)
	if err != nil || !ok {
		t.Fatalf("loaded plan = %#v, %v, %v", loaded, ok, err)
	}
	if loaded.ErrorClass != plan.ErrorClass || !loaded.UpdatedAt.Equal(updated) || loaded.Steps[0].ErrorClass != plan.Steps[0].ErrorClass || !loaded.Steps[0].UpdatedAt.Equal(updated) {
		t.Fatalf("durability fields were not persisted: %#v", loaded)
	}
}

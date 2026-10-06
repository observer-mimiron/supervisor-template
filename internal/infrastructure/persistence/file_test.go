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
	if got, ok, err := restarted.LoadApproval(request.RunID, approvalRequest.StepID); err != nil || !ok || got.Status != approval.Pending {
		t.Fatalf("approval = %#v, %v, %v", got, ok, err)
	}
	if got, ok, err := restarted.LoadPlan(request.RunID); err != nil || !ok || got.PlanID != plan.PlanID {
		t.Fatalf("plan = %#v, %v, %v", got, ok, err)
	}
	if files, err := filepath.Glob(filepath.Join(dir, ".run-*.tmp")); err != nil || len(files) != 0 {
		t.Fatalf("temporary files remain: %v, %v", files, err)
	}
}

// TestFileRepositoryKeepsApprovalsPerStep 证明一个 run 的多条审批互不覆盖。
//
// 逐个批准（spec §3.2）下同一 run 会有多条审批记录；若存储仍按 run 单键保存，
// 第二个动作的审批会覆盖第一个，审计将无法回答"谁批准了哪一次写入"。
func TestFileRepositoryKeepsApprovalsPerStep(t *testing.T) {
	dir := t.TempDir()
	repo, err := NewFileRepository(dir)
	if err != nil {
		t.Fatal(err)
	}
	first := approval.Request{ApprovalID: "approval-1", RunID: "run-1", StepID: "run-1:step-1", Status: approval.Approved, Reviewer: "reviewer-1"}
	second := approval.Request{ApprovalID: "approval-2", RunID: "run-1", StepID: "run-1:step-2", Status: approval.Pending}
	for _, record := range []approval.Request{first, second} {
		if err := repo.SaveApproval(record); err != nil {
			t.Fatal(err)
		}
	}
	restarted, err := NewFileRepository(dir)
	if err != nil {
		t.Fatal(err)
	}
	gotFirst, ok, err := restarted.LoadApproval("run-1", "run-1:step-1")
	if err != nil || !ok || gotFirst.Status != approval.Approved || gotFirst.Reviewer != "reviewer-1" {
		t.Fatalf("step-1 approval = %#v, %v, %v", gotFirst, ok, err)
	}
	gotSecond, ok, err := restarted.LoadApproval("run-1", "run-1:step-2")
	if err != nil || !ok || gotSecond.Status != approval.Pending || gotSecond.ApprovalID != "approval-2" {
		t.Fatalf("step-2 approval = %#v, %v, %v", gotSecond, ok, err)
	}
	// 未审批过的步骤不得返回别的步骤的批准。
	if _, ok, err := restarted.LoadApproval("run-1", "run-1:step-3"); err != nil || ok {
		t.Fatalf("step-3 不应有审批记录: %v, %v", ok, err)
	}
}

// TestFileRepositoryRejectsV1Snapshot 证明旧格式被显式拒绝而不是被静默降级。
//
// v1 快照只有一个 approval 字段，无法表达"批准的是哪个步骤"。若被当成 v2 读取，
// 该 run 会表现为"没有任何批准"——一个已经获批的副作用可能因此重新走审批，
// 更糟的是审计记录凭空消失。fail-closed 是这里的正确行为。
func TestFileRepositoryRejectsV1Snapshot(t *testing.T) {
	dir := t.TempDir()
	repo, err := NewFileRepository(dir)
	if err != nil {
		t.Fatal(err)
	}
	request := conversation.ExecutionRequest{RunID: "run-1", ConversationID: "conversation-1", Message: "hello"}
	if err := repo.SaveRequest(request); err != nil {
		t.Fatal(err)
	}
	legacy := `{"schema_version":1,"request":{"RunID":"run-1","ConversationID":"conversation-1","Message":"hello"},"approval":{"ApprovalID":"approval-1","RunID":"run-1","StepID":"step-1","Status":"pending"}}`
	if err := os.WriteFile(repo.path(request.RunID), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := repo.LoadApproval("run-1", "step-1"); err == nil || ok {
		t.Fatalf("v1 快照必须被显式拒绝: ok=%v err=%v", ok, err)
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

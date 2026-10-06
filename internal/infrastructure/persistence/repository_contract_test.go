package persistence

import (
	"testing"

	"github.com/observer-mimiron/supervisor-template/internal/domain/approval"
)

// approvalStore 是两个 Repository 实现共同满足的最小审批存取面。
type approvalStore interface {
	SaveApproval(approval.Request) error
	GetApproval(runID, stepID string) (approval.Request, bool)
}

// TestRepositoryApprovalContract 是 memory 与 file 两个适配器共享的审批合同套件。
//
// 合同条款 R1–R5：提交/读取按 (run_id, step_id) 定位、未知 run 不返回记录、
// 拒绝不合法快照、缺 RunID/StepID/ApprovalID 一律被拒、往返后字段不变。
// 两个适配器跑**同一组断言**，避免其中一侧悄悄少覆盖一条：此前 file 与 memory 的
// 审批测试各自独立，条款声称的"行为一致"没有对应的证据。
//
// 覆盖差异（有意保留）：file 一侧的 reopen 会构造**新实例**读取同一目录，因此额外验证了
// 跨实例可见性（R5）；memory 的 reopen 返回同一实例，它验证的是同一实例内的读回。
// 快照损坏与版本不兼容（R6）只有 file 具备（memory 无文件），由
// `TestFileRepositoryRejectsCorruptAndUnsupportedVersion` 与
// `TestFileRepositoryRejectsV1Snapshot` 单独覆盖。
func TestRepositoryApprovalContract(t *testing.T) {
	cases := []struct {
		name string
		open func(t *testing.T) (approvalStore, func() approvalStore)
	}{
		{
			name: "memory",
			open: func(t *testing.T) (approvalStore, func() approvalStore) {
				repository := NewMemoryRepository()
				return repository, func() approvalStore { return repository }
			},
		},
		{
			name: "file",
			open: func(t *testing.T) (approvalStore, func() approvalStore) {
				dir := t.TempDir()
				repository, err := NewFileRepository(dir)
				if err != nil {
					t.Fatal(err)
				}
				return repository, func() approvalStore {
					reopened, err := NewFileRepository(dir)
					if err != nil {
						t.Fatal(err)
					}
					return reopened
				}
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			store, reopen := testCase.open(t)
			const (
				runID      = "run-contract"
				firstStep  = "run-contract:step-1"
				secondStep = "run-contract:step-2"
				thirdStep  = "run-contract:step-3"
			)
			firstDigest := approval.ComputeActionDigest(firstStep, "simulated_outreach", map[string]string{"message": "a"})
			secondDigest := approval.ComputeActionDigest(firstStep, "simulated_outreach", map[string]string{"message": "b"})
			if firstDigest == "" || firstDigest == secondDigest {
				t.Fatal("测试前提不成立：两个输入的指纹应非空且不同")
			}

			// R1：同一 run 的两个步骤各存一条，互不覆盖。
			first := approval.Request{ApprovalID: firstStep + ":approval", RunID: runID, StepID: firstStep, ToolID: "simulated_outreach", ActionDigest: firstDigest, Status: approval.Approved, Reviewer: "reviewer-1"}
			second := approval.Request{ApprovalID: secondStep + ":approval", RunID: runID, StepID: secondStep, ToolID: "simulated_outreach", ActionDigest: secondDigest, Status: approval.Pending}
			for _, record := range []approval.Request{first, second} {
				if err := store.SaveApproval(record); err != nil {
					t.Fatalf("SaveApproval(%s) = %v", record.StepID, err)
				}
			}

			// R5：重新打开后两条都在，且字段无损。
			reopened := reopen()
			gotFirst, ok := reopened.GetApproval(runID, firstStep)
			if !ok || gotFirst.Status != approval.Approved || gotFirst.Reviewer != "reviewer-1" || gotFirst.ActionDigest != firstDigest || gotFirst.ToolID != "simulated_outreach" {
				t.Fatalf("step-1 审批在重开后不一致: %#v, ok=%v", gotFirst, ok)
			}
			gotSecond, ok := reopened.GetApproval(runID, secondStep)
			if !ok || gotSecond.Status != approval.Pending || gotSecond.ActionDigest != secondDigest {
				t.Fatalf("step-2 审批在重开后不一致: %#v, ok=%v", gotSecond, ok)
			}

			// R2：未审批过的步骤不返回别的步骤的批准。
			if record, ok := reopened.GetApproval(runID, thirdStep); ok {
				t.Fatalf("step-3 不应有审批记录: %#v", record)
			}
			if record, ok := reopened.GetApproval("run-other", firstStep); ok {
				t.Fatalf("别的 run 不应返回审批记录: %#v", record)
			}

			// R3a：同指纹的终态记录不可被改写状态（防止已批准/已拒绝的决定被就地洗掉）。
			sameAction := first
			sameAction.Status = approval.Pending
			sameAction.Reviewer = ""
			if err := store.SaveApproval(sameAction); err == nil {
				t.Fatal("同指纹的终态审批被改写成 pending；终态保护失效")
			}

			// R3b：指纹不同即视为另一个动作，允许建立属于它自己的批准（Q1 的落地方式）。
			otherAction := first
			otherAction.ActionDigest = approval.ComputeActionDigest(firstStep, "simulated_outreach", map[string]string{"message": "changed"})
			otherAction.Status = approval.Pending
			otherAction.Reviewer = ""
			if err := store.SaveApproval(otherAction); err != nil {
				t.Fatalf("动作指纹变化后应允许重新请求审批: %v", err)
			}
			refreshed, ok := store.GetApproval(runID, firstStep)
			if !ok || refreshed.Status != approval.Pending || refreshed.ActionDigest == firstDigest {
				t.Fatalf("重新请求后应绑定新指纹且状态为 pending: %#v", refreshed)
			}

			// R4：缺少 RunID / StepID / ApprovalID 的快照一律被拒。
			for name, record := range map[string]approval.Request{
				"缺 run_id":      {ApprovalID: "a", StepID: firstStep},
				"缺 step_id":     {ApprovalID: "a", RunID: runID},
				"缺 approval_id": {RunID: runID, StepID: firstStep},
			} {
				if err := store.SaveApproval(record); err == nil {
					t.Errorf("%s 的审批快照应被拒绝", name)
				}
			}
		})
	}
}

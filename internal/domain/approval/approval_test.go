package approval

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestComputeActionDigestIsDeterministicAndOrderIndependent 证明指纹只由动作本身决定。
//
// 输入是 map，迭代顺序随机；若实现直接拼接 map，同一动作会产生不同指纹，
// 于是"重试沿用原批准"会随机失效。这里显式用两种插入顺序构造同一个 map。
func TestComputeActionDigestIsDeterministicAndOrderIndependent(t *testing.T) {
	first := map[string]string{}
	first["tool_id"] = "simulated_outreach"
	first["message"] = "触达 4 人"

	second := map[string]string{}
	second["message"] = "触达 4 人"
	second["tool_id"] = "simulated_outreach"

	left := ComputeActionDigest("run-1:step-2", "simulated_outreach", first)
	right := ComputeActionDigest("run-1:step-2", "simulated_outreach", second)
	if left != right {
		t.Fatalf("同一动作产生不同指纹: %q != %q", left, right)
	}
	if len(left) != 64 {
		t.Fatalf("期望 sha256 十六进制指纹（64 字符），得到 %d 字符: %q", len(left), left)
	}
}

// TestComputeActionDigestBindsEachComponent 证明指纹对三个组成部分都敏感。
func TestComputeActionDigestBindsEachComponent(t *testing.T) {
	base := ComputeActionDigest("run-1:step-2", "simulated_outreach", map[string]string{"message": "触达 4 人"})
	variants := map[string]string{
		"step_id 变化": ComputeActionDigest("run-1:step-3", "simulated_outreach", map[string]string{"message": "触达 4 人"}),
		"tool_id 变化": ComputeActionDigest("run-1:step-2", "user_query", map[string]string{"message": "触达 4 人"}),
		"参数值变化":      ComputeActionDigest("run-1:step-2", "simulated_outreach", map[string]string{"message": "触达 400 人"}),
		"参数名变化":      ComputeActionDigest("run-1:step-2", "simulated_outreach", map[string]string{"payload": "触达 4 人"}),
		"新增参数":       ComputeActionDigest("run-1:step-2", "simulated_outreach", map[string]string{"message": "触达 4 人", "channel": "sms"}),
	}
	for name, digest := range variants {
		if digest == base {
			t.Errorf("%s 后指纹不变，审批会错误地沿用旧批准", name)
		}
	}
}

// TestComputeActionDigestAvoidsFieldBoundaryCollision 证明长度前缀编码不会被拼接口径绕过。
//
// 朴素拼接（key+value 直接相连）会让 {"a":"bc"} 与 {"ab":"c"} 产生同一串。
func TestComputeActionDigestAvoidsFieldBoundaryCollision(t *testing.T) {
	left := ComputeActionDigest("step", "tool", map[string]string{"a": "bc"})
	right := ComputeActionDigest("step", "tool", map[string]string{"ab": "c"})
	if left == right {
		t.Fatal("不同参数输入得到同一指纹：字段边界未做长度前缀，审批绑定可被构造绕过")
	}
}

// TestComputeActionDigestDegradesClosedWhenUnbindable 覆盖 spec 的"指纹为空或缺失"边界。
//
// step_id 或 tool_id 缺失时无法定义"哪个动作"，必须返回空指纹；空指纹不得匹配
// 任何已批准记录，否则等于放行一个身份不明的动作。
func TestComputeActionDigestDegradesClosedWhenUnbindable(t *testing.T) {
	cases := map[string]string{
		"缺 step_id": ComputeActionDigest("", "simulated_outreach", map[string]string{"message": "x"}),
		"缺 tool_id": ComputeActionDigest("step-1", "", map[string]string{"message": "x"}),
		"两者都缺":      ComputeActionDigest("", "", map[string]string{"message": "x"}),
	}
	for name, digest := range cases {
		if digest != "" {
			t.Errorf("%s 时应降级为空指纹，得到 %q", name, digest)
		}
	}
	if (Request{Status: Approved, ActionDigest: ""}).BindsAction("") {
		t.Error("空指纹的已批准记录匹配了空指纹请求；无法绑定的动作被放行")
	}
}

// TestBindsActionRequiresApprovedAndMatchingDigest 证明绑定判断三个条件缺一不可。
func TestBindsActionRequiresApprovedAndMatchingDigest(t *testing.T) {
	digest := ComputeActionDigest("run-1:step-2", "simulated_outreach", map[string]string{"message": "触达 4 人"})
	other := ComputeActionDigest("run-1:step-2", "simulated_outreach", map[string]string{"message": "触达 400 人"})

	approved := Request{Status: Approved, ActionDigest: digest}
	if !approved.BindsAction(digest) {
		t.Fatal("已批准且指纹相同的记录未绑定该动作")
	}
	if approved.BindsAction(other) {
		t.Error("参数变化后仍判定为同一动作：批准可被复用到另一个动作")
	}
	for name, status := range map[string]Status{"待审批": Pending, "已拒绝": Rejected, "已过期": Expired} {
		record := Request{Status: status, ActionDigest: digest}
		if record.BindsAction(digest) {
			t.Errorf("%s 的记录被判定为已绑定动作", name)
		}
	}
}

// TestRequestSnapshotRoundTripsThroughJSON 守护合同 C9：审批记录可被既有 JSON 快照
// 无损序列化/反序列化（file 适配器依赖这一性质）。
//
// 注意它**不**验证"领域仅标准库"——那是 SC-007 的依赖一侧，由 cmd/archcheck 的
// nonStdlibDomainImport 规则守护（见 cmd/archcheck/main_test.go）。
func TestRequestSnapshotRoundTripsThroughJSON(t *testing.T) {
	record := Request{
		ApprovalID:    "run-1:step-2:approval",
		RunID:         "run-1",
		StepID:        "run-1:step-2",
		ToolID:        "simulated_outreach",
		ActionDigest:  ComputeActionDigest("run-1:step-2", "simulated_outreach", map[string]string{"message": "触达 4 人"}),
		ActionSummary: "模拟触达示例用户",
		Risk:          "side_effect",
		Status:        Approved,
		RequestedAt:   time.Unix(0, 0).UTC(),
		Reviewer:      "subject-1",
		ReviewedAt:    time.Unix(1, 0).UTC(),
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("审批记录序列化失败: %v", err)
	}
	for _, key := range []string{"ActionDigest", "StepID", "ToolID", "ActionSummary", "Reviewer", "ReviewedAt"} {
		if !strings.Contains(string(data), key) {
			t.Errorf("序列化结果缺少 %s: %s", key, data)
		}
	}
	var restored Request
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("审批记录反序列化失败: %v", err)
	}
	if restored != record {
		t.Fatalf("往返后审批记录发生变化:\n want %+v\n got  %+v", record, restored)
	}
}

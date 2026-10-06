package examplebusiness

import (
	"strings"
	"testing"

	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
)

func TestRegistrationReturnsDetachedDescriptors(t *testing.T) {
	workers := Workers()
	workers[0].AllowedTools[0] = "tampered"
	if Workers()[0].AllowedTools[0] != ReadOnlyToolID {
		t.Fatal("worker registration leaked mutable slice state")
	}
	routes := Routes()
	routes[0].Matches[0] = "tampered"
	if Routes()[0].Matches[0] != "分析" {
		t.Fatal("route registration leaked mutable slice state")
	}
}

func TestRegistrationRejectsUnknownBusinessReferences(t *testing.T) {
	if _, ok := WorkerFor("missing_worker"); ok {
		t.Fatal("unknown worker was registered")
	}
	if _, ok := RouteFor("missing_route"); ok {
		t.Fatal("unknown route was registered")
	}
	if ToolImplementationRegistered("missing_tool", ReadOnlyToolFake) {
		t.Fatal("unknown tool was registered")
	}
}

// TestEveryRegisteredToolDeclaresAnActionSummary 守护 FR-005 的来源侧。
//
// 审批文案必须来自注册描述。若某个 Tool 没有 Summary，应用层就没有可用的动作说明，
// 只会退回硬编码文案——因此这里把"注册描述必须自带说明"变成合同。
func TestEveryRegisteredToolDeclaresAnActionSummary(t *testing.T) {
	contracts := ToolContractsWithMySQL(ReadOnlyToolFake)
	if len(contracts) == 0 {
		t.Fatal("没有注册任何 Tool 合同")
	}
	for _, contract := range contracts {
		if strings.TrimSpace(contract.Summary) == "" {
			t.Errorf("Tool %q 未声明 Summary，审批记录将没有可审计的动作说明", contract.ToolID)
		}
	}
	// 副作用 Tool 是最需要审计的那个：它的 Summary 必须存在且与其他 Tool 不同，
	// 否则审批事件无法区分"将要执行的到底是哪类动作"。
	for _, contract := range contracts {
		if contract.Risk != string(agent.RiskSideEffect) {
			continue
		}
		if contract.Summary == "" {
			t.Errorf("副作用 Tool %q 缺少 Summary", contract.ToolID)
		}
	}
}

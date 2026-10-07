package tool

import (
	"context"
	"strings"
	"testing"

	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/examplebusiness"
)

func TestFakeRegistryIsIdempotent(t *testing.T) {
	registry := examplebusiness.NewFakeRuntime()
	input := map[string]string{"message": `{"customer_ids":["cust-001","cust-002"]}`}
	for i := 0; i < 2; i++ {
		if _, err := registry.Execute(context.Background(), "simulated_outreach", input, "run-1:step-1"); err != nil {
			t.Fatal(err)
		}
	}
	if registry.OutreachCount() != 1 {
		t.Fatalf("count = %d, want 1", registry.OutreachCount())
	}
}

func TestFakeRegistrySyntheticAudienceAndSummary(t *testing.T) {
	registry := examplebusiness.NewFakeRuntime()
	audience, err := registry.Execute(context.Background(), "user_query", map[string]string{"message": `{"as_of":"2026-09-25"}`}, "run-1:step-1")
	if err != nil {
		t.Fatal(err)
	}
	if audience != `{"count":4,"customer_ids":["cust-001","cust-002","cust-006","cust-008"],"spend_365d_total":6200}` {
		t.Fatalf("audience = %s", audience)
	}
	summary, err := registry.Execute(context.Background(), "user_summary_query", map[string]string{"message": audience}, "run-1:step-2")
	if err != nil {
		t.Fatal(err)
	}
	if summary != `{"count":4,"spend_365d_total":6200,"segments":["dormant","consented"]}` {
		t.Fatalf("summary = %s", summary)
	}
}

func TestFakeRegistryRejectsIdentityAndOversizedHandoff(t *testing.T) {
	registry := examplebusiness.NewFakeRuntime()
	for _, input := range []string{
		`{"count":1,"customer_ids":["cust-001"],"spend_365d_total":1200,"name":"Alice"}`,
		`{"count":1,"customer_ids":["cust-001"],"spend_365d_total":1200,"email":"alice@example.com"}`,
		`{"count":1,"customer_ids":["cust-001"],"spend_365d_total":1200,"phone":"555"}`,
		`{"count":1,"customer_ids":["cust-001"],"spend_365d_total":1200,"extra":true}`,
		`{"count":2,"customer_ids":["cust-001"],"spend_365d_total":1200}`,
		`{"count":1,"customer_ids":["user-001"],"spend_365d_total":1200}`,
	} {
		if _, err := registry.Execute(context.Background(), "user_summary_query", map[string]string{"message": input}, "run-1:step-2"); err == nil {
			t.Fatalf("invalid handoff accepted: %s", input)
		}
	}
	if _, err := registry.Execute(context.Background(), "user_summary_query", map[string]string{"message": strings.Repeat("x", 70<<10)}, "run-1:step-2"); err == nil {
		t.Fatal("oversized handoff accepted")
	}
}

func TestRegistryRejectsUnknownToolID(t *testing.T) {
	registry, err := NewRegistry(0, examplebusiness.ToolContracts(examplebusiness.ReadOnlyToolFake))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Execute(context.Background(), "not_registered", nil, ""); err == nil {
		t.Fatal("expected unknown tool rejection")
	}
}

func TestContractsAreExplicitAndStable(t *testing.T) {
	contracts := examplebusiness.ToolContracts(examplebusiness.ReadOnlyToolMCP)
	if len(contracts) != 4 || contracts[0].ToolID != "user_query" || contracts[2].ToolID != "simulated_outreach" || contracts[3].ToolID != "raw_audience_export" {
		t.Fatalf("unexpected descriptor registry: %#v", contracts)
	}
	if contracts[0].Implementation != "mcp.read_only" || !contracts[2].RequiresApproval || !contracts[2].IdempotencyRequired {
		t.Fatalf("descriptor contract lost safety fields: %#v", contracts)
	}
	// 原始导出是只读能力：它必须**不**要求审批、**不**需要幂等键，否则会被误当成
	// 副作用动作拦住，而它真正的保护点是 Final Guard（上游内容不得原样投影）。
	if contracts[3].Implementation != examplebusiness.RawExportToolFake || contracts[3].RequiresApproval || contracts[3].IdempotencyRequired || contracts[3].Risk != "read_only" {
		t.Fatalf("raw export contract is not a plain read-only tool: %#v", contracts[3])
	}
}

func TestRegistryValidatesRegisteredToolInputAndOutput(t *testing.T) {
	registry, err := NewRegistry(0, examplebusiness.ToolContracts(examplebusiness.ReadOnlyToolFake))
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateInput("user_query", map[string]string{}); err == nil {
		t.Fatal("expected required input rejection")
	}
	if err := registry.ValidateInput("user_query", map[string]string{"message": "查询"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateOutput("user_query", "\x00unsafe"); err == nil {
		t.Fatal("expected malformed output rejection")
	}
}

func TestRegistryRejectsDuplicateContractsAndHandlerBindings(t *testing.T) {
	contracts := examplebusiness.ToolContracts(examplebusiness.ReadOnlyToolFake)[:1]
	if _, err := NewRegistry(0, append(contracts, contracts[0])); err == nil {
		t.Fatal("duplicate contract accepted")
	}
	registry, err := NewRegistry(0, contracts)
	if err != nil {
		t.Fatal(err)
	}
	handler := func(context.Context, map[string]string, string) (string, error) { return "ok", nil }
	if err := registry.RegisterHandler("missing", handler); err == nil {
		t.Fatal("handler for unknown contract accepted")
	}
	if err := registry.RegisterHandler(examplebusiness.ReadOnlyToolID, handler); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterHandler(examplebusiness.ReadOnlyToolID, handler); err == nil {
		t.Fatal("duplicate handler accepted")
	}
}

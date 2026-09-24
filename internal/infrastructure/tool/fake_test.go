package tool

import (
	"context"
	"testing"
)

func TestFakeRegistryIsIdempotent(t *testing.T) {
	registry := NewFakeRegistry()
	input := map[string]string{"message": "模拟触达"}
	for i := 0; i < 2; i++ {
		if _, err := registry.Execute(context.Background(), "simulated_outreach", input, "run-1:step-1"); err != nil {
			t.Fatal(err)
		}
	}
	if registry.OutreachCount() != 1 {
		t.Fatalf("count = %d, want 1", registry.OutreachCount())
	}
}

func TestRegistryRejectsUnknownToolID(t *testing.T) {
	registry, err := NewRegistry("fake.user_query", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Execute(context.Background(), "not_registered", nil, ""); err == nil {
		t.Fatal("expected unknown tool rejection")
	}
}

func TestContractsAreExplicitAndStable(t *testing.T) {
	contracts := ContractsFor("mcp.read_only")
	if len(contracts) != 2 || contracts[0].ToolID != "user_query" || contracts[1].ToolID != "simulated_outreach" {
		t.Fatalf("unexpected descriptor registry: %#v", contracts)
	}
	if contracts[0].Implementation != "mcp.read_only" || !contracts[1].RequiresApproval || !contracts[1].IdempotencyRequired {
		t.Fatalf("descriptor contract lost safety fields: %#v", contracts)
	}
}

func TestRegistryValidatesRegisteredToolInputAndOutput(t *testing.T) {
	registry, err := NewRegistry("fake.user_query", "", 0)
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

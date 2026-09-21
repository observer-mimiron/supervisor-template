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

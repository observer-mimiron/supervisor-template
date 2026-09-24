package checkpoint

import (
	"testing"
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
)

func TestMemoryStoreIncrementsVersion(t *testing.T) {
	store := NewMemoryStore()
	base := agent.Checkpoint{RunID: "run-1", PlanID: "plan-1", Status: agent.RunRunning, Version: 1, SavedAt: time.Now()}
	if _, err := store.Save(base); err != nil {
		t.Fatal(err)
	}
	base.Version = 2
	if _, err := store.Save(base); err != nil {
		t.Fatal(err)
	}
	base.Version = 4
	if _, err := store.Save(base); err == nil {
		t.Fatal("expected version gap rejection")
	}
}

func TestMemoryStoreReturnsCloneIsolation(t *testing.T) {
	store := NewMemoryStore()
	snapshot := agent.Checkpoint{RunID: "run-clone", PlanID: "plan-1", Status: agent.RunRunning, Version: 1, CompletedStepIDs: []string{"step-1"}}
	if _, err := store.Save(snapshot); err != nil {
		t.Fatal(err)
	}
	loaded, ok := store.Get(snapshot.RunID)
	if !ok {
		t.Fatal("checkpoint not found")
	}
	loaded.CompletedStepIDs[0] = "tampered"
	again, _ := store.Get(snapshot.RunID)
	if again.CompletedStepIDs[0] != "step-1" {
		t.Fatalf("checkpoint leaked mutable state: %#v", again)
	}
}

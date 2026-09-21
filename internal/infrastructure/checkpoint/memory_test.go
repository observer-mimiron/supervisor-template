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

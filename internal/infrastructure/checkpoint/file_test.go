package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
)

func TestFileStorePersistsAcrossInstancesAndUsesAtomicFiles(t *testing.T) {
	dir := t.TempDir()
	first, err := NewFileStore(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := agent.Checkpoint{RunID: "../../outside", PlanID: "plan-1", Status: agent.RunRunning, Version: 1, NextStepID: "step-1", SavedAt: time.Now()}
	if _, err := first.Save(snapshot); err != nil {
		t.Fatal(err)
	}
	second, err := NewFileStore(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := second.GetWithError(snapshot.RunID)
	if err != nil || !ok || got.NextStepID != snapshot.NextStepID {
		t.Fatalf("read = %#v, %v, %v", got, ok, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "..", "outside")); !os.IsNotExist(err) {
		t.Fatalf("run id escaped checkpoint directory: %v", err)
	}
	if files, err := filepath.Glob(filepath.Join(dir, ".checkpoint-*.tmp")); err != nil || len(files) != 0 {
		t.Fatalf("temporary files remain: %v, %v", files, err)
	}
}

func TestFileStoreRejectsUnsupportedVersionAndCorruptFile(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"version": `{"schema_version":99}`, "corrupt": "{"} {
		runID := "run-" + name
		digest := sha256.Sum256([]byte(runID))
		path := filepath.Join(dir, hex.EncodeToString(digest[:])+".json")
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		_, ok, err := store.GetWithError(runID)
		if ok || err == nil || !strings.Contains(err.Error(), map[string]string{"version": "版本", "corrupt": "解析"}[name]) {
			t.Fatalf("%s read = %v, %v", name, ok, err)
		}
		if _, ok := store.Get(runID); ok {
			t.Fatalf("legacy Get accepted %s", name)
		}
	}
}

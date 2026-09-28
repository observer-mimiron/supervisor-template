package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
)

func TestFileStorePersistsAndRejectsCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory", "working.json")
	store, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	scope := conversation.MemoryScope{TenantID: "tenant", SubjectID: "subject", ConversationID: "conversation", Kind: conversation.MemoryWorking}
	if _, err := store.Append(context.Background(), scope, conversation.MemoryEntry{MemoryID: "m1", Scope: scope, Content: "remember this"}); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Query(context.Background(), conversation.MemoryQuery{Scope: scope, Limit: 1, MaxBytes: 64})
	if err != nil || len(got) != 1 || got[0].Content != "remember this" {
		t.Fatalf("restored memory=%#v err=%v", got, err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileStore(path); err == nil {
		t.Fatal("expected corrupt snapshot rejection")
	}
}

package memory

import (
	"context"
	"testing"
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
)

func TestStoreScopesFiltersAndBoundsRecall(t *testing.T) {
	store := NewStore()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	scope := conversation.MemoryScope{TenantID: "tenant-a", SubjectID: "subject-a", ConversationID: "chat-a", Kind: conversation.MemoryWorking}
	other := scope
	other.SubjectID = "subject-b"
	entries := []conversation.MemoryEntry{
		{MemoryID: "1", Scope: scope, Content: "first note"},
		{MemoryID: "2", Scope: scope, Content: "second note"},
		{MemoryID: "secret", Scope: scope, Content: "safe text", Sensitivity: conversation.MemorySensitive},
		{MemoryID: "expired", Scope: scope, Content: "old", ExpiresAt: now},
		{MemoryID: "other", Scope: other, Content: "private"},
	}
	for _, entry := range entries {
		if _, err := store.Append(context.Background(), entry.Scope, entry); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.Query(context.Background(), conversation.MemoryQuery{Scope: scope, Limit: 10, MaxBytes: 32, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].MemoryID != "1" || got[1].MemoryID != "2" {
		t.Fatalf("unsafe or unordered recall: %#v", got)
	}
	got, err = store.Query(context.Background(), conversation.MemoryQuery{Scope: scope, Limit: 10, MaxBytes: 5, Now: now})
	if err != nil || len(got) != 0 {
		t.Fatalf("oversized recall = %#v, err=%v", got, err)
	}
	if _, err := store.Query(context.Background(), conversation.MemoryQuery{Scope: scope, Limit: maxRecallEntries + 1, MaxBytes: 1}); err == nil {
		t.Fatal("expected hard recall limit")
	}
}

func TestStoreRejectsSensitiveAndInvalidEntries(t *testing.T) {
	store := NewStore()
	scope := conversation.MemoryScope{TenantID: "t", SubjectID: "s", ConversationID: "c", Kind: conversation.MemoryWorking}
	for _, content := range []string{"Authorization: Bearer secret", "bad\x00value"} {
		entry := conversation.MemoryEntry{Scope: scope, Content: content}
		if _, err := store.Append(context.Background(), scope, entry); err == nil {
			t.Fatalf("accepted unsafe memory %q", content)
		}
	}
}

func TestStoreExpireRemovesExpiredEntries(t *testing.T) {
	store := NewStore()
	now := time.Now()
	scope := conversation.MemoryScope{TenantID: "t", SubjectID: "s", ConversationID: "c", Kind: conversation.MemoryWorking}
	_, err := store.Append(context.Background(), scope, conversation.MemoryEntry{MemoryID: "old", Scope: scope, Content: "note", ExpiresAt: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	removed, err := store.Expire(context.Background(), now.Add(2*time.Second))
	if err != nil || removed != 1 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
}

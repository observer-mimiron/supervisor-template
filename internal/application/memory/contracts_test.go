package memory

import (
	"context"
	"testing"
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
)

func TestQueryRequiresCompleteWorkingScope(t *testing.T) {
	q := conversation.MemoryQuery{Scope: conversation.MemoryScope{TenantID: "t", SubjectID: "s", ConversationID: "c", Kind: conversation.MemoryWorking}, Limit: 1, MaxBytes: 32}
	if err := q.Validate(); err != nil {
		t.Fatal(err)
	}
	q.Scope.SubjectID = ""
	if err := q.Validate(); err == nil {
		t.Fatal("expected incomplete scope rejection")
	}
}

var _ Store = fakeStore{}
var _ Retriever = fakeStore{}

type fakeStore struct{}

func (fakeStore) Append(context.Context, conversation.MemoryScope, conversation.MemoryEntry) (conversation.MemoryEntry, error) {
	return conversation.MemoryEntry{}, nil
}
func (fakeStore) Expire(context.Context, time.Time) (int, error) { return 0, nil }
func (fakeStore) Query(context.Context, conversation.MemoryQuery) ([]conversation.MemoryEntry, error) {
	return nil, nil
}

package memory

import (
	"context"
	"errors"
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
)

var (
	ErrInvalidScope = errors.New("记忆作用域无效")
	ErrMemoryLimit  = errors.New("记忆召回超过上限")
)

// Store is the application port for bounded conversation memory.
type Store interface {
	Append(context.Context, conversation.MemoryScope, conversation.MemoryEntry) (conversation.MemoryEntry, error)
	Expire(context.Context, time.Time) (int, error)
	Query(context.Context, conversation.MemoryQuery) ([]conversation.MemoryEntry, error)
}

// Retriever is the read-only view used by workers.
type Retriever interface {
	Query(context.Context, conversation.MemoryQuery) ([]conversation.MemoryEntry, error)
}

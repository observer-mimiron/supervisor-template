// Package memory provides bounded short-term conversation memory adapters.
package memory

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	appmemory "github.com/observer-mimiron/supervisor-template/internal/application/memory"
	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
)

const (
	defaultTTL       = 24 * time.Hour
	maxEntryBytes    = 4096
	maxRecallEntries = 50
	maxRecallBytes   = 16 << 10
)

// Store keeps short-lived working memory in process.
type Store struct {
	mu       sync.RWMutex
	entries  map[string][]conversation.MemoryEntry
	now      func() time.Time
	sequence atomic.Uint64
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{entries: make(map[string][]conversation.MemoryEntry), now: time.Now}
}

func (s *Store) Append(ctx context.Context, scope conversation.MemoryScope, entry conversation.MemoryEntry) (conversation.MemoryEntry, error) {
	if err := ctx.Err(); err != nil {
		return conversation.MemoryEntry{}, err
	}
	if !scope.Valid() || entry.Scope != scope || strings.TrimSpace(entry.Content) == "" || !utf8.ValidString(entry.Content) || !safeEntry(entry) || len(entry.Content) > maxEntryBytes {
		return conversation.MemoryEntry{}, appmemory.ErrInvalidScope
	}
	now := s.now()
	entry = cloneEntry(entry)
	if entry.MemoryID == "" {
		entry.MemoryID = "memory-" + now.Format("20060102150405.000000000") + "-" + strconv.FormatUint(s.sequence.Add(1), 10)
	}
	if entry.Version <= 0 {
		entry.Version = 1
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = now
	}
	if entry.ExpiresAt.IsZero() {
		entry.ExpiresAt = entry.CreatedAt.Add(defaultTTL)
	}
	if entry.Sensitivity == "" {
		entry.Sensitivity = conversation.MemoryNormal
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := scope.Key()
	for _, existing := range s.entries[key] {
		if existing.MemoryID == entry.MemoryID {
			return conversation.MemoryEntry{}, errors.New("记忆 ID 重复")
		}
		if existing.Version >= entry.Version {
			entry.Version = existing.Version + 1
		}
	}
	s.entries[key] = append(s.entries[key], entry)
	return cloneEntry(entry), nil
}

func (s *Store) Expire(ctx context.Context, now time.Time) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if now.IsZero() {
		now = s.now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for key, entries := range s.entries {
		kept := entries[:0]
		for _, entry := range entries {
			if entry.Expired(now) {
				removed++
				continue
			}
			kept = append(kept, entry)
		}
		if len(kept) == 0 {
			delete(s.entries, key)
		} else {
			s.entries[key] = kept
		}
	}
	return removed, nil
}

func (s *Store) Query(ctx context.Context, query conversation.MemoryQuery) ([]conversation.MemoryEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := query.Validate(); err != nil {
		return nil, err
	}
	if query.Limit > maxRecallEntries || query.MaxBytes > maxRecallBytes {
		return nil, appmemory.ErrMemoryLimit
	}
	if query.Now.IsZero() {
		query.Now = s.now()
	}
	s.mu.RLock()
	entries := append([]conversation.MemoryEntry(nil), s.entries[query.Scope.Key()]...)
	s.mu.RUnlock()
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].CreatedAt.Equal(entries[j].CreatedAt) {
			return entries[i].MemoryID < entries[j].MemoryID
		}
		return entries[i].CreatedAt.Before(entries[j].CreatedAt)
	})
	result := make([]conversation.MemoryEntry, 0, min(query.Limit, len(entries)))
	used := 0
	needle := strings.ToLower(strings.TrimSpace(query.Query))
	for _, entry := range entries {
		if entry.Expired(query.Now) || entry.Sensitivity == conversation.MemorySensitive || !safeEntry(entry) {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(entry.Content), needle) {
			continue
		}
		if len(result) >= query.Limit || used+len(entry.Content) > query.MaxBytes {
			continue
		}
		result = append(result, cloneEntry(entry))
		used += len(entry.Content)
	}
	return result, nil
}

func safeEntry(entry conversation.MemoryEntry) bool {
	if !utf8.ValidString(entry.Content) {
		return false
	}
	value := strings.ToLower(entry.Content)
	for _, marker := range []string{"authorization:", "bearer ", "api_key", "apikey", "password=", "secret=", "sk-", "system prompt", "原始 prompt"} {
		if strings.Contains(value, marker) {
			return false
		}
	}
	for _, r := range entry.Content {
		if r == 0 || (r < 0x20 && r != '\n' && r != '\r' && r != '\t') {
			return false
		}
	}
	for key := range entry.Metadata {
		k := strings.ToLower(key)
		for _, marker := range []string{"token", "secret", "password", "authorization", "prompt", "credential"} {
			if strings.Contains(k, marker) {
				return false
			}
		}
	}
	return true
}

func cloneEntry(entry conversation.MemoryEntry) conversation.MemoryEntry {
	if entry.Metadata != nil {
		metadata := make(map[string]string, len(entry.Metadata))
		for key, value := range entry.Metadata {
			metadata[key] = value
		}
		entry.Metadata = metadata
	}
	return entry
}

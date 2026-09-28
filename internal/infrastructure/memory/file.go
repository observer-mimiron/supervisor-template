package memory

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
)

type memorySnapshot struct {
	Version int                                   `json:"version"`
	Entries map[string][]conversation.MemoryEntry `json:"entries"`
}

// FileStore persists one process-local working-memory snapshot with atomic replacement.
type FileStore struct {
	mu    sync.Mutex
	path  string
	store *Store
}

// OpenFileStore loads a versioned snapshot. Concurrent writers across processes are unsupported.
func OpenFileStore(path string) (*FileStore, error) {
	if path == "" {
		return nil, errors.New("memory 文件路径不能为空")
	}
	fs := &FileStore{path: path, store: NewStore()}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := fs.persist(); err != nil {
			return nil, err
		}
		return fs, nil
	}
	if err != nil {
		return nil, err
	}
	var snapshot memorySnapshot
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return nil, errors.New("memory 文件损坏")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("memory 文件包含尾随数据")
	}
	if snapshot.Version != 1 || snapshot.Entries == nil {
		return nil, errors.New("memory 文件版本不兼容")
	}
	for key, entries := range snapshot.Entries {
		for _, entry := range entries {
			if !entry.Valid() || entry.Scope.Key() != key || !safeEntry(entry) || len(entry.Content) > maxEntryBytes {
				return nil, errors.New("memory 文件包含非法记录")
			}
		}
	}
	fs.store.entries = snapshot.Entries
	return fs, nil
}

func (f *FileStore) Append(ctx context.Context, scope conversation.MemoryScope, entry conversation.MemoryEntry) (conversation.MemoryEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	before := f.snapshotEntries()
	stored, err := f.store.Append(ctx, scope, entry)
	if err != nil {
		return conversation.MemoryEntry{}, err
	}
	if err := f.persist(); err != nil {
		f.store.entries = before
		return conversation.MemoryEntry{}, err
	}
	return stored, nil
}

func (f *FileStore) Expire(ctx context.Context, now time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	before := f.snapshotEntries()
	count, err := f.store.Expire(ctx, now)
	if err != nil || count == 0 {
		return count, err
	}
	if err := f.persist(); err != nil {
		f.store.entries = before
		return 0, err
	}
	return count, nil
}

func (f *FileStore) Query(ctx context.Context, query conversation.MemoryQuery) ([]conversation.MemoryEntry, error) {
	return f.store.Query(ctx, query)
}

func (f *FileStore) snapshotEntries() map[string][]conversation.MemoryEntry {
	f.store.mu.RLock()
	defer f.store.mu.RUnlock()
	entries := make(map[string][]conversation.MemoryEntry, len(f.store.entries))
	for key, values := range f.store.entries {
		cloned := make([]conversation.MemoryEntry, len(values))
		for i, entry := range values {
			cloned[i] = cloneEntry(entry)
		}
		entries[key] = cloned
	}
	return entries
}

func (f *FileStore) persist() error {
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(memorySnapshot{Version: 1, Entries: f.snapshotEntries()})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.path), ".memory-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, f.path)
}

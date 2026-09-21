// Package eventbus 提供可重放 RunEvent 的事件记录器。
//
// 本文件把事件序列保存到磁盘，使持久化 run 在新进程中仍能重放终态；
// 事件总线只负责顺序和唯一终态，不负责推进业务状态。
package eventbus

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
)

const fileSchemaVersion = 1

// FileBus 为每个 run 保存完整有序事件列表。
type FileBus struct {
	mu  sync.Mutex
	dir string
}

type eventRecord struct {
	SchemaVersion int              `json:"schema_version"`
	Events        []agent.RunEvent `json:"events"`
}

// NewFileBus 创建文件版事件总线。
func NewFileBus(dir string) (*FileBus, error) {
	if dir == "" {
		return nil, errors.New("事件目录不能为空")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建事件目录失败: %w", err)
	}
	return &FileBus{dir: dir}, nil
}

// Append 追加连续事件，并拒绝重复终态。
func (b *FileBus) Append(event agent.RunEvent) (agent.RunEvent, error) {
	if event.RunID == "" || event.EventID == "" || event.Type == "" {
		return agent.RunEvent{}, errors.New("事件缺少必要字段")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	record, err := b.readLocked(event.RunID)
	if err != nil {
		return agent.RunEvent{}, err
	}
	expected := int64(len(record.Events) + 1)
	if event.Sequence == 0 {
		event.Sequence = expected
	}
	if event.Sequence != expected {
		return agent.RunEvent{}, errors.New("事件序列必须从 1 开始连续递增")
	}
	if isTerminalEvent(event.Type) {
		for _, item := range record.Events {
			if isTerminalEvent(item.Type) {
				return agent.RunEvent{}, errors.New("每个 run 只能有一个终态事件")
			}
		}
	}
	record.Events = append(record.Events, cloneEvent(event))
	if err := b.writeLocked(event.RunID, record); err != nil {
		return agent.RunEvent{}, err
	}
	return event, nil
}

// Events 返回某个 run 的事件副本。
func (b *FileBus) Events(runID string) []agent.RunEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	record, err := b.readLocked(runID)
	if err != nil {
		return nil
	}
	return cloneEvents(record.Events)
}

// readLocked 读取事件列表，并兼容缺失 schema_version 的早期文件。
func (b *FileBus) readLocked(runID string) (eventRecord, error) {
	data, err := os.ReadFile(b.path(runID))
	if errors.Is(err, os.ErrNotExist) {
		return eventRecord{SchemaVersion: fileSchemaVersion}, nil
	}
	if err != nil {
		return eventRecord{}, fmt.Errorf("读取事件失败: %w", err)
	}
	var record eventRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return eventRecord{}, fmt.Errorf("解析事件失败: %w", err)
	}
	if record.SchemaVersion != 0 && record.SchemaVersion != fileSchemaVersion {
		return eventRecord{}, fmt.Errorf("事件版本 %d 不受支持", record.SchemaVersion)
	}
	record.SchemaVersion = fileSchemaVersion
	return record, nil
}

// writeLocked 使用原子替换保存事件列表。
func (b *FileBus) writeLocked(runID string, record eventRecord) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("编码事件失败: %w", err)
	}
	tmp, err := os.CreateTemp(b.dir, ".events-*.tmp")
	if err != nil {
		return fmt.Errorf("创建事件临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("设置事件权限失败: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入事件失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("同步事件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭事件失败: %w", err)
	}
	if err := os.Rename(tmpName, b.path(runID)); err != nil {
		return fmt.Errorf("替换事件失败: %w", err)
	}
	return nil
}

// path 使用哈希文件名，避免 run_id 进入文件路径。
func (b *FileBus) path(runID string) string {
	digest := sha256.Sum256([]byte(runID))
	return filepath.Join(b.dir, hex.EncodeToString(digest[:])+".json")
}

// isTerminalEvent 判断事件是否为唯一终态事件。
func isTerminalEvent(eventType agent.EventType) bool {
	return eventType == agent.Completed || eventType == agent.Failed || eventType == agent.Canceled
}

// cloneEvent 复制事件数据，避免外部修改事件总线内容。
func cloneEvent(event agent.RunEvent) agent.RunEvent {
	if event.Data != nil {
		event.Data = make(map[string]string, len(event.Data))
		for key, value := range event.Data {
			event.Data[key] = value
		}
	}
	return event
}

// cloneEvents 复制事件列表及其数据。
func cloneEvents(events []agent.RunEvent) []agent.RunEvent {
	cloned := make([]agent.RunEvent, len(events))
	for index, event := range events {
		cloned[index] = cloneEvent(event)
	}
	return cloned
}

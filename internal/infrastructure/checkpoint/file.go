// Package checkpoint 提供可替换的恢复快照实现。
//
// 本文件把最小 Checkpoint 合同落到磁盘，使用原子替换支持跨进程读取；
// 不保存模型消息、Prompt、凭证或完整业务上下文。
package checkpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
)

func (s *FileStore) SaveContext(ctx context.Context, snapshot agent.Checkpoint) (agent.Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return agent.Checkpoint{}, err
	}
	return s.Save(snapshot)
}

func (s *FileStore) GetContext(ctx context.Context, runID string) (agent.Checkpoint, bool) {
	if err := ctx.Err(); err != nil {
		return agent.Checkpoint{}, false
	}
	return s.Get(runID)
}

func (s *FileStore) GetWithContext(ctx context.Context, runID string) (agent.Checkpoint, bool, error) {
	if err := ctx.Err(); err != nil {
		return agent.Checkpoint{}, false, err
	}
	return s.GetWithError(runID)
}

const fileSchemaVersion = 1

// FileStore 保存每个 run 的最新 Checkpoint JSON 快照。
//
// retention 决定快照的保留时长，来自 checkpoint.retention。清理是机会式的：在写入时
// 顺带扫描并删除过期文件，因此不需要额外的后台协程或生命周期管理。零值表示不清理。
type FileStore struct {
	mu         sync.Mutex
	dir        string
	retention  time.Duration
	lastPruned time.Time
}

type checkpointRecord struct {
	SchemaVersion int              `json:"schema_version"`
	Snapshot      agent.Checkpoint `json:"snapshot"`
}

// NewFileStore 创建文件版 checkpoint 存储。
func NewFileStore(dir string, retention time.Duration) (*FileStore, error) {
	if dir == "" {
		return nil, errors.New("checkpoint 目录不能为空")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建 checkpoint 目录失败: %w", err)
	}
	return &FileStore{dir: dir, retention: retention}, nil
}

// pruneLocked 删除超过保留期的快照文件。
//
// 清理失败不影响恢复主链路：快照仍然是最新的，只是磁盘回收推迟到下一次写入。
// 扫描按 retention 的十分之一节流，避免每次写入都遍历目录。
func (s *FileStore) pruneLocked(now time.Time) error {
	if s.retention <= 0 {
		return nil
	}
	interval := s.retention / 10
	if interval <= 0 {
		interval = time.Minute
	}
	if !s.lastPruned.IsZero() && now.Sub(s.lastPruned) < interval {
		return nil
	}
	s.lastPruned = now
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("读取 checkpoint 目录失败: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		if now.Sub(info.ModTime()) < s.retention {
			continue
		}
		_ = os.Remove(filepath.Join(s.dir, entry.Name()))
	}
	return nil
}

// Save 保存快照并保证领域版本连续递增。
func (s *FileStore) Save(snapshot agent.Checkpoint) (agent.Checkpoint, error) {
	if snapshot.RunID == "" || snapshot.PlanID == "" {
		return agent.Checkpoint{}, errors.New("checkpoint 缺少 run_id 或 plan_id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, ok, err := s.readLocked(snapshot.RunID)
	if err != nil {
		return agent.Checkpoint{}, err
	}
	if ok {
		if snapshot.Version != previous.Version+1 {
			return agent.Checkpoint{}, errors.New("checkpoint 版本必须连续递增")
		}
	} else if snapshot.Version != 1 {
		return agent.Checkpoint{}, errors.New("首个 checkpoint 版本必须为 1")
	}
	if err := s.writeLocked(snapshot); err != nil {
		return agent.Checkpoint{}, err
	}
	return snapshot, nil
}

// Get 读取某个 run 的最新 checkpoint。
func (s *FileStore) Get(runID string) (agent.Checkpoint, bool) {
	snapshot, ok, _ := s.GetWithError(runID)
	return snapshot, ok
}

// GetWithError 读取 checkpoint，并保留损坏文件和不兼容版本错误。
func (s *FileStore) GetWithError(runID string) (agent.Checkpoint, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, ok, err := s.readLocked(runID)
	if err != nil || !ok {
		return agent.Checkpoint{}, false, err
	}
	return cloneCheckpoint(snapshot), true, nil
}

// readLocked 兼容未写 schema_version 的早期快照，并拒绝未知版本。
func (s *FileStore) readLocked(runID string) (agent.Checkpoint, bool, error) {
	data, err := os.ReadFile(s.path(runID))
	if errors.Is(err, os.ErrNotExist) {
		return agent.Checkpoint{}, false, nil
	}
	if err != nil {
		return agent.Checkpoint{}, false, fmt.Errorf("读取 checkpoint 失败: %w", err)
	}
	var record checkpointRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return agent.Checkpoint{}, false, fmt.Errorf("解析 checkpoint 失败: %w", err)
	}
	if record.SchemaVersion != 0 && record.SchemaVersion != fileSchemaVersion {
		return agent.Checkpoint{}, false, fmt.Errorf("checkpoint 版本 %d 不受支持", record.SchemaVersion)
	}
	return record.Snapshot, true, nil
}

// writeLocked 用临时文件加 rename，避免中断造成不可解析的恢复快照。
func (s *FileStore) writeLocked(snapshot agent.Checkpoint) error {
	data, err := json.MarshalIndent(checkpointRecord{SchemaVersion: fileSchemaVersion, Snapshot: snapshot}, "", "  ")
	if err != nil {
		return fmt.Errorf("编码 checkpoint 失败: %w", err)
	}
	tmp, err := os.CreateTemp(s.dir, ".checkpoint-*.tmp")
	if err != nil {
		return fmt.Errorf("创建 checkpoint 临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("设置 checkpoint 权限失败: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入 checkpoint 失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("同步 checkpoint 失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭 checkpoint 失败: %w", err)
	}
	if err := os.Rename(tmpName, s.path(snapshot.RunID)); err != nil {
		return fmt.Errorf("替换 checkpoint 失败: %w", err)
	}
	_ = s.pruneLocked(time.Now())
	return nil
}

// path 使用哈希文件名，避免 run_id 进入文件路径。
func (s *FileStore) path(runID string) string {
	digest := sha256.Sum256([]byte(runID))
	return filepath.Join(s.dir, hex.EncodeToString(digest[:])+".json")
}

// cloneCheckpoint 复制切片，避免调用方修改存储内的快照。
func cloneCheckpoint(snapshot agent.Checkpoint) agent.Checkpoint {
	snapshot.CompletedStepIDs = append([]string(nil), snapshot.CompletedStepIDs...)
	return snapshot
}

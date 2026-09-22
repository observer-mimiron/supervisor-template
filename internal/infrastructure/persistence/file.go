// Package persistence 提供运行请求、计划和审批快照的持久化适配器。
//
// 本文件使用按 run 分文件的 JSON 快照和原子替换，负责跨进程恢复所需的最小状态；
// 不改变领域状态所有权，也不保存 Prompt、凭证或完整上下文。
package persistence

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
	"github.com/observer-mimiron/supervisor-template/internal/domain/approval"
	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
)

const fileSchemaVersion = 1

// FileRepository 将一个 run 的恢复快照保存为单个 JSON 文件。
type FileRepository struct {
	mu  sync.Mutex
	dir string
}

type runRecord struct {
	SchemaVersion int                            `json:"schema_version"`
	Request       *conversation.ExecutionRequest `json:"request,omitempty"`
	Approval      *approval.Request              `json:"approval,omitempty"`
	Plan          *agent.ExecutionPlan           `json:"plan,omitempty"`
}

// NewFileRepository 创建文件版 Repository，并确保目录权限为仅当前用户可读写。
func NewFileRepository(dir string) (*FileRepository, error) {
	if dir == "" {
		return nil, errors.New("持久化目录不能为空")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建持久化目录失败: %w", err)
	}
	return &FileRepository{dir: dir}, nil
}

// SaveRequest 保存请求，并拒绝使用同一 run_id 换绑另一请求。
func (r *FileRepository) SaveRequest(request conversation.ExecutionRequest) error {
	if request.RunID == "" || request.ConversationID == "" {
		return errors.New("请求快照缺少 run_id 或 conversation_id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := r.readLocked(request.RunID)
	if err != nil {
		return err
	}
	if record.Request != nil && *record.Request != request {
		return errors.New("run_id 已绑定其他请求")
	}
	record.Request = &request
	return r.writeLocked(request.RunID, record)
}

// GetRequest 读取一个已持久化的请求快照。
func (r *FileRepository) GetRequest(runID string) (conversation.ExecutionRequest, bool) {
	request, ok, _ := r.LoadRequest(runID)
	return request, ok
}

// LoadRequest 读取请求并保留损坏文件和不兼容版本错误。
func (r *FileRepository) LoadRequest(runID string) (conversation.ExecutionRequest, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := r.readLocked(runID)
	if err != nil || record.Request == nil {
		return conversation.ExecutionRequest{}, false, err
	}
	return *record.Request, true, nil
}

// SaveApproval 保存审批状态，供重启后的 resume 继续使用原审批结果。
func (r *FileRepository) SaveApproval(request approval.Request) error {
	if request.RunID == "" || request.ApprovalID == "" {
		return errors.New("审批快照缺少必要字段")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := r.readLocked(request.RunID)
	if err != nil {
		return err
	}
	if record.Approval != nil && record.Approval.Status != approval.Pending && record.Approval.Status != request.Status {
		return errors.New("审批终态不可覆盖")
	}
	record.Approval = &request
	return r.writeLocked(request.RunID, record)
}

// GetApproval 读取一个 run 的审批快照。
func (r *FileRepository) GetApproval(runID string) (approval.Request, bool) {
	request, ok, _ := r.LoadApproval(runID)
	return request, ok
}

// LoadApproval 读取审批并保留损坏文件和不兼容版本错误。
func (r *FileRepository) LoadApproval(runID string) (approval.Request, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := r.readLocked(runID)
	if err != nil || record.Approval == nil {
		return approval.Request{}, false, err
	}
	return *record.Approval, true, nil
}

// SavePlan 保存计划，并拒绝把已完成步骤回写为未完成。
func (r *FileRepository) SavePlan(plan agent.ExecutionPlan) error {
	if plan.RunID == "" || plan.PlanID == "" {
		return errors.New("计划快照缺少 run_id 或 plan_id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := r.readLocked(plan.RunID)
	if err != nil {
		return err
	}
	if record.Plan != nil {
		if len(record.Plan.Steps) != len(plan.Steps) {
			return errors.New("执行计划步骤数量不可改变")
		}
		for index, step := range record.Plan.Steps {
			if step.Status == agent.StepSucceeded && plan.Steps[index].Status != agent.StepSucceeded {
				return errors.New("已完成步骤不可回写")
			}
		}
	}
	copy := clonePlan(plan)
	record.Plan = &copy
	return r.writeLocked(plan.RunID, record)
}

// GetPlan 读取一个 run 的计划快照。
func (r *FileRepository) GetPlan(runID string) (agent.ExecutionPlan, bool) {
	plan, ok, _ := r.LoadPlan(runID)
	return plan, ok
}

// LoadPlan 读取计划并保留损坏文件和不兼容版本错误。
func (r *FileRepository) LoadPlan(runID string) (agent.ExecutionPlan, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := r.readLocked(runID)
	if err != nil || record.Plan == nil {
		return agent.ExecutionPlan{}, false, err
	}
	return clonePlan(*record.Plan), true, nil
}

// readLocked 读取单个 run 文件；不存在时返回空记录，便于首次写入。
func (r *FileRepository) readLocked(runID string) (runRecord, error) {
	data, err := os.ReadFile(r.path(runID))
	if errors.Is(err, os.ErrNotExist) {
		return runRecord{SchemaVersion: fileSchemaVersion}, nil
	}
	if err != nil {
		return runRecord{}, fmt.Errorf("读取运行快照失败: %w", err)
	}
	var record runRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return runRecord{}, fmt.Errorf("解析运行快照失败: %w", err)
	}
	if record.SchemaVersion != 0 && record.SchemaVersion != fileSchemaVersion {
		return runRecord{}, fmt.Errorf("运行快照版本 %d 不受支持", record.SchemaVersion)
	}
	record.SchemaVersion = fileSchemaVersion
	return record, nil
}

// writeLocked 用临时文件加 rename，避免进程中断留下半个 JSON 文件。
func (r *FileRepository) writeLocked(runID string, record runRecord) error {
	record.SchemaVersion = fileSchemaVersion
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("编码运行快照失败: %w", err)
	}
	tmp, err := os.CreateTemp(r.dir, ".run-*.tmp")
	if err != nil {
		return fmt.Errorf("创建运行快照临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("设置运行快照权限失败: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入运行快照失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("同步运行快照失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭运行快照失败: %w", err)
	}
	if err := os.Rename(tmpName, r.path(runID)); err != nil {
		return fmt.Errorf("替换运行快照失败: %w", err)
	}
	return nil
}

// path 用哈希文件名隔离用户输入，避免 run_id 参与路径拼接。
func (r *FileRepository) path(runID string) string {
	digest := sha256.Sum256([]byte(runID))
	return filepath.Join(r.dir, hex.EncodeToString(digest[:])+".json")
}

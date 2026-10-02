// Package tool 提供模板的 Tool 注册表和基础设施实现。
//
// 本文件负责按配置选择已注册实现；调用仍必须由 Application 经过 Policy Gate 后发起。
package tool

import (
	"context"
	"fmt"
	"time"

	domaintool "github.com/observer-mimiron/supervisor-template/internal/domain/tool"
)

// Registry 是按实现 ID 分派的 ToolExecutor。
type Registry struct {
	pool      *Pool
	contracts map[string]domaintool.Contract
}

type toolHandler func(context.Context, map[string]string, string) (string, error)

// NewRegistry creates a generic registry from composition-provided contracts.
func NewRegistry(timeout time.Duration, contracts []domaintool.Contract) (*Registry, error) {
	poolTimeout := timeout
	if poolTimeout <= 0 {
		poolTimeout = 30 * time.Second
	}
	pool, err := NewPool(8, poolTimeout)
	if err != nil {
		return nil, err
	}
	registry := &Registry{
		pool:      pool,
		contracts: make(map[string]domaintool.Contract),
	}
	for _, contract := range CloneContracts(contracts) {
		if contract.ToolID == "" {
			return nil, fmt.Errorf("Tool 合同缺少 id")
		}
		if _, exists := registry.contracts[contract.ToolID]; exists {
			return nil, fmt.Errorf("Tool %q 合同重复注册", contract.ToolID)
		}
		registry.contracts[contract.ToolID] = contract
	}
	return registry, nil
}

// Execute 分派到已注册 Tool；unknown ID 不会被隐式降级到另一个实现。
func (r *Registry) Execute(ctx context.Context, toolID string, input map[string]string, idempotencyKey string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("Tool 注册表未装配")
	}
	if err := r.ValidateInput(toolID, input); err != nil {
		return "", err
	}
	output, err := r.pool.Invoke(ctx, ToolInvocation{ToolID: toolID, Input: cloneToolInput(input), IdempotencyKey: idempotencyKey})
	if err != nil {
		return "", err
	}
	if err := r.ValidateOutput(toolID, output); err != nil {
		return "", err
	}
	return output, nil
}

func (r *Registry) register(toolID string, handler toolHandler) error {
	return r.pool.Register(toolID, func(ctx context.Context, invocation ToolInvocation) (string, error) {
		return handler(ctx, invocation.Input, invocation.IdempotencyKey)
	})
}

// RegisterHandler attaches one startup-selected external handler to a contract
// already known by the registry. Registration remains startup-only; callers
// cannot add a new capability without first declaring its contract.
func (r *Registry) RegisterHandler(toolID string, handler func(context.Context, map[string]string, string) (string, error)) error {
	if r == nil || handler == nil {
		return fmt.Errorf("Tool 注册表或 handler 未装配")
	}
	if _, ok := r.contracts[toolID]; !ok {
		return fmt.Errorf("Tool %q 合同未注册", toolID)
	}
	return r.register(toolID, handler)
}

func cloneToolInput(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	cloned := make(map[string]string, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

// ValidateInput checks the registered Tool schema before any external call.
func (r *Registry) ValidateInput(toolID string, input map[string]string) error {
	contract, ok := r.contracts[toolID]
	if !ok {
		return fmt.Errorf("Tool %q 未注册", toolID)
	}
	return validateInput(contract, input)
}

// ValidateOutput checks the registered Tool output contract before projection.
func (r *Registry) ValidateOutput(toolID, output string) error {
	contract, ok := r.contracts[toolID]
	if !ok {
		return fmt.Errorf("Tool %q 未注册", toolID)
	}
	if err := validateOutput(contract, output); err != nil {
		return &InvocationFailure{Kind: FailureInvalidOutput, Err: err}
	}
	return nil
}

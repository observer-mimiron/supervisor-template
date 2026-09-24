// Package tool 提供模板的 Tool 注册表和基础设施实现。
//
// 本文件负责按配置选择已注册实现；调用仍必须由 Application 经过 Policy Gate 后发起。
package tool

import (
	"context"
	"fmt"
	"time"

	domaintool "github.com/observer-mimiron/supervisor-template/internal/domain/tool"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/examplebusiness"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/mcp"
)

// Registry 是按实现 ID 分派的 ToolExecutor。
type Registry struct {
	fake      *FakeRegistry
	pool      *Pool
	contracts map[string]domaintool.Contract
}

type toolHandler func(context.Context, map[string]string, string) (string, error)

// NewRegistry 创建默认 fake 或配置指定的 HTTP 只读 Tool 注册表。
func NewRegistry(userQueryImplementation, endpoint string, timeout time.Duration) (*Registry, error) {
	return NewRegistryWithMCP(userQueryImplementation, endpoint, timeout, nil, "")
}

// NewRegistryWithMCP 创建 Tool 注册表，并可选接入已通过 allow-list 的 MCP client。
func NewRegistryWithMCP(userQueryImplementation, endpoint string, timeout time.Duration, mcpClient *mcp.Client, mcpServer string) (*Registry, error) {
	poolTimeout := timeout
	if poolTimeout <= 0 {
		poolTimeout = 30 * time.Second
	}
	pool, err := NewPool(8, poolTimeout)
	if err != nil {
		return nil, err
	}
	registry := &Registry{
		fake:      NewFakeRegistry(),
		pool:      pool,
		contracts: make(map[string]domaintool.Contract),
	}
	for _, contract := range ContractsFor(userQueryImplementation) {
		contract.RequiredInputs = append([]string(nil), contract.RequiredInputs...)
		registry.contracts[contract.ToolID] = contract
	}
	if err := registry.register(examplebusiness.SideEffectToolID, func(ctx context.Context, input map[string]string, key string) (string, error) {
		return registry.fake.Execute(ctx, examplebusiness.SideEffectToolID, input, key)
	}); err != nil {
		return nil, err
	}
	if userQueryImplementation == "" || userQueryImplementation == examplebusiness.ReadOnlyToolFake {
		if err := registry.register(examplebusiness.ReadOnlyToolID, func(ctx context.Context, input map[string]string, key string) (string, error) {
			return registry.fake.Execute(ctx, examplebusiness.ReadOnlyToolID, input, key)
		}); err != nil {
			return nil, err
		}
		return registry, nil
	}
	if userQueryImplementation != examplebusiness.ReadOnlyToolHTTP {
		if userQueryImplementation != examplebusiness.ReadOnlyToolMCP {
			return nil, fmt.Errorf("Tool 实现 %q 未注册", userQueryImplementation)
		}
		if mcpClient == nil || mcpServer == "" {
			return nil, fmt.Errorf("MCP Tool 未配置 client 或 server")
		}
		if err := registry.register(examplebusiness.ReadOnlyToolID, func(ctx context.Context, input map[string]string, _ string) (string, error) {
			return mcpClient.Call(ctx, mcpServer, examplebusiness.ReadOnlyToolID, input)
		}); err != nil {
			return nil, err
		}
		return registry, nil
	}
	readOnly, err := NewHTTPReadOnlyTool(endpoint, timeout)
	if err != nil {
		return nil, err
	}
	if err := registry.register(examplebusiness.ReadOnlyToolID, func(ctx context.Context, input map[string]string, _ string) (string, error) {
		return readOnly.Execute(ctx, input)
	}); err != nil {
		return nil, err
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

// OutreachCount 返回底层 fake 副作用 Tool 的实际执行次数，供本地合同测试使用。
func (r *Registry) OutreachCount() int {
	if r == nil || r.fake == nil {
		return 0
	}
	return r.fake.OutreachCount()
}

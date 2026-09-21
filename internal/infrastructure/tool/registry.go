// Package tool 提供模板的 Tool 注册表和基础设施实现。
//
// 本文件负责按配置选择已注册实现；调用仍必须由 Application 经过 Policy Gate 后发起。
package tool

import (
	"context"
	"fmt"
	"time"

	"github.com/observer-mimiron/suanming-agent/eino-supervisor-template/internal/infrastructure/mcp"
)

// Registry 是按实现 ID 分派的 ToolExecutor。
type Registry struct {
	fake                    *FakeRegistry
	readOnly                *HTTPReadOnlyTool
	mcpClient               *mcp.Client
	mcpServer               string
	userQueryImplementation string
}

// NewRegistry 创建默认 fake 或配置指定的 HTTP 只读 Tool 注册表。
func NewRegistry(userQueryImplementation, endpoint string, timeout time.Duration) (*Registry, error) {
	return NewRegistryWithMCP(userQueryImplementation, endpoint, timeout, nil, "")
}

// NewRegistryWithMCP 创建 Tool 注册表，并可选接入已通过 allow-list 的 MCP client。
func NewRegistryWithMCP(userQueryImplementation, endpoint string, timeout time.Duration, mcpClient *mcp.Client, mcpServer string) (*Registry, error) {
	registry := &Registry{
		fake:                    NewFakeRegistry(),
		userQueryImplementation: userQueryImplementation,
		mcpClient:               mcpClient,
		mcpServer:               mcpServer,
	}
	if userQueryImplementation == "" || userQueryImplementation == "fake.user_query" {
		registry.userQueryImplementation = "fake.user_query"
		return registry, nil
	}
	if userQueryImplementation != "http.read_only" {
		if userQueryImplementation != "mcp.read_only" {
			return nil, fmt.Errorf("Tool 实现 %q 未注册", userQueryImplementation)
		}
		if mcpClient == nil || mcpServer == "" {
			return nil, fmt.Errorf("MCP Tool 未配置 client 或 server")
		}
		return registry, nil
	}
	readOnly, err := NewHTTPReadOnlyTool(endpoint, timeout)
	if err != nil {
		return nil, err
	}
	registry.readOnly = readOnly
	return registry, nil
}

// Execute 分派到已注册 Tool；unknown ID 不会被隐式降级到另一个实现。
func (r *Registry) Execute(ctx context.Context, toolID string, input map[string]string, idempotencyKey string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("Tool 注册表未装配")
	}
	switch toolID {
	case "user_query":
		if r.userQueryImplementation == "http.read_only" {
			return r.readOnly.Execute(ctx, input)
		}
		if r.userQueryImplementation == "mcp.read_only" {
			return r.mcpClient.Call(ctx, r.mcpServer, toolID, input)
		}
		return r.fake.Execute(ctx, toolID, input, idempotencyKey)
	case "simulated_outreach":
		return r.fake.Execute(ctx, toolID, input, idempotencyKey)
	default:
		return "", fmt.Errorf("Tool %q 未注册", toolID)
	}
}

// OutreachCount 返回底层 fake 副作用 Tool 的实际执行次数，供本地合同测试使用。
func (r *Registry) OutreachCount() int {
	if r == nil || r.fake == nil {
		return 0
	}
	return r.fake.OutreachCount()
}

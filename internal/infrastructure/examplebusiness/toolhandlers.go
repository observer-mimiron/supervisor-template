// 本文件把 example 业务的 Tool 实现注册进 Tool 注册表。
//
// 它是"换掉这门示例业务"时的替换面：装配层只调用 RegisterToolHandlers，
// 不认识任何具体 Tool ID；换成自己的业务时替换本包（含本文件与 registry.go），
// 并同步配置里的 workers/routes/tools 注册项即可。
package examplebusiness

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ToolConfig 是注册 example 业务 Tool 所需的配置输入。
type ToolConfig struct {
	// Implementation 是只读 Tool 的实现选择：fake / http / mcp。
	Implementation string
	// Endpoint 是 http 实现的目标地址。
	Endpoint string
	// MCPServer 是 mcp 实现要调用的 server ID。
	MCPServer string
	// Timeout 是只读 Tool 的超时。
	Timeout time.Duration
	// FakeDelayMS 按 Tool ID 声明 fake 实现的延迟，"仍在执行"的步骤因此存在取消窗口。
	FakeDelayMS map[string]int
}

// HandlerRegistry 是注册 Tool handler 所需的最小能力。
//
// 用接口而不是具体注册表类型，本包就不必依赖 Tool 注册表的实现细节；
// 注册表本身仍然只接受已声明 contract 的 Tool ID，本包无法新增能力。
type HandlerRegistry interface {
	RegisterHandler(toolID string, handler func(context.Context, map[string]string, string) (string, error)) error
}

// MCPCaller 是只读 Tool 走 MCP 实现时需要的调用能力（*mcp.Client 满足）。
type MCPCaller interface {
	Call(ctx context.Context, serverID, toolID string, arguments map[string]string) (string, error)
}

// ReadOnlyExecutor 是 HTTP 版只读 Tool 需要满足的执行能力（*tool.HTTPReadOnlyTool 满足）。
type ReadOnlyExecutor interface {
	Execute(ctx context.Context, input map[string]string) (string, error)
}

// ReadOnlyToolBuilder 构建 HTTP 版只读 Tool，由装配层注入。
//
// 本包只声明"需要一个能执行的只读 Tool"，不 import Tool 实现包：Tool 包的测试反向
// 依赖本包的注册描述，若这里直接 import 会形成测试期的 import cycle。
type ReadOnlyToolBuilder func(endpoint string, timeout time.Duration) (ReadOnlyExecutor, error)

// OrderStore 是 MySQL 版订单 Tool 需要的读写能力（*mysqlinfra.OrderToolAdapter 满足）。
type OrderStore interface {
	Query(ctx context.Context, payload []byte) (string, error)
	Insert(ctx context.Context, payload []byte, idempotencyKey string) (string, error)
}

// ToolDeps 是注册 example 业务 Tool 所需的依赖；未启用的能力留 nil。
type ToolDeps struct {
	Registry HandlerRegistry
	Config   ToolConfig
	// Runtime 是 fake 实现的状态载体，装配层还需要它的 OutreachCount 作为写入计数。
	Runtime *FakeRuntime
	// HTTPReadOnly 仅在只读 Tool 选择 http 实现时需要。
	HTTPReadOnly ReadOnlyToolBuilder
	// MCP 仅在只读 Tool 选择 mcp 实现时需要。
	MCP MCPCaller
	// Orders 仅在接入 MySQL 后由装配层提供。
	Orders OrderStore
}

// RegisterToolHandlers 把 example 业务的 Tool 实现注册进注册表。
//
// 每个 handler 都对应一个已声明 contract 的 Tool ID；未注册的实现选择会让启动失败，
// 而不是留到请求期才发现某个 Tool 没有实现。
func RegisterToolHandlers(deps ToolDeps) error {
	if deps.Registry == nil || deps.Runtime == nil {
		return errors.New("Tool 注册表或 fake 运行时未装配")
	}
	deps.Runtime.SetToolDelay(ReadOnlyToolID, delay(deps.Config.FakeDelayMS, ReadOnlyToolID))
	deps.Runtime.SetToolDelay(SummaryToolID, delay(deps.Config.FakeDelayMS, SummaryToolID))
	deps.Runtime.SetToolDelay(SideEffectToolID, delay(deps.Config.FakeDelayMS, SideEffectToolID))

	if err := deps.Registry.RegisterHandler(SummaryToolID, func(ctx context.Context, input map[string]string, key string) (string, error) {
		return deps.Runtime.Execute(ctx, SummaryToolID, input, key)
	}); err != nil {
		return fmt.Errorf("Summary Tool 装配失败: %w", err)
	}
	if err := deps.Registry.RegisterHandler(SideEffectToolID, func(ctx context.Context, input map[string]string, key string) (string, error) {
		return deps.Runtime.Execute(ctx, SideEffectToolID, input, key)
	}); err != nil {
		return fmt.Errorf("副作用 Tool 装配失败: %w", err)
	}
	if err := registerReadOnlyTool(deps); err != nil {
		return err
	}
	return registerMySQLTools(deps)
}

// registerReadOnlyTool 按配置选择只读 Tool 的实现。
func registerReadOnlyTool(deps ToolDeps) error {
	switch deps.Config.Implementation {
	case "", ReadOnlyToolFake:
		if err := deps.Registry.RegisterHandler(ReadOnlyToolID, func(ctx context.Context, input map[string]string, key string) (string, error) {
			return deps.Runtime.Execute(ctx, ReadOnlyToolID, input, key)
		}); err != nil {
			return fmt.Errorf("查询 Tool 装配失败: %w", err)
		}
	case ReadOnlyToolHTTP:
		if deps.HTTPReadOnly == nil {
			return errors.New("HTTP 只读 Tool 未装配构建器")
		}
		readOnly, err := deps.HTTPReadOnly(deps.Config.Endpoint, deps.Config.Timeout)
		if err != nil {
			return err
		}
		if err := deps.Registry.RegisterHandler(ReadOnlyToolID, func(ctx context.Context, input map[string]string, _ string) (string, error) {
			return readOnly.Execute(ctx, input)
		}); err != nil {
			return fmt.Errorf("HTTP Tool 装配失败: %w", err)
		}
	case ReadOnlyToolMCP:
		if deps.MCP == nil || deps.Config.MCPServer == "" {
			return errors.New("MCP Tool 未配置 client 或 server")
		}
		if err := deps.Registry.RegisterHandler(ReadOnlyToolID, func(ctx context.Context, input map[string]string, _ string) (string, error) {
			return deps.MCP.Call(ctx, deps.Config.MCPServer, ReadOnlyToolID, input)
		}); err != nil {
			return fmt.Errorf("MCP Tool 装配失败: %w", err)
		}
	default:
		return fmt.Errorf("Tool 实现 %q 未注册", deps.Config.Implementation)
	}
	return nil
}

// registerMySQLTools 在接入 MySQL 时注册订单读写 Tool；没有 Orders 时两个 Tool 都不注册。
func registerMySQLTools(deps ToolDeps) error {
	if deps.Orders == nil {
		return nil
	}
	if err := deps.Registry.RegisterHandler(MySQLQueryToolID, func(ctx context.Context, input map[string]string, _ string) (string, error) {
		return deps.Orders.Query(ctx, []byte(input["message"]))
	}); err != nil {
		return fmt.Errorf("MySQL 查询 Tool 装配失败: %w", err)
	}
	if err := deps.Registry.RegisterHandler(MySQLInsertToolID, func(ctx context.Context, input map[string]string, key string) (string, error) {
		return deps.Orders.Insert(ctx, []byte(input["message"]), key)
	}); err != nil {
		return fmt.Errorf("MySQL 写入 Tool 装配失败: %w", err)
	}
	return nil
}

// delay 把配置里的毫秒延迟换算成 Duration；未声明时为零，即不延迟。
func delay(byTool map[string]int, toolID string) time.Duration {
	return time.Duration(byTool[toolID]) * time.Millisecond
}

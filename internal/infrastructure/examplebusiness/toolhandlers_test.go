package examplebusiness

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// recordingRegistry 记录被注册的 Tool ID 与 handler，用来断言注册结果，
// 不依赖真实 Tool 注册表实现（本包也不 import 它）。
type recordingRegistry struct {
	handlers map[string]func(context.Context, map[string]string, string) (string, error)
	order    []string
}

func (r *recordingRegistry) RegisterHandler(toolID string, handler func(context.Context, map[string]string, string) (string, error)) error {
	if r.handlers == nil {
		r.handlers = make(map[string]func(context.Context, map[string]string, string) (string, error))
	}
	if _, exists := r.handlers[toolID]; exists {
		return errors.New("重复注册: " + toolID)
	}
	r.handlers[toolID] = handler
	r.order = append(r.order, toolID)
	return nil
}

func (r *recordingRegistry) has(toolID string) bool {
	_, ok := r.handlers[toolID]
	return ok
}

// TestRegisterToolHandlersRegistersFakeTools 证明默认（fake）实现下三个 fake Tool
// 都被注册，且注册的 handler 真的能执行一次只读查询。
func TestRegisterToolHandlersRegistersFakeTools(t *testing.T) {
	registry := &recordingRegistry{}
	if err := RegisterToolHandlers(ToolDeps{
		Registry: registry,
		Runtime:  NewFakeRuntime(),
		Config:   ToolConfig{},
	}); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	for _, toolID := range []string{ReadOnlyToolID, SummaryToolID, SideEffectToolID} {
		if !registry.has(toolID) {
			t.Fatalf("Tool %q 没有注册 handler（已注册 %v）", toolID, registry.order)
		}
	}
	// fake 只读 Tool 按固定 JSON 快照查询，自由文本会被判定为输入无效。
	output, err := registry.handlers[ReadOnlyToolID](context.Background(), map[string]string{"message": fixedAudienceQuery}, "")
	if err != nil {
		t.Fatalf("只读 handler 执行失败: %v", err)
	}
	if strings.TrimSpace(output) == "" {
		t.Fatal("只读 handler 返回空结果")
	}
}

// TestRegisterToolHandlersRejectsUnknownImplementation 守住 fail-closed：
// 未注册的实现选择必须让启动失败，而不是留到请求期。
func TestRegisterToolHandlersRejectsUnknownImplementation(t *testing.T) {
	registry := &recordingRegistry{}
	err := RegisterToolHandlers(ToolDeps{
		Registry: registry,
		Runtime:  NewFakeRuntime(),
		Config:   ToolConfig{Implementation: "grpc"},
	})
	if err == nil || !strings.Contains(err.Error(), "未注册") {
		t.Fatalf("未注册的实现没有失败: %v", err)
	}
}

// TestRegisterToolHandlersRequiresDependenciesForChosenImplementation 证明选中的
// 实现缺少依赖时在装配期报错，而不是装配出一个调用即 panic 的 Tool。
func TestRegisterToolHandlersRequiresDependenciesForChosenImplementation(t *testing.T) {
	cases := []struct {
		name     string
		config   ToolConfig
		wantHint string
	}{
		{name: "mcp 缺少 caller", config: ToolConfig{Implementation: ReadOnlyToolMCP, MCPServer: "srv"}, wantHint: "MCP Tool 未配置 client 或 server"},
		{name: "http 缺少构建器", config: ToolConfig{Implementation: ReadOnlyToolHTTP}, wantHint: "HTTP 只读 Tool 未装配构建器"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := RegisterToolHandlers(ToolDeps{Registry: &recordingRegistry{}, Runtime: NewFakeRuntime(), Config: test.config})
			if err == nil || !strings.Contains(err.Error(), test.wantHint) {
				t.Fatalf("err = %v, want 包含 %q", err, test.wantHint)
			}
		})
	}
}

// TestRegisterToolHandlersUsesInjectedHTTPBuilder 证明 http 实现走装配层注入的
// 构建器，本包不 import Tool 实现包。
func TestRegisterToolHandlersUsesInjectedHTTPBuilder(t *testing.T) {
	registry := &recordingRegistry{}
	var gotEndpoint string
	var gotTimeout time.Duration
	err := RegisterToolHandlers(ToolDeps{
		Registry: registry,
		Runtime:  NewFakeRuntime(),
		Config:   ToolConfig{Implementation: ReadOnlyToolHTTP, Endpoint: "http://example.test/query", Timeout: 3 * time.Second},
		HTTPReadOnly: func(endpoint string, timeout time.Duration) (ReadOnlyExecutor, error) {
			gotEndpoint, gotTimeout = endpoint, timeout
			return stubExecutor{output: "from-http"}, nil
		},
	})
	if err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	if gotEndpoint != "http://example.test/query" || gotTimeout != 3*time.Second {
		t.Fatalf("构建器收到 endpoint=%q timeout=%v，与配置不一致", gotEndpoint, gotTimeout)
	}
	output, err := registry.handlers[ReadOnlyToolID](context.Background(), map[string]string{"message": "x"}, "ignored-key")
	if err != nil {
		t.Fatalf("HTTP handler 执行失败: %v", err)
	}
	if output != "from-http" {
		t.Fatalf("output = %q, want from-http", output)
	}
}

// TestRegisterToolHandlersSkipsMySQLUntilOrdersAreProvided 证明没有 MySQL 适配器时
// 不注册订单 Tool，接入后注册，并且写入 handler 会把幂等键透传给适配器。
func TestRegisterToolHandlersSkipsMySQLUntilOrdersAreProvided(t *testing.T) {
	withoutOrders := &recordingRegistry{}
	if err := RegisterToolHandlers(ToolDeps{Registry: withoutOrders, Runtime: NewFakeRuntime()}); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	if withoutOrders.has(MySQLQueryToolID) || withoutOrders.has(MySQLInsertToolID) {
		t.Fatal("没有 Orders 时仍注册了 MySQL Tool")
	}

	orders := &stubOrders{}
	withOrders := &recordingRegistry{}
	if err := RegisterToolHandlers(ToolDeps{Registry: withOrders, Runtime: NewFakeRuntime(), Orders: orders}); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	if !withOrders.has(MySQLQueryToolID) || !withOrders.has(MySQLInsertToolID) {
		t.Fatal("接入 Orders 后 MySQL Tool 没有注册")
	}
	if _, err := withOrders.handlers[MySQLInsertToolID](context.Background(), map[string]string{"message": `{"id":1}`}, "key-1"); err != nil {
		t.Fatalf("MySQL 写入 handler 执行失败: %v", err)
	}
	if orders.insertKey != "key-1" {
		t.Fatalf("透传的幂等键 = %q, want key-1", orders.insertKey)
	}
}

type stubExecutor struct{ output string }

func (s stubExecutor) Execute(context.Context, map[string]string) (string, error) {
	return s.output, nil
}

type stubOrders struct{ insertKey string }

func (s *stubOrders) Query(context.Context, []byte) (string, error) { return "[]", nil }

func (s *stubOrders) Insert(_ context.Context, _ []byte, idempotencyKey string) (string, error) {
	s.insertKey = idempotencyKey
	return "ok", nil
}

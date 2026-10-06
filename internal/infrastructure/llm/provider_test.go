package llm

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
)

// TestBuildProviderRejectsUnregisteredID 证明配置写错 provider 时启动直接失败，
// 而不是回退到某个默认实现；错误里必须列出已注册的 ID 以便定位。
func TestBuildProviderRejectsUnregisteredID(t *testing.T) {
	_, err := BuildProvider(context.Background(), "openai", ProviderParams{})
	if err == nil {
		t.Fatal("未注册的 provider 没有失败")
	}
	for _, id := range SupportedProviders() {
		if !strings.Contains(err.Error(), id) {
			t.Fatalf("错误信息没有列出已注册的 provider %q: %v", id, err)
		}
	}
}

func TestSupportedProvidersListsRegisteredIDs(t *testing.T) {
	got := SupportedProviders()
	want := []string{"deepseek", "fake"}
	if !slices.Equal(got, want) {
		t.Fatalf("SupportedProviders() = %v, want %v", got, want)
	}
}

// TestFakeProviderDoesNotReadInstruction 守住一条容易被改坏的边界：fake provider
// 不调用模型，因此也不应该读取 Supervisor 提示词，否则提示词文件缺失会让离线链路
// 起不来。指令加载器在这里故意返回错误。
func TestFakeProviderDoesNotReadInstruction(t *testing.T) {
	called := false
	provider, err := BuildProvider(context.Background(), "fake", ProviderParams{
		Routes: []FakeRoute{{WorkerID: "user_analysis", Intent: "query", Matches: []string{"查询"}, ToolID: "user_query", Risk: "read_only"}},
		Instruction: func() (string, error) {
			called = true
			return "", errors.New("fake provider 不应读取提示词")
		},
	})
	if err != nil {
		t.Fatalf("fake provider 构建失败: %v", err)
	}
	if called {
		t.Fatal("fake provider 调用了指令加载器")
	}
	if provider.Model() != nil {
		t.Fatal("fake provider 不应暴露模型")
	}
	decision, err := provider.Decide(context.Background(), conversation.ExecutionRequest{RunID: "run-fake", Message: "查询"})
	if err != nil {
		t.Fatalf("fake provider 无法产生候选: %v", err)
	}
	if decision.WorkerID != "user_analysis" || decision.Risk != "read_only" {
		t.Fatalf("fake provider 候选 = %+v，未按已注册路由产生", decision)
	}
}

// TestDeepSeekProviderSurfacesInstructionError 证明真实模型 provider 会调用指令
// 加载器并把读取失败按启动错误返回，不推迟到请求期。
func TestDeepSeekProviderSurfacesInstructionError(t *testing.T) {
	_, err := BuildProvider(context.Background(), "deepseek", ProviderParams{
		Instruction: func() (string, error) { return "", errors.New("提示词不可读") },
	})
	if err == nil || !strings.Contains(err.Error(), "提示词不可读") {
		t.Fatalf("指令加载失败没有向上传递: %v", err)
	}
}

func TestDeepSeekProviderRequiresInstructionLoader(t *testing.T) {
	_, err := BuildProvider(context.Background(), "deepseek", ProviderParams{})
	if err == nil || !strings.Contains(err.Error(), "指令加载器") {
		t.Fatalf("缺少指令加载器没有失败: %v", err)
	}
}

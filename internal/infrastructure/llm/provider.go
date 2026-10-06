// 本文件是模型 provider 的注册表：配置里的 model.provider 只能选中这里已经
// 显式注册的实现，未注册的 ID 在启动期直接失败。
//
// 新增一个模型 provider 的做法是加一个 newXxxProvider 并在此登记一行，
// 不需要改动 internal/composition/。注册表是普通 map，不做反射扫描、init()
// 自注册或热加载，因此"某个包恰好被 import"不会让一个 provider 悄悄生效。
package llm

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"

	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
)

// ProviderParams 是构建一个 provider 所需的参数契约。
//
// 它只描述"一个 provider 可能用到什么"，字段来自配置但类型不依赖 internal/config，
// 因为 infrastructure 不允许导入配置包。未接入真实模型的 provider 会忽略模型字段。
type ProviderParams struct {
	// 真实模型参数，来自 [model]。
	Name        string
	BaseURL     string
	APIKeyEnv   string
	Temperature float64
	MaxTokens   int
	Timeout     time.Duration

	// MaxSteps 是模型一次能提出的候选步骤上限，来自 agent.supervisor.max_steps。
	MaxSteps int

	// Instruction 返回 Supervisor 指令。只有真实模型 provider 会调用它，因此
	// fake provider 不会因为提示词文件缺失而影响启动。
	Instruction func() (string, error)

	// Routes 与 Decision 是确定性 fake provider 的路由快照和候选构建器。
	Routes   []FakeRoute
	Decision func(conversation.ExecutionRequest, string, string, string, agent.Risk) agent.SupervisorDecision
}

// Provider 是一个装配完成的 Supervisor。
//
// Model 返回供 Worker 适配器使用的 ToolCallingChatModel；不调用真实模型的
// provider 返回 nil，此时需要模型的 Worker Runner 会在启动期拒绝装配。
type Provider interface {
	Decide(context.Context, conversation.ExecutionRequest) (agent.SupervisorDecision, error)
	Model() einomodel.ToolCallingChatModel
}

// ProviderBuilder 由每个已注册的 provider 实现。
type ProviderBuilder func(ctx context.Context, params ProviderParams) (Provider, error)

// providerBuilders 是启动期冻结的注册表：provider ID -> 构建函数。
var providerBuilders = map[string]ProviderBuilder{
	"fake":     newFakeProvider,
	"deepseek": newDeepSeekProvider,
}

// BuildProvider 按已注册 ID 构建 provider。
//
// 未注册的 ID 返回错误而不是回退到默认实现：配置写错必须让服务起不来，
// 不能悄悄降级成另一种 provider。
func BuildProvider(ctx context.Context, providerID string, params ProviderParams) (Provider, error) {
	build, ok := providerBuilders[providerID]
	if !ok {
		return nil, fmt.Errorf("model provider %q 未注册（已注册：%s）", providerID, strings.Join(SupportedProviders(), "、"))
	}
	return build(ctx, params)
}

// SupportedProviders 返回已注册的 provider ID，按字典序排列。
func SupportedProviders() []string {
	ids := make([]string, 0, len(providerBuilders))
	for id := range providerBuilders {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// newFakeProvider 构建确定性 Supervisor：不调用模型，也不暴露模型。
func newFakeProvider(_ context.Context, params ProviderParams) (Provider, error) {
	return NewFakeSupervisorWithBuilder(params.Routes, params.Decision), nil
}

// newDeepSeekProvider 构建 DeepSeek ChatModel 并把 Supervisor 包在它上面。
func newDeepSeekProvider(ctx context.Context, params ProviderParams) (Provider, error) {
	if params.Instruction == nil {
		return nil, errors.New("deepseek provider 缺少 Supervisor 指令加载器")
	}
	instruction, err := params.Instruction()
	if err != nil {
		return nil, err
	}
	return NewDeepSeekSupervisor(ctx, ModelOptions{
		Name:        params.Name,
		BaseURL:     params.BaseURL,
		APIKeyEnv:   params.APIKeyEnv,
		Temperature: params.Temperature,
		MaxTokens:   params.MaxTokens,
		Timeout:     params.Timeout,
		MaxSteps:    params.MaxSteps,
	}, instruction)
}

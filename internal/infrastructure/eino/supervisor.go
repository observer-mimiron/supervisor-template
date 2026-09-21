// Package eino 提供 Eino ADK 的 Supervisor 装配适配。
//
// 本文件沿用 Eino 官方 supervisor.New 模式；权限和最终状态仍由模板自己的 Policy/Run 层负责。
package eino

import (
	"context"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/supervisor"
	"github.com/cloudwego/eino/components/model"
)

// BuildSupervisor 使用一个 ChatModelAgent 和若干子 Agent 创建 Supervisor。
func BuildSupervisor(ctx context.Context, chatModel model.ToolCallingChatModel, name, description, instruction string, subAgents ...adk.Agent) (adk.ResumableAgent, error) {
	supervisorAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        name,
		Description: description,
		Instruction: instruction,
		Model:       chatModel,
		Exit:        &adk.ExitTool{},
	})
	if err != nil {
		return nil, err
	}
	return supervisor.New(ctx, &supervisor.Config{
		Supervisor: supervisorAgent,
		SubAgents:  subAgents,
	})
}

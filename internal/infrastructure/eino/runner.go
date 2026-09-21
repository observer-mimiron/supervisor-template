// Package eino 提供 Eino ADK 的运行适配。
//
// 本文件只封装官方 Runner 的 Query/Resume 入口，让上层不依赖 Eino 具体装配细节；
// 领域层和应用层不直接导入 Eino。
package eino

import (
	"context"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
)

// Runner 是 Eino ADK Runner 的最小包装。
type Runner struct {
	runner *adk.Runner
}

// NewRunner 按官方示例创建支持流式事件和 checkpoint 的 Runner。
func NewRunner(ctx context.Context, agent adk.Agent, store compose.CheckPointStore) *Runner {
	return &Runner{runner: adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
		CheckPointStore: store,
	})}
}

// Query 启动一次带 checkpoint 标识的 Eino 执行。
func (r *Runner) Query(ctx context.Context, input, checkpointID string) *adk.AsyncIterator[*adk.AgentEvent] {
	return r.runner.Query(ctx, input, adk.WithCheckPointID(checkpointID))
}

// Resume 从 Eino checkpoint 继续执行。
func (r *Runner) Resume(ctx context.Context, checkpointID string) (*adk.AsyncIterator[*adk.AgentEvent], error) {
	return r.runner.Resume(ctx, checkpointID)
}

// Package llm 提供模型边界适配，包括 fake 和真实 ChatModel 实现。
//
// 本文件只负责创建模型、解析结构化候选路由和隐藏凭证；不负责授权、审批、幂等或终态。
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	deepseekmodel "github.com/cloudwego/eino-ext/components/model/deepseek"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/observer-mimiron/supervisor-template/internal/config"
	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
)

// RealSupervisor 使用真实 ChatModel 生成候选路由，随后仍由应用层 Policy Gate 校验。
type RealSupervisor struct {
	model       einomodel.ToolCallingChatModel
	instruction string
	timeout     time.Duration
}

// NewDeepSeekSupervisor 创建基于官方 Eino DeepSeek 适配器的 Supervisor。
// API key 只从配置指定的环境变量读取，缺失时直接拒绝启动，避免把失败推迟到请求期。
func NewDeepSeekSupervisor(ctx context.Context, cfg config.ModelConfig, instruction string) (*RealSupervisor, error) {
	envName := strings.TrimSpace(cfg.APIKeyEnv)
	if envName == "" {
		return nil, errors.New("真实模型缺少 API key 环境变量名")
	}
	apiKey := strings.TrimSpace(os.Getenv(envName))
	if apiKey == "" {
		return nil, fmt.Errorf("真实模型凭证未设置: %s", envName)
	}
	chatModel, err := deepseekmodel.NewChatModel(ctx, &deepseekmodel.ChatModelConfig{
		APIKey:             apiKey,
		BaseURL:            strings.TrimSpace(cfg.BaseURL),
		Model:              cfg.Name,
		Temperature:        float32(cfg.Temperature),
		MaxTokens:          cfg.MaxTokens,
		Timeout:            cfg.Timeout,
		ResponseFormatType: deepseekmodel.ResponseFormatTypeJSONObject,
	})
	if err != nil {
		return nil, fmt.Errorf("创建真实 ChatModel 失败: %w", err)
	}
	return NewRealSupervisor(chatModel, instruction, cfg.Timeout), nil
}

// NewRealSupervisor 包装一个已创建的 Eino ToolCallingChatModel，便于合同测试替换模型。
func NewRealSupervisor(chatModel einomodel.ToolCallingChatModel, instruction string, timeout time.Duration) *RealSupervisor {
	return &RealSupervisor{model: chatModel, instruction: instruction, timeout: timeout}
}

// Decide 调用模型并把 JSON 输出解析为候选路由；它不会把模型输出当成授权结果。
func (s *RealSupervisor) Decide(ctx context.Context, request conversation.ExecutionRequest) (agent.SupervisorDecision, error) {
	if s == nil || s.model == nil {
		return agent.SupervisorDecision{}, errors.New("真实模型未装配")
	}
	if strings.TrimSpace(request.Message) == "" {
		return agent.SupervisorDecision{}, errors.New("用户消息不能为空")
	}
	if s.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.timeout)
		defer cancel()
	}
	output, err := s.model.Generate(ctx, []*schema.Message{
		schema.SystemMessage(s.instruction),
		schema.UserMessage(request.Message),
	})
	if err != nil {
		return agent.SupervisorDecision{}, fmt.Errorf("真实模型调用失败: %w", err)
	}
	if output == nil {
		return agent.SupervisorDecision{}, errors.New("真实模型返回为空")
	}
	decision, err := parseDecision(output.Content)
	if err != nil {
		return agent.SupervisorDecision{}, err
	}
	// 决策 ID 由运行上下文生成，模型不能借此影响幂等或恢复键。
	decision.DecisionID = request.RunID + ":decision"
	return decision, nil
}

// parseDecision 只接受 SupervisorDecision Schema 的最小 JSON，并拒绝含糊的自然语言输出。
func parseDecision(raw string) (agent.SupervisorDecision, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return agent.SupervisorDecision{}, errors.New("真实模型输出不是合法 JSON")
	}
	var payload struct {
		DecisionID string            `json:"decision_id"`
		WorkerID   string            `json:"worker_id"`
		Intent     string            `json:"intent"`
		Arguments  map[string]string `json:"arguments"`
		Risk       agent.Risk        `json:"risk"`
		Confidence float64           `json:"confidence"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return agent.SupervisorDecision{}, fmt.Errorf("解析真实模型路由失败: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return agent.SupervisorDecision{}, errors.New("真实模型输出包含额外 JSON")
		}
		return agent.SupervisorDecision{}, fmt.Errorf("真实模型输出包含额外内容: %w", err)
	}
	if payload.DecisionID == "" || payload.WorkerID == "" || payload.Intent == "" || payload.Arguments == nil || payload.Arguments["tool_id"] == "" {
		return agent.SupervisorDecision{}, errors.New("真实模型路由缺少必要字段")
	}
	if payload.Risk != agent.RiskReadOnly && payload.Risk != agent.RiskSideEffect {
		return agent.SupervisorDecision{}, errors.New("真实模型路由风险字段非法")
	}
	if payload.Confidence < 0 || payload.Confidence > 1 {
		return agent.SupervisorDecision{}, errors.New("真实模型路由置信度非法")
	}
	return agent.SupervisorDecision{
		WorkerID:   payload.WorkerID,
		Intent:     payload.Intent,
		Arguments:  payload.Arguments,
		Risk:       payload.Risk,
		Confidence: payload.Confidence,
	}, nil
}

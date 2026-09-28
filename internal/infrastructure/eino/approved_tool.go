package eino

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/observer-mimiron/supervisor-template/internal/application"
	domaintool "github.com/observer-mimiron/supervisor-template/internal/domain/tool"
)

// ApprovedTool exposes exactly one already-approved application Tool to Eino.
type ApprovedTool struct {
	contract       domaintool.Contract
	toolID         string
	input          map[string]string
	idempotencyKey string
	executor       application.ToolExecutor
	validator      application.ToolContractValidator
	called         atomic.Bool
}

func NewApprovedTool(contract domaintool.Contract, toolID string, input map[string]string, idempotencyKey string, executor application.ToolExecutor, validator application.ToolContractValidator) (*ApprovedTool, error) {
	if contract.ToolID == "" || contract.ToolID != toolID || executor == nil || validator == nil || idempotencyKey == "" {
		return nil, errors.New("Eino Tool 未绑定到已批准的 Tool 调用")
	}
	if err := validator.ValidateInput(toolID, input); err != nil {
		return nil, &application.PreCallError{Err: err}
	}
	return &ApprovedTool{contract: contract, toolID: toolID, input: cloneToolInput(input), idempotencyKey: idempotencyKey, executor: executor, validator: validator}, nil
}

func (t *ApprovedTool) Info(context.Context) (*schema.ToolInfo, error) {
	params := make(map[string]*schema.ParameterInfo, len(t.input))
	for name := range t.input {
		params[name] = &schema.ParameterInfo{Type: schema.String, Required: true}
	}
	return &schema.ToolInfo{
		Name:        t.toolID,
		Desc:        fmt.Sprintf("已批准的 %s Tool；风险=%s，需审批=%t", t.contract.ToolID, t.contract.Risk, t.contract.RequiresApproval),
		ParamsOneOf: schema.NewParamsOneOfByParams(params),
	}, nil
}

func (t *ApprovedTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	if t == nil || t.executor == nil || t.validator == nil || ctx == nil {
		return "", &application.PreCallError{Err: errors.New("Eino Tool 未装配")}
	}
	if !t.called.CompareAndSwap(false, true) {
		return "", errors.New("Eino Worker 超出单步骤 Tool 调用预算")
	}
	var input map[string]string
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return "", &application.PreCallError{Err: fmt.Errorf("Eino Tool 参数无效: %w", err)}
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return "", &application.PreCallError{Err: errors.New("Eino Tool 参数包含额外内容")}
	}
	if !sameToolInput(input, t.input) {
		return "", &application.PreCallError{Err: errors.New("Eino Tool 参数超出 Application 固定输入")}
	}
	if err := t.validator.ValidateInput(t.toolID, input); err != nil {
		return "", &application.PreCallError{Err: err}
	}
	return t.executor.Execute(ctx, t.toolID, cloneToolInput(t.input), t.idempotencyKey)
}

func sameToolInput(actual, approved map[string]string) bool {
	if len(actual) != len(approved) {
		return false
	}
	for key, value := range approved {
		if actual[key] != value {
			return false
		}
	}
	return true
}

func cloneToolInput(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	copy := make(map[string]string, len(input))
	for key, value := range input {
		copy[key] = value
	}
	return copy
}

var _ tool.InvokableTool = (*ApprovedTool)(nil)

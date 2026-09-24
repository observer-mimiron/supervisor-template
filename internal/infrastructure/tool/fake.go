// Package tool 提供模板使用的 fake Tool 注册表。
//
// 这里复用父项目的“工具先注册、再按 id 执行”边界；不连接真实运营系统。
package tool

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/examplebusiness"
)

// FakeRegistry 保存只读查询和模拟副作用 Tool 的执行结果。
type FakeRegistry struct {
	mu            sync.Mutex
	outreachByKey map[string]string
	outreachCount int
	handlers      map[string]fakeHandler
}

type fakeHandler func(map[string]string, string) (string, error)

// NewFakeRegistry 创建默认 fake Tool 注册表。
func NewFakeRegistry() *FakeRegistry {
	r := &FakeRegistry{outreachByKey: make(map[string]string), handlers: make(map[string]fakeHandler)}
	r.handlers[examplebusiness.ReadOnlyToolID] = func(input map[string]string, _ string) (string, error) {
		return fmt.Sprintf("已查询示例用户信息：%s", input["message"]), nil
	}
	r.handlers[examplebusiness.SideEffectToolID] = func(input map[string]string, idempotencyKey string) (string, error) {
		if idempotencyKey == "" {
			return "", errors.New("副作用 Tool 缺少幂等键")
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if result, ok := r.outreachByKey[idempotencyKey]; ok {
			return result, nil
		}
		r.outreachCount++
		result := fmt.Sprintf("已模拟触达示例用户：%s", input["message"])
		r.outreachByKey[idempotencyKey] = result
		return result, nil
	}
	return r
}

// Execute 执行已由 Policy Gate 选中的 Tool。
func (r *FakeRegistry) Execute(ctx context.Context, toolID string, input map[string]string, idempotencyKey string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	contracts := ContractsFor(examplebusiness.ReadOnlyToolFake)
	var contractFound bool
	for _, contract := range contracts {
		if contract.ToolID == toolID {
			contractFound = true
			if err := validateInput(contract, input); err != nil {
				return "", err
			}
			break
		}
	}
	if !contractFound {
		return "", fmt.Errorf("Tool %q 未注册", toolID)
	}
	handler, ok := r.handlers[toolID]
	if !ok {
		return "", fmt.Errorf("Tool %q 未注册", toolID)
	}
	return handler(input, idempotencyKey)
}

// ValidateInput validates the fake registry's registered Tool schema.
func (r *FakeRegistry) ValidateInput(toolID string, input map[string]string) error {
	for _, contract := range ContractsFor(examplebusiness.ReadOnlyToolFake) {
		if contract.ToolID == toolID {
			return validateInput(contract, input)
		}
	}
	return fmt.Errorf("Tool %q 未注册", toolID)
}

// ValidateOutput validates the fake registry's registered Tool output schema.
func (r *FakeRegistry) ValidateOutput(toolID, output string) error {
	for _, contract := range ContractsFor(examplebusiness.ReadOnlyToolFake) {
		if contract.ToolID == toolID {
			return validateOutput(contract, output)
		}
	}
	return fmt.Errorf("Tool %q 未注册", toolID)
}

// OutreachCount 返回模拟副作用实际执行次数。
func (r *FakeRegistry) OutreachCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.outreachCount
}

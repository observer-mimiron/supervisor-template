// Package tool 提供模板使用的 fake Tool 注册表。
//
// 这里复用父项目的“工具先注册、再按 id 执行”边界；不连接真实运营系统。
package tool

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// FakeRegistry 保存只读查询和模拟副作用 Tool 的执行结果。
type FakeRegistry struct {
	mu            sync.Mutex
	outreachByKey map[string]string
	outreachCount int
}

// NewFakeRegistry 创建默认 fake Tool 注册表。
func NewFakeRegistry() *FakeRegistry {
	return &FakeRegistry{outreachByKey: make(map[string]string)}
}

// Execute 执行已由 Policy Gate 选中的 Tool。
func (r *FakeRegistry) Execute(_ context.Context, toolID string, input map[string]string, idempotencyKey string) (string, error) {
	switch toolID {
	case "user_query":
		return fmt.Sprintf("已查询示例用户信息：%s", input["message"]), nil
	case "simulated_outreach":
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
	default:
		return "", fmt.Errorf("Tool %q 未注册", toolID)
	}
}

// OutreachCount 返回模拟副作用实际执行次数。
func (r *FakeRegistry) OutreachCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.outreachCount
}

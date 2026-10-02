// Package examplebusiness 提供模板示例业务的 fake Tool runtime。
//
// 这里复用父项目的“工具先注册、再按 id 执行”边界；不连接真实运营系统。
package examplebusiness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	domaintool "github.com/observer-mimiron/supervisor-template/internal/domain/tool"
	"unicode/utf8"
)

// FakeRuntime 保存只读查询和模拟副作用 Tool 的执行结果。
type FakeRuntime struct {
	mu            sync.Mutex
	outreachByKey map[string]string
	outreachCount int
	handlers      map[string]fakeHandler
	toolDelays    map[string]time.Duration
}

// SetToolDelay makes the named Tool take at least delay before answering.
//
// A fake exists to reproduce behaviour a real dependency would show, and
// latency is one of them: without a step that is still running, an acceptance
// Case cannot cancel a Run *while it executes*, which is the only way to prove
// the cancel signal is wired up. Zero (the default) leaves every existing
// Case's timing unchanged. The wait observes the context, so a cancelled Run
// behaves like a real Tool that notices cancellation.
func (r *FakeRuntime) SetToolDelay(toolID string, delay time.Duration) {
	if r == nil || delay <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.toolDelays == nil {
		r.toolDelays = map[string]time.Duration{}
	}
	r.toolDelays[toolID] = delay
}

func (r *FakeRuntime) toolDelay(toolID string) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.toolDelays[toolID]
}

type fakeHandler func(map[string]string, string) (string, error)

type summaryOutput struct {
	Count          int      `json:"count"`
	Spend365dTotal int      `json:"spend_365d_total"`
	Segments       []string `json:"segments"`
}

func decodeStrictObject(message string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader([]byte(message)))
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("输入必须是 JSON 对象")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("输入包含尾随数据")
	}
	return object, nil
}

func rejectUnknownFields(object map[string]json.RawMessage, allowed ...string) error {
	set := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		set[key] = struct{}{}
	}
	for key := range object {
		if _, ok := set[key]; !ok {
			return fmt.Errorf("输入字段 %q 未注册", key)
		}
	}
	return nil
}

func decodeInt(object map[string]json.RawMessage, key string, required bool) (int, bool, error) {
	raw, ok := object[key]
	if !ok {
		if required {
			return 0, false, fmt.Errorf("输入缺少 %s", key)
		}
		return 0, false, nil
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false, err
	}
	return value, true, nil
}

func decodeIDs(object map[string]json.RawMessage, required bool) ([]string, error) {
	raw, ok := object["customer_ids"]
	if !ok {
		if required {
			return nil, errors.New("输入缺少 customer_ids")
		}
		return nil, nil
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil, err
	}
	return ids, nil
}

func validateAudiencePayload(object map[string]json.RawMessage) (int, []string, int, error) {
	if err := rejectUnknownFields(object, "count", "customer_ids", "spend_365d_total"); err != nil {
		return 0, nil, 0, err
	}
	count, _, err := decodeInt(object, "count", true)
	if err != nil {
		return 0, nil, 0, err
	}
	ids, err := decodeIDs(object, true)
	if err != nil {
		return 0, nil, 0, err
	}
	spend, _, err := decodeInt(object, "spend_365d_total", true)
	if err != nil {
		return 0, nil, 0, err
	}
	if count < 0 || count > 100 || count != len(ids) || spend < 0 {
		return 0, nil, 0, errors.New("客群结果超出边界")
	}
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	for i, id := range sorted {
		if id == "" || !strings.HasPrefix(id, "cust-") || (i > 0 && sorted[i-1] == id) {
			return 0, nil, 0, errors.New("客群 customer_ids 无效")
		}
	}
	return count, ids, spend, nil
}

func marshalBounded(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if len(data) > 1<<20 {
		return "", errors.New("Tool 输出超过大小限制")
	}
	return string(data), nil
}

// NewFakeRegistry 创建默认 fake Tool 注册表。
func NewFakeRuntime() *FakeRuntime {
	r := &FakeRuntime{outreachByKey: make(map[string]string), handlers: make(map[string]fakeHandler)}
	r.handlers[ReadOnlyToolID] = func(input map[string]string, _ string) (string, error) {
		object, err := decodeStrictObject(input["message"])
		if err != nil || len(object) != 1 {
			return "", errors.New("查询输入无效")
		}
		if err := rejectUnknownFields(object, "as_of"); err != nil {
			return "", err
		}
		var asOf string
		if err := json.Unmarshal(object["as_of"], &asOf); err != nil {
			return "", err
		}
		query, err := json.Marshal(map[string]string{"as_of": asOf})
		if err != nil {
			return "", err
		}
		audience, err := QueryAudience(query)
		if err != nil {
			return "", fmt.Errorf("查询输入无效: %w", err)
		}
		return marshalBounded(audience)
	}
	r.handlers[SummaryToolID] = func(input map[string]string, _ string) (string, error) {
		object, err := decodeStrictObject(input["message"])
		if err != nil {
			return "", fmt.Errorf("汇总输入无效: %w", err)
		}
		count, _, spend, err := validateAudiencePayload(object)
		if err != nil {
			return "", err
		}
		segments := []string{"dormant", "consented"}
		if count == 0 {
			segments = []string{"empty"}
		}
		return marshalBounded(summaryOutput{Count: count, Spend365dTotal: spend, Segments: segments})
	}
	r.handlers[SideEffectToolID] = func(input map[string]string, idempotencyKey string) (string, error) {
		if idempotencyKey == "" {
			return "", errors.New("副作用 Tool 缺少幂等键")
		}
		object, err := decodeStrictObject(input["message"])
		if err != nil {
			return "", fmt.Errorf("触达输入无效: %w", err)
		}
		if err := rejectUnknownFields(object, "count", "customer_ids", "spend_365d_total"); err != nil {
			return "", err
		}
		ids, err := decodeIDs(object, true)
		if err != nil {
			return "", err
		}
		countValue, hasCount, err := decodeInt(object, "count", false)
		if err != nil {
			return "", err
		}
		spendValue, hasSpend, err := decodeInt(object, "spend_365d_total", false)
		if err != nil {
			return "", err
		}
		if len(ids) > 100 || hasCount && (countValue < 0 || countValue != len(ids)) || hasSpend && spendValue < 0 {
			return "", errors.New("触达输入超出边界")
		}
		sorted := append([]string(nil), ids...)
		sort.Strings(sorted)
		for i, id := range sorted {
			if id == "" || !strings.HasPrefix(id, "cust-") || (i > 0 && sorted[i-1] == id) {
				return "", errors.New("触达 customer_ids 无效")
			}
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if result, ok := r.outreachByKey[idempotencyKey]; ok {
			return result, nil
		}
		r.outreachCount++
		result := fmt.Sprintf("已模拟触达匿名用户：%d", len(ids))
		r.outreachByKey[idempotencyKey] = result
		return result, nil
	}
	return r
}

// Execute 执行已由 Policy Gate 选中的 Tool。
func (r *FakeRuntime) Execute(ctx context.Context, toolID string, input map[string]string, idempotencyKey string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	contracts := ToolContracts(ReadOnlyToolFake)
	var contractFound bool
	for _, contract := range contracts {
		if contract.ToolID == toolID {
			contractFound = true
			if err := validateFakeInput(contract, input); err != nil {
				return "", err
			}
			break
		}
	}
	if !contractFound {
		return "", fmt.Errorf("Tool %q 未注册", toolID)
	}
	if delay := r.toolDelay(toolID); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timer.C:
		}
	}
	handler, ok := r.handlers[toolID]
	if !ok {
		return "", fmt.Errorf("Tool %q 未注册", toolID)
	}
	return handler(input, idempotencyKey)
}

// ValidateInput validates the fake registry's registered Tool schema.
func (r *FakeRuntime) ValidateInput(toolID string, input map[string]string) error {
	for _, contract := range ToolContracts(ReadOnlyToolFake) {
		if contract.ToolID == toolID {
			return validateFakeInput(contract, input)
		}
	}
	return fmt.Errorf("Tool %q 未注册", toolID)
}

// ValidateOutput validates the fake registry's registered Tool output schema.
func (r *FakeRuntime) ValidateOutput(toolID, output string) error {
	for _, contract := range ToolContracts(ReadOnlyToolFake) {
		if contract.ToolID == toolID {
			return validateFakeOutput(contract, output)
		}
	}
	return fmt.Errorf("Tool %q 未注册", toolID)
}

// OutreachCount 返回模拟副作用实际执行次数。
func (r *FakeRuntime) OutreachCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.outreachCount
}

func validateFakeInput(contract domaintool.Contract, input map[string]string) error {
	if input == nil {
		return errors.New("Tool 输入不能为空")
	}
	for key, value := range input {
		if strings.TrimSpace(key) == "" || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
			return errors.New("Tool 输入包含非法字段")
		}
	}
	for _, required := range contract.RequiredInputs {
		if strings.TrimSpace(input[required]) == "" {
			return fmt.Errorf("Tool 输入缺少 %s", required)
		}
	}
	if contract.MaxInputBytes > 0 {
		total := 0
		for key, value := range input {
			total += len(key) + len(value)
		}
		if total > contract.MaxInputBytes {
			return errors.New("Tool 输入超过大小限制")
		}
	}
	return nil
}

func validateFakeOutput(contract domaintool.Contract, output string) error {
	if strings.TrimSpace(output) == "" {
		return errors.New("Tool 输出为空")
	}
	if !utf8.ValidString(output) {
		return errors.New("Tool 输出不是有效 UTF-8")
	}
	if contract.MaxOutputBytes > 0 && len(output) > contract.MaxOutputBytes {
		return errors.New("Tool 输出超过大小限制")
	}
	return nil
}

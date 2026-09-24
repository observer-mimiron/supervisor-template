// Package tool 提供模板的 Tool 适配器。
//
// 本文件只实现固定地址的 HTTP GET 只读能力，负责输入校验、超时和响应体上限；
// 权限、审批和公开结果安全仍由上层合同负责。
package tool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxReadOnlyBodyBytes = 1 << 20

// HTTPReadOnlyTool 通过固定配置地址执行 HTTP GET，不接受模型提供的目标 URL。
type HTTPReadOnlyTool struct {
	endpoint string
	client   *http.Client
}

// NewHTTPReadOnlyTool 创建标准库 HTTP 只读适配器。
func NewHTTPReadOnlyTool(endpoint string, timeout time.Duration) (*HTTPReadOnlyTool, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("只读 Tool endpoint 必须是 http/https 地址")
	}
	client := &http.Client{}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return &HTTPReadOnlyTool{endpoint: parsed.String(), client: client}, nil
}

// Execute 使用输入中的 message 作为查询参数，并只接受 2xx 文本响应。
func (t *HTTPReadOnlyTool) Execute(ctx context.Context, input map[string]string) (string, error) {
	if t == nil || t.client == nil {
		return "", errors.New("只读 Tool 未装配")
	}
	message := strings.TrimSpace(input["message"])
	if message == "" {
		return "", errors.New("只读 Tool 缺少 message")
	}
	target, err := url.Parse(t.endpoint)
	if err != nil {
		return "", errors.New("只读 Tool endpoint 无效")
	}
	query := target.Query()
	query.Set("message", message)
	target.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return "", errors.New("创建只读 Tool 请求失败")
	}
	request.Header.Set("Accept", "text/plain, application/json")
	request.Header.Set("User-Agent", "eino-supervisor-template/1")
	response, err := t.client.Do(request)
	if err != nil {
		kind := FailureUnavailable
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			kind = FailureTimeout
		} else if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			kind = FailureCanceled
		}
		return "", &InvocationFailure{Kind: kind, Err: err}
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		kind := FailureBusiness
		if response.StatusCode >= http.StatusInternalServerError {
			kind = FailureUnavailable
		}
		return "", &InvocationFailure{Kind: kind, Err: fmt.Errorf("只读 Tool 返回 HTTP %d", response.StatusCode)}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxReadOnlyBodyBytes+1))
	if err != nil {
		return "", &InvocationFailure{Kind: FailureUnavailable, Err: errors.New("读取只读 Tool 响应失败")}
	}
	if len(body) > maxReadOnlyBodyBytes {
		return "", &InvocationFailure{Kind: FailureInvalidOutput, Err: errors.New("只读 Tool 响应超过大小限制")}
	}
	result := strings.TrimSpace(string(body))
	if result == "" {
		return "", &InvocationFailure{Kind: FailureInvalidOutput, Err: errors.New("只读 Tool 响应为空")}
	}
	return result, nil
}

// Package mcp 提供受控的 MCP Tool HTTP 适配器。
//
// 本文件只负责协议传输、服务器/工具 allow-list、超时和错误分类；
// 不向 Domain 暴露 MCP 类型，也不决定 Worker、Policy Gate 或最终答复。
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const maxResponseBytes = 1 << 20
const maxArgumentBytes = 64 << 10

// Server 描述一个启动时注册的 MCP 服务端及其工具白名单。
type Server struct {
	Endpoint     string
	AllowedTools []string
}

// ErrorClass 是可安全记录和映射的 MCP 失败分类。
type ErrorClass string

const (
	ErrorInvalidConfig ErrorClass = "invalid_config"
	ErrorDenied        ErrorClass = "server_denied"
	ErrorTimeout       ErrorClass = "timeout"
	ErrorUnavailable   ErrorClass = "unavailable"
	ErrorHTTP          ErrorClass = "http_error"
	ErrorProtocol      ErrorClass = "protocol_error"
	ErrorBusiness      ErrorClass = "business_error"
	ErrorService       ErrorClass = "service_error"
)

// Error 保留 MCP 错误分类，同时隐藏底层协议细节给上层公开响应。
type Error struct {
	Class ErrorClass
	Err   error
}

// Error 返回底层错误文本，供日志和测试使用。
func (e *Error) Error() string { return e.Err.Error() }

// Unwrap 支持标准库的 context 超时判断。
func (e *Error) Unwrap() error { return e.Err }

// Classify 返回 MCP 错误的稳定分类。
func Classify(err error) ErrorClass {
	var classified *Error
	if errors.As(err, &classified) {
		return classified.Class
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorTimeout
	}
	return ErrorService
}

// Client 调用启动时注册且通过 allow-list 的 MCP 服务端。
type Client struct {
	servers map[string]Server
	client  *http.Client
}

// NewClient 创建 MCP 客户端；空服务器表是合法的默认关闭状态。
func NewClient(servers map[string]Server, timeout time.Duration) (*Client, error) {
	registered := make(map[string]Server, len(servers))
	for serverID, server := range servers {
		if strings.TrimSpace(serverID) == "" {
			return nil, &Error{Class: ErrorInvalidConfig, Err: errors.New("MCP server id 不能为空")}
		}
		parsed, err := url.Parse(strings.TrimSpace(server.Endpoint))
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return nil, &Error{Class: ErrorInvalidConfig, Err: fmt.Errorf("MCP server %q endpoint 无效", serverID)}
		}
		if len(server.AllowedTools) == 0 {
			return nil, &Error{Class: ErrorInvalidConfig, Err: fmt.Errorf("MCP server %q 没有工具 allow-list", serverID)}
		}
		server.AllowedTools = append([]string(nil), server.AllowedTools...)
		registered[serverID] = server
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{servers: registered, client: &http.Client{Timeout: timeout}}, nil
}

// Call 调用固定服务器上的固定工具，不接受模型提供的 endpoint 或服务器名。
func (c *Client) Call(ctx context.Context, serverID, toolID string, arguments map[string]string) (string, error) {
	if c == nil || c.client == nil {
		return "", &Error{Class: ErrorService, Err: errors.New("MCP client 未装配")}
	}
	if strings.TrimSpace(toolID) == "" {
		return "", &Error{Class: ErrorProtocol, Err: errors.New("MCP tool id 不能为空")}
	}
	server, ok := c.servers[serverID]
	if !ok || !contains(server.AllowedTools, toolID) {
		return "", &Error{Class: ErrorDenied, Err: errors.New("MCP server 或 tool 不在 allow-list")}
	}
	for key, value := range arguments {
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n\x00") || strings.ContainsAny(value, "\r\n\x00") {
			return "", &Error{Class: ErrorProtocol, Err: errors.New("MCP 参数包含非法字符")}
		}
	}
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      toolID,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      toolID,
			"arguments": arguments,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", &Error{Class: ErrorProtocol, Err: err}
	}
	if len(body) > maxArgumentBytes {
		return "", &Error{Class: ErrorProtocol, Err: errors.New("MCP 请求参数超过大小限制")}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.Endpoint, strings.NewReader(string(body)))
	if err != nil {
		return "", &Error{Class: ErrorInvalidConfig, Err: err}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", &Error{Class: ErrorTimeout, Err: err}
		}
		return "", &Error{Class: ErrorUnavailable, Err: err}
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		class := ErrorHTTP
		if response.StatusCode >= http.StatusInternalServerError {
			class = ErrorUnavailable
		}
		return "", &Error{Class: class, Err: fmt.Errorf("MCP server returned HTTP %d", response.StatusCode)}
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return "", &Error{Class: ErrorUnavailable, Err: err}
	}
	if len(responseBody) > maxResponseBytes {
		return "", &Error{Class: ErrorProtocol, Err: errors.New("MCP response exceeds size limit")}
	}
	return parseResult(responseBody)
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// parseResult 接受 MCP 常见 text content 形态，并拒绝不可公开的任意结构。
func parseResult(data []byte) (string, error) {
	var response rpcResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return "", &Error{Class: ErrorProtocol, Err: err}
	}
	if response.Error != nil {
		return "", &Error{Class: ErrorBusiness, Err: errors.New(response.Error.Message)}
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Text   string `json:"text"`
		Output string `json:"output"`
	}
	if err := json.Unmarshal(response.Result, &result); err == nil {
		for _, item := range result.Content {
			if item.Type == "text" && strings.TrimSpace(item.Text) != "" {
				return strings.TrimSpace(item.Text), nil
			}
		}
		if strings.TrimSpace(result.Text) != "" {
			return strings.TrimSpace(result.Text), nil
		}
		if strings.TrimSpace(result.Output) != "" {
			return strings.TrimSpace(result.Output), nil
		}
	}
	var direct string
	if err := json.Unmarshal(response.Result, &direct); err == nil && strings.TrimSpace(direct) != "" {
		return strings.TrimSpace(direct), nil
	}
	return "", &Error{Class: ErrorProtocol, Err: errors.New("MCP result 缺少 text content")}
}

// contains 判断工具是否在固定 allow-list 中。
func contains(values []string, want string) bool {
	return slices.Contains(values, want)
}

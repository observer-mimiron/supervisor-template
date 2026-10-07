package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/observer-mimiron/supervisor-template/internal/application"
	"github.com/observer-mimiron/supervisor-template/internal/composition"
	"github.com/observer-mimiron/supervisor-template/internal/config"
	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	einoinfra "github.com/observer-mimiron/supervisor-template/internal/infrastructure/eino"
)

func TestChatProjectsReadOnlyRunAsOrderedSSE(t *testing.T) {
	router := newTestRouter(t)
	body := "{\"conversation_id\":\"demo\",\"message\":\"分析示例用户分群\"}"
	request := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	withBearer(request)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content type = %q", response.Header().Get("Content-Type"))
	}
	if response.Header().Get("X-Trace-ID") == "" || response.Header().Get("X-Request-ID") == "" {
		t.Fatalf("trace/request response headers missing: %#v", response.Header())
	}
	output := response.Body.String()
	if !strings.Contains(output, "event: completed") || !strings.Contains(output, "\"sequence\":1") || !strings.Contains(output, "\"trace_id\":\""+response.Header().Get("X-Trace-ID")+"\"") {
		t.Fatalf("unexpected SSE output: %s", output)
	}
}

func TestChatFakeFlowCompletesWithinFiveSeconds(t *testing.T) {
	router := newTestRouter(t)
	request := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"conversation_id":"demo","message":"分析示例用户分群"}`))
	request.Header.Set("Content-Type", "application/json")
	withBearer(request)
	response := httptest.NewRecorder()
	started := time.Now()
	router.ServeHTTP(response, request)
	if elapsed := time.Since(started); elapsed >= 5*time.Second {
		t.Fatalf("fake /api/chat flow took %s", elapsed)
	}
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "event: completed") {
		t.Fatalf("unexpected timed smoke response: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestChatSerialTwoWorkerFlowProjectsOrderedSteps(t *testing.T) {
	router := newTestRouter(t)
	request := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"conversation_id":"demo","message":"先分析用户，再总结结果"}`))
	request.Header.Set("Content-Type", "application/json")
	withBearer(request)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, want := range []string{"\"step_count\":\"2\"", "\"step_1_worker_id\":\"user_analysis\"", "\"step_2_worker_id\":\"user_summary\"", "\"step_1_tool_id\":\"user_query\"", "\"step_2_tool_id\":\"user_summary_query\"", "event: completed"} {
		if !strings.Contains(body, want) {
			t.Fatalf("serial flow missing %q: %s", want, body)
		}
	}
	if strings.Index(body, "user_query") > strings.Index(body, "user_summary_query") {
		t.Fatalf("serial SSE order reversed: %s", body)
	}
}

func TestProtectedRoutesRequireBearerToken(t *testing.T) {
	router := newTestRouter(t)
	request := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"conversation_id":"demo","message":"分析示例用户分群"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

// TestHealthEndpointReturnsJSON 固定 /healthz 的对外形状：健康与不健康使用同一个
// JSON 包络与同一个字段名，只有状态码不同。此前健康分支返回纯文本 `ok`，探针必须
// 为两种 content-type 各写一套解析，且这条路由没有任何测试断言它的响应体。
func TestHealthEndpointReturnsJSON(t *testing.T) {
	router, app := newTestRouterWithApp(t)
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("healthy status = %d, body = %s", response.Code, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("healthy content type = %q", contentType)
	}
	if body := strings.TrimSpace(response.Body.String()); body != `{"status":"ok"}` {
		t.Fatalf("healthy body = %s", body)
	}

	// 依赖不完整：同一个包络、字段名不变，状态码变为 503。
	unhealthy := NewRouter(app.Run, application.HealthService{}, app.Authenticator, Options{})
	request = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response = httptest.NewRecorder()

	unhealthy.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unhealthy status = %d, body = %s", response.Code, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("unhealthy content type = %q", contentType)
	}
	if body := strings.TrimSpace(response.Body.String()); body != `{"status":"unhealthy"}` {
		t.Fatalf("unhealthy body = %s", body)
	}
}

func TestSSEProjectionRedactsSensitiveFields(t *testing.T) {
	router := newTestRouter(t)
	request := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"conversation_id":"demo","message":"分析示例用户分群"}`))
	request.Header.Set("Content-Type", "application/json")
	withBearer(request)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "authorization") {
		t.Fatalf("unexpected redaction response: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRedactEventDataDropsSensitiveKeysAndValues(t *testing.T) {
	data := redactEventData(map[string]string{
		"token":         "secret-token",
		"prompt_text":   "private prompt",
		"trace":         "stack trace: internal/path",
		"content":       "safe result",
		"note":          "Bearer hidden",
		"authorization": "hidden",
	})
	if len(data) != 1 || data["content"] != "safe result" {
		t.Fatalf("sensitive event data was not redacted: %#v", data)
	}
}

func TestApprovalRejectsClientSuppliedReviewer(t *testing.T) {
	router := newTestRouter(t)
	request := httptest.NewRequest(http.MethodPost, "/api/runs/unknown/approval", strings.NewReader(`{"decision":"approve","reviewer":"forged"}`))
	request.Header.Set("Content-Type", "application/json")
	withBearer(request)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "INVALID_REQUEST") {
		t.Fatalf("unexpected reviewer rejection: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRunAuthorizationDoesNotRevealRunExistence(t *testing.T) {
	router := newTestRouter(t)
	create := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"run_id":"run-authz","conversation_id":"demo","message":"模拟触达示例用户"}`))
	create.Header.Set("Content-Type", "application/json")
	withBearer(create)
	created := httptest.NewRecorder()
	router.ServeHTTP(created, create)
	if created.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}

	foreign := httptest.NewRequest(http.MethodPost, "/api/runs/run-authz/approval", strings.NewReader(`{"decision":"approve"}`))
	foreign.Header.Set("Content-Type", "application/json")
	withBearerToken(foreign, "other-token")
	foreignResponse := httptest.NewRecorder()
	router.ServeHTTP(foreignResponse, foreign)

	missing := httptest.NewRequest(http.MethodPost, "/api/runs/no-such-run/approval", strings.NewReader(`{"decision":"approve"}`))
	missing.Header.Set("Content-Type", "application/json")
	withBearerToken(missing, "other-token")
	missingResponse := httptest.NewRecorder()
	router.ServeHTTP(missingResponse, missing)

	if foreignResponse.Code != http.StatusForbidden || missingResponse.Code != http.StatusForbidden {
		t.Fatalf("authorization responses differ: foreign=%d/%s missing=%d/%s", foreignResponse.Code, foreignResponse.Body.String(), missingResponse.Code, missingResponse.Body.String())
	}
	if !strings.Contains(foreignResponse.Body.String(), "ACCESS_DENIED") || !strings.Contains(missingResponse.Body.String(), "ACCESS_DENIED") {
		t.Fatalf("authorization classification leaked: foreign=%s missing=%s", foreignResponse.Body.String(), missingResponse.Body.String())
	}
}

func TestApprovalAndResumeAreSeparateHTTPSteps(t *testing.T) {
	router := newTestRouter(t)
	body := "{\"conversation_id\":\"demo\",\"run_id\":\"run-http-approval\",\"message\":\"模拟触达示例用户\"}"
	request := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	withBearer(request)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "approval_required") || strings.Contains(response.Body.String(), "event: completed") {
		t.Fatalf("unexpected approval SSE: status=%d body=%s", response.Code, response.Body.String())
	}

	approvalBody, _ := json.Marshal(map[string]string{"decision": "approve"})
	approvalRequest := httptest.NewRequest(http.MethodPost, "/api/runs/run-http-approval/approval", bytes.NewReader(approvalBody))
	approvalRequest.Header.Set("Content-Type", "application/json")
	withBearer(approvalRequest)
	approvalResponse := httptest.NewRecorder()
	router.ServeHTTP(approvalResponse, approvalRequest)
	if approvalResponse.Code != http.StatusOK {
		t.Fatalf("approval status = %d, body = %s", approvalResponse.Code, approvalResponse.Body.String())
	}

	resumeRequest := httptest.NewRequest(http.MethodPost, "/api/runs/run-http-approval/resume", nil)
	withBearer(resumeRequest)
	resumeResponse := httptest.NewRecorder()
	router.ServeHTTP(resumeResponse, resumeRequest)
	if resumeResponse.Code != http.StatusOK || !strings.Contains(resumeResponse.Body.String(), "event: completed") {
		t.Fatalf("unexpected resume SSE: status=%d body=%s", resumeResponse.Code, resumeResponse.Body.String())
	}
}

func TestEinoSelectedHTTPApprovalAndResumeStayApplicationOwned(t *testing.T) {
	var supervisorCalls, workerCalls atomic.Int32
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer integration-key" {
			http.Error(w, "unexpected model request", http.StatusBadRequest)
			return
		}
		var input struct {
			Tools    []json.RawMessage `json:"tools"`
			Stream   bool              `json:"stream"`
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "invalid model request", http.StatusBadRequest)
			return
		}
		message := map[string]any{"role": "assistant"}
		finishReason := "stop"
		hasToolResult := false
		for _, message := range input.Messages {
			hasToolResult = hasToolResult || message.Role == "tool"
		}
		if len(input.Tools) == 0 {
			supervisorCalls.Add(1)
			message["content"] = `{"decision_id":"model","worker_id":"user_analysis","intent":"user_analysis","arguments":{"tool_id":"simulated_outreach","message":"{\"count\":4,\"customer_ids\":[\"cust-001\",\"cust-002\",\"cust-006\",\"cust-008\"],\"spend_365d_total\":6200}"},"risk":"side_effect","confidence":0.9}`
		} else if hasToolResult {
			message["content"] = "已模拟触达示例用户"
		} else {
			workerCalls.Add(1)
			message["content"] = nil
			message["tool_calls"] = []any{map[string]any{
				"id": "call-1", "type": "function",
				"function": map[string]string{"name": "simulated_outreach", "arguments": `{"tool_id":"simulated_outreach","message":"{\"count\":4,\"customer_ids\":[\"cust-001\",\"cust-002\",\"cust-006\",\"cust-008\"],\"spend_365d_total\":6200}"}`},
			}}
			call := message["tool_calls"].([]any)[0].(map[string]any)
			function := call["function"].(map[string]string)
			function["arguments"] = strings.Replace(function["arguments"], `{"tool_id":"simulated_outreach",`, `{`, 1)
			finishReason = "tool_calls"
		}
		if input.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			writeChunk := func(delta map[string]any, finish string) {
				chunk, _ := json.Marshal(map[string]any{
					"id": "local", "object": "chat.completion.chunk", "created": 1, "model": "deepseek-chat",
					"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
				})
				_, _ = w.Write(append(append([]byte("data: "), chunk...), '\n', '\n'))
			}
			writeChunk(map[string]any{"role": "assistant"}, "")
			delta := map[string]any{}
			if calls, ok := message["tool_calls"]; ok {
				delta["tool_calls"] = calls
			} else if content, ok := message["content"]; ok {
				delta["content"] = content
			}
			writeChunk(delta, finishReason)
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "local", "object": "chat.completion", "created": 1, "model": "deepseek-chat",
			"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finishReason}},
			"usage":   map[string]int{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer modelServer.Close()
	t.Setenv("TEST_EINO_LLM_API_KEY", "integration-key")
	t.Setenv("AGENT_AUTH_DEMO_TOKEN_SHA256", tokenSHA256("test-token"))
	cfg, err := config.Load(filepath.Join("..", "..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Model.Provider = "deepseek"
	cfg.Model.Name = "deepseek-chat"
	cfg.Model.BaseURL = modelServer.URL
	cfg.Model.APIKeyEnv = "TEST_EINO_LLM_API_KEY"
	worker := cfg.Agent.Workers["user_analysis"]
	worker.Runner = "eino_adk"
	cfg.Agent.Workers["user_analysis"] = worker
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	app, err := composition.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(context.Background())
	router := NewRouter(app.Run, app.Health, app.Authenticator, Options{})
	dispatcher, ok := app.Health.Dependencies.Runner.(*composition.WorkerRunnerDispatcher)
	if !ok {
		t.Fatalf("runner dispatcher = %T", app.Health.Dependencies.Runner)
	}
	if runner, ok := dispatcher.RunnerFor("user_analysis"); !ok {
		t.Fatal("Eino-selected Worker was not registered")
	} else if _, ok := runner.(*einoinfra.Runner); !ok {
		t.Fatalf("selected Worker runner = %T, want *eino.Runner", runner)
	}
	send := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		withBearer(req)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	create := func(runID string) *httptest.ResponseRecorder {
		return send(http.MethodPost, "/api/chat", `{"run_id":"`+runID+`","conversation_id":"demo","message":"模拟触达示例用户"}`)
	}
	if response := create("run-eino-reject"); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "approval_required") {
		t.Fatalf("initial approval response = %d %s", response.Code, response.Body.String())
	}
	if supervisorCalls.Load() != 1 || workerCalls.Load() != 0 || app.FakeWriteCount() != 0 {
		t.Fatalf("pending Eino run executed early: supervisor=%d worker=%d writes=%d", supervisorCalls.Load(), workerCalls.Load(), app.FakeWriteCount())
	}
	if response := send(http.MethodPost, "/api/runs/run-eino-reject/approval", `{"decision":"reject"}`); response.Code != http.StatusOK {
		t.Fatalf("reject response = %d %s", response.Code, response.Body.String())
	}
	rejected := app.Run.Events("run-eino-reject")
	if len(rejected) == 0 || rejected[len(rejected)-1].Type != agent.Failed || workerCalls.Load() != 0 || app.FakeWriteCount() != 0 {
		t.Fatalf("rejected Eino run executed: events=%#v worker=%d writes=%d", rejected, workerCalls.Load(), app.FakeWriteCount())
	}
	if response := create("run-eino-approve"); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "approval_required") {
		t.Fatalf("second approval response = %d %s", response.Code, response.Body.String())
	}
	if response := send(http.MethodPost, "/api/runs/run-eino-approve/approval", `{"decision":"approve"}`); response.Code != http.StatusOK {
		t.Fatalf("approve response = %d %s", response.Code, response.Body.String())
	}
	for i := 0; i < 2; i++ {
		response := send(http.MethodPost, "/api/runs/run-eino-approve/resume", "")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "event: completed") {
			t.Fatalf("resume %d response = %d %s (supervisor=%d worker=%d writes=%d)", i, response.Code, response.Body.String(), supervisorCalls.Load(), workerCalls.Load(), app.FakeWriteCount())
		}
	}
	if workerCalls.Load() != 1 || app.FakeWriteCount() != 1 {
		t.Fatalf("approved Eino step replayed: worker=%d writes=%d", workerCalls.Load(), app.FakeWriteCount())
	}
	terminalCount := 0
	for _, event := range app.Run.Events("run-eino-approve") {
		if event.Type == agent.Completed || event.Type == agent.Failed || event.Type == agent.Canceled {
			terminalCount++
		}
	}
	if terminalCount != 1 {
		t.Fatalf("terminal event count = %d", terminalCount)
	}
}

func TestCancelProjectsCanceledSSE(t *testing.T) {
	router := newTestRouter(t)
	body := "{\"conversation_id\":\"demo\",\"run_id\":\"run-http-cancel\",\"message\":\"模拟触达示例用户\"}"
	request := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	withBearer(request)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "approval_required") {
		t.Fatalf("unexpected initial approval response: status=%d body=%s", response.Code, response.Body.String())
	}

	cancelRequest := httptest.NewRequest(http.MethodPost, "/api/runs/run-http-cancel/cancel", nil)
	withBearer(cancelRequest)
	cancelResponse := httptest.NewRecorder()
	router.ServeHTTP(cancelResponse, cancelRequest)
	if cancelResponse.Code != http.StatusOK || !strings.Contains(cancelResponse.Body.String(), "event: canceled") || strings.Contains(cancelResponse.Body.String(), "event: completed") {
		t.Fatalf("unexpected cancel SSE: status=%d body=%s", cancelResponse.Code, cancelResponse.Body.String())
	}
}

func TestUnknownCapabilityRemainsClassifiedInSSE(t *testing.T) {
	router := newTestRouter(t)
	body := "{\"conversation_id\":\"demo\",\"message\":\"未知能力\"}"
	request := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	withBearer(request)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "UNKNOWN_CAPABILITY") {
		t.Fatalf("unexpected unknown-capability response: status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "stack trace") || strings.Contains(response.Body.String(), "internal/") {
		t.Fatalf("public SSE leaked internals: %s", response.Body.String())
	}
}

func TestArbitraryUnmatchedMessageDoesNotCallReadOnlyTool(t *testing.T) {
	router, app := newTestRouterWithApp(t)
	body := `{"conversation_id":"demo","message":"请帮我写一首诗"}`
	request := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	withBearer(request)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "UNKNOWN_CAPABILITY") {
		t.Fatalf("unexpected unmatched response: status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "event: tool_call") {
		t.Fatalf("unmatched message entered Tool execution: %s", response.Body.String())
	}
	dispatcher, ok := app.Health.Dependencies.Runner.(*composition.WorkerRunnerDispatcher)
	if !ok {
		t.Fatal("composition did not install WorkerRunnerDispatcher")
	}
	runnerValue, ok := dispatcher.RunnerFor("user_analysis")
	if !ok {
		t.Fatal("composition did not install user_analysis runner")
	}
	if _, ok := runnerValue.(application.SingleToolRunner); !ok {
		t.Fatalf("composition installed runner %T, want SingleToolRunner", runnerValue)
	}
	if app.FakeWriteCount() != 0 {
		t.Fatalf("unmatched message entered a Tool: count=%d", app.FakeWriteCount())
	}
}

func newTestRouter(t *testing.T) *gin.Engine {
	router, _ := newTestRouterWithApp(t)
	return router
}

func newTestRouterWithApp(t *testing.T) (*gin.Engine, *composition.App) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("AGENT_AUTH_DEMO_TOKEN_SHA256", tokenSHA256("test-token"))
	t.Setenv("AGENT_AUTH_OTHER_TOKEN_SHA256", tokenSHA256("other-token"))
	cfg, err := config.Load(filepath.Join("..", "..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Model.Provider = "fake"
	cfg.Auth.Credentials = append(cfg.Auth.Credentials, config.BearerCredential{
		TokenSHA256Env: "AGENT_AUTH_OTHER_TOKEN_SHA256",
		TenantID:       "other-tenant",
		SubjectID:      "other-user",
	})
	app, err := composition.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(app.Run, app.Health, app.Authenticator, Options{}), app
}

func withBearer(request *http.Request) {
	withBearerToken(request, "test-token")
}

func withBearerToken(request *http.Request, token string) {
	request.Header.Set("Authorization", "Bearer "+token)
}

func tokenSHA256(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func TestChatProjectsSyntheticAudienceAndSummaryResults(t *testing.T) {
	router := newTestRouter(t)
	requests := []string{
		`{"conversation_id":"demo","message":"分析沉睡客户召回客群"}`,
		`{"conversation_id":"demo","message":"先分析沉睡客户，再总结结果"}`,
	}
	for _, body := range requests {
		request := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		withBearer(request)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		output := response.Body.String()
		for _, want := range []string{`"count\":4`, `cust-001`, `cust-008`, `"spend_365d_total\":6200`, "event: completed"} {
			if !strings.Contains(output, want) {
				t.Fatalf("response missing %q: %s", want, output)
			}
		}
	}
}

// sseEventData 解析 SSE 响应体，返回指定事件类型的包络数据。
//
// 只读 data: 行并做严格 JSON 解析，避免用字符串匹配把"某个事件里出现过这个 ID"
// 误当成"审批事件绑定到了这一步"。
func sseEventData(t *testing.T, body string, want agent.EventType) map[string]string {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var envelope EventEnvelope
		if err := json.Unmarshal([]byte(strings.ReplaceAll(payload, "\\n", "\n")), &envelope); err != nil {
			continue
		}
		if envelope.Type == want {
			return envelope.Data
		}
	}
	return nil
}

// TestApprovalHTTPInterfaceCarriesTheStepDimension 覆盖 spec §4 的对外接口变更。
//
// 混合计划下审批必须指名写入那一步：省略 step_id 时缺省取当前待审批的步骤，
// 显式指定时必须是同一步，指错动作要被拒绝而不是"顺手批准别的东西"。
func TestApprovalHTTPInterfaceCarriesTheStepDimension(t *testing.T) {
	router := newTestRouter(t)
	runID := "run-http-mixed"
	body := `{"conversation_id":"demo","run_id":"` + runID + `","message":"混合：分析目标客群然后触达"}`
	chatRequest := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
	chatRequest.Header.Set("Content-Type", "application/json")
	withBearer(chatRequest)
	chatResponse := httptest.NewRecorder()
	router.ServeHTTP(chatResponse, chatRequest)
	if chatResponse.Code != http.StatusOK {
		t.Fatalf("chat status = %d, body = %s", chatResponse.Code, chatResponse.Body.String())
	}
	approvalData := sseEventData(t, chatResponse.Body.String(), agent.ApprovalRequired)
	if approvalData == nil {
		t.Fatalf("混合计划必须产生审批事件: %s", chatResponse.Body.String())
	}
	sideEffectStep := runID + ":step-2"
	if approvalData["step_id"] != sideEffectStep || approvalData["tool_id"] != "simulated_outreach" {
		t.Fatalf("审批事件必须绑定写入步骤 %q，实际 %#v", sideEffectStep, approvalData)
	}
	if strings.Contains(chatResponse.Body.String(), "event: completed") {
		t.Fatal("批准前不得完成")
	}

	approve := func(stepID string) *httptest.ResponseRecorder {
		t.Helper()
		payload := map[string]string{"decision": "approve"}
		if stepID != "" {
			payload["step_id"] = stepID
		}
		encoded, _ := json.Marshal(payload)
		request := httptest.NewRequest(http.MethodPost, "/api/runs/"+runID+"/approval", bytes.NewReader(encoded))
		request.Header.Set("Content-Type", "application/json")
		withBearer(request)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}

	// 指到只读步骤：必须拒绝，且不得放行写入。
	if wrong := approve(runID + ":step-1"); wrong.Code == http.StatusOK {
		t.Fatalf("批准一个不等待审批的步骤应被拒绝，得到 %d: %s", wrong.Code, wrong.Body.String())
	}
	resumeAfterWrong := httptest.NewRecorder()
	resumeRequest := httptest.NewRequest(http.MethodPost, "/api/runs/"+runID+"/resume", nil)
	withBearer(resumeRequest)
	router.ServeHTTP(resumeAfterWrong, resumeRequest)
	if strings.Contains(resumeAfterWrong.Body.String(), "event: completed") {
		t.Fatalf("指错步骤的批准不得放行写入: %s", resumeAfterWrong.Body.String())
	}

	// 先只读步骤不应存在审批记录：对它的批准被拒绝即为证据。
	// 正确的步骤：显式指定应被接受，并在响应里回显 step_id。
	accepted := approve(sideEffectStep)
	if accepted.Code != http.StatusOK {
		t.Fatalf("批准写入步骤失败: %d %s", accepted.Code, accepted.Body.String())
	}
	if !strings.Contains(accepted.Body.String(), sideEffectStep) {
		t.Fatalf("审批响应应回显 step_id: %s", accepted.Body.String())
	}

	resumeResponse := httptest.NewRecorder()
	resumeValid := httptest.NewRequest(http.MethodPost, "/api/runs/"+runID+"/resume", nil)
	withBearer(resumeValid)
	router.ServeHTTP(resumeResponse, resumeValid)
	if resumeResponse.Code != http.StatusOK || !strings.Contains(resumeResponse.Body.String(), "event: completed") {
		t.Fatalf("批准后 resume 应完成: status=%d body=%s", resumeResponse.Code, resumeResponse.Body.String())
	}
}

// TestApprovalHTTPInterfaceDefaultsToTheWaitingStep 证明 step_id 省略时仍可用。
func TestApprovalHTTPInterfaceDefaultsToTheWaitingStep(t *testing.T) {
	router := newTestRouter(t)
	runID := "run-http-default-step"
	body := `{"conversation_id":"demo","run_id":"` + runID + `","message":"模拟触达"}`
	chatRequest := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
	chatRequest.Header.Set("Content-Type", "application/json")
	withBearer(chatRequest)
	chatResponse := httptest.NewRecorder()
	router.ServeHTTP(chatResponse, chatRequest)
	if chatResponse.Code != http.StatusOK {
		t.Fatalf("chat status = %d, body = %s", chatResponse.Code, chatResponse.Body.String())
	}
	encoded, _ := json.Marshal(map[string]string{"decision": "approve"})
	approvalRequest := httptest.NewRequest(http.MethodPost, "/api/runs/"+runID+"/approval", bytes.NewReader(encoded))
	approvalRequest.Header.Set("Content-Type", "application/json")
	withBearer(approvalRequest)
	approvalResponse := httptest.NewRecorder()
	router.ServeHTTP(approvalResponse, approvalRequest)
	if approvalResponse.Code != http.StatusOK {
		t.Fatalf("省略 step_id 的审批应缺省取当前待审批步骤: %d %s", approvalResponse.Code, approvalResponse.Body.String())
	}
}

package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/observer-mimiron/supervisor-template/internal/composition"
	"github.com/observer-mimiron/supervisor-template/internal/config"
)

// newStreamingTestRouter 装配一个慢 Tool 的应用，用来观察长 Run 期间的 SSE 行为。
func newStreamingTestRouter(t *testing.T, delayMS int, options Options) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("AGENT_AUTH_DEMO_TOKEN_SHA256", tokenSHA256("test-token"))
	cfg, err := config.Load(filepath.Join("..", "..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Model.Provider = "fake"
	tool := cfg.Tools["user_query"]
	tool.FakeDelayMS = delayMS
	cfg.Tools["user_query"] = tool
	app, err := composition.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close(context.Background()) })
	return NewRouter(app.Run, app.Health, app.Authenticator, options)
}

// TestLongRunEmitsSSEHeartbeat 证明 sse_heartbeat 真的生效：Run 超过心跳间隔后，
// 响应会先提交为 SSE 并持续发送注释，长连接不会因为整个 Run 期间无数据而被切断。
func TestLongRunEmitsSSEHeartbeat(t *testing.T) {
	router := newStreamingTestRouter(t, 400, Options{SSEHeartbeat: 50 * time.Millisecond})

	request := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"conversation_id":"demo","message":"分析沉睡客户召回客群"}`))
	withBearer(request)
	recorder := httptest.NewRecorder()
	start := time.Now()
	router.ServeHTTP(recorder, request)
	elapsed := time.Since(start)

	body := recorder.Body.String()
	if !strings.Contains(body, ": heartbeat") {
		t.Fatalf("long run should emit SSE heartbeats, body=%q", body)
	}
	if !strings.Contains(body, "event: completed") {
		t.Fatalf("heartbeat stream must still deliver the terminal event, body=%q", body)
	}
	if strings.Count(body, "event: completed") != 1 {
		t.Fatalf("terminal event must stay unique, body=%q", body)
	}
	if elapsed < 400*time.Millisecond {
		t.Fatalf("test did not exercise a long run, elapsed=%s", elapsed)
	}
}

// TestFastRunKeepsJSONErrorSemantics 是对照组：心跳间隔内完成的调用不能改变原有行为。
// 恢复一个不存在的 Run 会在授权阶段失败且没有任何事件，因此必须是 JSON 错误
// （fail-closed 返回 ACCESS_DENIED），而不是被推进带心跳的 SSE 分支。
func TestFastRunKeepsJSONErrorSemantics(t *testing.T) {
	router := newStreamingTestRouter(t, 0, Options{SSEHeartbeat: time.Second})

	request := httptest.NewRequest(http.MethodPost, "/api/runs/missing-run/resume", strings.NewReader(""))
	withBearer(request)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	body := recorder.Body.String()
	if strings.Contains(body, ": heartbeat") || strings.Contains(body, "event: ") {
		t.Fatalf("run without events must keep JSON error semantics, body=%q", body)
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d, body=%q", recorder.Code, http.StatusForbidden, body)
	}
	if !strings.Contains(body, "ACCESS_DENIED") {
		t.Fatalf("body should carry the stable error code, got %q", body)
	}
}

// TestRequestBodyLimitComesFromOptions 证明 server.request_body_limit 真的被使用。
func TestRequestBodyLimitComesFromOptions(t *testing.T) {
	router := newStreamingTestRouter(t, 0, Options{RequestBodyLimit: 64})

	request := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"conversation_id":"demo","message":"`+strings.Repeat("a", 4096)+`"}`))
	withBearer(request)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body over configured limit)", recorder.Code, http.StatusBadRequest)
	}
}

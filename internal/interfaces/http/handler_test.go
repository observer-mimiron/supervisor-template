package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/observer-mimiron/suanming-agent/eino-supervisor-template/internal/composition"
	"github.com/observer-mimiron/suanming-agent/eino-supervisor-template/internal/config"
)

func TestChatProjectsReadOnlyRunAsOrderedSSE(t *testing.T) {
	router := newTestRouter(t)
	body := "{\"conversation_id\":\"demo\",\"message\":\"分析示例用户分群\"}"
	request := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content type = %q", response.Header().Get("Content-Type"))
	}
	output := response.Body.String()
	if !strings.Contains(output, "event: completed") || !strings.Contains(output, "\"sequence\":1") {
		t.Fatalf("unexpected SSE output: %s", output)
	}
}

func TestApprovalAndResumeAreSeparateHTTPSteps(t *testing.T) {
	router := newTestRouter(t)
	body := "{\"conversation_id\":\"demo\",\"run_id\":\"run-http-approval\",\"message\":\"模拟触达示例用户\"}"
	request := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "approval_required") || strings.Contains(response.Body.String(), "event: completed") {
		t.Fatalf("unexpected approval SSE: status=%d body=%s", response.Code, response.Body.String())
	}

	approvalBody, _ := json.Marshal(map[string]string{"decision": "approve", "reviewer": "operator-1"})
	approvalRequest := httptest.NewRequest(http.MethodPost, "/api/runs/run-http-approval/approval", bytes.NewReader(approvalBody))
	approvalRequest.Header.Set("Content-Type", "application/json")
	approvalResponse := httptest.NewRecorder()
	router.ServeHTTP(approvalResponse, approvalRequest)
	if approvalResponse.Code != http.StatusOK {
		t.Fatalf("approval status = %d, body = %s", approvalResponse.Code, approvalResponse.Body.String())
	}

	resumeRequest := httptest.NewRequest(http.MethodPost, "/api/runs/run-http-approval/resume", nil)
	resumeResponse := httptest.NewRecorder()
	router.ServeHTTP(resumeResponse, resumeRequest)
	if resumeResponse.Code != http.StatusOK || !strings.Contains(resumeResponse.Body.String(), "event: completed") {
		t.Fatalf("unexpected resume SSE: status=%d body=%s", resumeResponse.Code, resumeResponse.Body.String())
	}
}

func TestCancelProjectsCanceledSSE(t *testing.T) {
	router := newTestRouter(t)
	body := "{\"conversation_id\":\"demo\",\"run_id\":\"run-http-cancel\",\"message\":\"模拟触达示例用户\"}"
	request := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "approval_required") {
		t.Fatalf("unexpected initial approval response: status=%d body=%s", response.Code, response.Body.String())
	}

	cancelRequest := httptest.NewRequest(http.MethodPost, "/api/runs/run-http-cancel/cancel", nil)
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
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "UNKNOWN_CAPABILITY") {
		t.Fatalf("unexpected unknown-capability response: status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "stack trace") || strings.Contains(response.Body.String(), "internal/") {
		t.Fatalf("public SSE leaked internals: %s", response.Body.String())
	}
}

func newTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg, err := config.Load(filepath.Join("..", "..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	app, err := composition.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(app.Run, app.Health)
}

package http

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/observer-mimiron/supervisor-template/internal/composition"
	"github.com/observer-mimiron/supervisor-template/internal/config"
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
	output := response.Body.String()
	if !strings.Contains(output, "event: completed") || !strings.Contains(output, "\"sequence\":1") {
		t.Fatalf("unexpected SSE output: %s", output)
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

func newTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("AGENT_AUTH_DEMO_TOKEN_SHA256", tokenSHA256("test-token"))
	t.Setenv("AGENT_AUTH_OTHER_TOKEN_SHA256", tokenSHA256("other-token"))
	cfg, err := config.Load(filepath.Join("..", "..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Auth.Credentials = append(cfg.Auth.Credentials, config.BearerCredential{
		TokenSHA256Env: "AGENT_AUTH_OTHER_TOKEN_SHA256",
		TenantID:       "other-tenant",
		SubjectID:      "other-user",
	})
	app, err := composition.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(app.Run, app.Health, app.Authenticator)
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

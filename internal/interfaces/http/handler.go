// Package http 负责 Gin 请求适配、SSE 事件投影和公开错误映射。
//
// 本包不推进运行状态，不直接调用 Tool；所有业务动作都通过 application/run 完成。
package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/observer-mimiron/supervisor-template/internal/application"
	"github.com/observer-mimiron/supervisor-template/internal/application/run"
	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
	"github.com/observer-mimiron/supervisor-template/internal/domain/identity"
)

const subjectContextKey = "authenticated_subject"

// Request 是 POST /api/chat 的公开输入。
type Request struct {
	ConversationID string `json:"conversation_id"`
	Message        string `json:"message"`
	RunID          string `json:"run_id,omitempty"`
}

// ApprovalInput 是审批接口的公开输入。
type ApprovalInput struct {
	Decision string `json:"decision"`
}

// EventEnvelope 是 SSE 的稳定公开包络。
type EventEnvelope struct {
	EventID  string            `json:"event_id"`
	RunID    string            `json:"run_id"`
	TraceID  string            `json:"trace_id,omitempty"`
	Sequence int64             `json:"sequence"`
	Type     agent.EventType   `json:"type"`
	Data     map[string]string `json:"data,omitempty"`
}

// NewRouter 创建经认证的 M1/M2 HTTP 路由；Gin 只做协议适配和事件投影。
func NewRouter(service *run.Service, health application.HealthService, authenticator application.Authenticator) *gin.Engine {
	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(traceMiddleware())
	router.GET("/healthz", func(c *gin.Context) {
		if !health.Healthy() {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy"})
			return
		}
		c.String(http.StatusOK, "ok")
	})
	api := router.Group("/api", authenticate(authenticator))
	api.POST("/chat", func(c *gin.Context) { handleChat(c, service) })
	api.POST("/runs/:run_id/approval", func(c *gin.Context) { handleApproval(c, service) })
	api.POST("/runs/:run_id/resume", func(c *gin.Context) { handleResume(c, service) })
	api.POST("/runs/:run_id/cancel", func(c *gin.Context) { handleCancel(c, service) })
	return router
}

// authenticate 将 HTTP Bearer 凭证解析为可信主体，未认证请求不会进入应用主链路。
func authenticate(authenticator application.Authenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := bearerToken(c.GetHeader("Authorization"))
		if !ok || authenticator == nil {
			writePublicError(c, http.StatusUnauthorized, &run.Error{Code: agent.ErrorUnauthenticated, Message: "身份凭证无效"})
			c.Abort()
			return
		}
		subject, err := authenticator.Authenticate(c.Request.Context(), token)
		if err != nil {
			writePublicError(c, http.StatusUnauthorized, &run.Error{Code: agent.ErrorUnauthenticated, Message: "身份凭证无效"})
			c.Abort()
			return
		}
		c.Set(subjectContextKey, subject)
		c.Next()
	}
}

// bearerToken 只接受一个非空的 Bearer 凭证，避免把其他认证方案误送入认证器。
func bearerToken(header string) (string, bool) {
	scheme, token, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return "", false
	}
	return strings.TrimSpace(token), true
}

// requestSubject 读取认证中间件写入的主体；路由组保证其存在。
func requestSubject(c *gin.Context) (identity.Subject, bool) {
	value, ok := c.Get(subjectContextKey)
	subject, valid := value.(identity.Subject)
	return subject, ok && valid && subject.Valid()
}

// handleChat 将请求交给运行用例，再把已有事件按顺序投影为 SSE。
func handleChat(c *gin.Context, service *run.Service) {
	subject, ok := requestSubject(c)
	if !ok {
		writePublicError(c, http.StatusUnauthorized, &run.Error{Code: agent.ErrorUnauthenticated, Message: "身份凭证无效"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	var input Request
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writePublicError(c, http.StatusBadRequest, &run.Error{Code: "INVALID_REQUEST", Message: "请求格式无效"})
		return
	}
	runID, err := service.Start(c.Request.Context(), conversation.ExecutionRequest{
		RunID:          input.RunID,
		ConversationID: input.ConversationID,
		Subject:        subject,
		Message:        input.Message,
	})
	events := service.Events(runID)
	if coded, ok := err.(*run.Error); ok && coded.Code == agent.ErrorRunNotResumable {
		writePublicErrorWithRunID(c, statusFor(err), err, runID)
		return
	}
	if err != nil && len(events) == 0 {
		writePublicErrorWithRunID(c, statusFor(err), err, runID)
		return
	}
	writeEvents(c, events)
}

// handleApproval 只写审批结果，不在 HTTP 层执行副作用 Tool。
func handleApproval(c *gin.Context, service *run.Service) {
	subject, ok := requestSubject(c)
	if !ok {
		writePublicError(c, http.StatusUnauthorized, &run.Error{Code: agent.ErrorUnauthenticated, Message: "身份凭证无效"})
		return
	}
	var input ApprovalInput
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writePublicError(c, http.StatusBadRequest, &run.Error{Code: "INVALID_REQUEST", Message: "审批请求格式无效"})
		return
	}
	runID := c.Param("run_id")
	if err := service.Approve(c.Request.Context(), subject, runID, input.Decision); err != nil {
		writePublicErrorWithRunID(c, statusFor(err), err, runID)
		return
	}
	c.JSON(http.StatusOK, gin.H{"run_id": runID, "decision": input.Decision})
}

// handleResume 调用运行用例恢复未完成步骤，终态只重放原事件。
func handleResume(c *gin.Context, service *run.Service) {
	subject, ok := requestSubject(c)
	if !ok {
		writePublicError(c, http.StatusUnauthorized, &run.Error{Code: agent.ErrorUnauthenticated, Message: "身份凭证无效"})
		return
	}
	runID := c.Param("run_id")
	events, err := service.Resume(c.Request.Context(), subject, runID)
	if err != nil {
		if hasTerminalEvent(events) {
			writeEvents(c, events)
			return
		}
		writePublicErrorWithRunID(c, statusFor(err), err, runID)
		return
	}
	writeEvents(c, events)
}

// handleCancel 将显式取消交给运行用例，并投影保存的事件。
func handleCancel(c *gin.Context, service *run.Service) {
	subject, ok := requestSubject(c)
	if !ok {
		writePublicError(c, http.StatusUnauthorized, &run.Error{Code: agent.ErrorUnauthenticated, Message: "身份凭证无效"})
		return
	}
	runID := c.Param("run_id")
	if err := service.Cancel(c.Request.Context(), subject, runID); err != nil {
		writePublicErrorWithRunID(c, statusFor(err), err, runID)
		return
	}
	writeEvents(c, service.Events(runID))
}

// writeEvents 复用父项目的 event/data/flush SSE 写法，但只投影领域事件。
func writeEvents(c *gin.Context, events []agent.RunEvent) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	for _, event := range events {
		payload, err := json.Marshal(EventEnvelope{
			EventID:  event.EventID,
			RunID:    event.RunID,
			TraceID:  traceID(c),
			Sequence: event.Sequence,
			Type:     event.Type,
			Data:     redactEventData(event.Data),
		})
		if err != nil {
			writePublicError(c, http.StatusInternalServerError, &run.Error{Code: "INTERNAL_ERROR", Message: "事件投影失败"})
			return
		}
		if _, err := fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event.Type, strings.ReplaceAll(string(payload), "\n", "\\n")); err != nil {
			return
		}
		c.Writer.Flush()
	}
}

func redactEventData(data map[string]string) map[string]string {
	if data == nil {
		return nil
	}
	redacted := make(map[string]string, len(data))
	for key, value := range data {
		lowerKey := strings.ToLower(key)
		lowerValue := strings.ToLower(value)
		if strings.Contains(lowerKey, "token") || strings.Contains(lowerKey, "secret") ||
			strings.Contains(lowerKey, "authorization") || strings.Contains(lowerKey, "prompt") ||
			strings.Contains(lowerKey, "stack") || strings.Contains(lowerKey, "path") ||
			strings.Contains(lowerValue, "bearer ") || strings.Contains(lowerValue, "stack trace") {
			continue
		}
		redacted[key] = value
	}
	return redacted
}

// hasTerminalEvent 判断恢复失败是否已经保存了可重放的终态事件。
func hasTerminalEvent(events []agent.RunEvent) bool {
	if len(events) == 0 {
		return false
	}
	switch events[len(events)-1].Type {
	case agent.Completed, agent.Failed, agent.Canceled:
		return true
	default:
		return false
	}
}

// writePublicError 只公开稳定分类、用户消息和 run_id，不透传内部错误。
func writePublicError(c *gin.Context, status int, err error) {
	writePublicErrorWithRunID(c, status, err, "")
}

// writePublicErrorWithRunID 只公开稳定分类、用户消息和可用的 run_id。
func writePublicErrorWithRunID(c *gin.Context, status int, err error, runID string) {
	code := "INTERNAL_ERROR"
	message := "服务暂时不可用"
	if coded, ok := err.(*run.Error); ok {
		code, message = string(coded.Code), coded.Message
	}
	body := gin.H{"code": code, "message": message}
	if runID != "" {
		body["run_id"] = runID
	}
	c.JSON(status, body)
}

// statusFor 将领域错误映射为稳定 HTTP 状态，不改变错误分类。
func statusFor(err error) int {
	if coded, ok := err.(*run.Error); ok {
		switch coded.Code {
		case "INVALID_REQUEST":
			return http.StatusBadRequest
		case agent.ErrorApprovalRequired:
			return http.StatusConflict
		case agent.ErrorRunNotResumable:
			return http.StatusNotFound
		case agent.ErrorPolicyDenied, agent.ErrorUnknownCapability:
			return http.StatusForbidden
		case agent.ErrorAccessDenied:
			return http.StatusForbidden
		case agent.ErrorUnauthenticated:
			return http.StatusUnauthorized
		case agent.ErrorToolTimeout:
			return http.StatusGatewayTimeout
		case agent.ErrorCanceled:
			return http.StatusConflict
		case agent.ErrorOutcomeUnknown:
			return http.StatusConflict
		case agent.ErrorInvalidState:
			return http.StatusConflict
		case agent.ErrorRunBusy, agent.ErrorLeaseLost:
			return http.StatusConflict
		case agent.ErrorBudgetExceeded:
			return http.StatusTooManyRequests
		case agent.ErrorInvalidOutput:
			return http.StatusBadGateway
		}
	}
	return http.StatusInternalServerError
}

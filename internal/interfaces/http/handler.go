// Package http 负责 Gin 请求适配、SSE 事件投影和公开错误映射。
//
// 本包不推进运行状态，不直接调用 Tool；所有业务动作都通过 application/run 完成。
// 它也不读取配置对象：需要的运行参数由启动装配投影成 Options，避免传输层依赖配置。
package http

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/observer-mimiron/supervisor-template/internal/application"
	"github.com/observer-mimiron/supervisor-template/internal/application/run"
	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
	"github.com/observer-mimiron/supervisor-template/internal/domain/identity"
)

const (
	subjectContextKey = "authenticated_subject"
	optionsContextKey = "http_options"
)

// Options 是从启动配置投影到 HTTP 适配层的运行参数。
//
// 零值可用：withDefaults 会补齐与配置层一致的默认值，避免这里再出现第二套硬编码。
type Options struct {
	// RequestBodyLimit 是单个请求体的字节上限，同时用于 /api/chat 和审批接口。
	RequestBodyLimit int64
	// SSEHeartbeat 是 SSE 连接的空闲刷新间隔。Run 超过该间隔仍未结束时，
	// 响应会立即提交为 SSE 并开始发送心跳注释，避免长连接被代理判定为空闲。
	SSEHeartbeat time.Duration
	// WriteTimeout 是"两次写之间的空闲上限"，不是整个响应的时长。
	// 每次写出前都会用它刷新连接写截止时间，因此长 Run 不会被截断。
	WriteTimeout time.Duration
}

func (o Options) withDefaults() Options {
	if o.RequestBodyLimit <= 0 {
		o.RequestBodyLimit = 1 << 20
	}
	if o.SSEHeartbeat <= 0 {
		o.SSEHeartbeat = 15 * time.Second
	}
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = 30 * time.Second
	}
	return o
}

// Request 是 POST /api/chat 的公开输入。
type Request struct {
	ConversationID string `json:"conversation_id"`
	Message        string `json:"message"`
	RunID          string `json:"run_id,omitempty"`
}

// ApprovalInput 是审批接口的公开输入。
//
// step_id 可选：省略时针对该 Run 当前等待审批的那一步。指定时必须正好是那一步，
// 否则接口拒绝——批准是针对一个具体动作的，不能写给别的步骤。
type ApprovalInput struct {
	Decision string `json:"decision"`
	StepID   string `json:"step_id,omitempty"`
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

// NewRouter 创建经认证的 HTTP 路由；Gin 只做协议适配和事件投影。
func NewRouter(service *run.Service, health application.HealthService, authenticator application.Authenticator, options Options) *gin.Engine {
	options = options.withDefaults()
	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(traceMiddleware())
	router.Use(func(c *gin.Context) {
		c.Set(optionsContextKey, options)
		c.Next()
	})
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

// optionsOf 读取路由装配注入的运行参数；缺失时退回安全默认值。
func optionsOf(c *gin.Context) Options {
	value, ok := c.Get(optionsContextKey)
	options, valid := value.(Options)
	if !ok || !valid {
		return Options{}.withDefaults()
	}
	return options.withDefaults()
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

// runOutcome 是一次运行调用的结果投影：已有事件、run_id 和可能的错误。
//
// eventsOnError 保留各入口原有的错误语义：/api/chat 在已有事件时仍然投影事件，
// /api/runs/:id/cancel 则始终用 JSON 错误响应，避免取消失败被读成成功流。
type runOutcome struct {
	events        []agent.RunEvent
	runID         string
	err           error
	eventsOnError bool
}

// handleChat 将请求交给运行用例，再把已有事件按顺序投影为 SSE。
func handleChat(c *gin.Context, service *run.Service) {
	subject, ok := requestSubject(c)
	if !ok {
		writePublicError(c, http.StatusUnauthorized, &run.Error{Code: agent.ErrorUnauthenticated, Message: "身份凭证无效"})
		return
	}
	var input Request
	if err := decodeRequest(c, &input); err != nil {
		writePublicError(c, http.StatusBadRequest, &run.Error{Code: "INVALID_REQUEST", Message: "请求格式无效"})
		return
	}
	request := conversation.ExecutionRequest{
		RunID:          input.RunID,
		ConversationID: input.ConversationID,
		Subject:        subject,
		Message:        input.Message,
	}
	done := make(chan runOutcome, 1)
	go func() {
		runID, err := service.Start(c.Request.Context(), request)
		done <- runOutcome{events: service.Events(runID), runID: runID, err: err, eventsOnError: true}
	}()
	awaitRun(c, done)
}

// handleApproval 只写审批结果，不在 HTTP 层执行副作用 Tool。
func handleApproval(c *gin.Context, service *run.Service) {
	subject, ok := requestSubject(c)
	if !ok {
		writePublicError(c, http.StatusUnauthorized, &run.Error{Code: agent.ErrorUnauthenticated, Message: "身份凭证无效"})
		return
	}
	var input ApprovalInput
	if err := decodeRequest(c, &input); err != nil {
		writePublicError(c, http.StatusBadRequest, &run.Error{Code: "INVALID_REQUEST", Message: "审批请求格式无效"})
		return
	}
	runID := c.Param("run_id")
	if err := service.Approve(c.Request.Context(), subject, runID, input.StepID, input.Decision); err != nil {
		writePublicErrorWithRunID(c, statusFor(err), err, runID)
		return
	}
	response := gin.H{"run_id": runID, "decision": input.Decision}
	if input.StepID != "" {
		response["step_id"] = input.StepID
	}
	writeJSON(c, http.StatusOK, response)
}

// handleResume 调用运行用例恢复未完成步骤，终态只重放原事件。
func handleResume(c *gin.Context, service *run.Service) {
	subject, ok := requestSubject(c)
	if !ok {
		writePublicError(c, http.StatusUnauthorized, &run.Error{Code: agent.ErrorUnauthenticated, Message: "身份凭证无效"})
		return
	}
	runID := c.Param("run_id")
	done := make(chan runOutcome, 1)
	go func() {
		events, err := service.Resume(c.Request.Context(), subject, runID)
		if err != nil && hasTerminalEvent(events) {
			err = nil
		}
		done <- runOutcome{events: events, runID: runID, err: err, eventsOnError: true}
	}()
	awaitRun(c, done)
}

// handleCancel 将显式取消交给运行用例，并投影保存的事件。
func handleCancel(c *gin.Context, service *run.Service) {
	subject, ok := requestSubject(c)
	if !ok {
		writePublicError(c, http.StatusUnauthorized, &run.Error{Code: agent.ErrorUnauthenticated, Message: "身份凭证无效"})
		return
	}
	runID := c.Param("run_id")
	done := make(chan runOutcome, 1)
	go func() {
		err := service.Cancel(c.Request.Context(), subject, runID)
		done <- runOutcome{events: service.Events(runID), runID: runID, err: err}
	}()
	awaitRun(c, done)
}

// awaitRun 等待一次运行调用。
//
// Run 在 SSE 心跳间隔内完成时保持原有行为：错误仍是 JSON 响应，事件一次性投影。
// 超过间隔仍未结束时，响应提交为 SSE 并开始心跳，让长 Run（等待审批、慢 Tool）
// 不会被代理或网关按空闲连接切断。
func awaitRun(c *gin.Context, done <-chan runOutcome) {
	options := optionsOf(c)
	timer := time.NewTimer(options.SSEHeartbeat)
	defer timer.Stop()
	select {
	case result := <-done:
		writeOutcome(c, result)
	case <-timer.C:
		writeStreaming(c, options, done)
	case <-c.Request.Context().Done():
	}
}

// writeOutcome 投影一次已完成调用的结果。
func writeOutcome(c *gin.Context, result runOutcome) {
	if coded, ok := result.err.(*run.Error); ok && coded.Code == agent.ErrorRunNotResumable {
		writePublicErrorWithRunID(c, statusFor(result.err), result.err, result.runID)
		return
	}
	if result.err != nil && (!result.eventsOnError || len(result.events) == 0) {
		writePublicErrorWithRunID(c, statusFor(result.err), result.err, result.runID)
		return
	}
	writeEventStream(c, result.events)
}

// writeStreaming 在响应已经提交为 SSE 之后继续等待调用结果。
//
// 响应一旦提交就无法再改成 JSON 错误，因此调用失败且没有终态事件时补一条 failed
// 事件，让客户端仍然拿到唯一的终止信号。
func writeStreaming(c *gin.Context, options Options, done <-chan runOutcome) {
	writeSSEHeaders(c)
	writeHeartbeat(c)
	ticker := time.NewTicker(options.SSEHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case result := <-done:
			writeEventStream(c, result.events)
			failed := result.err != nil && (!result.eventsOnError || len(result.events) == 0)
			if failed && !hasTerminalEvent(result.events) {
				writeSyntheticFailure(c, result)
			}
			return
		case <-ticker.C:
			writeHeartbeat(c)
		case <-c.Request.Context().Done():
			return
		}
	}
}

// writeSSEHeaders 固定 SSE 响应头。它必须与首次写出一起提交，因此单独暴露给
// 心跳路径，让响应先于任何领域事件建立起流。
func writeSSEHeaders(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
}

// writeEventStream 复用父项目的 event/data/flush SSE 写法，但只投影领域事件。
func writeEventStream(c *gin.Context, events []agent.RunEvent) {
	if len(events) == 0 && !c.Writer.Written() {
		return
	}
	writeSSEHeaders(c)
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
		if err := writeChunk(c, fmt.Sprintf("event: %s\ndata: %s\n\n", event.Type, strings.ReplaceAll(string(payload), "\n", "\\n"))); err != nil {
			return
		}
	}
}

// writeHeartbeat 发送一条 SSE 注释，保持连接活跃；它不是领域事件，不占用序号。
func writeHeartbeat(c *gin.Context) {
	_ = writeChunk(c, ": heartbeat\n\n")
}

// writeSyntheticFailure 在响应已提交为 SSE、但调用没有产生任何事件时补一条终态事件。
func writeSyntheticFailure(c *gin.Context, result runOutcome) {
	code := "INTERNAL_ERROR"
	message := "服务暂时不可用"
	if coded, ok := result.err.(*run.Error); ok {
		code, message = string(coded.Code), coded.Message
	}
	event := agent.RunEvent{
		EventID:  result.runID + ":client-error",
		RunID:    result.runID,
		Sequence: 1,
		Type:     agent.Failed,
		Data:     map[string]string{"code": code, "message": message},
	}
	writeEventStream(c, []agent.RunEvent{event})
}

// writeChunk 写出一个 SSE 分片，并把连接写截止时间刷新到"现在 + WriteTimeout"。
//
// 这样 server.write_timeout 表达的是写空闲上限；http.Server 的 WriteTimeout 是绝对
// 上限，会截断心跳维持的长 Run，因此服务入口不设置它。
func writeChunk(c *gin.Context, payload string) error {
	options := optionsOf(c)
	if options.WriteTimeout > 0 {
		_ = http.NewResponseController(c.Writer).SetWriteDeadline(time.Now().Add(options.WriteTimeout))
	}
	if _, err := io.WriteString(c.Writer, payload); err != nil {
		return err
	}
	c.Writer.Flush()
	return nil
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

// decodeRequest 按配置的请求体上限解析 JSON，并拒绝未知字段。
func decodeRequest(c *gin.Context, target any) error {
	limit := optionsOf(c).RequestBodyLimit
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
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
	writeJSON(c, status, body)
}

// writeJSON 写出一个非流式响应，并在写出前刷新连接写截止时间。
func writeJSON(c *gin.Context, status int, body any) {
	if options := optionsOf(c); options.WriteTimeout > 0 {
		_ = http.NewResponseController(c.Writer).SetWriteDeadline(time.Now().Add(options.WriteTimeout))
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

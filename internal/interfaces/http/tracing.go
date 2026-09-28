package http

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type headerCarrier struct{ header map[string]string }

func (c headerCarrier) Get(key string) string { return c.header[key] }
func (c headerCarrier) Set(key, value string) { c.header[key] = value }
func (c headerCarrier) Keys() []string {
	keys := make([]string, 0, len(c.header))
	for key := range c.header {
		keys = append(keys, key)
	}
	return keys
}

// traceMiddleware keeps HTTP propagation at the transport boundary.
func traceMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		headers := map[string]string{
			"traceparent": c.GetHeader("traceparent"),
			"tracestate":  c.GetHeader("tracestate"),
		}
		parent := otel.GetTextMapPropagator().Extract(c.Request.Context(), headerCarrier{header: headers})
		ctx, span := otel.Tracer("github.com/observer-mimiron/supervisor-template/http").Start(parent, "http.server", trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(
			attribute.String("eval.case_id", evalHeader(c.GetHeader("X-Eval-Case-ID"))),
			attribute.String("eval.case_version", evalHeader(c.GetHeader("X-Eval-Case-Version"))),
			attribute.String("eval.code_version", evalHeader(c.GetHeader("X-Eval-Code-Version"))),
			attribute.String("eval.evaluator_version", evalHeader(c.GetHeader("X-Eval-Evaluator-Version"))),
		))
		defer span.End()
		traceID := span.SpanContext().TraceID().String()
		if !span.SpanContext().IsValid() {
			traceID = randomID()
		}
		requestID := c.GetHeader("X-Request-ID")
		if requestID == "" {
			requestID = randomID()
		}
		c.Request = c.Request.WithContext(ctx)
		c.Request = c.Request.WithContext(observability.WithRequestID(ctx, requestID))
		c.Header("X-Request-ID", requestID)
		c.Set("trace_id", traceID)
		c.Header("X-Trace-ID", traceID)
		started := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		attrs := metric.WithAttributes(attribute.String("method", c.Request.Method), attribute.String("route", route), attribute.Int("status", c.Writer.Status()))
		meter := otel.Meter("github.com/observer-mimiron/supervisor-template/http")
		if counter, err := meter.Int64Counter("http.server.requests"); err == nil {
			counter.Add(c.Request.Context(), 1, attrs)
		}
		if histogram, err := meter.Float64Histogram("http.server.duration_ms"); err == nil {
			histogram.Record(c.Request.Context(), float64(time.Since(started).Microseconds())/1000, attrs)
		}
		span.SetAttributes(attribute.String("http.route", route), attribute.Int("http.status_code", c.Writer.Status()), attribute.String("request.id", requestID))
	}
}

func evalHeader(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 128 {
		return value[:128]
	}
	return value
}

func randomID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("req-%d", len(raw))
	}
	return hex.EncodeToString(raw[:])
}

// traceID returns the current request trace ID for projections and errors.
func traceID(c *gin.Context) string {
	if value, ok := c.Get("trace_id"); ok {
		if id, valid := value.(string); valid {
			return id
		}
	}
	span := trace.SpanFromContext(c.Request.Context())
	if !span.SpanContext().IsValid() {
		return ""
	}
	return span.SpanContext().TraceID().String()
}

var _ propagation.TextMapCarrier = headerCarrier{}

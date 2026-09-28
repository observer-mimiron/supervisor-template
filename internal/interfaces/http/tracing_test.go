package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTraceMiddlewareAddsLowCardinalityEvaluationAttributes(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer provider.Shutdown(t.Context())
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)

	router := gin.New()
	router.Use(traceMiddleware())
	router.GET("/healthz", func(c *gin.Context) { c.Status(http.StatusOK) })
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("X-Eval-Case-ID", "case-1")
	request.Header.Set("X-Eval-Case-Version", "1")
	request.Header.Set("X-Eval-Code-Version", "dev")
	request.Header.Set("X-Eval-Evaluator-Version", "1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("span count = %d", len(spans))
	}
	attrs := map[string]string{}
	for _, attr := range spans[0].Attributes {
		attrs[string(attr.Key)] = attr.Value.AsString()
	}
	for key, want := range map[string]string{"eval.case_id": "case-1", "eval.case_version": "1", "eval.code_version": "dev", "eval.evaluator_version": "1"} {
		if attrs[key] != want {
			t.Fatalf("attribute %s = %q, want %q", key, attrs[key], want)
		}
	}
}

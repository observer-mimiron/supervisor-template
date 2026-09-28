package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	einocallbacks "github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/observer-mimiron/supervisor-template/internal/application"
	"github.com/observer-mimiron/supervisor-template/internal/config"
	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/eventbus"
)

func TestLogRedactsSensitiveFieldsAndRotatesBoundedFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.jsonl")
	writer, err := NewRotatingFileWriter(path, 24, false, 2)
	if err != nil {
		t.Fatal(err)
	}
	logger := NewLogger("info", writer)
	ctx := WithRequestID(WithFields(context.Background(), map[string]string{"run_id": "run-1", "api_key": "sk-secret", "path": "/tmp/private"}), "request-1")
	Log(ctx, logger, slog.LevelInfo, "operation completed", slog.String("phase", "tool"))
	Log(ctx, logger, slog.LevelInfo, "second operation", slog.String("phase", "tool"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	rotated := false
	for _, entry := range entries {
		data, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(data), "sk-secret") || strings.Contains(string(data), "/tmp/private") {
			t.Fatalf("sensitive value leaked: %s", data)
		}
		if entry.Name() == "agent.jsonl.1" {
			rotated = true
		}
	}
	if !rotated {
		t.Fatal("expected at least one rotated log file")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	if _, err := os.Stat(path + ".3"); err == nil {
		t.Fatalf("rotation exceeded retention: found %s.3", path)
	}
}

func TestDiagnosticFieldsUseStableKeysAndUnits(t *testing.T) {
	var output bytes.Buffer
	logger := NewLogger("info", &output)
	Log(context.Background(), logger, slog.LevelInfo, "diagnostic", DiagnosticFields{
		ErrorCode: "TOOL_TIMEOUT", ErrorClass: "timeout", Phase: "tool", RetryDecision: "never",
		Fingerprint: "abc123", Attempt: 2, Duration: 1500 * time.Millisecond,
	}.Attrs()...)
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["error_code"] != "TOOL_TIMEOUT" || record["error_class"] != "timeout" || record["duration_ms"] != float64(1500) {
		t.Fatalf("unexpected diagnostic record: %#v", record)
	}
}

func TestEventStoreEmitsOnlyStructuralRunAttributes(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer provider.Shutdown(context.Background())
	runtime := &Runtime{tracer: provider.Tracer(instrumentationName)}
	store := runtime.WrapEventStore(eventbus.NewMemoryBus())

	_, err := store.Append(agent.RunEvent{
		EventID: "event-1",
		RunID:   "run-1",
		Type:    agent.Text,
		Data:    map[string]string{"content": "secret should not become an attribute"},
	})
	if err != nil {
		t.Fatal(err)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("span count = %d, want 1", len(spans))
	}
	if spans[0].Name != "run.event" {
		t.Fatalf("span name = %q", spans[0].Name)
	}
	for _, attr := range spans[0].Attributes {
		if attr.Key == "content" || attr.Value.AsString() == "secret should not become an attribute" {
			t.Fatal("event payload leaked into trace attributes")
		}
	}
}

func TestRuntimeObserverCorrelatesApplicationSpanAndRedactsLogFields(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer provider.Shutdown(context.Background())
	var output bytes.Buffer
	runtime := &Runtime{tracer: provider.Tracer(instrumentationName), logger: NewLogger("info", &output)}
	parentCtx, parent := provider.Tracer("http").Start(context.Background(), "http.server")
	parentSpanID := parent.SpanContext().SpanID()
	runtime.Observe(parentCtx, application.RuntimeObservation{
		RunID: "run-1", WorkerID: "user_analysis", ToolID: "user_query", Phase: "tool.success",
		Attempt: 1, Duration: 12 * time.Millisecond,
	})
	parent.End()
	spans := exporter.GetSpans()
	if len(spans) != 2 || spans[0].Name != "agent.tool.success" {
		t.Fatalf("observer spans=%#v", spans)
	}
	if spans[0].Parent.SpanID() != parentSpanID {
		t.Fatalf("observer span parent=%s, http span=%s", spans[0].Parent.SpanID(), parentSpanID)
	}
	if !strings.Contains(output.String(), `"phase":"tool.success"`) || !strings.Contains(output.String(), `"duration_ms":12`) {
		t.Fatalf("structured observer log=%s", output.String())
	}
	if strings.Contains(output.String(), "run-1") == false {
		t.Fatal("observer log lost run correlation")
	}
	for _, attr := range spans[0].Attributes {
		if attr.Key == "content" || attr.Key == "prompt" || strings.Contains(attr.Value.AsString(), "secret") {
			t.Fatalf("observer span leaked payload: %#v", attr)
		}
	}
}

type contextEventStore struct{ ctx context.Context }

func (s *contextEventStore) Append(agent.RunEvent) (agent.RunEvent, error) {
	return agent.RunEvent{}, nil
}

func (s *contextEventStore) Events(string) []agent.RunEvent { return nil }

func (s *contextEventStore) AppendContext(ctx context.Context, event agent.RunEvent) (agent.RunEvent, error) {
	s.ctx = ctx
	return event, nil
}

func (s *contextEventStore) EventsContext(context.Context, string) []agent.RunEvent { return nil }

var _ application.ContextEventStore = (*contextEventStore)(nil)

func TestEventStorePassesChildSpanContextToContextAwareStore(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer provider.Shutdown(context.Background())
	runtime := &Runtime{tracer: provider.Tracer(instrumentationName)}
	next := &contextEventStore{}
	store := runtime.WrapEventStore(next)
	ctx, span := provider.Tracer("test").Start(context.Background(), "parent")
	defer span.End()
	if _, err := store.(application.ContextEventStore).AppendContext(ctx, agent.RunEvent{EventID: "event-1", RunID: "run-1", Type: agent.Started}); err != nil {
		t.Fatal(err)
	}
	if next.ctx == nil {
		t.Fatal("context-aware store did not receive context")
	}
	if got := trace.SpanContextFromContext(next.ctx).TraceID(); !got.IsValid() {
		t.Fatal("child span context was not propagated")
	}
}

func TestEinoCallbackEmitsModelSpanWithoutMessageContent(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer provider.Shutdown(context.Background())
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)

	einocallbacks.InitCallbackHandlers(nil)
	defer einocallbacks.InitCallbackHandlers(nil)
	einocallbacks.AppendGlobalHandlers(newEinoCallbackHandler())
	ctx := einocallbacks.ReuseHandlers(context.Background(), &einocallbacks.RunInfo{
		Name:      "real_supervisor",
		Component: components.ComponentOfChatModel,
	})
	ctx = einocallbacks.OnStart(ctx, &einomodel.CallbackInput{
		Messages: []*schema.Message{schema.UserMessage("sensitive user message")},
	})
	einocallbacks.OnEnd(ctx, &einomodel.CallbackOutput{
		TokenUsage: &einomodel.TokenUsage{PromptTokens: 2, CompletionTokens: 3},
	})

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("span count = %d, want 1", len(spans))
	}
	for _, attr := range spans[0].Attributes {
		if attr.Value.AsString() == "sensitive user message" {
			t.Fatal("model input leaked into trace")
		}
	}
	if spans[0].Name != "eino.real_supervisor" {
		t.Fatalf("span name = %q", spans[0].Name)
	}
}

func TestSetupExportsTraceAndMetricToLocalOTLPCollector(t *testing.T) {
	var mu sync.Mutex
	paths := map[string]int{}
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths[r.URL.Path]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()

	previousTracer := otel.GetTracerProvider()
	previousMeter := otel.GetMeterProvider()
	defer otel.SetTracerProvider(previousTracer)
	defer otel.SetMeterProvider(previousMeter)
	runtime, err := Setup(context.Background(), config.ObservabilityConfig{
		Enabled:         true,
		Endpoint:        collector.URL,
		ServiceName:     "template-test",
		Insecure:        true,
		TraceSampleRate: 1,
		MetricsEnabled:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	store := runtime.WrapEventStore(eventbus.NewMemoryBus())
	if _, err := store.Append(agent.RunEvent{EventID: "event-1", RunID: "run-1", Type: agent.Started}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if paths["/v1/traces"] == 0 || paths["/v1/metrics"] == 0 {
		t.Fatalf("collector paths = %#v, want traces and metrics", paths)
	}
}

func TestSetupCanRunWithoutExporterAndShutdownIsExplicit(t *testing.T) {
	runtime, err := Setup(context.Background(), config.ObservabilityConfig{Enabled: true, ServiceName: "test-no-exporter", TraceSampleRate: 1})
	if err != nil {
		t.Fatal(err)
	}
	if runtime == nil || runtime.tracer == nil {
		t.Fatal("expected local observability runtime")
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLangfuseExporterAddsIngestionHeader(t *testing.T) {
	var gotHeader string
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/traces" {
			gotHeader = r.Header.Get("x-langfuse-ingestion-version")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	t.Setenv("TEST_LANGFUSE_HEADERS", "Authorization=Basic-redacted")
	runtime, err := Setup(context.Background(), config.ObservabilityConfig{
		Enabled: true, LangfuseEnabled: true, LangfuseEndpoint: collector.URL,
		LangfuseHeadersEnv: "TEST_LANGFUSE_HEADERS", ServiceName: "langfuse-test",
		Insecure: true, TraceSampleRate: 1, MetricsEnabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	store := runtime.WrapEventStore(eventbus.NewMemoryBus())
	if _, err := store.Append(agent.RunEvent{EventID: "langfuse-event", RunID: "run-1", Type: agent.Started}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotHeader != "4" {
		t.Fatalf("x-langfuse-ingestion-version = %q, want 4", gotHeader)
	}
}

func TestNormalizeOTLPEndpointAppendsSignalPathToLangfuseBase(t *testing.T) {
	tests := []struct {
		name, raw, signal, want string
	}{
		{name: "collector base", raw: "http://collector:4318", signal: "traces", want: "http://collector:4318/v1/traces"},
		{name: "langfuse base", raw: "http://langfuse/api/public/otel", signal: "traces", want: "http://langfuse/api/public/otel/v1/traces"},
		{name: "existing signal path", raw: "http://langfuse/api/public/otel/v1/traces", signal: "metrics", want: "http://langfuse/api/public/otel/v1/metrics"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeOTLPEndpoint(tt.raw, tt.signal); got != tt.want {
				t.Fatalf("normalizeOTLPEndpoint(%q, %q) = %q, want %q", tt.raw, tt.signal, got, tt.want)
			}
		})
	}
}

func TestTraceSnapshotIsAtomicRedactedAndBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.json")
	spans := []TraceSnapshotSpan{{
		Name: "agent.tool", TraceID: "trace-1", SpanID: "span-1", Status: "ok",
		Attributes: map[string]string{"tool_id": "mysql_order_query", "payload": "do-not-write", "path": "/tmp/private"},
	}}
	if err := WriteTraceSnapshot(path, spans); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode = %o, want 600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "do-not-write") || strings.Contains(string(data), "/tmp/private") {
		t.Fatalf("snapshot leaked sensitive attributes: %s", data)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), ".trace-snapshot-")); err == nil {
		t.Fatal("temporary snapshot file remained")
	}
}

func TestTraceSnapshotCanBeReadAfterProcessRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.json")
	if err := WriteTraceSnapshot(path, []TraceSnapshotSpan{{Name: "run.event", TraceID: "trace-1"}}); err != nil {
		t.Fatal(err)
	}
	spans, err := ReadTraceSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 1 || spans[0].TraceID != "trace-1" {
		t.Fatalf("snapshot after restart = %#v", spans)
	}
}

func TestTraceSnapshotRotatesWithRetention(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trace.json")
	if err := WriteTraceSnapshot(path, []TraceSnapshotSpan{{Name: "first"}}); err != nil {
		t.Fatal(err)
	}
	if err := rotateTraceSnapshotIfNeeded(path, 1, false, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("rotated snapshot missing: %v", err)
	}
	if err := WriteTraceSnapshot(path, []TraceSnapshotSpan{{Name: "second"}}); err != nil {
		t.Fatal(err)
	}
	if err := rotateTraceSnapshotIfNeeded(path, 1, false, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".2"); err != nil {
		t.Fatalf("retained snapshot missing: %v", err)
	}
}

func TestDegradedSignalIsEmittedOnce(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	runtime := &Runtime{tracer: provider.Tracer(instrumentationName)}
	runtime.SignalDegraded(context.Background(), "file")
	runtime.SignalDegraded(context.Background(), "exporter")
	if got := runtime.degradedSignals.Load(); got != 1 {
		t.Fatalf("degraded signal count = %d, want one", got)
	}
}

package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	einocallbacks "github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/observer-mimiron/supervisor-template/internal/config"
	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/eventbus"
)

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

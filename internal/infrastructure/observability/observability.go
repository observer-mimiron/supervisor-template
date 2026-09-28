// Package observability 提供可选的 OpenTelemetry 和 Eino Callback 适配。
//
// 本包只投影运行证据，不参与路由、权限、审批、Tool 结果或终态判断；观测失败不能阻断业务。
package observability

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	einocallbacks "github.com/cloudwego/eino/callbacks"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	einoutils "github.com/cloudwego/eino/utils/callbacks"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/observer-mimiron/supervisor-template/internal/application"
	"github.com/observer-mimiron/supervisor-template/internal/config"
	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
)

const instrumentationName = "github.com/observer-mimiron/supervisor-template"

// NewLogger returns the process logger with the configured level and JSON output.
// Callers keep using slog's standard API; no application code depends on a logger type.
func NewLogger(level string, w io.Writer) *slog.Logger {
	var minimum slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		minimum = slog.LevelDebug
	case "warn", "warning":
		minimum = slog.LevelWarn
	case "error":
		minimum = slog.LevelError
	default:
		minimum = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: minimum}))
}

var callbackOnce sync.Once

type spanKey struct{}

// Runtime 是进程级观测运行时，拥有 tracer、meter 和 exporter 的关闭入口。
type Runtime struct {
	tracer           trace.Tracer
	meter            metric.Meter
	metrics          bool
	traceFile        string
	traceMaxBytes    int64
	traceRotateDaily bool
	traceRetention   int
	snapshotMu       sync.Mutex
	snapshot         []TraceSnapshotSpan
	degraded         sync.Once
	degradedSignals  atomic.Uint32
	degradedCounter  metric.Int64Counter
	logger           *slog.Logger
	shutdown         func(context.Context) error
}

// SetLogger attaches the already-configured process logger without making the
// application layer depend on slog or this package.
func (r *Runtime) SetLogger(logger *slog.Logger) {
	if r != nil {
		r.logger = logger
	}
}

// Observe records one bounded application lifecycle observation. The phase,
// worker and tool identifiers are startup-registered values; payloads never
// cross this port.
func (r *Runtime) Observe(ctx context.Context, observation application.RuntimeObservation) {
	if r == nil {
		return
	}
	phase := strings.TrimSpace(observation.Phase)
	if phase == "" {
		phase = "runtime"
	}
	spanName := "agent." + phase
	attributes := []attribute.KeyValue{
		attribute.String("phase", safeValue("phase", phase)),
		attribute.String("worker.id", safeValue("worker.id", observation.WorkerID)),
		attribute.String("tool.id", safeValue("tool.id", observation.ToolID)),
	}
	spanCtx := ctx
	if spanCtx == nil {
		spanCtx = context.Background()
	}
	spanCtx, span := r.tracer.Start(spanCtx, spanName, trace.WithAttributes(attributes...))
	if observation.Duration >= 0 {
		span.SetAttributes(attribute.Int64("duration_ms", observation.Duration.Milliseconds()))
	}
	if observation.ErrorCode != "" {
		span.SetStatus(codes.Error, safeValue("error_code", observation.ErrorCode))
	}
	if r.metrics && r.meter != nil {
		metricAttrs := metric.WithAttributes(attribute.String("phase", safeValue("phase", phase)))
		if counter, err := r.meter.Int64Counter("agent.runtime.calls"); err == nil {
			counter.Add(spanCtx, 1, metricAttrs)
		}
		if observation.ErrorCode != "" || observation.ErrorClass != "" {
			if counter, err := r.meter.Int64Counter("agent.runtime.errors"); err == nil {
				counter.Add(spanCtx, 1, metricAttrs)
			}
		}
		if observation.Duration >= 0 {
			if histogram, err := r.meter.Float64Histogram("agent.runtime.duration_ms"); err == nil {
				histogram.Record(spanCtx, float64(observation.Duration.Microseconds())/1000, metricAttrs)
			}
		}
	}
	if r.logger != nil {
		level := slog.LevelInfo
		if observation.ErrorCode != "" || observation.ErrorClass != "" {
			level = slog.LevelWarn
		}
		fields := DiagnosticFields{ErrorCode: observation.ErrorCode, ErrorClass: observation.ErrorClass, Phase: phase, RetryDecision: observation.RetryDecision, Attempt: observation.Attempt, Duration: observation.Duration}
		attrs := append(fields.Attrs(), slog.String("run_id", safeValue("run_id", observation.RunID)), slog.String("worker_id", safeValue("worker_id", observation.WorkerID)), slog.String("tool_id", safeValue("tool_id", observation.ToolID)))
		Log(spanCtx, r.logger, level, "runtime."+phase, attrs...)
	}
	r.recordSpan(spanName, span, map[string]string{"run.id": observation.RunID, "worker.id": observation.WorkerID, "tool.id": observation.ToolID, "phase": phase})
	span.End()
}

// SignalDegraded emits one bounded observation-side degradation signal.
// Business state is intentionally unaffected.
func (r *Runtime) SignalDegraded(ctx context.Context, reason string) {
	if r == nil {
		return
	}
	r.degraded.Do(func() {
		r.degradedSignals.Add(1)
		if r.degradedCounter != nil {
			r.degradedCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("signal", safeValue("signal", reason))))
		}
	})
}

// Setup 创建可选的 OTLP trace exporter，并安装一次 Eino 全局 Callback。
// endpoint 为空时仍建立本地 tracer，但不会自动连接外部 Collector。
func Setup(ctx context.Context, cfg config.ObservabilityConfig) (*Runtime, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	serviceName := cfg.ServiceName
	if serviceName == "" {
		serviceName = "eino-supervisor-template"
	}
	if !cfg.Enabled {
		return &Runtime{
			tracer:        otel.Tracer(instrumentationName),
			meter:         otel.Meter(instrumentationName),
			metrics:       cfg.MetricsEnabled,
			traceFile:     strings.TrimSpace(cfg.TraceFile),
			traceMaxBytes: cfg.RotateMaxBytes, traceRotateDaily: cfg.RotateDaily, traceRetention: cfg.RetentionFiles,
			shutdown: func(context.Context) error { return nil },
		}, nil
	}

	resourceAttributes := []attribute.KeyValue{attribute.String("service.name", serviceName)}
	keys := make([]string, 0, len(cfg.ResourceAttributes))
	for key := range cfg.ResourceAttributes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if strings.TrimSpace(key) == "" || key == "service.name" {
			continue
		}
		resourceAttributes = append(resourceAttributes, attribute.String(key, cfg.ResourceAttributes[key]))
	}
	res, err := resource.New(ctx, resource.WithAttributes(resourceAttributes...))
	if err != nil {
		return nil, err
	}
	options := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.TraceSampleRate))),
	}
	endpoint := strings.TrimSpace(cfg.Endpoint)
	headerEnv := "OTEL_EXPORTER_OTLP_HEADERS"
	if cfg.LangfuseEnabled {
		endpoint = strings.TrimSpace(cfg.LangfuseEndpoint)
		if cfg.LangfuseHeadersEnv != "" {
			headerEnv = cfg.LangfuseHeadersEnv
		}
	}
	if endpoint != "" {
		exporterOptions := []otlptracehttp.Option{otlptracehttp.WithEndpointURL(normalizeOTLPEndpoint(endpoint, "traces"))}
		if cfg.Insecure {
			exporterOptions = append(exporterOptions, otlptracehttp.WithInsecure())
		}
		headers := parseHeaders(os.Getenv(headerEnv))
		if cfg.LangfuseEnabled {
			if _, ok := headers["x-langfuse-ingestion-version"]; !ok {
				headers["x-langfuse-ingestion-version"] = "4"
			}
		}
		if len(headers) > 0 {
			exporterOptions = append(exporterOptions, otlptracehttp.WithHeaders(headers))
		}
		exporter, exportErr := otlptracehttp.New(ctx, exporterOptions...)
		if exportErr != nil {
			return nil, exportErr
		}
		options = append(options, sdktrace.WithBatcher(exporter))
	}
	provider := sdktrace.NewTracerProvider(options...)
	otel.SetTracerProvider(provider)
	var meterProvider *sdkmetric.MeterProvider
	if cfg.MetricsEnabled {
		metricOptions := []sdkmetric.Option{sdkmetric.WithResource(res)}
		if endpoint != "" {
			metricExporterOptions := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpointURL(normalizeOTLPEndpoint(endpoint, "metrics"))}
			if cfg.Insecure {
				metricExporterOptions = append(metricExporterOptions, otlpmetrichttp.WithInsecure())
			}
			headers := parseHeaders(os.Getenv(headerEnv))
			if cfg.LangfuseEnabled {
				if _, ok := headers["x-langfuse-ingestion-version"]; !ok {
					headers["x-langfuse-ingestion-version"] = "4"
				}
			}
			if len(headers) > 0 {
				metricExporterOptions = append(metricExporterOptions, otlpmetrichttp.WithHeaders(headers))
			}
			metricExporter, metricErr := otlpmetrichttp.New(ctx, metricExporterOptions...)
			if metricErr != nil {
				_ = provider.Shutdown(context.Background())
				return nil, metricErr
			}
			metricOptions = append(metricOptions, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)))
		}
		meterProvider = sdkmetric.NewMeterProvider(metricOptions...)
		otel.SetMeterProvider(meterProvider)
	}
	callbackOnce.Do(func() {
		einocallbacks.AppendGlobalHandlers(newEinoCallbackHandler())
	})
	runtime := &Runtime{
		tracer:        provider.Tracer(instrumentationName),
		meter:         otel.Meter(instrumentationName),
		metrics:       cfg.MetricsEnabled,
		traceFile:     strings.TrimSpace(cfg.TraceFile),
		traceMaxBytes: cfg.RotateMaxBytes, traceRotateDaily: cfg.RotateDaily, traceRetention: cfg.RetentionFiles,
		shutdown: func(shutdownCtx context.Context) error {
			shutdownErr := provider.Shutdown(shutdownCtx)
			if meterProvider != nil {
				shutdownErr = errors.Join(shutdownErr, meterProvider.Shutdown(shutdownCtx))
			}
			return shutdownErr
		},
	}
	if cfg.MetricsEnabled && runtime.meter != nil {
		runtime.degradedCounter, _ = runtime.meter.Int64Counter("observability.degraded", metric.WithDescription("observation side-channel degradation signals"))
	}
	return runtime, nil
}

// WrapEventStore 为 run、step 和 Tool 事件增加关联 span 和可选计数。
func (r *Runtime) WrapEventStore(next application.EventStore) application.EventStore {
	if r == nil {
		return next
	}
	var counter metric.Int64Counter
	var terminalCounter metric.Int64Counter
	if r.metrics && r.meter != nil {
		counter, _ = r.meter.Int64Counter("agent.run_events", metric.WithDescription("number of emitted run events"))
		terminalCounter, _ = r.meter.Int64Counter("agent.run_terminals", metric.WithDescription("number of terminal run events"))
	}
	return &eventStore{next: next, tracer: r.tracer, counter: counter, terminalCounter: terminalCounter, runtime: r}
}

// Shutdown 关闭 exporter；关闭失败只由进程入口记录，不回写业务状态。
func (r *Runtime) Shutdown(ctx context.Context) error {
	if r == nil || r.shutdown == nil {
		return nil
	}
	err := r.shutdown(ctx)
	if err != nil {
		r.SignalDegraded(ctx, "exporter_shutdown")
	}
	return err
}

func (r *Runtime) recordSpan(name string, span trace.Span, attrs map[string]string) {
	if r == nil || strings.TrimSpace(r.traceFile) == "" || span == nil {
		return
	}
	spanContext := span.SpanContext()
	copyAttrs := make(map[string]string, len(attrs))
	for key, value := range attrs {
		copyAttrs[key] = safeValue(key, value)
	}
	r.snapshotMu.Lock()
	defer r.snapshotMu.Unlock()
	if len(r.snapshot) >= 128 {
		r.snapshot = r.snapshot[1:]
	}
	r.snapshot = append(r.snapshot, TraceSnapshotSpan{Name: name, TraceID: spanContext.TraceID().String(), SpanID: spanContext.SpanID().String(), Attributes: copyAttrs})
	if err := rotateTraceSnapshotIfNeeded(r.traceFile, r.traceMaxBytes, r.traceRotateDaily, r.traceRetention); err != nil {
		r.SignalDegraded(context.Background(), "trace_snapshot_rotation")
	}
	if err := WriteTraceSnapshot(r.traceFile, r.snapshot); err != nil {
		r.SignalDegraded(context.Background(), "trace_snapshot")
	}
}

type eventStore struct {
	next            application.EventStore
	tracer          trace.Tracer
	counter         metric.Int64Counter
	terminalCounter metric.Int64Counter
	runtime         *Runtime
}

func (s *eventStore) AppendContext(ctx context.Context, event agent.RunEvent) (agent.RunEvent, error) {
	spanCtx, span := s.tracer.Start(ctx, "run.event", trace.WithAttributes(
		attribute.String("run.id", event.RunID),
		attribute.String("run.event.type", string(event.Type)),
		attribute.Int64("run.event.sequence", event.Sequence),
	))
	if s.counter != nil {
		s.counter.Add(spanCtx, 1, metric.WithAttributes(attribute.String("event.type", string(event.Type))))
	}
	if s.terminalCounter != nil && isTerminal(event.Type) {
		s.terminalCounter.Add(spanCtx, 1, metric.WithAttributes(attribute.String("terminal", string(event.Type))))
	}
	defer span.End()
	defer func() {
		s.runtime.recordSpan("run.event", span, map[string]string{"run.id": event.RunID, "event.type": string(event.Type)})
	}()
	if err := ctx.Err(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "event append canceled")
		return agent.RunEvent{}, err
	}
	var stored agent.RunEvent
	var err error
	if store, ok := s.next.(application.ContextEventStore); ok {
		stored, err = store.AppendContext(spanCtx, event)
	} else {
		stored, err = s.next.Append(event)
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "event append failed")
	}
	return stored, err
}

func (s *eventStore) EventsContext(ctx context.Context, runID string) []agent.RunEvent {
	if err := ctx.Err(); err != nil {
		return nil
	}
	if store, ok := s.next.(application.ContextEventStore); ok {
		return store.EventsContext(ctx, runID)
	}
	return s.next.Events(runID)
}

func (s *eventStore) Append(event agent.RunEvent) (agent.RunEvent, error) {
	ctx := context.Background()
	spanCtx, span := s.tracer.Start(ctx, "run.event", trace.WithAttributes(
		attribute.String("run.id", event.RunID),
		attribute.String("run.event.type", string(event.Type)),
		attribute.Int64("run.event.sequence", event.Sequence),
	))
	if s.counter != nil {
		s.counter.Add(spanCtx, 1, metric.WithAttributes(attribute.String("event.type", string(event.Type))))
	}
	if s.terminalCounter != nil && isTerminal(event.Type) {
		s.terminalCounter.Add(spanCtx, 1, metric.WithAttributes(attribute.String("terminal", string(event.Type))))
	}
	defer span.End()
	defer func() {
		s.runtime.recordSpan("run.event", span, map[string]string{"run.id": event.RunID, "event.type": string(event.Type)})
	}()
	stored, err := s.next.Append(event)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "event append failed")
	}
	return stored, err
}

func (s *eventStore) Events(runID string) []agent.RunEvent { return s.next.Events(runID) }

func isTerminal(eventType agent.EventType) bool {
	return eventType == agent.Completed || eventType == agent.Failed || eventType == agent.Canceled
}

func observeComponentCall(component string, failed bool) {
	meter := otel.Meter(instrumentationName)
	attrs := metric.WithAttributes(attribute.String("component", component))
	if counter, err := meter.Int64Counter("agent.component.calls"); err == nil {
		counter.Add(context.Background(), 1, attrs)
	}
	if failed {
		if counter, err := meter.Int64Counter("agent.component.errors"); err == nil {
			counter.Add(context.Background(), 1, attrs)
		}
	}
}

// newEinoCallbackHandler 复用 Eino 的官方回调分发，只保留最小可关联字段。
func newEinoCallbackHandler() einocallbacks.Handler {
	return einoutils.NewHandlerHelper().
		ChatModel(&einoutils.ModelCallbackHandler{
			OnStart: func(ctx context.Context, info *einocallbacks.RunInfo, _ *einomodel.CallbackInput) context.Context {
				observeComponentCall("chat_model", false)
				name := "eino.chat_model"
				if info != nil && info.Name != "" && info.Name != string(info.Component) {
					name = "eino." + info.Name
				}
				_, span := otel.Tracer(instrumentationName).Start(ctx, name, trace.WithAttributes(attribute.String("component", "chat_model")))
				return context.WithValue(ctx, spanKey{}, span)
			},
			OnEnd: func(ctx context.Context, _ *einocallbacks.RunInfo, output *einomodel.CallbackOutput) context.Context {
				spanFromContext(ctx, "chat_model", output)
				return ctx
			},
			OnError: func(ctx context.Context, _ *einocallbacks.RunInfo, err error) context.Context {
				observeComponentCall("chat_model", true)
				finishSpan(ctx, err)
				return ctx
			},
		}).
		Tool(&einoutils.ToolCallbackHandler{
			OnStart: func(ctx context.Context, info *einocallbacks.RunInfo, _ *einotool.CallbackInput) context.Context {
				observeComponentCall("tool", false)
				name := "eino.tool"
				if info != nil && info.Name != "" {
					name = "eino." + info.Name
				}
				_, span := otel.Tracer(instrumentationName).Start(ctx, name, trace.WithAttributes(attribute.String("component", "tool")))
				return context.WithValue(ctx, spanKey{}, span)
			},
			OnEnd: func(ctx context.Context, _ *einocallbacks.RunInfo, _ *einotool.CallbackOutput) context.Context {
				finishSpan(ctx, nil)
				return ctx
			},
			OnError: func(ctx context.Context, _ *einocallbacks.RunInfo, err error) context.Context {
				observeComponentCall("tool", true)
				finishSpan(ctx, err)
				return ctx
			},
		}).
		Handler()
}

// spanFromContext records token counts without retaining message content.
func spanFromContext(ctx context.Context, component string, output *einomodel.CallbackOutput) {
	span, ok := ctx.Value(spanKey{}).(trace.Span)
	if !ok || span == nil {
		return
	}
	if output != nil && output.TokenUsage != nil {
		span.SetAttributes(
			attribute.Int("gen_ai.usage.input_tokens", output.TokenUsage.PromptTokens),
			attribute.Int("gen_ai.usage.output_tokens", output.TokenUsage.CompletionTokens),
		)
	}
	span.SetAttributes(attribute.String("component", component))
	span.End()
}

// finishSpan 收口回调错误；观测错误只影响 span，不影响业务终态。
func finishSpan(ctx context.Context, err error) {
	span, ok := ctx.Value(spanKey{}).(trace.Span)
	if !ok || span == nil {
		return
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "component failed")
	}
	span.End()
}

func parseHeaders(raw string) map[string]string {
	result := map[string]string{}
	for _, part := range strings.Split(raw, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
			result[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return result
}

// normalizeOTLPEndpoint accepts a collector base URL and derives a signal path.
func normalizeOTLPEndpoint(raw, signal string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return raw
	}
	path := strings.TrimSuffix(parsed.Path, "/")
	if path == "" {
		parsed.Path = "/v1/" + signal
	} else if strings.HasSuffix(path, "/v1/traces") || strings.HasSuffix(path, "/v1/metrics") {
		parsed.Path = path[:strings.LastIndex(path, "/v1/")] + "/v1/" + signal
	} else {
		parsed.Path = path + "/v1/" + signal
	}
	return parsed.String()
}

// Package observability 提供可选的 OpenTelemetry 和 Eino Callback 适配。
//
// 本包只投影运行证据，不参与路由、权限、审批、Tool 结果或终态判断；观测失败不能阻断业务。
package observability

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"

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
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/observer-mimiron/suanming-agent/eino-supervisor-template/internal/application"
	"github.com/observer-mimiron/suanming-agent/eino-supervisor-template/internal/config"
	"github.com/observer-mimiron/suanming-agent/eino-supervisor-template/internal/domain/agent"
)

const instrumentationName = "github.com/observer-mimiron/suanming-agent/eino-supervisor-template"

var callbackOnce sync.Once

type spanKey struct{}

// Runtime 是进程级观测运行时，拥有 tracer、meter 和 exporter 的关闭入口。
type Runtime struct {
	tracer   trace.Tracer
	meter    metric.Meter
	metrics  bool
	shutdown func(context.Context) error
}

// Setup 创建可选的 OTLP trace exporter，并安装一次 Eino 全局 Callback。
// endpoint 为空时仍建立本地 tracer，但不会自动连接外部 Collector。
func Setup(ctx context.Context, cfg config.ObservabilityConfig) (*Runtime, error) {
	serviceName := cfg.ServiceName
	if serviceName == "" {
		serviceName = "eino-supervisor-template"
	}
	if !cfg.Enabled {
		return &Runtime{
			tracer:   otel.Tracer(instrumentationName),
			meter:    otel.Meter(instrumentationName),
			metrics:  cfg.MetricsEnabled,
			shutdown: func(context.Context) error { return nil },
		}, nil
	}

	res, err := resource.New(ctx, resource.WithAttributes(attribute.String("service.name", serviceName)))
	if err != nil {
		return nil, err
	}
	options := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.TraceSampleRate))),
	}
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint != "" {
		exporterOptions := []otlptracehttp.Option{otlptracehttp.WithEndpointURL(normalizeOTLPEndpoint(endpoint, "traces"))}
		if cfg.Insecure {
			exporterOptions = append(exporterOptions, otlptracehttp.WithInsecure())
		}
		if headers := parseHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS")); len(headers) > 0 {
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
			if headers := parseHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS")); len(headers) > 0 {
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
	return &Runtime{
		tracer:  provider.Tracer(instrumentationName),
		meter:   otel.Meter(instrumentationName),
		metrics: cfg.MetricsEnabled,
		shutdown: func(shutdownCtx context.Context) error {
			shutdownErr := provider.Shutdown(shutdownCtx)
			if meterProvider != nil {
				shutdownErr = errors.Join(shutdownErr, meterProvider.Shutdown(shutdownCtx))
			}
			return shutdownErr
		},
	}, nil
}

// WrapEventStore 为 run、step 和 Tool 事件增加关联 span 和可选计数。
func (r *Runtime) WrapEventStore(next application.EventStore) application.EventStore {
	if r == nil {
		return next
	}
	var counter metric.Int64Counter
	if r.metrics && r.meter != nil {
		counter, _ = r.meter.Int64Counter("agent.run_events", metric.WithDescription("number of emitted run events"))
	}
	return &eventStore{next: next, tracer: r.tracer, counter: counter}
}

// Shutdown 关闭 exporter；关闭失败只由进程入口记录，不回写业务状态。
func (r *Runtime) Shutdown(ctx context.Context) error {
	if r == nil || r.shutdown == nil {
		return nil
	}
	return r.shutdown(ctx)
}

type eventStore struct {
	next    application.EventStore
	tracer  trace.Tracer
	counter metric.Int64Counter
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
	defer span.End()
	stored, err := s.next.Append(event)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "event append failed")
	}
	return stored, err
}

func (s *eventStore) Events(runID string) []agent.RunEvent { return s.next.Events(runID) }

// newEinoCallbackHandler 复用 Eino 的官方回调分发，只保留最小可关联字段。
func newEinoCallbackHandler() einocallbacks.Handler {
	return einoutils.NewHandlerHelper().
		ChatModel(&einoutils.ModelCallbackHandler{
			OnStart: func(ctx context.Context, info *einocallbacks.RunInfo, _ *einomodel.CallbackInput) context.Context {
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
				finishSpan(ctx, err)
				return ctx
			},
		}).
		Tool(&einoutils.ToolCallbackHandler{
			OnStart: func(ctx context.Context, info *einocallbacks.RunInfo, _ *einotool.CallbackInput) context.Context {
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
	if path == "" || path == "/v1/traces" || path == "/v1/metrics" {
		parsed.Path = "/v1/" + signal
	}
	return parsed.String()
}

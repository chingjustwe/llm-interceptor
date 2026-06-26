package plugins

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/chingjustwe/llm-interceptor/internal/plugin"
)

type OTelExporter struct {
	name      string
	tp        *sdktrace.TracerProvider
	mp        *sdkmetric.MeterProvider
	tracer    trace.Tracer
	meter     metric.Meter

	// Metrics
	tokenInputTotal  metric.Int64Counter
	tokenOutputTotal metric.Int64Counter
	requestTotal     metric.Int64Counter
	requestDuration  metric.Int64Histogram
}

type OTelExporterConfig struct {
	Endpoint     string
	Headers      map[string]string
	MetricPrefix string
	ServiceName  string
}

func NewOTelExporter(ctx context.Context, cfg OTelExporterConfig) (*OTelExporter, error) {
	prefix := cfg.MetricPrefix
	if prefix == "" {
		prefix = "llm_proxy."
	}
	service := cfg.ServiceName
	if service == "" {
		service = "llm-interceptor"
	}

	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		attribute.String("service.name", service),
		attribute.String("service.version", "0.1.0"),
	)

	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(cfg.Endpoint),
		otlptracehttp.WithInsecure(),
	}
	if len(cfg.Headers) > 0 {
		opts = append(opts, otlptracehttp.WithHeaders(cfg.Headers))
	}
	traceExporter, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("create trace exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	mOpts := []otlpmetrichttp.Option{
		otlpmetrichttp.WithEndpoint(cfg.Endpoint),
		otlpmetrichttp.WithInsecure(),
	}
	if len(cfg.Headers) > 0 {
		mOpts = append(mOpts, otlpmetrichttp.WithHeaders(cfg.Headers))
	}
	metricExporter, err := otlpmetrichttp.New(ctx, mOpts...)
	if err != nil {
		return nil, fmt.Errorf("create metric exporter: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(
			sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(10*time.Second)),
		),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	e := &OTelExporter{
		name:   "otel-exporter",
		tp:     tp,
		mp:     mp,
		tracer: tp.Tracer("llm-interceptor"),
		meter:  mp.Meter("llm-interceptor"),
	}

	e.tokenInputTotal = mustMetric(e.meter.Int64Counter(prefix+"llm.token.input_total",
		metric.WithDescription("Total input tokens")))
	e.tokenOutputTotal = mustMetric(e.meter.Int64Counter(prefix+"llm.token.output_total",
		metric.WithDescription("Total output tokens")))
	e.requestTotal = mustMetric(e.meter.Int64Counter(prefix+"llm.request.total",
		metric.WithDescription("Total LLM requests")))
	e.requestDuration = mustHistogram(e.meter.Int64Histogram(prefix+"llm.request.duration",
		metric.WithDescription("Request duration in ms"),
		metric.WithUnit("ms"),
	))

	return e, nil
}

func mustMetric(m metric.Int64Counter, err error) metric.Int64Counter {
	if err != nil {
		panic("create metric: " + err.Error())
	}
	return m
}

func mustHistogram(m metric.Int64Histogram, err error) metric.Int64Histogram {
	if err != nil {
		panic("create histogram: " + err.Error())
	}
	return m
}

func (o *OTelExporter) Name() string { return o.name }

func (o *OTelExporter) OnRequest(ctx *plugin.RequestContext) (*plugin.HookResult, error) {
	if o.tracer == nil {
		return nil, nil
	}
	_, span := o.tracer.Start(ctx.Context, "POST /v1/messages",
		trace.WithSpanKind(trace.SpanKindClient))
	span.SetAttributes(
		attribute.String("gen_ai.system", "anthropic"),
		attribute.String("gen_ai.conversation.id", ctx.SessionID),
		attribute.String("llm_proxy.agent_id", ctx.AgentID),
	)
	ctx.Metadata["otel_span"] = span
	return nil, nil
}

func (o *OTelExporter) OnResponse(ctx *plugin.ResponseContext) error {
	span, ok := ctx.Metadata["otel_span"].(trace.Span)
	if !ok {
		return nil
	}
	defer span.End()

	span.SetAttributes(
		attribute.String("gen_ai.response.model", ctx.Model),
		attribute.Int("gen_ai.usage.input_tokens", ctx.Usage.InputTokens),
		attribute.Int("gen_ai.usage.output_tokens", ctx.Usage.OutputTokens),
		attribute.Int("gen_ai.usage.cache_read_input_tokens", ctx.Usage.CacheReadTokens),
		attribute.Int("gen_ai.usage.cache_creation_input_tokens", ctx.Usage.CacheCreationTokens),
		attribute.String("llm_proxy.stop_reason", ctx.StopReason),
		attribute.Int("llm_proxy.duration_ms", int(ctx.DurationMs)),
		attribute.Int("http.status_code", ctx.StatusCode),
	)

	o.tokenInputTotal.Add(context.Background(), int64(ctx.Usage.InputTokens),
		metric.WithAttributes(
			attribute.String("model", ctx.Model),
			attribute.String("session_id", ctx.SessionID),
		),
	)
	o.tokenOutputTotal.Add(context.Background(), int64(ctx.Usage.OutputTokens),
		metric.WithAttributes(
			attribute.String("model", ctx.Model),
			attribute.String("session_id", ctx.SessionID),
		),
	)
	o.requestTotal.Add(context.Background(), 1,
		metric.WithAttributes(
			attribute.String("model", ctx.Model),
			attribute.String("status", "ok"),
		),
	)
	o.requestDuration.Record(context.Background(), ctx.DurationMs,
		metric.WithAttributes(
			attribute.String("model", ctx.Model),
		),
	)
	return nil
}

func (o *OTelExporter) Shutdown(ctx context.Context) error {
	_ = o.tp.Shutdown(ctx)
	return o.mp.Shutdown(ctx)
}

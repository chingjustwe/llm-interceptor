// Package plugins contains built-in plugin implementations for the LLM Interceptor
// plugin system. Currently includes an OpenTelemetry exporter for traces and metrics.
package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

// OTelExporter implements the Plugin interface to export OpenTelemetry traces
// and metrics (token counts, request counts, request duration) to an OTLP
// collector. It creates a span on OnRequest and closes it on OnResponse with
// all available LLM metadata as attributes.
type OTelExporter struct {
	name   string
	tp     *sdktrace.TracerProvider
	mp     *sdkmetric.MeterProvider
	tracer trace.Tracer
	meter  metric.Meter

	// Metrics
	tokenInputTotal  metric.Int64Counter
	tokenOutputTotal metric.Int64Counter
	requestTotal     metric.Int64Counter
	requestDuration  metric.Int64Histogram
}

// OTelExporterConfig configures the OTLP exporter endpoint, headers, metric
// prefix, service name, and span attribute length limit for OpenTelemetry
// traces and metrics.
type OTelExporterConfig struct {
	Endpoint     string
	Headers      map[string]string
	MetricPrefix string
	ServiceName  string
	MaxAttrLen   int
}

// NewOTelExporter creates an OTelExporter, initializing the OTLP trace and
// metric exporters, meter provider, tracer provider, and common LLM metrics
// (input/output tokens, request count, request duration histogram).
func NewOTelExporter(ctx context.Context, cfg OTelExporterConfig) (*OTelExporter, error) {
	prefix := cfg.MetricPrefix
	if prefix == "" {
		prefix = "llm_proxy."
	}
	service := cfg.ServiceName
	if service == "" {
		service = "llm-interceptor"
	}
	attrLimit := cfg.MaxAttrLen
	if attrLimit <= 0 {
		attrLimit = 65535
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

	spanLimits := sdktrace.NewSpanLimits()
	spanLimits.AttributeValueLengthLimit = attrLimit
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
		sdktrace.WithSpanLimits(spanLimits),
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

// mustMetric panics if creating an Int64Counter fails. Used for compile-time-like
// safety during meter initialization.
func mustMetric(m metric.Int64Counter, err error) metric.Int64Counter {
	if err != nil {
		panic("create metric: " + err.Error())
	}
	return m
}

// mustHistogram panics if creating an Int64Histogram fails. Used for compile-time-like
// safety during meter initialization.
func mustHistogram(m metric.Int64Histogram, err error) metric.Int64Histogram {
	if err != nil {
		panic("create histogram: " + err.Error())
	}
	return m
}

// Name returns "otel-exporter" as the plugin identifier.
func (o *OTelExporter) Name() string { return o.name }

// OnRequest starts an OpenTelemetry span and attaches LLM metadata (agent,
// session, current-turn request) as span attributes. Only the last user
// message from the messages array is included to avoid sending full
// conversation history to the trace backend. The span is stored in
// ctx.Metadata for retrieval in OnResponse.
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
		attribute.String("gen_ai.request.content", extractUserContent(ctx.Body)),
	)
	ctx.Metadata["otel_span"] = span
	return nil, nil
}

// extractUserContent parses the request body and extracts just the current
// user message as plain text, handling both Anthropic and OpenAI message
// formats. For Anthropic, content is an array of blocks (text, tool_result,
// etc.); for OpenAI, content can be a plain string or an array.
// System prompts and conversation history are excluded.
func extractUserContent(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return ""
	}
	msgs, ok := raw["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return ""
	}
	last, ok := msgs[len(msgs)-1].(map[string]any)
	if !ok {
		return ""
	}
	return extractText(last["content"])
}

// extractText converts the content field of an LLM message to plain text.
// Supports both Anthropic format (content_block array) and OpenAI format
// (plain string or array). Text blocks, tool results, and tool uses are
// concatenated with type labels for clarity in traces.
func extractText(content any) string {
	if content == nil {
		return ""
	}
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, block := range v {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			tp, _ := b["type"].(string)
			switch tp {
			case "text":
				if t, ok := b["text"].(string); ok {
					parts = append(parts, t)
				}
			case "tool_result":
				if c, ok := b["content"]; ok {
					parts = append(parts, extractText(c))
				}
			case "tool_use":
				if name, ok := b["name"].(string); ok {
					parts = append(parts, "[tool_use: "+name+"]")
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return fmt.Sprint(v)
	}
}

const maxResponseBodyAttr = 8192

// OnResponse closes the OpenTelemetry span created in OnRequest, setting
// LLM-specific attributes (model, token usage, stop reason, duration,
// request/response content) and recording metric counters.
func (o *OTelExporter) OnResponse(ctx *plugin.ResponseContext) error {
	span, ok := ctx.Metadata["otel_span"].(trace.Span)
	if !ok {
		return nil
	}
	defer span.End()

	span.SetAttributes(
		attribute.String("gen_ai.response.model", ctx.Model),
		attribute.String("gen_ai.response.content", truncateBody(ctx.Body, maxResponseBodyAttr)),
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

func truncateBody(body []byte, maxLen int) string {
	if len(body) == 0 {
		return ""
	}
	if len(body) <= maxLen {
		return string(body)
	}
	return string(body[:maxLen]) + "... (truncated)"
}

// Shutdown flushes and shuts down both the tracer provider and meter provider.
func (o *OTelExporter) Shutdown(ctx context.Context) error {
	_ = o.tp.Shutdown(ctx)
	return o.mp.Shutdown(ctx)
}

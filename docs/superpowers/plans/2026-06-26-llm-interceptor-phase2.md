# LLM Interceptor — Implementation Plan (Phase 2: OTel Exporter Plugin)

**Goal:** Add OpenTelemetry observability as a native plugin, producing traces and metrics from proxied LLM requests.

**Architecture:** A single `otel-exporter` plugin implementing `plugin.Plugin`. OnRequest creates a span and stores it in metadata; OnResponse ends the span and records metrics. OTel SDK initialized at startup via config-driven OTLP endpoint.

**Tech Stack:** Go 1.22+, `go.opentelemetry.io/otel`, `go.opentelemetry.io/otel/sdk`, `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp`, `go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp`, `go.opentelemetry.io/otel/semconv/v1.26.0`

---

### Task 1: OTel exporter plugin

**Files:**
- Create: `internal/plugins/otel.go`
- Create: `internal/plugins/otel_test.go`
- Modify: `internal/config/config.go`

- [ ] **Step 1: Define OTel plugin config in config.go**

Add to `internal/config/config.go`:

```go
type PluginConfig struct {
	OTelExporter OTelExporterConfig `yaml:"otel-exporter,omitempty"`
}

type OTelExporterConfig struct {
	Enabled  bool              `yaml:"enabled"`
	Endpoint string            `yaml:"endpoint"`   // e.g. "localhost:4318"
	Headers  map[string]string `yaml:"headers,omitempty"`
}

type Config struct {
	// ... existing fields ...
	Plugins PluginConfig `yaml:"plugins"`
}
```

- [ ] **Step 2: Write the OTel exporter plugin (test-first)**

`internal/plugins/otel_test.go`:

```go
package plugins

import (
	"context"
	"testing"

	"github.com/chingjustwe/llm-Interceptor/internal/plugin"
)

func TestOTelExporter_Name(t *testing.T) {
	exporter := &OTelExporter{name: "otel-exporter"}
	if exporter.Name() != "otel-exporter" {
		t.Fatalf("expected 'otel-exporter', got %s", exporter.Name())
	}
}

func TestOTelExporter_OnRequestStoresSpanInMetadata(t *testing.T) {
	exporter := &OTelExporter{name: "otel-exporter"}
	ctx := &plugin.RequestContext{
		Context:  context.Background(),
		Metadata: make(map[string]any),
		SessionID: "sess_1",
	}
	result, err := exporter.OnRequest(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil result, got %+v", result)
	}
	if _, ok := ctx.Metadata["otel_span"]; !ok {
		t.Fatal("expected otel_span in metadata")
	}
}
```

- [ ] **Step 3: Write OTel exporter implementation**

`internal/plugins/otel.go`:

```go
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

	"github.com/chingjustwe/llm-Interceptor/internal/plugin"
)

type OTelExporter struct {
	name      string
	tp        trace.TracerProvider
	mp        metric.MeterProvider
	tracer    trace.Tracer
	meter     metric.Meter

	// Metrics
	tokenInputTotal   metric.Int64Counter
	tokenOutputTotal  metric.Int64Counter
	requestTotal      metric.Int64Counter
	requestDuration   metric.Int64Histogram
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
```

- [ ] **Step 4: Install OTel dependencies**

```bash
go get go.opentelemetry.io/otel
go get go.opentelemetry.io/otel/sdk
go get go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp
go get go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp
go get go.opentelemetry.io/otel/semconv/v1.26.0
```

- [ ] **Step 5: Run tests**

```bash
go test ./internal/plugins/ -v -run TestOTelExporter
```

Expected: 2 tests pass.

- [ ] **Step 6: Build to verify compilation**

```bash
go build ./internal/plugins/
echo "build ok"
```

- [ ] **Step 7: Commit**

```bash
git add internal/plugins/ go.mod go.sum
git commit -m "feat(plugin): add OTel exporter with traces and metrics"
```

---

### Task 2: Wire OTel plugin into server via config

**Files:**
- Modify: `cmd/llm-interceptor/main.go`
- Modify: `internal/config/config.go`

- [ ] **Step 1: Update config to include plugins section (already done in Task 1 Step 1 — verify it's present)**

- [ ] **Step 2: Wire OTel plugin initialization in main.go**

In `cmd/llm-interceptor/main.go`, replace the empty `plugin.NewDispatcher(nil)` with:

```go
var pluginList []plugin.Plugin

if cfg.Plugins.OTelExporter.Enabled {
	exporter, err := plugins.NewOTelExporter(ctx, plugins.OTelExporterConfig{
		Endpoint:     cfg.Plugins.OTelExporter.Endpoint,
		Headers:      cfg.Plugins.OTelExporter.Headers,
		MetricPrefix: cfg.MetricPrefix,
	})
	if err != nil {
		log.Fatalf("failed to init otel exporter: %v", err)
	}
	pluginList = append(pluginList, exporter)
	defer exporter.Shutdown(ctx)
}

disp := plugin.NewDispatcher(pluginList)
```

Add import: `"github.com/chingjustwe/llm-Interceptor/internal/plugins"`

- [ ] **Step 3: Build and verify**

```bash
go build ./cmd/llm-interceptor/
echo "build succeeded"
```

- [ ] **Step 4: Update config.example.yaml**

Add to `config.example.yaml`:

```yaml
plugins:
  otel-exporter:
    enabled: false
    endpoint: "localhost:4318"
    headers: {}
```

- [ ] **Step 5: Commit**

```bash
git add cmd/ internal/config/ config.example.yaml
git commit -m "feat: wire OTel exporter into server via config"
```

---

### Self-Review

**1. Spec coverage:**
- Full OTel data model (traces + metrics) ✓
- Config-driven OTLP endpoint ✓
- Metrics: token counts, request count, duration ✓
- OnRequest creates span, OnResponse ends span ✓

**2. Design decisions:**
- OTel exporter implements `plugin.Plugin` — no special treatment
- `Shutdown()` method for graceful provider shutdown
- Metric prefix defaults to `llm_proxy.` but is configurable
- Span uses `gen_ai.*` attribute names per OpenTelemetry gen_ai semantic conventions
- Service name defaults to `llm-interceptor` but is overridable

**3. Backward compatibility:**
- Config has `plugins.otel-exporter.enabled: false` by default
- Phase 1 users get no behavioral change

package plugins

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/chingjustwe/llm-interceptor/internal/plugin"
)

func TestOTelExporter_Name(t *testing.T) {
	exporter := &OTelExporter{name: "otel-exporter"}
	if exporter.Name() != "otel-exporter" {
		t.Fatalf("expected 'otel-exporter', got %s", exporter.Name())
	}
}

func TestOTelExporter_OnRequestStoresSpanInMetadata(t *testing.T) {
	exporter := &OTelExporter{
		name:   "otel-exporter",
		tracer: trace.NewNoopTracerProvider().Tracer("test"),
	}
	ctx := &plugin.RequestContext{
		Context:   context.Background(),
		Metadata:  make(map[string]any),
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

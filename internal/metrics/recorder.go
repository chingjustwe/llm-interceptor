package metrics

import (
	"context"
	"github.com/chingjustwe/llm-interceptor/internal/types"
	"github.com/prometheus/client_golang/prometheus"
	"log/slog"
)

// RecordRequest records all metrics for a completed request. This is
// non-blocking and safe to call from any goroutine.
func (m *Metrics) RecordRequest(ctx context.Context, req *types.StoredRequest, costUSD float64) {
	labels := LabelsFromRequest(req)

	m.RequestsTotal.With(labels).Inc()
	m.RequestDuration.With(prometheus.Labels{"model": labels["model"]}).
		Observe(float64(req.DurationMs) / 1000.0)

	inputTokens := float64(req.Usage.InputTokens + req.Usage.CacheReadTokens + req.Usage.CacheCreationTokens)
	outputTokens := float64(req.Usage.OutputTokens)
	if inputTokens > 0 {
		m.TokensTotal.With(prometheus.Labels{"type": "input"}).Add(inputTokens)
	}
	if outputTokens > 0 {
		m.TokensTotal.With(prometheus.Labels{"type": "output"}).Add(outputTokens)
	}

	if costUSD > 0 {
		m.CostUSDTotal.Add(costUSD)
	}

	m.ActiveRequests.Dec()

	slog.DebugContext(ctx, "metrics: recorded request",
		"model", labels["model"],
		"status_code", labels["status_code"],
		"duration_ms", req.DurationMs,
		"cost_usd", costUSD,
	)
}

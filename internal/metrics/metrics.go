// Package metrics provides Prometheus instrumentation for the LLM Interceptor.
// All metrics are registered on a default registry and exposed via /metrics.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"net/http"
)

// Metrics holds all Prometheus metric collectors for the LLM Interceptor.
type Metrics struct {
	RequestsTotal   *prometheus.CounterVec
	RequestDuration *prometheus.HistogramVec
	TokensTotal     *prometheus.CounterVec
	CostUSDTotal    prometheus.Counter
	ActiveRequests  prometheus.Gauge

	registry *prometheus.Registry
}

// NewMetrics creates and registers all Prometheus metric collectors on a new
// isolated registry that does not pollute the global default registry.
func NewMetrics() *Metrics {
	registry := prometheus.NewRegistry()

	m := &Metrics{
		RequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "requests_total",
				Help: "Total number of LLM proxy requests.",
			},
			[]string{"model", "status_code", "error_type"},
		),
		RequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "request_duration_seconds",
				Help:    "Request duration in seconds.",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"model"},
		),
		TokensTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "tokens_total",
				Help: "Total number of tokens processed.",
			},
			[]string{"type"},
		),
		CostUSDTotal: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "cost_usd_total",
				Help: "Total cost in USD.",
			},
		),
		ActiveRequests: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "active_requests",
				Help: "Number of currently active requests.",
			},
		),
		registry: registry,
	}

	m.MustRegister()
	return m
}

// MustRegister registers all metrics and panics on failure.
func (m *Metrics) MustRegister() {
	m.registry.MustRegister(
		m.RequestsTotal,
		m.RequestDuration,
		m.TokensTotal,
		m.CostUSDTotal,
		m.ActiveRequests,
	)
}

// Handler returns an http.Handler that serves the metrics in Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

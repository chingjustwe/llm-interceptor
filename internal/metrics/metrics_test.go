package metrics

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/chingjustwe/llm-interceptor/internal/types"
)

func TestNewMetrics(t *testing.T) {
	m := NewMetrics()
	if m == nil {
		t.Fatal("NewMetrics() returned nil")
	}
	if m.registry == nil {
		t.Fatal("registry is nil")
	}
	if m.RequestsTotal == nil {
		t.Error("RequestsTotal is nil")
	}
	if m.RequestDuration == nil {
		t.Error("RequestDuration is nil")
	}
	if m.TokensTotal == nil {
		t.Error("TokensTotal is nil")
	}
	if m.CostUSDTotal == nil {
		t.Error("CostUSDTotal is nil")
	}
	if m.ActiveRequests == nil {
		t.Error("ActiveRequests is nil")
	}
}

func TestLabelsFromRequest(t *testing.T) {
	req := &types.StoredRequest{
		Model:      "claude-sonnet-4",
		StatusCode: 200,
	}
	labels := LabelsFromRequest(req)
	if labels["model"] != "claude-sonnet-4" {
		t.Errorf("expected model 'claude-sonnet-4', got %q", labels["model"])
	}
	if labels["status_code"] != "200" {
		t.Errorf("expected status_code '200', got %q", labels["status_code"])
	}
	if labels["error_type"] != "" {
		t.Errorf("expected empty error_type, got %q", labels["error_type"])
	}

	errMsg := "rate_limit_error"
	req.ErrorType = &errMsg
	labels = LabelsFromRequest(req)
	if labels["error_type"] != "rate_limit_error" {
		t.Errorf("expected 'rate_limit_error', got %q", labels["error_type"])
	}

	req.Model = ""
	labels = LabelsFromRequest(req)
	if labels["model"] != "unknown" {
		t.Errorf("expected 'unknown', got %q", labels["model"])
	}
}

func TestRecordRequest(t *testing.T) {
	m := NewMetrics()
	m.ActiveRequests.Inc()

	req := &types.StoredRequest{
		Model:      "claude-sonnet-4",
		StatusCode: 200,
		DurationMs: 500,
		Usage: types.TokenUsage{
			InputTokens:         100,
			OutputTokens:        50,
			CacheReadTokens:     10,
			CacheCreationTokens: 5,
		},
	}

	m.RecordRequest(context.Background(), req, 0.002)

	// Gather and verify.
	families, err := m.registry.Gather()
	if err != nil {
		t.Fatalf("gather failed: %v", err)
	}

	for _, f := range families {
		switch f.GetName() {
		case "requests_total":
			if len(f.GetMetric()) == 0 || f.GetMetric()[0].GetCounter().GetValue() != 1 {
				t.Errorf("expected 1 request total, got %v", f.GetMetric()[0].GetCounter().GetValue())
			}
		case "tokens_total":
			for _, m := range f.GetMetric() {
				label := m.GetLabel()[0].GetValue()
				val := m.GetCounter().GetValue()
				switch label {
				case "input":
					if val != 115 {
						t.Errorf("expected 115 input tokens, got %v", val)
					}
				case "output":
					if val != 50 {
						t.Errorf("expected 50 output tokens, got %v", val)
					}
				}
			}
		case "cost_usd_total":
			if f.GetMetric()[0].GetCounter().GetValue() != 0.002 {
				t.Errorf("expected 0.002 cost, got %v", f.GetMetric()[0].GetCounter().GetValue())
			}
		case "active_requests":
			if f.GetMetric()[0].GetGauge().GetValue() != 0 {
				t.Errorf("expected 0 active requests, got %v", f.GetMetric()[0].GetGauge().GetValue())
			}
		}
	}
}

func TestConcurrentSafety(t *testing.T) {
	m := NewMetrics()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			m.ActiveRequests.Inc()
			req := &types.StoredRequest{
				Model:      fmt.Sprintf("model-%d", n%3),
				StatusCode: 200 + (n%3)*100,
				DurationMs: int64(n * 100),
				Usage: types.TokenUsage{
					InputTokens:  n * 10,
					OutputTokens: n * 5,
				},
			}
			m.RecordRequest(context.Background(), req, float64(n)*0.001)
		}(i)
	}
	wg.Wait()

	// Verify that concurrent access does not cause panics or deadlocks.
	// Count requests_total across all label combinations.
	families, err := m.registry.Gather()
	if err != nil {
		t.Fatalf("gather failed: %v", err)
	}

	var totalRequests float64
	for _, f := range families {
		if f.GetName() == "requests_total" {
			for _, metric := range f.GetMetric() {
				totalRequests += metric.GetCounter().GetValue()
			}
		}
	}
	if totalRequests != 20 {
		t.Errorf("expected 20 total requests, got %v", totalRequests)
	}
}

func TestHandler(t *testing.T) {
	m := NewMetrics()
	handler := m.Handler()

	m.ActiveRequests.Inc()
	req := &types.StoredRequest{
		Model:      "test-model",
		StatusCode: 200,
		DurationMs: 100,
		Usage: types.TokenUsage{
			InputTokens:  10,
			OutputTokens: 5,
		},
	}
	m.RecordRequest(context.Background(), req, 0.001)

	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	content := string(body)

	checks := []string{
		"requests_total",
		"request_duration_seconds",
		"tokens_total",
		"cost_usd_total",
		"active_requests",
		"# HELP",
		"# TYPE",
	}
	for _, check := range checks {
		if !stringsContains(content, check) {
			t.Errorf("expected %q in metrics output", check)
		}
	}
}

func TestHandler_NoAuth(t *testing.T) {
	m := NewMetrics()
	handler := m.Handler()
	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func stringsContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

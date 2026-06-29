package alerting

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{"5m", 5 * time.Minute},
		{"1h", time.Hour},
		{"30s", 30 * time.Second},
		{"", 0},
		{"invalid", 0},
	}
	for _, tt := range tests {
		got := parseDuration(tt.input)
		if got != tt.want {
			t.Errorf("parseDuration(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestEvaluator_RuleCheck(t *testing.T) {
	var checkCount atomic.Int32
	e := NewEvaluator(AlertConfig{
		Rules: []Rule{
			{
				Name:      "test-rule",
				Metric:    "error_rate",
				Threshold: 0.1,
				Duration:  "1m",
				Channels:  []string{},
			},
		},
	}, func(ctx context.Context, rule Rule) (float64, error) {
		checkCount.Add(1)
		return 0.2, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	e.evaluateAll(ctx)

	if checkCount.Load() == 0 {
		t.Errorf("expected check function to be called")
	}

	e.mu.RLock()
	lastFired, exists := e.lastFired["test-rule"]
	e.mu.RUnlock()
	if !exists {
		t.Errorf("expected alert to be fired (lastFired entry missing)")
	}
	if lastFired.IsZero() {
		t.Errorf("expected non-zero lastFired time")
	}
}

func TestEvaluator_NoFireBelowThreshold(t *testing.T) {
	e := NewEvaluator(AlertConfig{
		Rules: []Rule{
			{
				Name:      "no-fire",
				Metric:    "something",
				Threshold: 100,
			},
		},
	}, func(ctx context.Context, rule Rule) (float64, error) {
		return 50, nil
	})

	e.evaluateAll(context.Background())

	e.mu.RLock()
	_, exists := e.lastFired["no-fire"]
	e.mu.RUnlock()
	if exists {
		t.Errorf("expected NO alert when value is below threshold")
	}
}

func TestSlackNotifier(t *testing.T) {
	var receivedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		receivedBody = string(buf[:n])
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	n := &SlackNotifier{WebhookURL: server.URL}
	err := n.Send(context.Background(), &FiredAlert{
		RuleName:  "test",
		Metric:    "error_rate",
		Value:     0.5,
		Threshold: 0.1,
		Severity:  SeverityCritical,
		Message:   "Alert triggered",
	})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	if !stringsContains(receivedBody, "CRITICAL") {
		t.Errorf("expected CRITICAL in payload, got: %s", receivedBody)
	}
}

func TestGenericWebhookNotifier(t *testing.T) {
	var receivedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		receivedBody = string(buf[:n])
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	n := &GenericWebhookNotifier{WebhookURL: server.URL}
	err := n.Send(context.Background(), &FiredAlert{
		RuleName: "webhook-test",
		Value:    99.9,
	})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	if !stringsContains(receivedBody, "webhook-test") {
		t.Errorf("expected rule name in payload, got: %s", receivedBody)
	}
}

func TestAlertDedup(t *testing.T) {
	var callCount atomic.Int32
	e := NewEvaluator(AlertConfig{
		Rules: []Rule{
			{
				Name:      "dedup-test",
				Metric:    "error_rate",
				Threshold: 0.1,
				Duration:  "5m",
			},
		},
	}, func(ctx context.Context, rule Rule) (float64, error) {
		callCount.Add(1)
		return 0.5, nil
	})

	ctx := context.Background()
	e.evaluateAll(ctx)
	time.Sleep(time.Millisecond)

	e.evaluateAll(ctx)
	time.Sleep(time.Millisecond)

	e.mu.RLock()
	lastFired := e.lastFired["dedup-test"]
	e.mu.RUnlock()

	if lastFired.IsZero() {
		t.Errorf("expected lastFired to be set")
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

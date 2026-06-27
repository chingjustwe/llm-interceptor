package plugins

import (
	"testing"

	"github.com/chingjustwe/llm-interceptor/internal/plugin"
)

func TestCostTracker_TracksCost(t *testing.T) {
	tracker := NewCostTracker(nil)

	ctx := &plugin.ResponseContext{
		Model:     "claude-sonnet-4-6",
		Usage:     plugin.Usage{InputTokens: 1000, OutputTokens: 500},
		SessionID: "sess_1",
	}

	if err := tracker.OnResponse(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cost := tracker.SessionCost("sess_1")
	if cost <= 0 {
		t.Fatalf("expected positive cost, got %f", cost)
	}
}

func TestCostTracker_UnknownModelUsesFallback(t *testing.T) {
	tracker := NewCostTracker(nil)
	// 150 tokens at $2/M = 0.0003
	cost := tracker.CalculateCost("nonexistent-model", 100, 50)
	expected := 150.0 / 1_000_000 * 2.0
	if cost != expected {
		t.Fatalf("expected %f for unknown model, got %f", expected, cost)
	}
}

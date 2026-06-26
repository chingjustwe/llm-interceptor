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

func TestCostTracker_UnknownModelReturnsZero(t *testing.T) {
	tracker := NewCostTracker(nil)
	cost := tracker.CalculateCost("nonexistent-model", 100, 50)
	if cost != 0 {
		t.Fatalf("expected 0 for unknown model, got %f", cost)
	}
}

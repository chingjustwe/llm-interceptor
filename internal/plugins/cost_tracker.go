// CostTracker tracks LLM API usage costs per session and in aggregate, using
// per-model pricing tables and the state backend for counter persistence.

package plugins

import (
	"context"
	"sync"
	"time"

	"github.com/chingjustwe/llm-interceptor/internal/plugin"
	"github.com/chingjustwe/llm-interceptor/internal/state"
)

// priceEntry holds per-million-token prices for a model.
type priceEntry struct {
	InputPerM  float64
	OutputPerM float64
}

// Default per-million-token prices for Anthropic models, keyed by model ID.
// Prices are sourced from https://docs.anthropic.com/en/docs/about-claude/pricing.
var defaultPrices = map[string]struct {
	InputPerM  float64
	OutputPerM float64
}{
	"claude-sonnet-4-6":               {InputPerM: 3.0, OutputPerM: 15.0},
	"claude-sonnet-4-20250506":        {InputPerM: 3.0, OutputPerM: 15.0},
	"claude-3-5-sonnet-20241022":      {InputPerM: 3.0, OutputPerM: 15.0},
	"claude-3-opus-20240229":          {InputPerM: 15.0, OutputPerM: 75.0},
	"claude-3-haiku-20240307":         {InputPerM: 0.25, OutputPerM: 1.25},
	"claude-3-5-haiku-20241022":       {InputPerM: 0.25, OutputPerM: 1.25},
}

// CostTracker implements the Plugin interface to track LLM usage costs per
// session. It calculates costs based on token usage and per-model pricing,
// stores session-level costs in memory, and persists cumulative costs to the
// state backend for cross-process visibility.
type CostTracker struct {
	state    state.Backend
	mu       sync.RWMutex
	sessions map[string]float64
	prices   map[string]priceEntry
}

// NewCostTracker creates a CostTracker with the given state backend (may be
// nil for in-memory-only tracking) and default Anthropic pricing.
func NewCostTracker(st state.Backend) *CostTracker {
	prices := make(map[string]priceEntry, len(defaultPrices))
	for k, v := range defaultPrices {
		prices[k] = priceEntry(v)
	}
	return &CostTracker{
		state:    st,
		sessions: make(map[string]float64),
		prices:   prices,
	}
}

// Name returns "cost-tracker" as the plugin identifier.
func (c *CostTracker) Name() string { return "cost-tracker" }

// OnRequest is a no-op for the cost tracker; all work happens on response.
func (c *CostTracker) OnRequest(ctx *plugin.RequestContext) (*plugin.HookResult, error) {
	return nil, nil
}

// OnResponse calculates the cost of the completed LLM request and records it
// in both the in-memory session map and the state backend (via atomic counter
// increments for session and daily totals).
func (c *CostTracker) OnResponse(ctx *plugin.ResponseContext) error {
	cost := c.CalculateCost(ctx.Model, ctx.Usage.InputTokens, ctx.Usage.OutputTokens)
	if cost == 0 {
		return nil
	}
	costMicro := int64(cost * 1_000_000)
	if ctx.Metadata != nil {
		ctx.Metadata["cost_usd"] = cost
	}

	c.mu.Lock()
	c.sessions[ctx.SessionID] += cost
	c.mu.Unlock()

	if c.state != nil {
		today := time.Now().UTC().Format("2006-01-02")
		// Use background context because the HTTP request context may already
		// be cancelled by the time OnResponse runs.
		c.state.Increment(context.Background(), "cost:session:"+ctx.SessionID, costMicro)
		c.state.Increment(context.Background(), "cost:daily:"+today, costMicro)
	}
	return nil
}

// CalculateCost computes the USD cost for a given model and token counts using
// the tracker's current price table. Returns 0 if the model is unknown.
// Safe for concurrent use — holds a read lock on the price table.
func (c *CostTracker) CalculateCost(model string, inputTokens, outputTokens int) float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, ok := c.prices[model]
	if !ok {
		return 0
	}
	inputCost := float64(inputTokens) / 1_000_000 * p.InputPerM
	outputCost := float64(outputTokens) / 1_000_000 * p.OutputPerM
	return inputCost + outputCost
}

// SessionCost returns the accumulated cost for the given session from the
// in-memory map. This is not persisted and resets on process restart.
func (c *CostTracker) SessionCost(sessionID string) float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sessions[sessionID]
}

// SetPrices replaces the current price table. Useful for runtime updates from
// configuration reloads or admin API endpoints.
func (c *CostTracker) SetPrices(prices map[string]priceEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prices = prices
}

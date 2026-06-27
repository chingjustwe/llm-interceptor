// CostTracker tracks LLM API usage costs per session and in aggregate, using
// per-model pricing tables and the state backend for counter persistence.

package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/chingjustwe/llm-interceptor/internal/plugin"
	"github.com/chingjustwe/llm-interceptor/internal/state"
)

// PriceEntry holds per-million-token prices for a model.
type PriceEntry struct {
	InputPerM  float64
	OutputPerM float64
}

// Default per-million-token prices for Anthropic models, keyed by model ID.
// Sourced from https://docs.anthropic.com/en/docs/about-claude/pricing.
var defaultPrices = map[string]PriceEntry{
	"claude-sonnet-4-6":          {InputPerM: 3.0, OutputPerM: 15.0},
	"claude-sonnet-4-20250506":   {InputPerM: 3.0, OutputPerM: 15.0},
	"claude-3-5-sonnet-20241022": {InputPerM: 3.0, OutputPerM: 15.0},
	"claude-3-opus-20240229":     {InputPerM: 15.0, OutputPerM: 75.0},
	"claude-3-haiku-20240307":    {InputPerM: 0.25, OutputPerM: 1.25},
	"claude-3-5-haiku-20241022":  {InputPerM: 0.25, OutputPerM: 1.25},
}

// CostTracker implements the Plugin interface to track LLM usage costs per
// session. It calculates costs based on token usage and per-model pricing,
// stores session-level costs in memory, and persists cumulative costs to the
// state backend for cross-process visibility.
type CostTracker struct {
	state          state.Backend
	mu             sync.RWMutex
	sessions       map[string]float64
	prices         map[string]PriceEntry
	defaultPerM    float64          // fallback $/1M tokens for unknown models (input + output combined)
	configPrices   map[string]PriceEntry // preserved for re-apply on periodic refresh
	pricingURL     string
	pricingRefresh time.Duration
	stopRefresh    chan struct{}
}

// NewCostTracker creates a CostTracker with the given state backend (may be
// nil for in-memory-only tracking) and default Anthropic pricing.
// Unknown models fall back to defaultPerM ($/1M tokens, combined input+output).
func NewCostTracker(st state.Backend) *CostTracker {
	prices := make(map[string]PriceEntry, len(defaultPrices))
	for k, v := range defaultPrices {
		prices[k] = v
	}
	return &CostTracker{
		state:       st,
		sessions:    make(map[string]float64),
		prices:      prices,
		defaultPerM: 2.0,
	}
}

// SetConfigPrices stores config-level price overrides so they can be re-applied
// after each periodic refresh of the online pricing source.
func (c *CostTracker) SetConfigPrices(prices map[string]PriceEntry) {
	c.configPrices = prices
}

// ConfigPrices returns the stored config-level price overrides.
func (c *CostTracker) ConfigPrices() map[string]PriceEntry {
	return c.configPrices
}

// StartPricingRefresh begins a background goroutine that fetches model pricing
// from the given URL every interval. Config-level price overrides (set via
// SetConfigPrices) are re-applied after each fetch.
// Pass a cancellable context to stop the goroutine (or call StopPricingRefresh).
func (c *CostTracker) StartPricingRefresh(ctx context.Context, url string, interval time.Duration) {
	c.pricingURL = url
	c.pricingRefresh = interval
	c.stopRefresh = make(chan struct{})

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-c.stopRefresh:
				return
			case <-ticker.C:
				prices, err := FetchOnlinePricing(url)
				if err != nil {
					log.Printf("cost: refresh failed (%v), keeping existing prices", err)
					continue
				}
				c.MergePrices(prices)
				// Re-apply config overrides so they survive the refresh.
				if len(c.configPrices) > 0 {
					c.MergePrices(c.configPrices)
				}
				log.Printf("cost: refreshed %d model prices from %s", len(prices), url)
			}
		}
	}()
}

// StopPricingRefresh signals the background refresh goroutine to exit.
func (c *CostTracker) StopPricingRefresh() {
	if c.stopRefresh != nil {
		close(c.stopRefresh)
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
		c.state.Increment(context.Background(), "cost:total", costMicro)
	}
	return nil
}

// CalculateCost computes the USD cost for a given model and token counts using
// the tracker's current price table. If the model is unknown, it falls back
// to defaultPerM (a blended $/1M tokens for both input and output).
// Safe for concurrent use — holds a read lock on the price table.
func (c *CostTracker) CalculateCost(model string, inputTokens, outputTokens int) float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, ok := c.prices[model]
	if !ok {
		total := float64(inputTokens + outputTokens)
		return total / 1_000_000 * c.defaultPerM
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
func (c *CostTracker) SetPrices(prices map[string]PriceEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prices = prices
}

// MergePrices merges additional prices into the tracker's price table.
// Existing model prices are overwritten; new models are added.
func (c *CostTracker) MergePrices(additional map[string]PriceEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range additional {
		c.prices[k] = v
	}
}

// FetchOnlinePricing fetches model pricing from a remote JSON URL (formatted
// like https://models.dev/api.json) and returns a flat model_id → PriceEntry
// map. The expected format is:
//
//	{ "<provider>": { "models": { "<model_id>": { "cost": { "input": ..., "output": ... } } } } }
func FetchOnlinePricing(url string) (map[string]PriceEntry, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch pricing: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch pricing: HTTP %d", resp.StatusCode)
	}

	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode pricing: %w", err)
	}

	prices := make(map[string]PriceEntry)
	for _, provider := range raw {
		p, ok := provider.(map[string]any)
		if !ok {
			continue
		}
		models, ok := p["models"].(map[string]any)
		if !ok {
			continue
		}
		for modelID, modelData := range models {
			if _, exists := prices[modelID]; exists {
				continue // first provider wins
			}
			m, ok := modelData.(map[string]any)
			if !ok {
				continue
			}
			cost, ok := m["cost"].(map[string]any)
			if !ok {
				continue
			}
			input, _ := cost["input"].(float64)
			output, _ := cost["output"].(float64)
			if input <= 0 || output <= 0 {
				continue
			}
			prices[modelID] = PriceEntry{InputPerM: input, OutputPerM: output}
		}
	}
	if len(prices) == 0 {
		return nil, fmt.Errorf("fetch pricing: no valid models found at %s", url)
	}
	log.Printf("cost: loaded %d model prices from %s", len(prices), url)
	return prices, nil
}

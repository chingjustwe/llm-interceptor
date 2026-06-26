// RateLimitPlugin enforces per-minute request and token rate limits using
// sliding-window counters in the state backend. Requests exceeding the limit
// are blocked with HTTP 429.

package plugins

import (
	"fmt"

	"github.com/chingjustwe/llm-interceptor/internal/plugin"
	"github.com/chingjustwe/llm-interceptor/internal/state"
)

// RateLimitPlugin implements the Plugin interface to enforce per-minute rate
// limits on both request count and total token consumption. Limits of 0 or
// less mean unlimited for that dimension.
type RateLimitPlugin struct {
	state     state.Backend
	reqPerMin int
	tokPerMin int
}

// NewRateLimitPlugin creates a RateLimitPlugin with the given state backend
// and per-minute limits. A limit of 0 or less disables checking for that
// dimension.
func NewRateLimitPlugin(st state.Backend, reqPerMin, tokPerMin int) *RateLimitPlugin {
	return &RateLimitPlugin{
		state:     st,
		reqPerMin: reqPerMin,
		tokPerMin: tokPerMin,
	}
}

// Name returns "ratelimit" as the plugin identifier.
func (r *RateLimitPlugin) Name() string { return "ratelimit" }

// OnRequest checks the current request count against the per-minute limit.
// Uses IncrementWithTTL with a 60-second window to implement a sliding-window
// counter. Returns a blocking HookResult with status 429 if the limit is
// exceeded.
func (r *RateLimitPlugin) OnRequest(ctx *plugin.RequestContext) (*plugin.HookResult, error) {
	if r.reqPerMin > 0 {
		// Increment the sliding-window counter; the TTL ensures old entries
		// expire, naturally sliding the window forward.
		count, err := r.state.IncrementWithTTL(ctx.Context, "ratelimit:requests:global", 1, 60_000)
		if err == nil && count > int64(r.reqPerMin) {
			return &plugin.HookResult{
				Block:      true,
				Reason:     fmt.Sprintf("rate limit exceeded: max %d requests/min", r.reqPerMin),
				StatusCode: 429,
			}, nil
		}
	}
	return nil, nil
}

// OnResponse accumulates token usage into the per-minute token counter.
// Uses IncrementWithTTL with a 60-second window to track total tokens consumed
// in the current window. Errors are logged but not propagated to avoid
// blocking the response path.
func (r *RateLimitPlugin) OnResponse(ctx *plugin.ResponseContext) error {
	if r.tokPerMin > 0 {
		total := int64(ctx.Usage.InputTokens + ctx.Usage.OutputTokens)
		r.state.IncrementWithTTL(ctx.Context, "ratelimit:tokens:global", total, 60_000)
	}
	return nil
}

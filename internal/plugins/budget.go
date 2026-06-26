// BudgetPlugin checks accumulated LLM costs against per-session and per-day
// budget limits, blocking requests that would exceed configured thresholds.

package plugins

import (
	"fmt"
	"time"

	"github.com/chingjustwe/llm-interceptor/internal/plugin"
	"github.com/chingjustwe/llm-interceptor/internal/state"
)

// microsPerDollar is the number of microdollars in one USD. Costs are stored
// in the state backend as int64 microdollars for integer precision.
const microsPerDollar = 1_000_000

// BudgetPlugin implements the Plugin interface to enforce cost budgets. It
// reads accumulated costs from the state backend (populated by CostTracker)
// and blocks requests when per-session or per-day limits are exceeded.
type BudgetPlugin struct {
	state         state.Backend
	maxPerSession float64
	maxPerDay     float64
}

// NewBudgetPlugin creates a BudgetPlugin with the given state backend and
// budget limits. A limit of 0 or less means unlimited for that dimension.
func NewBudgetPlugin(st state.Backend, maxPerSession, maxPerDay float64) *BudgetPlugin {
	return &BudgetPlugin{
		state:         st,
		maxPerSession: maxPerSession,
		maxPerDay:     maxPerDay,
	}
}

// Name returns "budget" as the plugin identifier.
func (b *BudgetPlugin) Name() string { return "budget" }

// OnRequest checks accumulated costs against configured budget limits. Costs
// are stored in the state backend as microdollars by the CostTracker plugin.
// Returns a blocking HookResult with status 429 if either limit is exceeded.
func (b *BudgetPlugin) OnRequest(ctx *plugin.RequestContext) (*plugin.HookResult, error) {
	if b.maxPerSession > 0 {
		costMicro, err := b.state.Get(ctx.Context, "cost:session:"+ctx.SessionID)
		if err == nil && float64(costMicro)/microsPerDollar >= b.maxPerSession {
			return &plugin.HookResult{
				Block:      true,
				Reason:     fmt.Sprintf("session budget exceeded (max $%.2f)", b.maxPerSession),
				StatusCode: 429,
			}, nil
		}
	}
	if b.maxPerDay > 0 {
		today := time.Now().UTC().Format("2006-01-02")
		costMicro, err := b.state.Get(ctx.Context, "cost:daily:"+today)
		if err == nil && float64(costMicro)/microsPerDollar >= b.maxPerDay {
			return &plugin.HookResult{
				Block:      true,
				Reason:     fmt.Sprintf("daily budget exceeded (max $%.2f)", b.maxPerDay),
				StatusCode: 429,
			}, nil
		}
	}
	return nil, nil
}

// OnResponse is a no-op for the budget plugin; all checks happen on request.
func (b *BudgetPlugin) OnResponse(ctx *plugin.ResponseContext) error {
	return nil
}

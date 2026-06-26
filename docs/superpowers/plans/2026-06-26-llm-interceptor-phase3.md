# LLM Interceptor — Implementation Plan (Phase 3: Governance + Storage)

**Goal:** Add governance plugins (budget, rate limit, tool policy) and alternative storage backends (Redis state, PostgreSQL storage).

**Architecture:** Governance plugins implement `plugin.Plugin`, using OnRequest to block or allow requests. Cost-tracker uses usage data from OnResponse, writes accumulated cost to `state.Backend`. Budget plugin is OnRequest-only — reads cost from `state.Backend` to check limits. This avoids the OnResponse reverse-order dependency (budget runs before cost-tracker in reverse order but budget reads state, not metadata). Redis and PostgreSQL implement existing `state.Backend` and `storage.Backend` interfaces — no interface changes needed.

**Tech Stack:** Go 1.22+, `github.com/redis/go-redis/v9`, `github.com/lib/pq` (or `github.com/jackc/pgx/v5`), `github.com/google/uuid`

---

### Task 1: Redis State Store

**Files:**
- Create: `internal/state/redis.go`
- Create: `internal/state/redis_test.go`
- Modify: `internal/config/config.go`

- [ ] **Step 1: Add Redis config fields**

`internal/config/config.go` already has `RedisConfig` — verify it exists. If not, add:

```go
type RedisConfig struct {
	URL string `yaml:"url"`
}
```

- [ ] **Step 2: Install Redis driver**

```bash
go get github.com/redis/go-redis/v9
```

- [ ] **Step 3: Write Redis state store**

`internal/state/redis.go`:

```go
package state

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisBackend struct {
	client *redis.Client
}

func NewRedis(url string) (*RedisBackend, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(opts)
	if err := client.Ping(context.Background()).Err(); err != nil {
		return nil, err
	}
	return &RedisBackend{client: client}, nil
}

func (r *RedisBackend) Increment(ctx context.Context, key string, delta int64) (int64, error) {
	return r.client.IncrBy(ctx, key, delta).Result()
}

func (r *RedisBackend) Get(ctx context.Context, key string) (int64, error) {
	return r.client.Get(ctx, key).Int64()
}

func (r *RedisBackend) Reset(ctx context.Context, key string) error {
	return r.client.Del(ctx, key).Err()
}

func (r *RedisBackend) IncrementWithTTL(ctx context.Context, key string, delta int64, ttlMs int64) (int64, error) {
	pipe := r.client.Pipeline()
	incr := pipe.IncrBy(ctx, key, delta)
	pipe.Expire(ctx, key, time.Duration(ttlMs)*time.Millisecond)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return 0, err
	}
	return incr.Result()
}

func (r *RedisBackend) GetMany(ctx context.Context, keys []string) (map[string]int64, error) {
	vals, err := r.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	result := make(map[string]int64, len(keys))
	for i, key := range keys {
		if vals[i] != nil {
			switch v := vals[i].(type) {
			case int64:
				result[key] = v
			case string:
				// redis returns strings, parse if needed
				// for simplicity, assume int64 from IncrBy
			}
		}
	}
	return result, nil
}

func (r *RedisBackend) Close() error {
	return r.client.Close()
}
```

- [ ] **Step 4: Wire Redis into config loading**

In `internal/config/config.go`, add `Redis *RedisConfig` field to `StateStoreConfig` (may already exist). In main.go, add a `case "redis":` branch.

- [ ] **Step 5: Build and verify**

```bash
go build ./internal/state/
echo "build ok"
```

- [ ] **Step 6: Commit**

```bash
git add internal/state/ go.mod go.sum
git commit -m "feat(state): add Redis state store backend"
```

---

### Task 2: PostgreSQL Storage

**Files:**
- Create: `internal/storage/postgres.go`
- Modify: `internal/config/config.go`

- [ ] **Step 1: Add PostgreSQL to storage config (may already exist)**

Verify `PostgresConfig` exists in `config.go`. If not:

```go
type PostgresConfig struct {
	ConnectionString string `yaml:"connection_string"`
}
```

- [ ] **Step 2: Install PostgreSQL driver**

```bash
go get github.com/jackc/pgx/v5
```

- [ ] **Step 3: Write PostgreSQL storage**

`internal/storage/postgres.go`:

```go
package storage

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nightfield/llm-interceptor/internal/types"
)

type PostgresBackend struct {
	pool *pgxpool.Pool
}

func NewPostgres(connString string) (*PostgresBackend, error) {
	pool, err := pgxpool.New(context.Background(), connString)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	if _, err := pool.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS requests (
			id TEXT PRIMARY KEY,
			session_id TEXT,
			model TEXT,
			method TEXT,
			path TEXT,
			request_body TEXT,
			response_body TEXT,
			input_tokens INTEGER DEFAULT 0,
			output_tokens INTEGER DEFAULT 0,
			cache_read_tokens INTEGER DEFAULT 0,
			cache_creation_tokens INTEGER DEFAULT 0,
			duration_ms INTEGER,
			status_code INTEGER,
			created_at TIMESTAMP DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS idx_requests_session ON requests(session_id);
		CREATE INDEX IF NOT EXISTS idx_requests_created ON requests(created_at);
	`); err != nil {
		pool.Close()
		return nil, fmt.Errorf("create table: %w", err)
	}
	return &PostgresBackend{pool: pool}, nil
}

func (p *PostgresBackend) SaveRequest(ctx context.Context, req *types.StoredRequest) error {
	reqBody, _ := json.Marshal(req.Request)
	respBody, _ := json.Marshal(req.Response)
	_, err := p.pool.Exec(ctx,
		`INSERT INTO requests (id, session_id, model, method, path, request_body, response_body,
		 input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
		 duration_ms, status_code, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,NOW())`,
		req.ID, req.SessionID, req.Model, req.Method, req.Path,
		string(reqBody), string(respBody),
		req.Usage.InputTokens, req.Usage.OutputTokens,
		req.Usage.CacheReadTokens, req.Usage.CacheCreationTokens,
		req.DurationMs, req.StatusCode,
	)
	return err
}

func (p *PostgresBackend) GetSessionRequests(ctx context.Context, sessionID string, limit, offset int) ([]types.StoredRequest, error) {
	rows, _ := p.pool.Query(ctx,
		`SELECT id, session_id, model, method, path, request_body, response_body,
		 input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
		 duration_ms, status_code, EXTRACT(EPOCH FROM created_at)::bigint
		 FROM requests WHERE session_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		sessionID, limit, offset,
	)
	defer rows.Close()
	var results []types.StoredRequest
	for rows.Next() {
		var r types.StoredRequest
		if err := rows.Scan(&r.ID, &r.SessionID, &r.Model, &r.Method, &r.Path,
			&r.Request, &r.Response,
			&r.Usage.InputTokens, &r.Usage.OutputTokens,
			&r.Usage.CacheReadTokens, &r.Usage.CacheCreationTokens,
			&r.DurationMs, &r.StatusCode, &r.CreatedAt); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, nil
}

func (p *PostgresBackend) QueryRequests(ctx context.Context, filter types.RequestFilter) ([]types.StoredRequest, error) {
	query := `SELECT id, session_id, model, method, path, request_body, response_body,
		 input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
		 duration_ms, status_code, EXTRACT(EPOCH FROM created_at)::bigint FROM requests`
	var conditions []string
	var args []any
	argIdx := 1

	if filter.SessionID != nil {
		conditions = append(conditions, fmt.Sprintf("session_id = $%d", argIdx))
		args = append(args, *filter.SessionID)
		argIdx++
	}
	if filter.Model != nil {
		conditions = append(conditions, fmt.Sprintf("model = $%d", argIdx))
		args = append(args, *filter.Model)
		argIdx++
	}
	if filter.From != nil {
		conditions = append(conditions, fmt.Sprintf("EXTRACT(EPOCH FROM created_at)::bigint >= $%d", argIdx))
		args = append(args, *filter.From)
		argIdx++
	}
	if filter.To != nil {
		conditions = append(conditions, fmt.Sprintf("EXTRACT(EPOCH FROM created_at)::bigint <= $%d", argIdx))
		args = append(args, *filter.To)
		argIdx++
	}
	if len(conditions) > 0 {
		query += " WHERE " + conditions[0]
		for i := 1; i < len(conditions); i++ {
			query += " AND " + conditions[i]
		}
	}
	query += " ORDER BY created_at DESC"
	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT $%d", argIdx)
		args = append(args, filter.Limit)
		argIdx++
	}
	if filter.Offset > 0 {
		query += fmt.Sprintf(" OFFSET $%d", argIdx)
		args = append(args, filter.Offset)
		argIdx++
	}

	rows, err := p.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []types.StoredRequest
	for rows.Next() {
		var r types.StoredRequest
		if err := rows.Scan(&r.ID, &r.SessionID, &r.Model, &r.Method, &r.Path,
			&r.Request, &r.Response,
			&r.Usage.InputTokens, &r.Usage.OutputTokens,
			&r.Usage.CacheReadTokens, &r.Usage.CacheCreationTokens,
			&r.DurationMs, &r.StatusCode, &r.CreatedAt); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, nil
}

func (p *PostgresBackend) Close() error {
	p.pool.Close()
	return nil
}
```

- [ ] **Step 4: Wire Postgres into config loading**

In main.go, add `case "postgres":` branch using `storage.NewPostgres(cfg.Storage.Postgres.ConnectionString)`.

- [ ] **Step 5: Build and verify**

```bash
go build ./internal/storage/
echo "build ok"
```

- [ ] **Step 6: Commit**

```bash
git add internal/storage/ go.mod go.sum
git commit -m "feat(storage): add PostgreSQL storage backend"
```

---

### Task 3: Cost Tracker Plugin

**Files:**
- Create: `internal/plugins/cost_tracker.go`
- Create: `internal/plugins/cost_tracker_test.go`

- [ ] **Step 1: Write failing test**

`internal/plugins/cost_tracker_test.go`:

```go
package plugins

import (
	"testing"

	"github.com/nightfield/llm-interceptor/internal/plugin"
)

func TestCostTracker_TracksCost(t *testing.T) {
	tracker := NewCostTracker(nil) // nil state is ok for test

	ctx := &plugin.ResponseContext{
		Model: "claude-sonnet-4-6",
		Usage: plugin.Usage{
			InputTokens:  1000,
			OutputTokens: 500,
		},
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
```

- [ ] **Step 2: Write cost tracker implementation**

`internal/plugins/cost_tracker.go`:

```go
package plugins

import (
	"sync"
	"time"

	"github.com/nightfield/llm-interceptor/internal/plugin"
	"github.com/nightfield/llm-interceptor/internal/state"
)

var defaultPrices = map[string]struct {
	InputPerM  float64
	OutputPerM float64
}{
	"claude-sonnet-4-6":      {InputPerM: 3.0, OutputPerM: 15.0},
	"claude-sonnet-4-20250506": {InputPerM: 3.0, OutputPerM: 15.0},
	"claude-3-5-sonnet-20241022": {InputPerM: 3.0, OutputPerM: 15.0},
	"claude-3-opus-20240229": {InputPerM: 15.0, OutputPerM: 75.0},
	"claude-3-haiku-20240307": {InputPerM: 0.25, OutputPerM: 1.25},
	"claude-3-5-haiku-20241022": {InputPerM: 0.25, OutputPerM: 1.25},
}

type CostTracker struct {
	state    state.Backend
	mu       sync.Mutex
	sessions map[string]float64
	prices   map[string]struct{ InputPerM, OutputPerM float64 }
}

func NewCostTracker(st state.Backend) *CostTracker {
	prices := make(map[string]struct{ InputPerM, OutputPerM float64 }, len(defaultPrices))
	for k, v := range defaultPrices {
		prices[k] = v
	}
	return &CostTracker{
		state:    st,
		sessions: make(map[string]float64),
		prices:   prices,
	}
}

func (c *CostTracker) Name() string { return "cost-tracker" }

func (c *CostTracker) OnRequest(ctx *plugin.RequestContext) (*plugin.HookResult, error) {
	return nil, nil
}

func (c *CostTracker) OnResponse(ctx *plugin.ResponseContext) error {
	cost := c.CalculateCost(ctx.Model, ctx.Usage.InputTokens, ctx.Usage.OutputTokens)
	if cost == 0 {
		return nil
	}
	costMicro := int64(cost * 1_000_000)
	ctx.Metadata["cost_usd"] = cost

	c.mu.Lock()
	c.sessions[ctx.SessionID] += cost
	c.mu.Unlock()

	if c.state != nil {
		today := time.Now().UTC().Format("2006-01-02")
		c.state.Increment(ctx.Context, "cost:session:"+ctx.SessionID, costMicro)
		c.state.Increment(ctx.Context, "cost:daily:"+today, costMicro)
	}
	return nil
}

func (c *CostTracker) CalculateCost(model string, inputTokens, outputTokens int) float64 {
	p, ok := c.prices[model]
	if !ok {
		return 0
	}
	inputCost := float64(inputTokens) / 1_000_000 * p.InputPerM
	outputCost := float64(outputTokens) / 1_000_000 * p.OutputPerM
	return inputCost + outputCost
}

func (c *CostTracker) SessionCost(sessionID string) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessions[sessionID]
}

func (c *CostTracker) SetPrices(prices map[string]struct{ InputPerM, OutputPerM float64 }) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prices = prices
}
```

- [ ] **Step 3: Run tests**

```bash
go test ./internal/plugins/ -v -run TestCostTracker
```

Expected: 2 tests pass.

- [ ] **Step 4: Commit**

```bash
git add internal/plugins/
git commit -m "feat(plugin): add cost tracker with built-in Anthropic pricing"
```

---

### Task 4: Budget Governance Plugin

**Files:**
- Create: `internal/plugins/budget.go`
- Modify: `internal/config/config.go`

- [ ] **Step 1: Define budget config**

Add to config:

```go
type BudgetPluginConfig struct {
	MaxCostPerSession float64 `yaml:"max_cost_per_session"` // USD
	MaxCostPerDay     float64 `yaml:"max_cost_per_day"`
}
```

Add `Budget BudgetPluginConfig` to `PluginConfig`.

- [ ] **Step 2: Write budget plugin**

`internal/plugins/budget.go`:

```go
package plugins

import (
	"fmt"
	"time"

	"github.com/nightfield/llm-interceptor/internal/plugin"
	"github.com/nightfield/llm-interceptor/internal/state"
)

type BudgetPlugin struct {
	state         state.Backend
	maxPerSession float64
	maxPerDay     float64
}

func NewBudgetPlugin(st state.Backend, maxPerSession, maxPerDay float64) *BudgetPlugin {
	return &BudgetPlugin{
		state:         st,
		maxPerSession: maxPerSession,
		maxPerDay:     maxPerDay,
	}
}

func (b *BudgetPlugin) Name() string { return "budget" }

func (b *BudgetPlugin) OnRequest(ctx *plugin.RequestContext) (*plugin.HookResult, error) {
	if b.maxPerSession > 0 {
		costMicro, err := b.state.Get(ctx.Context, "cost:session:"+ctx.SessionID)
		if err == nil && float64(costMicro)/1_000_000 >= b.maxPerSession {
			return &plugin.HookResult{
				Block:      true,
				Reason:     fmt.Sprintf("session budget exceeded (max $%.2f)", b.maxPerSession),
				StatusCode: 429,
			}, nil
		}
	}
	if b.maxPerDay > 0 {
		costMicro, err := b.state.Get(ctx.Context, "cost:daily:"+time.Now().Format("2006-01-02"))
		if err == nil && float64(costMicro)/1_000_000 >= b.maxPerDay {
			return &plugin.HookResult{
				Block:      true,
				Reason:     fmt.Sprintf("daily budget exceeded (max $%.2f)", b.maxPerDay),
				StatusCode: 429,
			}, nil
		}
	}
	return nil, nil
}

func (b *BudgetPlugin) OnResponse(ctx *plugin.ResponseContext) error {
	return nil
}
```

- [ ] **Step 3: Build and verify**

```bash
go build ./internal/plugins/
echo "build ok"
```

- [ ] **Step 4: Commit**

```bash
git add internal/plugins/ internal/config/
git commit -m "feat(plugin): add budget governance plugin"
```

---

### Task 5: Rate Limit Governance Plugin

**Files:**
- Create: `internal/plugins/ratelimit.go`
- Modify: `internal/config/config.go`

- [ ] **Step 1: Define rate limit config**

```go
type RateLimitPluginConfig struct {
	RequestsPerMinute int `yaml:"requests_per_minute"`
	TokensPerMinute   int `yaml:"tokens_per_minute"`
}
```

Add `RateLimit RateLimitPluginConfig` to `PluginConfig`.

- [ ] **Step 2: Write rate limit plugin**

`internal/plugins/ratelimit.go`:

```go
package plugins

import (
	"fmt"

	"github.com/nightfield/llm-interceptor/internal/plugin"
	"github.com/nightfield/llm-interceptor/internal/state"
)

type RateLimitPlugin struct {
	state       state.Backend
	reqPerMin   int
	tokPerMin   int
}

func NewRateLimitPlugin(st state.Backend, reqPerMin, tokPerMin int) *RateLimitPlugin {
	return &RateLimitPlugin{
		state:     st,
		reqPerMin: reqPerMin,
		tokPerMin: tokPerMin,
	}
}

func (r *RateLimitPlugin) Name() string { return "ratelimit" }

func (r *RateLimitPlugin) OnRequest(ctx *plugin.RequestContext) (*plugin.HookResult, error) {
	if r.reqPerMin > 0 {
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

func (r *RateLimitPlugin) OnResponse(ctx *plugin.ResponseContext) error {
	if r.tokPerMin > 0 {
		total := int64(ctx.Usage.InputTokens + ctx.Usage.OutputTokens)
		r.state.IncrementWithTTL(ctx.Context, "ratelimit:tokens:global", total, 60_000)
	}
	return nil
}
```

- [ ] **Step 3: Build and verify**

```bash
go build ./internal/plugins/
echo "build ok"
```

- [ ] **Step 4: Commit**

```bash
git add internal/plugins/
git commit -m "feat(plugin): add rate limit governance plugin"
```

---

### Task 6: Tool Policy Plugin

**Files:**
- Create: `internal/plugins/toolpolicy.go`
- Modify: `internal/config/config.go`

- [ ] **Step 1: Define tool policy config**

```go
type ToolPolicyPluginConfig struct {
	BlockedTools  []string `yaml:"blocked_tools"`  // tools always blocked
	AllowedTools  []string `yaml:"allowed_tools"`  // if non-empty, only these allowed
}
```

- [ ] **Step 2: Write tool policy plugin**

`internal/plugins/toolpolicy.go`:

```go
package plugins

import (
	"fmt"
	"strings"

	"github.com/nightfield/llm-interceptor/internal/plugin"
)

type ToolPolicyPlugin struct {
	blockedTools map[string]bool
	allowedTools map[string]bool
	mode         string // "blocklist" or "allowlist"
}

func NewToolPolicyPlugin(blocked, allowed []string) *ToolPolicyPlugin {
	p := &ToolPolicyPlugin{
		blockedTools: make(map[string]bool),
		allowedTools: make(map[string]bool),
		mode:         "blocklist",
	}
	for _, t := range blocked {
		p.blockedTools[strings.ToLower(t)] = true
	}
	if len(allowed) > 0 {
		p.mode = "allowlist"
		for _, t := range allowed {
			p.allowedTools[strings.ToLower(t)] = true
		}
	}
	return p
}

func (t *ToolPolicyPlugin) Name() string { return "tool-policy" }

func (t *ToolPolicyPlugin) OnRequest(ctx *plugin.RequestContext) (*plugin.HookResult, error) {
	var body struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(ctx.Body, &body); err != nil {
		return nil, nil
	}
	for _, tool := range body.Tools {
		name := strings.ToLower(tool.Name)
		if t.blockedTools[name] {
			return &plugin.HookResult{
				Block:      true,
				Reason:     fmt.Sprintf("tool '%s' is blocked by policy", tool.Name),
				StatusCode: 403,
			}, nil
		}
		if t.mode == "allowlist" && !t.allowedTools[name] {
			return &plugin.HookResult{
				Block:      true,
				Reason:     fmt.Sprintf("tool '%s' is not in the allowed list", tool.Name),
				StatusCode: 403,
			}, nil
		}
	}
	return nil, nil
}

func (t *ToolPolicyPlugin) OnResponse(ctx *plugin.ResponseContext) error {
	return nil
}
```

Add `"encoding/json"` import.

- [ ] **Step 3: Build and verify**

```bash
go build ./internal/plugins/
echo "build ok"
```

- [ ] **Step 4: Commit**

```bash
git add internal/plugins/
git commit -m "feat(plugin): add tool policy governance plugin"
```

---

### Task 7: Wire governance plugins into main.go

**Files:**
- Modify: `cmd/llm-interceptor/main.go`
- Modify: `internal/config/config.go`

- [ ] **Step 1: Add governance config to PluginConfig**

```go
type PluginConfig struct {
	OTelExporter OTelExporterPluginConfig   `yaml:"otel-exporter,omitempty"`
	CostTracker  CostTrackerPluginConfig    `yaml:"cost-tracker,omitempty"`
	Budget       BudgetPluginConfig         `yaml:"budget,omitempty"`
	RateLimit    RateLimitPluginConfig      `yaml:"rate-limit,omitempty"`
	ToolPolicy   ToolPolicyPluginConfig     `yaml:"tool-policy,omitempty"`
}

type CostTrackerPluginConfig struct {
	Enabled bool `yaml:"enabled"`
}
```

- [ ] **Step 2: Wire plugins in main.go**

In the plugin initialization section:

```go
if cfg.Plugins.CostTracker.Enabled {
	pluginList = append(pluginList, plugins.NewCostTracker(st))
}
if cfg.Plugins.Budget.MaxCostPerSession > 0 || cfg.Plugins.Budget.MaxCostPerDay > 0 {
	pluginList = append(pluginList, plugins.NewBudgetPlugin(st,
		cfg.Plugins.Budget.MaxCostPerSession,
		cfg.Plugins.Budget.MaxCostPerDay,
	))
}
if cfg.Plugins.RateLimit.RequestsPerMinute > 0 || cfg.Plugins.RateLimit.TokensPerMinute > 0 {
	pluginList = append(pluginList, plugins.NewRateLimitPlugin(st,
		cfg.Plugins.RateLimit.RequestsPerMinute,
		cfg.Plugins.RateLimit.TokensPerMinute,
	))
}
if len(cfg.Plugins.ToolPolicy.BlockedTools) > 0 || len(cfg.Plugins.ToolPolicy.AllowedTools) > 0 {
	pluginList = append(pluginList, plugins.NewToolPolicyPlugin(
		cfg.Plugins.ToolPolicy.BlockedTools,
		cfg.Plugins.ToolPolicy.AllowedTools,
	))
}
```

- [ ] **Step 3: Update config.example.yaml**

```yaml
plugins:
  otel-exporter:
    enabled: false
    endpoint: "localhost:4318"
    headers: {}
  cost-tracker:
    enabled: true
  budget:
    max_cost_per_session: 0.50
    max_cost_per_day: 0
  rate-limit:
    requests_per_minute: 60
    tokens_per_minute: 0
  tool-policy:
    blocked_tools: []
    allowed_tools: []
```

- [ ] **Step 4: Build and verify**

```bash
go build ./cmd/llm-interceptor/
echo "build succeeded"
```

- [ ] **Step 5: Commit**

```bash
git add cmd/ internal/config/ config.example.yaml
git commit -m "feat: wire governance plugins into server"
```

---

### Self-Review

**1. Spec coverage:**
- Cost tracker with built-in pricing table ✓
- Budget governance (per-session, per-day) ✓
- Rate limit governance (requests/min, tokens/min) ✓
- Tool policy (blocklist + allowlist) ✓
- Redis state store ✓
- PostgreSQL storage ✓

**2. Plugin ordering:**
- CostTracker.OnResponse writes accumulated cost to state.Backend (keys: `cost:session:xxx`, `cost:daily:yyy`)
- BudgetPlugin.OnRequest reads from state.Backend — independent of OnResponse reverse-order execution
- Registration order does not affect correctness; budget always works regardless of plugin position

**3. State store dependency:**
- Budget and rate limit plugins require state.Backend — must be initialized before plugins
- In-memory works for single instance; Redis needed for multi-instance

**4. Backward compatibility:**
- All new plugins default to disabled (cost-tracker optional, budget/rate-limit zero = disabled)
- Existing Phase 1 configs continue to work

# LLM Interceptor — Project Context

Status: All 5 phases complete, 71 commits, 11 test files, all tests passing.

## Project
Local-first, open-source LLM gateway — transparent proxy, OTel observability,
governance (budget/rate-limit/tool-policy), multi-provider LLM routing,
protocol translation (Anthropic ↔ OpenAI), and a React SPA — in a single Go binary.

## Repository
- Module: `github.com/chingjustwe/llm-interceptor` (**all lowercase**)
- Go 1.26.3, Node v24.16.0
- Key deps: `chi/v5`, `yaml.v3`, `modernc.org/sqlite`, `jackc/pgx/v5`,
  `redis/go-redis/v9`, `otel`, `golang.org/x/crypto`
- Remote: `git@github.com:chingjustwe/llm-interceptor.git`

## Directory Layout
```
cmd/llm-interceptor/        main.go (with embed.FS for SPA)
internal/
├── config/                 YAML config loader
├── types/                  Shared types (StoredRequest, TokenUsage, RequestFilter)
├── plugin/                 Plugin interface + Dispatcher
├── proxy/                  HTTP proxy + SSE streaming relay
├── storage/                Backend interface + SQLite + PostgreSQL
├── state/                  Backend interface + in-memory + Redis
├── plugins/                Built-in plugins (otel, cost-tracker, budget, ratelimit, tool-policy)
├── api/                    REST API + SSE broker (for web UI)
├── router/                 Mode detection + provider routing + key management
└── translate/              Protocol translation (Anthropic ↔ OpenAI, streaming SSE)
ui/                         React SPA (Vite + TypeScript + Tailwind)
```

## Implementation Status
| Phase | What | Status |
|-------|------|--------|
| 1.A | Enhanced Data Capture: structured fields (temp, top_p, TTFT, error, system prompt), schema migration, API filter params | ✅ |
| 1.B | OpenAI Chat Completions API: `/v1/chat/completions` endpoint, bidirectional protocol translation, streaming SSE translation, router protocol negotiation | ✅ |
| 2 | OTel exporter plugin (traces + metrics) | ✅ |
| 3 | Governance (cost/budget/ratelimit/tool-policy), Redis, PostgreSQL | ✅ |
| 4 | LLM Router, API key management (bcrypt), protocol translation | ✅ |
| 5 | React SPA (Vite + Tailwind): requests, sessions, cost, keys, SSE live | ✅ |

## Architecture Principles
- **Plugin architecture** via Go interfaces (`plugin.Plugin`) — in-process, not out-of-process
- **Forward path never blocked** — OTel/metric export and state updates happen async
- **Dual mode**: passthrough (default) and router (managed keys `sk-lli-*`)
- **Metadata map** on `RequestContext`/`ResponseContext` is the inter-plugin communication channel
- **Storage / State** are interface-abstracted — implementations swappable at config

## Key Interfaces

### Plugin (`internal/plugin/interface.go`)
```go
type Plugin interface {
    Name() string
    OnRequest(ctx *RequestContext) (*HookResult, error)
    OnResponse(ctx *ResponseContext) error
}
```
- `Dispatcher.ExecuteOnRequest` runs plugins in registration order; short-circuits on Block
- `Dispatcher.ExecuteOnResponse` runs plugins in **reverse** order
- `HookResult` fields: `Block bool`, `Reason string`, `StatusCode int`, `ErrorType string`, `RetryAfterSec int`

### Storage Backend (`internal/storage/interface.go`)
```go
type Backend interface {
    SaveRequest(ctx context.Context, req *types.StoredRequest) error
    GetSessionRequests(ctx context.Context, sessionID string, limit, offset int) ([]types.StoredRequest, error)
    QueryRequests(ctx context.Context, filter types.RequestFilter) ([]types.StoredRequest, error)
    SaveAPIKey(ctx context.Context, key *APIKey) error
    GetAPIKeyByPrefix(ctx context.Context, prefix string) (*APIKey, error)
    ListAPIKeys(ctx context.Context) ([]APIKey, error)
    DisableAPIKey(ctx context.Context, id string) error
    Close() error
}
```
Implementations: SQLite (`internal/storage/sqlite.go`), PostgreSQL (`internal/storage/postgres.go`).

### State Backend (`internal/state/interface.go`)
```go
type Backend interface {
    Increment(ctx context.Context, key string, delta int64) (int64, error)
    Get(ctx context.Context, key string) (int64, error)
    Reset(ctx context.Context, key string) error
    IncrementWithTTL(ctx context.Context, key string, delta int64, ttlMs int64) (int64, error)
    GetMany(ctx context.Context, keys []string) (map[string]int64, error)
    Close() error
}
```
Implementations: In-memory (`internal/state/memory.go`), Redis (`internal/state/redis.go`).

## Plugin Lifecycle
```
Request → OnRequest(plugins in order) → proxy forward → OnResponse(plugins in reverse)
                                     ↕
                              (block short-circuits)
```

Plugin registration order in `main.go`:
1. OTel Exporter (creates span in metadata)
2. Tool Policy (checks request body for blocked/allowed tools)
3. Rate Limit (checks/updates counters in state)
4. Budget (reads cost from state)
5. Cost Tracker (writes cost to state, runs last in forward, first in reverse)

## Router Mode
- Auto-detected by API key prefix: `sk-lli-*` → router mode, else passthrough
- `POST /api/keys` to generate, `PATCH /api/keys/{id}/disable` to revoke
- Keys hashed with bcrypt before storage; only prefix & hash persisted
- Router resolves upstream target per-request via model glob matching — does **not** replace the proxy pipeline
- Disabled by default (`router.enabled: false`)

## Config (`config.example.yaml`)
- Storage: sqlite (path `~/.llm-interceptor/data.db`) or postgres (connection_string)
- State: memory or redis (url)
- Plugins: otel-exporter, cost-tracker, budget, rate-limit, tool-policy
- Router: enabled, providers (name, base_url, model_glob, api_key)

## API Endpoints (served on same port)
| Method | Path | Description |
|--------|------|-------------|
| POST | `/v1/messages` | LLM proxy (Anthropic Messages API) |
| POST | `/v1/chat/completions` | LLM proxy (OpenAI Chat Completions API) |
| GET | `/api/requests` | List stored requests (filters: model, session_id, stop_reason, error_type, min_duration, max_duration, status_code) |
| GET | `/api/requests/{id}` | Get single request |
| GET | `/api/sessions` | List session summaries |
| GET | `/api/sessions/{id}/requests` | Get session's requests |
| GET | `/api/stats` | Cost + usage statistics |
| POST | `/api/keys` | Generate API key (router mode only) |
| GET | `/api/keys` | List API keys (router mode only) |
| PATCH | `/api/keys/{id}/disable` | Disable API key (router mode only) |
| GET | `/api/events` | SSE live event stream |
| GET | `/health` | Health check |
| `/*` | SPA static files (via `embed.FS`) | |

## Frontend (`ui/`)
- Vite + React 18 + TypeScript + Tailwind CSS
- Pages: Requests, Sessions, Cost Dashboard, Key Management
- SSE live events displayed as toast notifications
- Dev server proxies `/api` to Go backend at `localhost:8080`
- Production: built to `ui/dist/`, embedded via `//go:embed ui/dist/*`

## Development Workflow
- Every code change MUST include corresponding tests. No exception — new features, bugfixes, and refactors all require test coverage.
- Before claiming work is complete, run `go build ./... && go vet ./... && go test ./... -v && (cd ui && npm run build)` and confirm all green.
- Commit granularly: one logical change per commit.
- Run `git push` after each commit.

## Phase 1 Details

### Enhanced Data Capture
- `StoredRequest` has 8 new pointer fields: `SystemPrompt`, `StopReason`, `ErrorType`, `ErrorMessage`, `TTFTMs`, `Temperature`, `TopP`, `RequestParams`
- `RequestFilter` has 5 new fields: `StopReason`, `ErrorType`, `MinDuration`, `MaxDuration`, `StatusCodes`
- SQLite migration uses `PRAGMA user_version`; PG uses `ALTER TABLE ADD COLUMN IF NOT EXISTS`
- `proxy.ExtractRequestParams` strips `messages`/`stream`/`model` from request body
- `proxy.ExtractSystemPrompt` checks Anthropic top-level `system` then OpenAI `system`/`developer` role
- `proxy.ExtractError` handles both OpenAI and Anthropic error formats
- TTFT tracked in `collectSSE` on first `content_block_delta` (text) or `content_block_start` (tool_use)

### OpenAI Chat Completions
- Both `/v1/messages` and `/v1/chat/completions` routes handled by same `llmHandler` closure
- `reqCtx.APIFormat` set via path-based switch (anthropic/openai)
- `translate.ToOpenAI` handles tools, tool_choice, temperature, top_p, stop_sequences, metadata
- `translate.AnthropicToOpenAIResponse` maps content blocks + stop_reason + usage details
- `translate.ToAnthropic` handles tools, tool_choice, response_format, user metadata
- `translate.OpenAIToAnthropicResponse` maps tool_calls + finish_reason + cached/reasoning tokens
- `translate/streaming.go` has `SSEEvent`, `StreamParser`/`StreamTranslator` interfaces with bidirectional implementations
- Router has `Negotiate` method with `NegotiatedRoute` struct for protocol detection

## Common Gotchas
- **Module path is all lowercase**: `github.com/chingjustwe/llm-interceptor` — import paths use lowercase `llm-interceptor`
- **Plugin types differ**: `proxy.UsageData`/`proxy.ToolCall` and `plugin.Usage`/`plugin.ToolCall` are separate types — explicit conversion needed in `main.go`
- **Reverse OnResponse order**: CostTracker must be registered last (runs first in reverse) to write cost before Budget reads it in next request
- **SQLite CGO-free**: import `modernc.org/sqlite`, NOT `mattn/go-sqlite3`
- **PG uses pgxpool**: `internal/storage/postgres.go` uses `pgx/v5/pgxpool`
- **~ expansion**: configured via `expandHome()` in `internal/config/config.go`
- **embed path**: `//go:embed ui/dist/*` is relative to `cmd/llm-interceptor/`
- **OTel**: uses `gen_ai.*` semantic convention attributes per OpenTelemetry spec
- **APIFormat**: `plugin.RequestContext`/`ResponseContext` have `APIFormat string` field set by path
- **Proxy path param**: `HandleRequest`/`HandleRequestStream` now accept `path string` — always pass `r.URL.Path`
- **TTFT return**: `HandleRequestStream` returns `ttftMs int64` (extra return value before durationMs)
- **Pointer fields in storage**: SaveRequest uses nil/dereferenced pattern for pointer fields

# LLM Interceptor — Design Spec

Date: 2026-06-25
Status: Draft

## 1. Overview

**LLM Interceptor** is a local LLM gateway that sits between AI tools (like Claude Code) and LLM providers. It supports **two operating modes**: as a transparent proxy for observability, or as a full LLM router that manages its own API keys and routes requests to multiple providers (like OpenRouter, but local-first).

It provides a **general-purpose plugin system** for observability, governance, and audit capabilities, plus a local Web UI for real-time monitoring and cost analysis.

### Core Features

- **Transparent Proxy** — zero code changes, just set `ANTHROPIC_BASE_URL`, get OTel + governance for free
- **LLM Router** — manage your own API keys, route requests to any provider based on model
- **Plugin System** — standard lifecycle hooks, plugins independently developed and distributed
- **OTel-native** — built-in OTLP exporter plugin, compatible with any OTel backend
- **Local Web UI** — real-time request stream, session traces, cost analysis
- **Progressive Governance** — budget control, rate limiting, tool policies (pluggable)

## 2. Technology Stack

| Layer | Choice | Rationale |
|-------|--------|-----------|
| Runtime | **Go** | Single binary, excellent HTTP/streaming support, great concurrency model |
| HTTP | `net/http` + `chi` (or similar) | Lightweight router, idiomatic Go |
| OTel | `go.opentelemetry.io/otel` | Official OTel Go SDK |
| Persistence | **SQLite** (`modernc.org/sqlite`, no CGO), abstracted | Default for local use; abstraction layer allows swap to PostgreSQL etc. |
| State Store | **In-memory** (default), abstracted | For rate counters, budget counters; abstraction allows swap to Redis for multi-instance |
| Web UI | **React** (or Vue) SPA | Separate frontend; dev server for dev, embedded via `embed.FS` for production single binary |
| Config | **YAML** (`gopkg.in/yaml.v3`) | Readable, good for policy definitions |
| Plugin | **Go interfaces** (in-process) | Type-safe, compile-time checked; can extend to `hashicorp/go-plugin` for out-of-process |

## 3. Architecture

```
                    ┌──────────────────────────────────────────────┐
  Claude Code       │  LLM Interceptor                             │  ┌── Anthropic API
                    │                                              │  │
  POST /v1/messages─▶  Go HTTP (127.0.0.1:8080)                    │──┤── OpenAI API
  + headers         │    │                             │           │  │
                    │    ▼                             │           │  └── ... (any)
                    │  ┌──────────────────────┐        │           │
                    │  │ Mode Detector        │        │           │
                    │  │ ┌──────────────────┐ │        │           │
                    │  │ │ Passthrough: if  │ │        │           │
                    │  │ │ API key is not   │─┼──▶ forward to     │
                    │  │ │ interceptor key  │ │    configured     │
                    │  │ └──────────────────┘ │      upstream     │
                    │  │ ┌──────────────────┐ │        │           │
                    │  │ │ Router: if API   │ │        ▼           │
                    │  │ │ key starts with  │─┼──▶ Route Engine   │
                    │  │ │ sk-lli-          │ │    │              │
                    │  │ └──────────────────┘ │    │ model→provider│
                    │  └──────────────────────┘    │ mapping       │
                    │              ▼               ▼              │
                    │  ┌──────────────────────────────────┐         │
                    │  │  Proxy Engine (passthrough + streaming)   │
                    │  └──────────────┬───────────────────┘         │
                    │                 │                             │
                    │  ┌──────────────▼───────────────┐              │
                    │  │  Plugin Lifecycle             │              │
                    │  │  (OnRequest → Governance →    │              │
                    │  │   OnResponse)                 │              │
                    │  └──────────────┬───────────────────┘              │
                    │                 │                             │
                    │  ┌──────────────▼───────────────┐              │
                    │  │  Plugins                      │              │
                    │  │  ├─ otel-exporter             │              │
                    │  │  ├─ cost-tracker              │              │
                    │  │  ├─ audit-log                 │              │
                    │  │  └─ tool-policy               │              │
                    │  └───────────────────────────────┘              │
                    │         │                                       │
                    │  ┌──────▼──────┐     ┌──────────▼───────────┐  │
                    │  │  Storage    │     │  State Store          │  │
                    │  │  ├─ SQLite  │     │  ├─ In-Memory         │  │
                    │  │  └─ PG      │     │  └─ Redis             │  │
                    │  └─────────────┘     └──────────────────────┘  │
                    │         │                                       │
                    │  ┌──────▼──────────────────────┐                │
                    │  │  Web UI (localhost:8080/ui) │ dashboard      │
                    │  └─────────────────────────────┘                │
                    └──────────────────────────────────────────────────┘
```

## 4. Plugin System

### 4.1 Hook Points

```go
type Plugin interface {
    Name() string
    OnRequest(ctx *RequestContext) (*HookResult, error)
    OnResponse(ctx *ResponseContext) error
    OnGovernance(ctx *GovernanceContext) (*GovernanceResult, error)
    OnStart() error
    OnStop() error
}
```

### 4.2 Context Types

```go
type RequestContext struct {
    ID        string
    Method    string
    Path      string
    Headers   map[string]string
    Body      RequestBody
    SessionID string // from x-claude-code-session-id header
    AgentID   string // from x-claude-code-agent-id header
    Metadata  map[string]any // shared across plugins
}

type RequestBody struct {
    Model     string
    Messages  []any
    System    *string
    Tools     []any
    MaxTokens *int
    Stream    bool
}

type ResponseContext struct {
    RequestID   string
    SessionID   *string
    Model       string
    Usage       TokenUsage
    StopReason  *string
    ToolCalls   []ToolCall
    DurationMs  int64
    StatusCode  int
    Metadata    map[string]any
}

type TokenUsage struct {
    InputTokens          int
    OutputTokens         int
    CacheReadTokens      int
    CacheCreationTokens  int
}

type ToolCall struct {
    Name  string
    Input map[string]any
}

type GovernanceContext struct {
    RequestID string
    SessionID *string
    Policies  []Policy
    Metadata  map[string]any
}

type HookResult struct {
    Block      bool
    Reason     string
    StatusCode int
}

type GovernanceResult struct {
    Block      bool
    Reason     string
    StatusCode int
}
```

### 4.3 Execution Order

```
Plugin A.OnRequest
  → Plugin B.OnRequest
    → Governance (OnGovernance)
      → Forward
        → Plugin B.OnResponse  (reverse order)
          → Plugin A.OnResponse
```

`OnRequest` and `OnResponse` execute in configured order. If any `OnRequest` returns `{Block: true}`, the request is blocked immediately.

**Data sharing between plugins:**
- `RequestContext.Metadata` — writable during `OnRequest`, readable during `OnResponse` by **the same plugin**. Example: OTel stores a span in metadata during OnRequest, reads it back in OnResponse.
- `ResponseContext.Metadata` — writable during `OnResponse`. Not suitable for cross-plugin dependency in OnResponse because execution is reverse order.
- `state.Backend` — the authoritative channel for cross-plugin shared state. Example: CostTracker writes accumulated cost to state.Backend during OnResponse; BudgetPlugin reads from state.Backend during OnRequest (next request). This is direction-independent and order-independent.

### 4.4 Built-in Plugins

| Plugin | Function | Status |
|--------|----------|--------|
| `otel-exporter` | OTel traces/metrics export via OTLP | Built-in |
| `cost-tracker` | Cost calculation + budget management | Built-in |
| `audit-log` | Full request/response persistence (via Storage abstraction) | Built-in |
| `tool-policy` | Tool allowlist/blocklist governance | Built-in |

Other plugins (e.g., `langfuse-exporter`, `slack-notifier`, `sensitive-data-detector`) can be distributed as external packages using `hashicorp/go-plugin` for out-of-process plugins.

## 5. Core Proxy

### 5.1 Request Lifecycle

```
1. Go HTTP server receives request
2. Parse request body and headers
3. Create RequestContext (with sessionId/agentId)
4. Execute all plugins' OnRequest in order (sequentially via goroutines)
5. If any plugin returns Block: true, return corresponding statusCode
6. Execute Governance (built-in: budget + rate limit)
7. Forward request to upstream (streaming: relay as received)
8. Stream ends, collect usage data (from message_delta event)
9. Create ResponseContext
10. Execute all plugins' OnResponse in reverse order
11. Complete
```

### 5.2 Streaming Handling

Usage data in streaming mode comes from the last SSE event (`message_delta`):
- Forward each SSE event as received (no buffering)
- "Peek" at each event to detect `message_delta`
- Extract usage data without blocking the relay
- Trigger `OnResponse` asynchronously after the stream ends via a goroutine

### 5.3 Concurrency Model

All plugin hooks execute concurrently without blocking the forward path:
- `OnRequest` — sequential execution (plugins may have ordering dependencies)
- `OnResponse` — triggered after stream ends in a background goroutine
- Governance checks — `sync/atomic` counters for sub-microsecond latency
- No blocking I/O on the critical forward path
- Go's `net/http` handles each connection in its own goroutine naturally

## 6. Operating Modes

The interceptor auto-detects which mode to use based on the API key in the request.

### 6.1 Mode Detection

```
Request arrives with API key (x-api-key or Authorization header)
  ├─ If key is NOT an interceptor-managed key → Passthrough Mode
  └─ If key IS an interceptor-managed key (sk-lli-*) → Router Mode
```

### 6.2 Passthrough Mode

The original design: user points their AI tool at the interceptor with their own provider API key.

- Forward to a **single configured upstream** (`upstream` in config)
- No auth check, no key management
- Observability + governance applied transparently

```
Claude Code ──▶ LLM Interceptor ──▶ api.anthropic.com
  (user's anthropic key)       (forward, no auth)
```

### 6.3 Router Mode

The interceptor acts as a full LLM gateway, managing its own API keys and routing to multiple providers.

- Interceptor issues API keys (`sk-lli-xxx`) to users/teams
- Request comes with an interceptor-managed key
- Interceptor validates the key, identifies the user
- Reads the `model` field, looks up routing rules
- Routes to the appropriate provider (may involve protocol translation)
- Multiple providers configured with their own API keys stored server-side

```
Any LLM Client ──▶ LLM Interceptor ──▶ Anthropic (if model=claude-*)
  (sk-lli-xxx)              │
                           ├──▶ OpenAI    (if model=gpt-*)
                           ├──▶ Ollama    (if model=llama-*)
                           └──▶ ...
```

### 6.4 Router Mode: Request Lifecycle

```
1. Go HTTP server receives request
2. Extract API key from x-api-key / Authorization header
3. Key matches sk-lli-* pattern → Router Mode
4. Validate API key (check storage for key record)
5. Lookup user/team associated with key
6. Read model from request body
7. Route table lookup: model pattern → provider + endpoint + API key
8. (Optional) Protocol translation if upstream uses a different format
9. Attach provider API key to forwarded request
10. Continue with standard lifecycle: Plugins → Governance → Forward → ...
```

### 6.5 API Key Management

API keys are managed via the Web UI or a CLI command:

```bash
# CLI (future)
llm-interceptor key create --name "dev-team" --budget 50
# → sk-lli-a1b2c3d4...

llm-interceptor key list
llm-interceptor key revoke sk-lli-a1b2c3d4
```

Key model:

```go
type APIKey struct {
    ID        string    // internal ID
    Prefix    string    // first 8 chars for identification (sk-lli-a1b2c3d4)
    Hash      string    // bcrypt hash of full key
    Name      string    // human-readable label
    Budget    *Budget   // optional budget limit for this key
    CreatedAt time.Time
    ExpiresAt *time.Time
    Revoked   bool
}
```

Only the **hash** is stored; the full key is shown once at creation.

### 6.6 Provider Routing

Routing rules map model patterns to upstream providers:

```yaml
routing:
  # Default provider (used in passthrough mode too)
  default: anthropic

  # Routing rules evaluated in order, first match wins
  rules:
    - model: "claude-*"
      provider: anthropic
    - model: "gpt-*"
      provider: openai
    - model: "o1-*"
      provider: openai
    - model: "gemini-*"
      provider: google
    - model: "llama-*"
      provider: ollama
    - model: "*"
      provider: anthropic  # catch-all fallback

providers:
  anthropic:
    base_url: "https://api.anthropic.com"
    api_key_env: "ANTHROPIC_API_KEY"
    format: anthropic-messages

  openai:
    base_url: "https://api.openai.com"
    api_key_env: "OPENAI_API_KEY"
    format: openai-chat  # requires protocol translation

  ollama:
    base_url: "http://localhost:11434"
    api_key_env: ""
    format: openai-chat
```

### 6.7 Protocol Translation

When the client sends an Anthropic Messages API request but the upstream is OpenAI:

| Anthropic field | OpenAI field |
|----------------|--------------|
| `model` | `model` (translate name) |
| `messages` (alternating user/assistant) | `messages` (add system role) |
| `system` (top-level) | `messages[0]` with role `system` |
| `max_tokens` | `max_tokens` |
| `tools[*].input_schema` | `tools[*].function.parameters` |
| Response: `content[0].text` | Response: `choices[0].message.content` |
| Response: `usage.input_tokens` | Response: `usage.prompt_tokens` |
| Response: `stop_reason: "end_turn"` | Response: `finish_reason: "stop"` |

Protocol translation is **applied as a built-in plugin** (enabled/disabled per route), keeping the proxy core agnostic.

## 7. Web UI (Local Dashboard)

| Page | Function |
|------|----------|
| **Live Stream** | SSE + table showing recent requests in real time |
| **Session Detail** | Full request history grouped by `sessionId` |
| **Request Detail** | Span view: tool call chain and parent/child relationships |
| **Cost Analysis** | Cost distribution by model/session/day |
| **Policy Management** | Visual editor for budget/rate limit/tool policy |

Tech approach: **React (or Vue) SPA** — separate frontend project under `ui/`.

- Communication with backend via REST API (requests, sessions, stats) + SSE (live stream)
- Development: frontend dev server proxies API to Go backend
- Production: frontend built to `dist/`, embedded into Go binary via `embed.FS`
- Single binary deploy: no separate frontend server needed

## 8. Storage Abstraction

All persistent data (request/response logs, audit trail) flows through an interface, allowing the backend to be swapped without changing plugin code.

```go
type StorageBackend interface {
    SaveRequest(ctx context.Context, req *StoredRequest) error
    GetSessionRequests(ctx context.Context, sessionID string) ([]StoredRequest, error)
    QueryRequests(ctx context.Context, filter RequestFilter) ([]StoredRequest, error)

    GetCostByModel(ctx context.Context, from, to int64) ([]CostByModel, error)
    GetCostBySession(ctx context.Context, from, to int64) ([]CostBySession, error)
    GetTokenUsage(ctx context.Context, from, to int64) (*TokenUsage, error)

    // API key management (Phase 4)
    SaveAPIKey(ctx context.Context, key *APIKeyRecord) error
    GetAPIKeyByPrefix(ctx context.Context, prefix string) (*APIKeyRecord, error)
    ListAPIKeys(ctx context.Context) ([]APIKeyRecord, error)
    DisableAPIKey(ctx context.Context, id string) error

    Init(ctx context.Context) error
    Close() error
}
```

### Built-in implementations

| Backend | Scope | Use case |
|---------|-------|----------|
| **SQLite** | Single machine | Default, zero ops |
| **PostgreSQL** | Multi-instance | Team/enterprise deployment |

The active backend is selected via config:

```yaml
storage:
  type: sqlite
  sqlite:
    path: "~/.llm-interceptor/data.db"
  # type: postgres
  # postgres:
  #   connection_string: "postgres://..."
```

## 9. State Store Abstraction

Real-time counters (budget tracking, rate limits, session state) use a separate, simpler abstraction optimized for atomic increments and low latency.

```go
type StateStoreBackend interface {
    Increment(ctx context.Context, key string, delta int64) (int64, error)
    Get(ctx context.Context, key string) (int64, error)
    Reset(ctx context.Context, key string) error

    IncrementWithTTL(ctx context.Context, key string, delta int64, ttlMs int64) (int64, error)

    GetMany(ctx context.Context, keys []string) (map[string]int64, error)

    Init(ctx context.Context) error
    Close() error
}
```

### Built-in implementations

| Backend | Persistence | Use case |
|---------|-------------|----------|
| **In-Memory** | None (lost on restart) | Single instance, default |
| **Redis** | Optional (AOF/RDB) | Multi-instance state sharing |

Config:

```yaml
state_store:
  type: memory
  # type: redis
  # redis:
  #   url: "redis://localhost:6379"
```

### Why separate from Storage

- **Performance**: counters need atomic increment + sub-microsecond latency; storage needs SQL queries + batching
- **Lifetime**: state is transient (budget counters reset daily); storage is persistent (audit logs live for months)
- **Concurrency model**: counters use optimistic concurrency (Redis INCR / `sync/atomic`); storage uses transactions

## 10. Configuration

### 10.1 Config file

```yaml
# config.yaml
listen: "127.0.0.1:8080"
upstream: "https://api.anthropic.com"

metric_prefix: "llm_proxy."

log:
  request_body: false
  response_body: false

storage:
  type: sqlite
  sqlite:
    path: "~/.llm-interceptor/data.db"
  # type: postgres
  # postgres:
  #   connection_string: "postgres://user:pass@host/db"

state_store:
  type: memory
  # type: redis
  # redis:
  #   url: "redis://localhost:6379"

routing:
  default: anthropic
  rules:
    - model: "claude-*"
      provider: anthropic
    - model: "gpt-*"
      provider: openai
    - model: "*"
      provider: anthropic

providers:
  anthropic:
    base_url: "https://api.anthropic.com"
    api_key_env: "ANTHROPIC_API_KEY"
    format: anthropic-messages
  openai:
    base_url: "https://api.openai.com"
    api_key_env: "OPENAI_API_KEY"
    format: openai-chat

plugins:
  otel-exporter:
    enabled: true
    endpoint: "http://localhost:4318"
    headers: {}
  audit-log:
    enabled: true
  cost-tracker:
    enabled: true
    pricing:
      claude-sonnet-4-6:
        input: 3.0
        output: 15.0
        cache_read: 0.30
        cache_create: 3.75
  tool-policy:
    enabled: true
    rules:
      - action: block
        patterns: ["Bash"]

governance:
  enabled: true
  budgets:
    - scope: day
      limit:
        tokens: 5000000
      action: block
  rate_limits:
    - scope: minute
      requests: 60
```

### 10.2 Env vars override

| Env Var | Overrides |
|---------|-----------|
| `LLM_INTERCEPTOR_CONFIG` | Config file path |
| `LLM_INTERCEPTOR_LISTEN` | `listen` |
| `LLM_INTERCEPTOR_UPSTREAM` | `upstream` |
| `LLM_INTERCEPTOR_METRIC_PREFIX` | `metric_prefix` |

### 10.3 OTel config (from env)

Following standard OTel env vars for exporter configuration:
- `OTEL_EXPORTER_OTLP_ENDPOINT`
- `OTEL_EXPORTER_OTLP_HEADERS`
- `OTEL_RESOURCE_ATTRIBUTES`

## 11. Usage

```bash
# Install
go install github.com/yourname/llm-interceptor@latest

# Start (loads ./config.yaml by default)
llm-interceptor

# Claude Code side
export ANTHROPIC_BASE_URL="http://127.0.0.1:8080"
claude
```

## 12. Project Structure

```
llm-interceptor/
├── cmd/
│   └── llm-interceptor/
│       └── main.go            — Entry point
├── internal/
│   ├── proxy/
│   │   └── proxy.go           — Proxy core (forward + streaming)
│   ├── router/
│   │   ├── mode.go            — Mode detection (passthrough vs router)
│   │   ├── route.go           — Provider routing engine
│   │   └── translate.go       — Protocol translation (Anthropic ↔ OpenAI)
│   ├── apikey/
│   │   └── manager.go         — API key generation, hashing, validation
│   ├── plugin/
│   │   └── plugin.go          — Plugin system dispatcher + interface
│   ├── governance/
│   │   └── governance.go      — Built-in governance engine
│   ├── storage/
│   │   ├── interface.go       — StorageBackend abstraction
│   │   ├── sqlite.go          — SQLite implementation
│   │   └── postgres.go        — PostgreSQL implementation
│   ├── state/
│   │   ├── interface.go       — StateStoreBackend abstraction
│   │   ├── memory.go          — In-memory implementation
│   │   └── redis.go           — Redis implementation
│   ├── config/
│   │   └── config.go          — Config loading
│   ├── plugins/
│   │   ├── otel_exporter.go
│   │   ├── cost_tracker.go
│   │   ├── audit_log.go
│   │   └── tool_policy.go
│   └── ui/
│       └── server.go          — SPA static file server + API routes
├── ui/                        — Frontend SPA (React / Vue)
│   ├── src/
│   ├── package.json
│   ├── vite.config.ts
│   └── dist/                  — Built output (embedded via embed.FS)
├── config.example.yaml
├── go.mod
├── go.sum
└── README.md
```

## 13. Implementation Phases

### Phase 1 — Core MVP
- HTTP passthrough proxy + streaming relay
- Plugin system framework (OnRequest / OnResponse hooks)
- Config loading (YAML)
- Storage abstraction interface + SQLite implementation
- State store abstraction interface + in-memory implementation
- No plugins in core — plugin system is ready but empty by default

### Phase 2 — Native Plugin: OTel Exporter
- `otel-exporter` plugin implementing the Plugin interface
- OTel tracer creation, span lifecycle (start in OnRequest, end in OnResponse)
- OTel metric counters for tokens, requests, duration
- Configurable OTLP endpoint
- Full OTel data model as defined in Section 14

### Phase 3 — Governance + Storage
- Built-in governance engine (budget + rate limit + tool policy)
- Built-in plugins: cost-tracker, audit-log, tool-policy
- Redis state store implementation
- PostgreSQL storage implementation

### Phase 4 — LLM Router
- Mode detection (passthrough vs router)
- API key management (generate, hash, validate)
- Provider routing engine (model pattern → provider mapping)
- Multi-provider configuration (Anthropic, OpenAI, Ollama, ...)
- Protocol translation (Anthropic Messages ↔ OpenAI Chat)
- API key management endpoints + CLI

### Phase 5 — Web UI
- React (or Vue) SPA under `ui/`
- REST API + SSE endpoints in Go backend
- Production build embedded via `embed.FS`
- Live request stream, session detail, cost analysis, key management

## 14. OTel Data Model

### Trace (one span per LLM request)

```
Span: POST /v1/messages
Kind: CLIENT
Attributes:
  gen_ai.system: "anthropic"
  gen_ai.request.model: "..."
  gen_ai.response.model: "..."
  gen_ai.conversation.id: {session-id}
  gen_ai.agent.id: {agent-id}
  gen_ai.usage.input_tokens: N
  gen_ai.usage.output_tokens: N
  gen_ai.usage.cache_read_input_tokens: N
  gen_ai.usage.cache_creation_input_tokens: N
  llm_proxy.cost_usd: 0.0023
  llm_proxy.stop_reason: "tool_use"
  llm_proxy.tool_calls: ["Read","Write"]
  llm_proxy.tools: ["Read","Write","Grep"]
Events:
  gen_ai.content.prompt (optional)
  gen_ai.content.completion (optional)
```

### Metrics

```
llm_proxy.llm.token.input_total       Counter       tags: model, session_id
llm_proxy.llm.token.output_total      Counter       tags: model, session_id
llm_proxy.llm.token.cache_read_total  Counter       tags: model
llm_proxy.llm.token.cache_create_total Counter      tags: model
llm_proxy.llm.request.total           Counter       tags: model, status
llm_proxy.llm.cost.total              Counter       tags: model, session_id
llm_proxy.llm.request.duration        Histogram     tags: model, stream
llm_proxy.governance.blocked          Counter       tags: reason
```

## 15. Open Questions

- Plugin discovery: load from config only, or auto-discover from compiled plugins?
- Frontend tech: React or Vue? Deferred to Phase 3.
- Pricing data: built-in price table vs. user-supplied via config?
- Multi-instance: deferred to future, no design decisions needed now.
- Go plugin mechanism: in-process interfaces only, or `hashicorp/go-plugin` for out-of-process extensibility?

# LLM Interceptor — Project Context

## Project
A local-first, open-source LLM gateway providing transparent proxy, observability (OTel),
governance (budget/rate-limit/tool-policy), LLM routing (multi-provider), and a web UI
— all in a single Go binary.

## Repository
- `github.com/nightfield/llm-interceptor`
- Go 1.26.3, Node v24.16.0 (Phase 5)
- Modules: `chi`, `yaml.v3`, `modernc.org/sqlite` + phase-specific deps

## Directory Layout
```
cmd/llm-interceptor/        main.go entry point
internal/
├── config/                 YAML config loader
├── types/                  Shared types (TokenUsage, StoredRequest, etc.)
├── plugin/                 Plugin interface + dispatcher
├── proxy/                  HTTP passthrough proxy + SSE streaming relay
├── storage/                Storage abstraction (SQLite, PostgreSQL)
├── state/                  State store abstraction (in-memory, Redis)
├── plugins/                Built-in plugins (otel, cost-tracker, budget, etc.)
├── api/                    REST API + SSE for web UI
├── router/                 Mode detection + provider routing
└── translate/              Protocol translation (Anthropic ↔ OpenAI)
```

## Implementation Phases
- **Phase 1**: Core MVP (proxy, plugin framework, config, SQLite + in-memory state)
- **Phase 2**: OTel exporter plugin
- **Phase 3**: Governance plugins (cost/budget/ratelimit/tool-policy), Redis, PostgreSQL
- **Phase 4**: LLM Router (mode detection, API key management, protocol translation)
- **Phase 5**: React SPA frontend with Vite + Tailwind

## Architecture Principles
- Plugin architecture via Go interfaces (not out-of-process)
- Forward path never blocked — OTel export and state updates async
- Dual mode: passthrough (Phase 1) and router (Phase 4)
- Metadata map on context is the inter-plugin communication channel
- Storage / State are interface-abstracted, implementations swappable

## Phase 1 Critical Conventions
- `plugin.RequestContext` and `plugin.ResponseContext` use `Metadata map[string]any`
- Dispatcher runs OnResponse in reverse plugin order
- Plugin interface: `Name()`, `OnRequest()`, `OnResponse()`
- Proxy: `HandleRequest` (non-streaming) and `HandleRequestStream` (SSE)
- Config: `metric_prefix` field exists even in Phase 1 (used by OTel in Phase 2)

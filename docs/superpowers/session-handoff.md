# Session Handoff — LLM Interceptor

> Prepared: 2026-06-26
> Next action: Execute Phase 1 implementation in a new session

---

## Project Overview

**LLM Interceptor**: A local-first, open-source LLM gateway providing transparent proxy, observability (OTel), governance (budget/rate-limit/tool-policy), LLM routing (multi-provider), and a web UI — all in a single Go binary.

### Core Principles

- Plugin architecture via Go interfaces (not hashcorp/go-plugin)
- Forward path never blocked — OTel export and state updates async
- Dual mode: passthrough (Phase 1) and router (Phase 4)
- Only Anthropic Messages API in Phase 1; protocol translation deferred to Phase 4
- Every phase increments on previous — no rewrites

---

## What's Done (this session)

### Files created

```
docs/superpowers/
├── specs/
│   └── 2026-06-25-llm-interceptor-design.md      — Full design spec (714 lines)
└── plans/
    ├── 2026-06-25-llm-interceptor-phase1.md       — Phase 1: Core MVP (6 tasks, 1522 lines)
    ├── 2026-06-26-llm-interceptor-phase2.md       — Phase 2: OTel Exporter plugin
    ├── 2026-06-26-llm-interceptor-phase3.md       — Phase 3: Governance + Storage
    ├── 2026-06-26-llm-interceptor-phase4.md       — Phase 4: LLM Router
    └── 2026-06-26-llm-interceptor-phase5.md       — Phase 5: Web UI
```

### Key decisions made during session

| Decision | Detail |
|----------|--------|
| **Language** | Go 1.22+ (switched from TypeScript/Bun) |
| **OTel timing** | Moved from Phase 1 → Phase 2 as a native plugin |
| **Module path** | `github.com/chingjustwe/llm-Interceptor` |
| **Project dir** | `~/self/git/llm-interceptor/` |
| **Phase 1 scope** | No plugins in core — plugin system ready but empty |
| **API key routing** | Auto-detect mode by key prefix (`sk-lli-*` = router) |
| **UI tech** | React + Vite + Tailwind (deferred to Phase 5) |
| **Storage** | SQLite (default, CGO-free via modernc.org/sqlite), PostgreSQL (Phase 3) |
| **State** | In-memory (default), Redis (Phase 3) |

---

## Architecture

### Directory structure (Phase 1)

```
llm-interceptor/
├── cmd/
│   └── llm-interceptor/
│       └── main.go                   — Entry point
├── internal/
│   ├── config/config.go              — YAML config loader
│   ├── types/types.go                — Shared types
│   ├── plugin/
│   │   ├── interface.go              — Plugin interface + context types
│   │   └── dispatcher.go             — Plugin hook executor
│   ├── proxy/
│   │   ├── proxy.go                  — HTTP passthrough proxy
│   │   └── streaming.go              — SSE streaming relay
│   ├── storage/
│   │   ├── interface.go              — Storage Backend interface
│   │   └── sqlite.go                 — SQLite implementation
│   └── state/
│       ├── interface.go              — State Store Backend interface
│       └── memory.go                 — In-memory implementation
├── go.mod
├── config.example.yaml
└── README.md
```

### Plugin lifecycle

```
Request → Dispatcher.OnRequest (ordered) → Proxy → Dispatcher.OnResponse (reverse order)
                                                                    ↓
                                                              (OTel, cost, audit in metadata)
```

Key detail: `metadata map[string]any` on context objects is the inter-plugin communication channel. OTel stores span in metadata, cost tracker reads from metadata.

---

## Phase 1 Implementation (the one to execute next)

### Task list

1. **Task 1**: Project scaffold, config, shared types
2. **Task 2**: Plugin system interface and dispatcher (with tests)
3. **Task 3**: Storage + State Store abstractions (SQLite + in-memory)
4. **Task 4**: HTTP proxy core (non-streaming)
5. **Task 5**: Streaming relay (SSE)
6. **Task 6**: Wire everything in main.go

Each task has exact file paths, code skeletons, test cases, build verification, and commit commands.

### Dependencies (from plan)

```bash
go mod init github.com/nightfield/llm-interceptor
go get github.com/go-chi/chi/v5
go get gopkg.in/yaml.v3
go get modernc.org/sqlite
```

No OTel dependencies — those come in Phase 2.

### Repository state

`~/self/git/llm-interceptor/` exists with `go.mod` initialized. No source code written yet. Can `git init` and start.

### Critical conventions from plan

- `plugin.RequestContext` and `plugin.ResponseContext` use `Metadata map[string]any` for cross-plugin data
- Dispatcher runs OnResponse in **reverse** plugin order
- Plugin interface: `Name()`, `OnRequest(*RequestContext) (*HookResult, error)`, `OnResponse(*ResponseContext) error`
- Proxy has two modes: `HandleRequest` (non-streaming) and `HandleRequestStream` (SSE)
- `UsageData` and `ToolCall` types exist in both `plugin` and `proxy` packages (local to each)
- Storage path uses `~` expansion via `config.StoragePath()`
- Config has `metric_prefix` field even in Phase 1 (used by OTel in Phase 2)

---

## Later Phases (quick reference)

| Phase | Key additions | Files |
|-------|--------------|-------|
| **2** | OTel exporter plugin | `internal/plugins/otel.go`, OTel SDK deps |
| **3** | Redis, PostgreSQL, cost/budget/ratelimit/toolpolicy plugins | `internal/state/redis.go`, `internal/storage/postgres.go`, `internal/plugins/*.go` |
| **4** | Router, protocol translation, API key management, CLI | `internal/router/`, `internal/translate/`, `cmd/llm-interceptor-cli/` |
| **5** | React SPA, REST API, SSE broker | `ui/`, `internal/api/`, `embed.FS` |

---

## Useful commands

```bash
# Build
go build ./cmd/llm-interceptor/

# Test
go test ./internal/plugin/ -v
go test ./internal/proxy/ -v

# Run
./llm-interceptor config.yaml
curl http://127.0.0.1:8080/health

# Check types
go vet ./...
```

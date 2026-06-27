# LLM Interceptor

Local-first, open-source LLM gateway. Transparent proxy, observability (OTel), governance (budget/rate-limit/tool-policy), LLM routing (multi-provider), and a web UI — all in a single Go binary.

## Stack
- **Language:** Go 1.26.3
- **HTTP:** `chi/v5`
- **Config:** `yaml.v3`
- **Database:** SQLite (`modernc.org/sqlite`), PostgreSQL (`pgx`)
- **State:** In-memory, Redis
- **Observability:** OpenTelemetry
- **Frontend (Phase 5):** React, Vite, Tailwind CSS, TypeScript

## Directory Layout
```
cmd/llm-interceptor/        main.go
internal/
├── config/                 YAML config loader
├── types/                  Shared types
├── plugin/                 Plugin interface + dispatcher
├── proxy/                  HTTP passthrough proxy + SSE streaming relay
├── storage/                Storage abstraction (SQLite, PostgreSQL)
├── state/                  State store abstraction (in-memory, Redis)
├── plugins/                Built-in plugins
├── api/                    REST API + SSE for web UI
├── router/                 Mode detection + provider routing
└── translate/              Protocol translation
```

## Phases
1. Core MVP: proxy, plugin framework, config, SQLite + in-memory state
2. OTel exporter plugin
3. Governance: cost/budget/ratelimit/tool-policy, Redis, PostgreSQL
4. LLM Router: mode detection, API key management, protocol translation
5. React SPA frontend

## Code Style
- Every file must have a package-level comment explaining its purpose.
- Every exported type, function, and method must have a Go doc comment (`// PackageName ...`, `// TypeName ...`, `// FuncName ...`).
- Non-trivial internal/unexported logic must have inline comments explaining the "why" (not the "what").
- Avoid magic values — name them as constants with comments.
- Comments should be in English.

## Development Workflow
- Every bugfix MUST include one or more tests that reproduce the bug before the fix and pass after it. No exception.
- Before claiming work is complete, run `go build ./... && go vet ./... && go test ./... -v` and confirm all green.
- Commit granularly: one logical change per commit.

## Key Principles
- Plugin architecture via Go interfaces (in-process)
- Forward path never blocked — OTel/state updates async
- Dual mode: passthrough (Phase 1) and router (Phase 4)
- Metadata map on context is inter-plugin communication channel
- Storage/State are interface-abstracted, implementations swappable

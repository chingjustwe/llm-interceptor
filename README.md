# LLM Interceptor

[![Go Version](https://img.shields.io/badge/Go-1.26.3-blue)](https://go.dev/dl/)
[![License](https://img.shields.io/badge/license-MIT-green)](LICENSE)

**LLM Interceptor** is a local-first, open-source LLM gateway. It sits between your application and LLM providers (OpenAI, Anthropic, etc.), providing transparent proxying, observability, governance, and multi-provider routing — all in a single Go binary.

## Features

- **Transparent Proxy** — Drop-in replacement for any OpenAI-compatible endpoint; works with existing SDKs
- **Streaming Relay** — SSE passthrough with full metadata capture
- **Plugin Architecture** — Extend behavior via Go interfaces (OTel, cost tracking, rate limiting, custom logic)
- **Observability** — OpenTelemetry traces, metrics (token usage, latency, error rates)
- **Governance** — Per-key budget, rate limiting, tool-use policies
- **LLM Router** — Auto-detect provider from API key format, multi-tenant key management, protocol translation (Anthropic ↔ OpenAI)
- **Web UI** — Visual dashboard for requests, sessions, and configuration (Phase 5)
- **Dual Storage** — SQLite (dev/single-node) or PostgreSQL (production)
- **Dual State** — In-memory (dev) or Redis (production)
- **Config-driven** — Single YAML file for all settings

## Architecture

```
┌─────────────┐     ┌──────────────────┐     ┌──────────────┐
│ Application │────▶│ LLM Interceptor  │────▶│ LLM Provider │
│ (SDK/HTTP)  │     │  :8080           │     │ (OpenAI/...) │
└─────────────┘     └──────────────────┘     └──────────────┘
                           │
                    ┌──────┴──────┐
                    │  Plugins    │
                    │ (OTel,Cost, │
                    │  RateLimit…)│
                    └──────┬──────┘
                           │
               ┌───────────┴───────────┐
               │                       │
          ┌────┴────┐           ┌──────┴──────┐
          │ Storage │           │    State    │
          │(SQLite  │           │ (In-Memory  │
          │ /PG)    │           │  /Redis)    │
          └─────────┘           └─────────────┘
```

## Quick Start

```bash
# Clone
git clone https://github.com/chingjustwe/llm-Interceptor.git
cd llm-interceptor

# Build
go build -o llm-interceptor ./cmd/llm-interceptor/

# Run with default config (passthrough mode)
./llm-interceptor -config config.yaml

# Send a request (it proxies to OpenAI)
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-..." \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}'
```

## Configuration

See [docs/config.md](docs/config.md) for the full YAML reference.

## Development

```bash
# Run tests
go test ./...

# Run linter
golangci-lint run ./...
```

## Project Status

LLM Interceptor is under active development across five phases:

| Phase | Description | Status |
|-------|-------------|--------|
| 1 | Core MVP (proxy, plugins, SQLite, in-memory state) | Planning |
| 2 | OpenTelemetry exporter plugin | Planning |
| 3 | Governance (budget, rate-limit, tool-policy), Redis, PostgreSQL | Planning |
| 4 | LLM Router, API keys, protocol translation | Planning |
| 5 | React SPA frontend | Planning |

## License

MIT

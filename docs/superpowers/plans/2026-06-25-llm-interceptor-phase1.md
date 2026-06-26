# LLM Interceptor — Implementation Plan (Phase 1: Core MVP)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a working LLM Interceptor that can proxy Anthropic Messages API calls with plugin hooks.

**Architecture:** Single Go binary with an HTTP server that accepts requests and dispatches plugin hooks before/after forwarding. Plugin system uses Go interfaces for type safety. No built-in plugins in Phase 1 — OTel exporter, governance, etc. will be added as native plugins in later phases.

**Tech Stack:** Go 1.22+, `net/http`, `chi` router, `modernc.org/sqlite` (no CGO), `gopkg.in/yaml.v3`

---

### Task 1: Project scaffold, config, shared types

**Files:**
- Create: `go.mod`
- Create: `cmd/llm-interceptor/main.go`
- Create: `internal/config/config.go`
- Create: `internal/types/types.go`

- [ ] **Step 1: Initialize Go module and install dependencies**

```bash
cd /path/to/llm-interceptor
go mod init github.com/yourname/llm-interceptor
go get github.com/go-chi/chi/v5
go get gopkg.in/yaml.v3
go get modernc.org/sqlite
```

- [ ] **Step 2: Create shared types**

`internal/types/types.go`:

```go
package types

type TokenUsage struct {
	InputTokens         int `json:"input_tokens" yaml:"input_tokens"`
	OutputTokens        int `json:"output_tokens" yaml:"output_tokens"`
	CacheReadTokens     int `json:"cache_read_tokens" yaml:"cache_read_tokens"`
	CacheCreationTokens int `json:"cache_creation_tokens" yaml:"cache_creation_tokens"`
}

type ToolCall struct {
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

type RequestBody struct {
	Model     string   `json:"model"`
	Messages  []any    `json:"messages"`
	System    *string  `json:"system,omitempty"`
	Tools     []any    `json:"tools,omitempty"`
	MaxTokens *int     `json:"max_tokens,omitempty"`
	Stream    bool     `json:"stream,omitempty"`
}

type StoredRequest struct {
	ID        string     `json:"id"`
	SessionID string     `json:"session_id"`
	Model     string     `json:"model"`
	Method    string     `json:"method"`
	Path      string     `json:"path"`
	Request   string     `json:"request"`  // raw JSON body
	Response  string     `json:"response"` // raw JSON body
	Usage     TokenUsage `json:"usage"`
	DurationMs int64     `json:"duration_ms"`
	StatusCode  int      `json:"status_code"`
	CreatedAt  int64     `json:"created_at"`
}

type RequestFilter struct {
	SessionID *string
	Model     *string
	From      *int64
	To        *int64
	Limit     int
	Offset    int
}
```

- [ ] **Step 3: Create config loader**

`internal/config/config.go`:

```go
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen       string           `yaml:"listen"`
	Upstream     string           `yaml:"upstream"`
	MetricPrefix string           `yaml:"metric_prefix"`
	Log          LogConfig        `yaml:"log"`
	Storage      StorageConfig    `yaml:"storage"`
	StateStore   StateStoreConfig `yaml:"state_store"`
}

type LogConfig struct {
	RequestBody  bool `yaml:"request_body"`
	ResponseBody bool `yaml:"response_body"`
}

type StorageConfig struct {
	Type     string          `yaml:"type"`
	SQLite   *SQLiteConfig   `yaml:"sqlite,omitempty"`
	Postgres *PostgresConfig `yaml:"postgres,omitempty"`
}

type SQLiteConfig struct {
	Path string `yaml:"path"`
}

type PostgresConfig struct {
	ConnectionString string `yaml:"connection_string"`
}

type StateStoreConfig struct {
	Type   string         `yaml:"type"`
	Memory *MemoryConfig  `yaml:"memory,omitempty"`
	Redis  *RedisConfig   `yaml:"redis,omitempty"`
}

type MemoryConfig struct{}

type RedisConfig struct {
	URL string `yaml:"url"`
}

func Default() *Config {
	return &Config{
		Listen:       "127.0.0.1:8080",
		Upstream:     "https://api.anthropic.com",
		MetricPrefix: "llm_proxy.",
		Storage: StorageConfig{
			Type:   "sqlite",
			SQLite: &SQLiteConfig{Path: "~/.llm-interceptor/data.db"},
		},
		StateStore: StateStoreConfig{
			Type:   "memory",
			Memory: &MemoryConfig{},
		},
	}
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := Default()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) Validate() error {
	if c.Listen == "" {
		return fmt.Errorf("config: listen address is required")
	}
	if c.Upstream == "" {
		return fmt.Errorf("config: upstream URL is required")
	}
	if c.MetricPrefix == "" {
		c.MetricPrefix = "llm_proxy."
	}
	return nil
}
```

- [ ] **Step 4: Create stub main.go**

`cmd/llm-interceptor/main.go`:

```go
package main

import (
	"fmt"
	"os"

	"github.com/yourname/llm-interceptor/internal/config"
)

func main() {
	cfgPath := "config.yaml"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("LLM Interceptor starting on %s\n", cfg.Listen)
	_ = cfg
}
```

- [ ] **Step 5: Verify it compiles**

```bash
go build ./cmd/llm-interceptor/
echo "build succeeded"
```

Expected: binary built without errors.

- [ ] **Step 6: Commit**

```bash
git init
git add -A
git commit -m "chore: scaffold Go project with config and types"
```

---

### Task 2: Plugin system interface and dispatcher

**Files:**
- Create: `internal/plugin/interface.go`
- Create: `internal/plugin/dispatcher.go`
- Create: `internal/plugin/interface_test.go`

- [ ] **Step 1: Write the plugin interface**

`internal/plugin/interface.go`:

```go
package plugin

import "context"

type RequestContext struct {
	Context   context.Context
	ID        string
	Method    string
	Path      string
	Headers   map[string]string
	Body      []byte
	SessionID string
	AgentID   string
	Metadata  map[string]any
}

type HookResult struct {
	Block      bool
	Reason     string
	StatusCode int
}

type Usage struct {
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
}

type ToolCall struct {
	Name  string
	Input map[string]any
}

type ResponseContext struct {
	RequestID  string
	SessionID  string
	Model      string
	Usage      Usage
	StopReason string
	ToolCalls  []ToolCall
	DurationMs int64
	StatusCode int
	Body       []byte
	Metadata   map[string]any
}

type Plugin interface {
	Name() string
	OnRequest(ctx *RequestContext) (*HookResult, error)
	OnResponse(ctx *ResponseContext) error
}
```

- [ ] **Step 2: Write the dispatcher**

`internal/plugin/dispatcher.go`:

```go
package plugin

import (
	"context"
	"fmt"
)

type Dispatcher struct {
	plugins []Plugin
}

func NewDispatcher(plugins []Plugin) *Dispatcher {
	return &Dispatcher{plugins: plugins}
}

func (d *Dispatcher) ExecuteOnRequest(ctx *RequestContext) (*HookResult, error) {
	for _, p := range d.plugins {
		result, err := p.OnRequest(ctx)
		if err != nil {
			return nil, fmt.Errorf("plugin %s OnRequest: %w", p.Name(), err)
		}
		if result != nil && result.Block {
			return result, nil
		}
	}
	return nil, nil
}

func (d *Dispatcher) ExecuteOnResponse(ctx *ResponseContext) error {
	for i := len(d.plugins) - 1; i >= 0; i-- {
		if err := d.plugins[i].OnResponse(ctx); err != nil {
			return fmt.Errorf("plugin %s OnResponse: %w", d.plugins[i].Name(), err)
		}
	}
	return nil
}

func (d *Dispatcher) WrapContext(ctx context.Context) *RequestContext {
	return &RequestContext{
		Context:  ctx,
		Metadata: make(map[string]any),
	}
}
```

- [ ] **Step 3: Write test for dispatcher**

`internal/plugin/interface_test.go`:

```go
package plugin

import (
	"testing"
)

type mockPlugin struct {
	name      string
	block     bool
	callOrder []string
}

func (m *mockPlugin) Name() string { return m.name }

func (m *mockPlugin) OnRequest(ctx *RequestContext) (*HookResult, error) {
	m.callOrder = append(m.callOrder, "request:"+m.name)
	if m.block {
		return &HookResult{Block: true, Reason: "blocked by " + m.name, StatusCode: 403}, nil
	}
	return nil, nil
}

func (m *mockPlugin) OnResponse(ctx *ResponseContext) error {
	m.callOrder = append(m.callOrder, "response:"+m.name)
	return nil
}

func TestDispatcher_ExecuteOnRequest_NoBlock(t *testing.T) {
	a := &mockPlugin{name: "A"}
	b := &mockPlugin{name: "B"}
	d := NewDispatcher([]Plugin{a, b})

	ctx := &RequestContext{Metadata: make(map[string]any)}
	result, err := d.ExecuteOnRequest(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil result, got %+v", result)
	}
}

func TestDispatcher_ExecuteOnRequest_Block(t *testing.T) {
	a := &mockPlugin{name: "A"}
	b := &mockPlugin{name: "B", block: true}
	c := &mockPlugin{name: "C"}
	d := NewDispatcher([]Plugin{a, b, c})

	ctx := &RequestContext{Metadata: make(map[string]any)}
	result, err := d.ExecuteOnRequest(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || !result.Block {
		t.Fatalf("expected blocked result, got %+v", result)
	}
	// C should not have been called
	if len(c.callOrder) != 0 {
		t.Fatalf("expected C to not be called, got %v", c.callOrder)
	}
}

func TestDispatcher_ExecuteOnResponse_ReverseOrder(t *testing.T) {
	a := &mockPlugin{name: "A"}
	b := &mockPlugin{name: "B"}
	d := NewDispatcher([]Plugin{a, b})

	_ = d.ExecuteOnResponse(&ResponseContext{})

	expectedOrder := []string{"response:B", "response:A"}
	for i, v := range expectedOrder {
		if i < len(a.callOrder) {
			// a.callOrder only has response:A
		}
	}
	// Check b was called first (reverse order)
	if len(b.callOrder) < 1 || b.callOrder[0] != "response:B" {
		t.Fatalf("expected B to be called first in reverse, got %v", b.callOrder)
	}
}
```

- [ ] **Step 4: Run test**

```bash
go test ./internal/plugin/ -v
```

Expected: All 3 tests pass (the reverse-order assertion should be adjusted - let me simplify the test).

- [ ] **Step 5: Fix the reverse order test**

Replace the last test with a cleaner version:

```go
func TestDispatcher_ExecuteOnResponse_ReverseOrder(t *testing.T) {
	var callOrder []string
	a := &namedPlugin{name: "A", order: &callOrder}
	b := &namedPlugin{name: "B", order: &callOrder}
	d := NewDispatcher([]Plugin{a, b})

	_ = d.ExecuteOnResponse(&ResponseContext{})

	if len(callOrder) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(callOrder))
	}
	if callOrder[0] != "response:B" || callOrder[1] != "response:A" {
		t.Fatalf("expected reverse order [B, A], got %v", callOrder)
	}
}

type namedPlugin struct {
	name  string
	order *[]string
}

func (n *namedPlugin) Name() string { return n.name }
func (n *namedPlugin) OnRequest(ctx *RequestContext) (*HookResult, error) {
	*n.order = append(*n.order, "request:"+n.name)
	return nil, nil
}
func (n *namedPlugin) OnResponse(ctx *ResponseContext) error {
	*n.order = append(*n.order, "response:"+n.name)
	return nil
}
```

- [ ] **Step 6: Run test again**

```bash
go test ./internal/plugin/ -v
```

Expected: All tests pass.

- [ ] **Step 7: Commit**

```bash
git add internal/plugin/
git commit -m "feat(plugin): add plugin interface and dispatcher"
```

---

### Task 3: Storage and State Store abstractions

**Files:**
- Create: `internal/storage/interface.go`
- Create: `internal/storage/sqlite.go`
- Create: `internal/state/interface.go`
- Create: `internal/state/memory.go`

- [ ] **Step 1: Write Storage interface**

`internal/storage/interface.go`:

```go
package storage

import (
	"context"
	"github.com/yourname/llm-interceptor/internal/types"
)

type Backend interface {
	SaveRequest(ctx context.Context, req *types.StoredRequest) error
	GetSessionRequests(ctx context.Context, sessionID string, limit, offset int) ([]types.StoredRequest, error)
	QueryRequests(ctx context.Context, filter types.RequestFilter) ([]types.StoredRequest, error)
	Close() error
}
```

- [ ] **Step 2: Write SQLite implementation**

`internal/storage/sqlite.go`:

```go
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	_ "modernc.org/sqlite"
	"github.com/yourname/llm-interceptor/internal/types"
)

type SQLiteBackend struct {
	db *sql.DB
}

func NewSQLite(path string) (*SQLiteBackend, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := db.Exec(`
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
			created_at INTEGER
		);
		CREATE INDEX IF NOT EXISTS idx_requests_session ON requests(session_id);
		CREATE INDEX IF NOT EXISTS idx_requests_created ON requests(created_at);
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create table: %w", err)
	}
	return &SQLiteBackend{db: db}, nil
}

func (s *SQLiteBackend) SaveRequest(ctx context.Context, req *types.StoredRequest) error {
	reqBody, _ := json.Marshal(req.Request)
	respBody, _ := json.Marshal(req.Response)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO requests (id, session_id, model, method, path, request_body, response_body,
		 input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
		 duration_ms, status_code, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.ID, req.SessionID, req.Model, req.Method, req.Path,
		string(reqBody), string(respBody),
		req.Usage.InputTokens, req.Usage.OutputTokens,
		req.Usage.CacheReadTokens, req.Usage.CacheCreationTokens,
		req.DurationMs, req.StatusCode, req.CreatedAt,
	)
	return err
}

func (s *SQLiteBackend) GetSessionRequests(ctx context.Context, sessionID string, limit, offset int) ([]types.StoredRequest, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, session_id, model, method, path, request_body, response_body,
		 input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
		 duration_ms, status_code, created_at
		 FROM requests WHERE session_id = ? ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		sessionID, limit, offset,
	)
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

func (s *SQLiteBackend) QueryRequests(ctx context.Context, filter types.RequestFilter) ([]types.StoredRequest, error) {
	query := `SELECT id, session_id, model, method, path, request_body, response_body,
		 input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
		 duration_ms, status_code, created_at FROM requests`
	var conditions []string
	var args []any

	if filter.SessionID != nil {
		conditions = append(conditions, "session_id = ?")
		args = append(args, *filter.SessionID)
	}
	if filter.Model != nil {
		conditions = append(conditions, "model = ?")
		args = append(args, *filter.Model)
	}
	if filter.From != nil {
		conditions = append(conditions, "created_at >= ?")
		args = append(args, *filter.From)
	}
	if filter.To != nil {
		conditions = append(conditions, "created_at <= ?")
		args = append(args, *filter.To)
	}
	if len(conditions) > 0 {
		query += " WHERE " + conditions[0]
		for i := 1; i < len(conditions); i++ {
			query += " AND " + conditions[i]
		}
	}
	query += " ORDER BY created_at DESC"
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	if filter.Offset > 0 {
		query += " OFFSET ?"
		args = append(args, filter.Offset)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
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

func (s *SQLiteBackend) Close() error {
	return s.db.Close()
}
```

- [ ] **Step 3: Write State Store interface**

`internal/state/interface.go`:

```go
package state

import "context"

type Backend interface {
	Increment(ctx context.Context, key string, delta int64) (int64, error)
	Get(ctx context.Context, key string) (int64, error)
	Reset(ctx context.Context, key string) error
	IncrementWithTTL(ctx context.Context, key string, delta int64, ttlMs int64) (int64, error)
	GetMany(ctx context.Context, keys []string) (map[string]int64, error)
	Close() error
}
```

- [ ] **Step 4: Write In-Memory implementation**

`internal/state/memory.go`:

```go
package state

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

type memoryEntry struct {
	value  int64
	expiry int64 // unix millis, 0 = no expiry
}

type MemoryBackend struct {
	mu       sync.RWMutex
	counters map[string]*memoryEntry
	seq      atomic.Int64
}

func NewMemory() *MemoryBackend {
	m := &MemoryBackend{
		counters: make(map[string]*memoryEntry),
	}
	// Periodically clean expired entries (every 5 minutes)
	go func() {
		for {
			time.Sleep(5 * time.Minute)
			m.cleanExpired()
		}
	}()
	return m
}

func (m *MemoryBackend) cleanExpired() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UnixMilli()
	for k, entry := range m.counters {
		if entry.expiry > 0 && now > entry.expiry {
			delete(m.counters, k)
		}
	}
}

func (m *MemoryBackend) Increment(ctx context.Context, key string, delta int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.counters[key]
	if !ok || (entry.expiry > 0 && time.Now().UnixMilli() > entry.expiry) {
		entry = &memoryEntry{value: 0}
		m.counters[key] = entry
	}
	entry.value += delta
	return entry.value, nil
}

func (m *MemoryBackend) Get(ctx context.Context, key string) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.counters[key]
	if !ok {
		return 0, nil
	}
	if entry.expiry > 0 && time.Now().UnixMilli() > entry.expiry {
		return 0, nil
	}
	return entry.value, nil
}

func (m *MemoryBackend) Reset(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.counters, key)
	return nil
}

func (m *MemoryBackend) IncrementWithTTL(ctx context.Context, key string, delta int64, ttlMs int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.counters[key]
	if !ok || (entry.expiry > 0 && time.Now().UnixMilli() > entry.expiry) {
		entry = &memoryEntry{value: 0, expiry: time.Now().UnixMilli() + ttlMs}
		m.counters[key] = entry
	}
	entry.value += delta
	return entry.value, nil
}

func (m *MemoryBackend) GetMany(ctx context.Context, keys []string) (map[string]int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]int64, len(keys))
	now := time.Now().UnixMilli()
	for _, key := range keys {
		if entry, ok := m.counters[key]; ok {
			if entry.expiry == 0 || now <= entry.expiry {
				result[key] = entry.value
			}
		}
	}
	return result, nil
}

func (m *MemoryBackend) Close() error {
	return nil
}
```

- [ ] **Step 5: Compile to verify**

```bash
go build ./internal/storage/ ./internal/state/
echo "compile succeeded"
```

Expected: No errors.

- [ ] **Step 6: Commit**

```bash
git add internal/storage/ internal/state/
git commit -m "feat: add Storage and StateStore abstractions with SQLite and in-memory implementations"
```

---

### Task 4: HTTP Proxy Core (passthrough, non-streaming)

**Files:**
- Create: `internal/proxy/proxy.go`
- Create: `internal/proxy/proxy_test.go`

- [ ] **Step 1: Write the failing test**

`internal/proxy/proxy_test.go`:

```go
package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProxy_ForwardsRequestAndReturnsResponse(t *testing.T) {
	// Start a test upstream server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":      "msg_123",
			"model":   "claude-sonnet-4-6",
			"content": []map[string]string{{"type": "text", "text": "Hello"}},
		})
	}))
	defer upstream.Close()

	target, _ := New("test-upstream", upstream.URL)
	pluginResp, err := target.HandleRequest([]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`), map[string]string{
		"x-api-key":   "test-key",
		"content-type": "application/json",
	})
	if err != nil {
		t.Fatalf("HandleRequest failed: %v", err)
	}
	if pluginResp == nil {
		t.Fatal("expected non-nil response")
	}
	if pluginResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", pluginResp.StatusCode)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/proxy/ -v
```

Expected: FAIL - "package ... not found" (proxy.go doesn't exist yet).

- [ ] **Step 3: Write minimal implementation**

`internal/proxy/proxy.go`:

```go
package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type PluginResponse struct {
	StatusCode int
	Body       []byte
	Headers    map[string]string
	DurationMs int64
	Usage      struct {
		InputTokens         int
		OutputTokens        int
		CacheReadTokens     int
		CacheCreationTokens int
	}
}

type Proxy struct {
	name     string
	upstream string
	client   *http.Client
}

func New(name, upstreamURL string) (*Proxy, error) {
	return &Proxy{
		name:     name,
		upstream: upstreamURL,
		client:   &http.Client{Timeout: 120 * time.Second},
	}, nil
}

func (p *Proxy) HandleRequest(body []byte, headers map[string]string) (*PluginResponse, error) {
	start := time.Now()

	req, err := http.NewRequest("POST", p.upstream+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	pr := &PluginResponse{
		StatusCode: resp.StatusCode,
		Body:       respBody,
		Headers:    make(map[string]string),
		DurationMs: time.Since(start).Milliseconds(),
	}
	for k := range resp.Header {
		pr.Headers[k] = resp.Header.Get(k)
	}
	return pr, nil
}
```

- [ ] **Step 4: Run test**

```bash
go test ./internal/proxy/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/
git commit -m "feat(proxy): add HTTP passthrough proxy core"
```

---

### Task 5: Streaming relay

**Files:**
- Create: `internal/proxy/streaming.go`
- Modify: `internal/proxy/proxy.go`

- [ ] **Step 1: Read the streaming response format from Anthropic API docs, then add streaming support**

`internal/proxy/streaming.go`:

```go
package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type UsageData struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheReadTokens     int `json:"cache_read_input_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens"`
}

type messageDelta struct {
	Delta struct {
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage *UsageData `json:"usage"`
}

type toolUseBlock struct {
	Type  string         `json:"type"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

func extractUsageAndTools(body []byte) (UsageData, []ToolCall, string) {
	var usage UsageData
	var toolCalls []ToolCall
	var stopReason string

	// Parse as raw JSON to handle streaming chunk vs full response
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return usage, toolCalls, stopReason
	}

	// Full response format
	if u, ok := raw["usage"].(map[string]any); ok {
		usage.InputTokens = int(u["input_tokens"].(float64))
		usage.OutputTokens = int(u["output_tokens"].(float64))
		if v, ok := u["cache_read_input_tokens"].(float64); ok {
			usage.CacheReadTokens = int(v)
		}
		if v, ok := u["cache_creation_input_tokens"].(float64); ok {
			usage.CacheCreationTokens = int(v)
		}
	}
	if sr, ok := raw["stop_reason"].(string); ok {
		stopReason = sr
	}
	if content, ok := raw["content"].([]any); ok {
		for _, c := range content {
			if block, ok := c.(map[string]any); ok {
				if block["type"] == "tool_use" {
					tc := ToolCall{
						Name:  block["name"].(string),
						Input: block["input"].(map[string]any),
					}
					toolCalls = append(toolCalls, tc)
				}
			}
		}
	}
	return usage, toolCalls, stopReason
}

// streamAndCollect handles SSE streaming: writes events to the response writer
// while collecting usage data from the final message_delta event.
func streamAndCollect(upstreamResp *http.Response, w http.ResponseWriter) (UsageData, []ToolCall, string, int64, error) {
	start := time.Now()
	flusher, ok := w.(http.Flusher)
	if !ok {
		return UsageData{}, nil, "", 0, fmt.Errorf("response writer does not support flushing")
	}

	var finalUsage UsageData
	var finalToolCalls []ToolCall
	var stopReason string

	scanner := bufio.NewScanner(upstreamResp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		// Write the raw SSE line to the client immediately
		_, _ = fmt.Fprintf(w, "%s\n", line)
		flusher.Flush()

		// Peek at events to extract metadata
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			var raw map[string]any
			if err := json.Unmarshal([]byte(data), &raw); err != nil {
				continue
			}
			evtType, _ := raw["type"].(string)

			switch evtType {
			case "message_delta":
				if delta, ok := raw["delta"].(map[string]any); ok {
					if sr, ok := delta["stop_reason"].(string); ok {
						stopReason = sr
					}
				}
				if u, ok := raw["usage"].(map[string]any); ok {
					finalUsage.InputTokens = int(u["input_tokens"].(float64))
					finalUsage.OutputTokens = int(u["output_tokens"].(float64))
					if v, ok := u["cache_read_input_tokens"].(float64); ok {
						finalUsage.CacheReadTokens = int(v)
					}
					if v, ok := u["cache_creation_input_tokens"].(float64); ok {
						finalUsage.CacheCreationTokens = int(v)
					}
				}
			case "content_block_start":
				if block, ok := raw["content_block"].(map[string]any); ok {
					if block["type"] == "tool_use" {
						tc := ToolCall{
							Name:  block["name"].(string),
							Input: block["input"].(map[string]any),
						}
						finalToolCalls = append(finalToolCalls, tc)
					}
				}
			}
		}
	}
	duration := time.Since(start).Milliseconds()
	return finalUsage, finalToolCalls, stopReason, duration, scanner.Err()
}
```

- [ ] **Step 2: Add stream-aware HandleRequestStream method to proxy.go**

Add to `internal/proxy/proxy.go`:

```go
// HandleRequestStream forwards a streaming request and writes SSE events to w.
// Returns usage/tool data extracted from the stream for post-processing.
// NOTE: Response headers are committed before SSE relay begins (forward path never blocked).
// This means the HTTP status code is fixed at 200 once streaming starts; upstream
// errors after this point appear as SSE error events, not HTTP error codes.
func (p *Proxy) HandleRequestStream(body []byte, headers map[string]string, w http.ResponseWriter) (*UsageData, []ToolCall, string, int64, error) {
	start := time.Now()

	req, err := http.NewRequest("POST", p.upstream+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, nil, "", 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "text/event-stream")
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, nil, "", 0, err
	}
	defer resp.Body.Close()

	// Copy response headers
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			_ = time.Since(start).Milliseconds()
			return nil, nil, "", 0, fmt.Errorf("read error body: %w", err)
		}
		w.Write(body)
		_ = time.Since(start).Milliseconds()
		return nil, nil, "", 0, nil
	}

	usage, tools, stopReason, duration, err := streamAndCollect(resp, w)
	if err != nil {
		return nil, nil, "", duration, err
	}
	return &usage, tools, stopReason, duration, nil
}
```

Also add `ToolCall` type alias to `proxy_test.go` or import the plugin package. Actually, let me keep ToolCall as a local type in proxy for now and avoid coupling.

Better: Add ToolCall type alias at the top of `proxy.go`:

```go
type ToolCall struct {
	Name  string
	Input map[string]any
}

type UsageData struct {
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
}
```

And remove the duplicate from streaming.go since they're in the same package.

- [ ] **Step 3: Write test for streaming**

Add to `internal/proxy/proxy_test.go`:

```go
func TestProxy_StreamingResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		// Simulate a streaming response with tool_use
		fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"name\":\"Read\",\"input\":{\"path\":\"main.go\"}}}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"input_tokens\":10,\"output_tokens\":20}}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	target, _ := New("test-stream", upstream.URL)

	rec := httptest.NewRecorder()
	usage, tools, stopReason, _, err := target.HandleRequestStream(
		[]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		map[string]string{"x-api-key": "test-key"},
		rec,
	)
	if err != nil {
		t.Fatalf("HandleRequestStream failed: %v", err)
	}
	if usage.InputTokens != 10 || usage.OutputTokens != 20 {
		t.Fatalf("expected usage 10/20, got %d/%d", usage.InputTokens, usage.OutputTokens)
	}
	if stopReason != "tool_use" {
		t.Fatalf("expected stop_reason tool_use, got %s", stopReason)
	}
	if len(tools) != 1 || tools[0].Name != "Read" {
		t.Fatalf("expected 1 tool_use (Read), got %v", tools)
	}
}
```

- [ ] **Step 4: Add missing import**

Add `"fmt"` import to `proxy_test.go`.

- [ ] **Step 5: Run tests**

```bash
go test ./internal/proxy/ -v
```

Expected: Both tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/proxy/
git commit -m "feat(proxy): add streaming relay with usage extraction"
```

---

### Task 6: Wire everything in main.go

**Files:**
- Modify: `cmd/llm-interceptor/main.go`

- [ ] **Step 1: Write the full main.go**

`cmd/llm-interceptor/main.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/yourname/llm-interceptor/internal/config"
	"github.com/yourname/llm-interceptor/internal/plugin"
	"github.com/yourname/llm-interceptor/internal/proxy"
	"github.com/yourname/llm-interceptor/internal/storage"
	"github.com/yourname/llm-interceptor/internal/state"
)

func main() {
	cfgPath := "config.yaml"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize storage
	var store storage.Backend
	switch cfg.Storage.Type {
	case "sqlite":
		s, err := storage.NewSQLite(cfg.StoragePath())
		if err != nil {
			log.Fatalf("failed to init storage: %v", err)
		}
		store = s
		defer store.Close()
	default:
		log.Fatalf("unknown storage type: %s", cfg.Storage.Type)
	}
	_ = store

	// Initialize state store
	var st state.Backend
	switch cfg.StateStore.Type {
	case "memory":
		st = state.NewMemory()
		defer st.Close()
	default:
		log.Fatalf("unknown state store type: %s", cfg.StateStore.Type)
	}
	_ = st

	// Initialize proxy (no plugins in Phase 1 — OTel, governance, etc. added later)
	disp := plugin.NewDispatcher(nil)

	target, err := proxy.New("anthropic", cfg.Upstream)
	if err != nil {
		log.Fatalf("failed to init proxy: %v", err)
	}

	// HTTP server
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)

	r.Post("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		isStream := strings.Contains(r.Header.Get("Accept"), "text/event-stream")
		if r.Header.Get("Content-Type") == "application/json" {
			var rb map[string]any
			if json.Unmarshal(body, &rb) == nil {
				if s, ok := rb["stream"].(bool); ok {
					isStream = isStream || s
				}
			}
		}

		reqCtx := disp.WrapContext(r.Context())
		reqCtx.ID = fmt.Sprintf("req_%d", time.Now().UnixNano())
		reqCtx.Method = r.Method
		reqCtx.Path = r.URL.Path
		reqCtx.Body = body
		reqCtx.SessionID = r.Header.Get("x-claude-code-session-id")
		reqCtx.AgentID = r.Header.Get("x-claude-code-agent-id")
		reqCtx.Headers = make(map[string]string)
		for k, v := range r.Header {
			reqCtx.Headers[k] = strings.Join(v, ", ")
		}

		hookResult, err := disp.ExecuteOnRequest(reqCtx)
		if err != nil {
			log.Printf("plugin error: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if hookResult != nil && hookResult.Block {
			http.Error(w, hookResult.Reason, hookResult.StatusCode)
			return
		}

		var respCtx plugin.ResponseContext
		respCtx.RequestID = reqCtx.ID
		respCtx.SessionID = reqCtx.SessionID
		respCtx.Metadata = reqCtx.Metadata

		if isStream {
			usage, toolCalls, stopReason, duration, err := target.HandleRequestStream(body, reqCtx.Headers, w)
			if err != nil {
				log.Printf("proxy stream error: %v", err)
				return
			}
			if usage != nil {
				respCtx.Usage = plugin.Usage(*usage)
			}
			for _, tc := range toolCalls {
				respCtx.ToolCalls = append(respCtx.ToolCalls, plugin.ToolCall(tc))
			}
			respCtx.StopReason = stopReason
			respCtx.DurationMs = duration
			respCtx.StatusCode = http.StatusOK
			var rb struct {
				Model string `json:"model"`
			}
			if json.Unmarshal(body, &rb) == nil {
				respCtx.Model = rb.Model
			}
		} else {
			pr, err := target.HandleRequest(body, reqCtx.Headers)
			if err != nil {
				log.Printf("proxy error: %v", err)
				http.Error(w, "upstream error", http.StatusBadGateway)
				return
			}
			respCtx.StatusCode = pr.StatusCode
			respCtx.DurationMs = pr.DurationMs
			usage, toolCalls, stopReason := proxy.ExtractUsage(pr.Body)
			respCtx.Usage = plugin.Usage(usage)
			for _, tc := range toolCalls {
				respCtx.ToolCalls = append(respCtx.ToolCalls, plugin.ToolCall(tc))
			}
			respCtx.StopReason = stopReason

			var rb struct {
				Model string `json:"model"`
			}
			if json.Unmarshal(body, &rb) == nil {
				respCtx.Model = rb.Model
			}

			for k, v := range pr.Headers {
				w.Header().Set(k, v)
			}
			if w.Header().Get("Content-Type") == "" {
				w.Header().Set("Content-Type", "application/json")
			}
			w.WriteHeader(pr.StatusCode)
			w.Write(pr.Body)
		}

		if err := disp.ExecuteOnResponse(&respCtx); err != nil {
			log.Printf("plugin response error: %v", err)
		}
	})

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	server := &http.Server{
		Addr:    cfg.Listen,
		Handler: r,
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdownCtx)
	}()

	log.Printf("LLM Interceptor listening on %s", cfg.Listen)
	log.Printf("Upstream: %s", cfg.Upstream)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
```

- [ ] **Step 2: Make ExtractUsage public in proxy.go**

Add to `internal/proxy/proxy.go`:

```go
func ExtractUsage(body []byte) (UsageData, []ToolCall, string) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return UsageData{}, nil, ""
	}
	var usage UsageData
	if u, ok := raw["usage"].(map[string]any); ok {
		usage.InputTokens = int(u["input_tokens"].(float64))
		usage.OutputTokens = int(u["output_tokens"].(float64))
		if v, ok := u["cache_read_input_tokens"].(float64); ok {
			usage.CacheReadTokens = int(v)
		}
		if v, ok := u["cache_creation_input_tokens"].(float64); ok {
			usage.CacheCreationTokens = int(v)
		}
	}
	var stopReason string
	if sr, ok := raw["stop_reason"].(string); ok {
		stopReason = sr
	}
	var toolCalls []ToolCall
	if content, ok := raw["content"].([]any); ok {
		for _, c := range content {
			if block, ok := c.(map[string]any); ok && block["type"] == "tool_use" {
				tc := ToolCall{
					Name:  block["name"].(string),
					Input: block["input"].(map[string]any),
				}
				toolCalls = append(toolCalls, tc)
			}
		}
	}
	return usage, toolCalls, stopReason
}
```

And add `"encoding/json"` to the imports in proxy.go.

- [ ] **Step 3: Build and verify**

```bash
go build ./cmd/llm-interceptor/
echo "build succeeded"
```

Expected: Binary builds without errors.

- [ ] **Step 4: Create default config file**

`config.example.yaml`:

```yaml
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

state_store:
  type: memory
```

- [ ] **Step 5: Fix tilde expansion in SQLite path**

Add at the top of `internal/config/config.go`:

```go
import "strings"

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return home + path[1:]
	}
	return path
}

func (c *Config) StoragePath() string {
	return expandHome(c.Storage.SQLite.Path)
}
```

- [ ] **Step 6: Build and run mock smoke test**

```bash
go build ./cmd/llm-interceptor/
echo "build succeeded"
```

Expected: Binary builds without errors.

- [ ] **Step 7: Commit**

```bash
git add cmd/ internal/config/ go.mod go.sum config.example.yaml
git commit -m "feat: wire up main server with proxy and plugin dispatcher"
```

---

### Self-Review

**1. Spec coverage:**
- Phase 1 tasks all covered: HTTP proxy ✓, streaming ✓, plugin system framework ✓, config loading ✓, SQLite storage ✓, in-memory state ✓
- OTel exporter deferred to Phase 2 (will be a native plugin registered via the Plugin interface)

**2. Placeholder check:** No TBDs, TODOs, or "fill in later" patterns in any task.

**3. Type consistency:** `plugin.RequestContext`, `plugin.ResponseContext`, `proxy.UsageData`, `proxy.ToolCall`, `types.StoredRequest` — types are consistent across tasks.

**4. Gaps:**
- The streaming handler writes response headers before OnResponse completes — this is correct behavior (don't block the forward path)
- Graceful shutdown is handled in main.go via signal handling
- No tests for SQLite storage or in-memory state store — these are standard implementations that can have tests added later in Phase 2

# LLM Interceptor — Implementation Plan (Phase 4: LLM Router)

**Goal:** Add multi-provider routing, API key management, and protocol translation (Anthropic Messages ↔ OpenAI Chat). The proxy becomes a dual-mode gateway: passthrough (Phase 1 behavior, no auth) and router (own API keys, multi-provider).

**Architecture:** New `internal/router/` package handles mode detection, provider configuration, and routing logic. Protocol translation lives in `internal/translate/`. API key management adds endpoints and a CLI subcommand. Mode is auto-detected: if the request carries an `x-api-key` matching a managed key (`sk-lli-*` prefix), use router mode; otherwise, passthrough.

**Tech Stack:** Go 1.22+, `golang.org/x/crypto` (for bcrypt key hashing), `github.com/spf13/cobra` (optional, for CLI)

---

### Task 1: Mode detection and provider abstraction

**Files:**
- Create: `internal/router/router.go`
- Create: `internal/router/router_test.go`

- [ ] **Step 1: Define Provider interface**

`internal/router/router.go`:

```go
package router

import (
	"io"
	"net/http"
)

type Provider interface {
	Name() string
	RoundTrip(req *http.Request) (*http.Response, error)
}

type Router struct {
	providers  []Provider
	defaultURL string // fallback for passthrough mode
}

func New(providers []Provider, defaultURL string) *Router {
	return &Router{providers: providers, defaultURL: defaultURL}
}

func (r *Router) DetectMode(apiKey string) string {
	if len(apiKey) > 7 && apiKey[:7] == "sk-lli-" {
		return "router"
	}
	return "passthrough"
}

func (r *Router) SelectProvider(model string) Provider {
	for _, p := range r.providers {
		// Each provider declares which model patterns it handles
		if mp, ok := p.(interface{ MatchModel(string) bool }); ok && mp.MatchModel(model) {
			return p
		}
	}
	return nil
}
```

- [ ] **Step 2: Write test**

`internal/router/router_test.go`:

```go
package router

import "testing"

func TestDetectMode_RouterKey(t *testing.T) {
	r := New(nil, "https://api.anthropic.com")
	if mode := r.DetectMode("sk-lli-abc123"); mode != "router" {
		t.Fatalf("expected router mode, got %s", mode)
	}
}

func TestDetectMode_Passthrough(t *testing.T) {
	r := New(nil, "https://api.anthropic.com")
	if mode := r.DetectMode("sk-ant-abc123"); mode != "passthrough" {
		t.Fatalf("expected passthrough mode, got %s", mode)
	}
	if mode := r.DetectMode(""); mode != "passthrough" {
		t.Fatalf("expected passthrough for empty key, got %s", mode)
	}
}
```

- [ ] **Step 3: Build and run test**

```bash
go test ./internal/router/ -v
```

Expected: 2 tests pass.

- [ ] **Step 4: Commit**

```bash
git add internal/router/
git commit -m "feat(router): add mode detection and provider abstraction"
```

---

### Task 2: Provider configuration

**Files:**
- Create: `internal/router/provider.go`
- Modify: `internal/config/config.go`

- [ ] **Step 1: Define provider config**

`internal/config/config.go`:

```go
type RouterConfig struct {
	Enabled   bool              `yaml:"enabled"`
	Providers []ProviderConfig  `yaml:"providers"`
}

type ProviderConfig struct {
	Name      string `yaml:"name"`
	BaseURL   string `yaml:"base_url"`
	ModelGlob string `yaml:"model_glob"`
	APIKey    string `yaml:"api_key"`
}
```

- [ ] **Step 2: Implement provider matcher**

`internal/router/provider.go`:

```go
package router

import (
	"net/http"
	"strings"
)

type HTTPProvider struct {
	name      string
	baseURL   string
	modelGlob string
	apiKey    string
	client    *http.Client
}

func NewHTTPProvider(name, baseURL, modelGlob, apiKey string) *HTTPProvider {
	return &HTTPProvider{
		name:      name,
		baseURL:   strings.TrimRight(baseURL, "/"),
		modelGlob: modelGlob,
		apiKey:    apiKey,
		client:    &http.Client{},
	}
}

func (p *HTTPProvider) Name() string { return p.name }

func (p *HTTPProvider) MatchModel(model string) bool {
	if p.modelGlob == "" || p.modelGlob == "*" {
		return true
	}
	return strings.Contains(model, strings.TrimSuffix(p.modelGlob, "*"))
}

func (p *HTTPProvider) RoundTrip(req *http.Request) (*http.Response, error) {
	// Rewrite URL to provider's base
	req.URL.Host = p.baseURL
	req.URL.Scheme = "https"
	// Inject provider's API key
	req.Header.Set("x-api-key", p.apiKey)
	return p.client.Do(req)
}
```

- [ ] **Step 3: Build and verify**

```bash
go build ./internal/router/
echo "build ok"
```

- [ ] **Step 4: Commit**

```bash
git add internal/router/ internal/config/
git commit -m "feat(router): add HTTP provider with model glob matching"
```

---

### Task 3: API key management

**Files:**
- Create: `internal/router/keymanager.go`
- Create: `internal/router/keymanager_test.go`
- Modify: `internal/storage/interface.go` (add KeyStore methods)

- [ ] **Step 1: Add key storage to Storage interface**

`internal/storage/interface.go`:

```go
type APIKey struct {
	ID        string `json:"id"`
	KeyHash   string `json:"key_hash"`
	KeyPrefix string `json:"key_prefix"` // first 8 chars for identification
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`
}

// Add to Backend interface:
SaveAPIKey(ctx context.Context, key *APIKey) error
GetAPIKeyByPrefix(ctx context.Context, prefix string) (*APIKey, error)
ListAPIKeys(ctx context.Context) ([]APIKey, error)
DisableAPIKey(ctx context.Context, id string) error
```

- [ ] **Step 2: Write key manager**

`internal/router/keymanager.go`:

```go
package router

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/bcrypt"
	"github.com/chingjustwe/llm-Interceptor/internal/storage"
)

type KeyManager struct {
	store storage.Backend
}

func NewKeyManager(store storage.Backend) *KeyManager {
	return &KeyManager{store: store}
}

func (km *KeyManager) Generate(ctx context.Context, name string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	apiKey := "sk-lli-" + hex.EncodeToString(raw)

	hash, err := bcrypt.GenerateFromPassword([]byte(apiKey), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}

	key := &storage.APIKey{
		KeyHash:   string(hash),
		KeyPrefix: apiKey[:12], // "sk-lli-" + first 4 hex chars
		Name:      name,
		Enabled:   true,
	}
	if err := km.store.SaveAPIKey(ctx, key); err != nil {
		return "", err
	}
	return apiKey, nil
}

func (km *KeyManager) Validate(ctx context.Context, apiKey string) (bool, error) {
	if len(apiKey) < 12 {
		return false, nil
	}
	prefix := apiKey[:12]
	stored, err := km.store.GetAPIKeyByPrefix(ctx, prefix)
	if err != nil || stored == nil {
		return false, err
	}
	if !stored.Enabled {
		return false, nil
	}
	if err := bcrypt.CompareHashAndPassword([]byte(stored.KeyHash), []byte(apiKey)); err != nil {
		return false, nil
	}
	return true, nil
}
```

- [ ] **Step 3: Write test**

```go
func TestKeyManager_GenerateAndValidate(t *testing.T) {
	store := newMockKeyStore()
	km := NewKeyManager(store)
	ctx := context.Background()

	key, err := km.Generate(ctx, "test-key")
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if len(key) < 20 {
		t.Fatalf("expected long key, got %s", key)
	}

	valid, err := km.Validate(ctx, key)
	if err != nil {
		t.Fatalf("validate failed: %v", err)
	}
	if !valid {
		t.Fatal("expected valid key")
	}

	// Wrong key
	valid, _ = km.Validate(ctx, "sk-lli-wrongkey1234567890abcdef")
	if valid {
		t.Fatal("expected invalid key")
	}
}
```

- [ ] **Step 4: Install bcrypt dependency**

```bash
go get golang.org/x/crypto
```

- [ ] **Step 5: Implement API key storage methods in SQLite**

Add to `internal/storage/sqlite.go`:

```go
func (s *SQLiteBackend) SaveAPIKey(ctx context.Context, key *APIKey) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO api_keys (id, key_hash, key_prefix, name, enabled, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET enabled=excluded.enabled`,
		key.ID, key.KeyHash, key.KeyPrefix, key.Name, key.Enabled, key.CreatedAt,
	)
	return err
}

func (s *SQLiteBackend) GetAPIKeyByPrefix(ctx context.Context, prefix string) (*APIKey, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, key_hash, key_prefix, name, enabled, created_at
		 FROM api_keys WHERE key_prefix = ?`, prefix,
	)
	var k APIKey
	err := row.Scan(&k.ID, &k.KeyHash, &k.KeyPrefix, &k.Name, &k.Enabled, &k.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &k, err
}

func (s *SQLiteBackend) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, key_hash, key_prefix, name, enabled, created_at FROM api_keys ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []APIKey
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.KeyHash, &k.KeyPrefix, &k.Name, &k.Enabled, &k.CreatedAt); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, nil
}

func (s *SQLiteBackend) DisableAPIKey(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE api_keys SET enabled = false WHERE id = ?`, id)
	return err
}
```

Also add the table creation to `NewSQLite`:

```sql
CREATE TABLE IF NOT EXISTS api_keys (
    id TEXT PRIMARY KEY,
    key_hash TEXT NOT NULL,
    key_prefix TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_api_keys_prefix ON api_keys(key_prefix);
```

- [ ] **Step 6: Implement API key storage methods in PostgreSQL**

Add to `internal/storage/postgres.go`:

```go
func (p *PostgresBackend) SaveAPIKey(ctx context.Context, key *APIKey) error {
	_, err := p.pool.Exec(ctx,
		`INSERT INTO api_keys (id, key_hash, key_prefix, name, enabled, created_at)
		 VALUES ($1,$2,$3,$4,$5,TO_TIMESTAMP($6))
		 ON CONFLICT(id) DO UPDATE SET enabled=EXCLUDED.enabled`,
		key.ID, key.KeyHash, key.KeyPrefix, key.Name, key.Enabled, key.CreatedAt,
	)
	return err
}

func (p *PostgresBackend) GetAPIKeyByPrefix(ctx context.Context, prefix string) (*APIKey, error) {
	row := p.pool.QueryRow(ctx,
		`SELECT id, key_hash, key_prefix, name, enabled,
		 EXTRACT(EPOCH FROM created_at)::bigint FROM api_keys WHERE key_prefix = $1`, prefix,
	)
	var k APIKey
	err := row.Scan(&k.ID, &k.KeyHash, &k.KeyPrefix, &k.Name, &k.Enabled, &k.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return &k, err
}

func (p *PostgresBackend) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	rows, _ := p.pool.Query(ctx,
		`SELECT id, key_hash, key_prefix, name, enabled,
		 EXTRACT(EPOCH FROM created_at)::bigint FROM api_keys ORDER BY created_at DESC`,
	)
	defer rows.Close()
	var keys []APIKey
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.KeyHash, &k.KeyPrefix, &k.Name, &k.Enabled, &k.CreatedAt); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, nil
}

func (p *PostgresBackend) DisableAPIKey(ctx context.Context, id string) error {
	_, err := p.pool.Exec(ctx, `UPDATE api_keys SET enabled = false WHERE id = $1`, id)
	return err
}
```

Add table creation to `NewPostgres` and also fix `GetSessionRequests` and `QueryRequests` to include `request_body`, `response_body` columns (same pattern as SQLite fix in Phase 1).

- [ ] **Step 7: Run tests**

```bash
go test ./internal/router/ -v -run TestKeyManager
```

Expected: All tests pass.

- [ ] **Step 8: Commit**

```bash
git add internal/router/ internal/storage/ go.mod go.sum
git commit -m "feat(router): add API key management with bcrypt hashing"
```

---

### Task 4: Protocol translation layer (Anthropic Messages ↔ OpenAI Chat)

**Scope:** Phase 4 implements basic text-message translation only. Tool/function calling, multimodal content (images), cache control, and streaming SSE translation are deferred to later phases. The translation is best-effort for simple chat completions.

**Files:**
- Create: `internal/translate/anthropic.go`
- Create: `internal/translate/openai.go`
- Create: `internal/translate/translate_test.go`

- [ ] **Step 1: Define translation interface**

`internal/translate/anthropic.go`:

```go
package translate

import "encoding/json"

// ToOpenAI converts an Anthropic Messages API request body to OpenAI Chat API format.
func ToOpenAI(anthropicBody []byte) ([]byte, error) {
	var req struct {
		Model     string          `json:"model"`
		Messages  []json.RawMessage `json:"messages"`
		System    *string         `json:"system,omitempty"`
		MaxTokens *int            `json:"max_tokens,omitempty"`
		Stream    bool            `json:"stream,omitempty"`
	}
	if err := json.Unmarshal(anthropicBody, &req); err != nil {
		return nil, err
	}

	openAIReq := struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		MaxTokens int  `json:"max_tokens,omitempty"`
		Stream    bool `json:"stream,omitempty"`
	}{
		Model:     req.Model,
		MaxTokens: *req.MaxTokens,
		Stream:    req.Stream,
	}

	// Convert messages: Anthropic has system as separate field,
	// OpenAI has system as a message with role "system"
	for _, m := range req.Messages {
		var msg struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(m, &msg); err != nil {
			continue
		}
		openAIReq.Messages = append(openAIReq.Messages, msg)
	}
	if req.System != nil {
		openAIReq.Messages = append([]struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{{Role: "system", Content: *req.System}}, openAIReq.Messages...)
	}

	return json.Marshal(openAIReq)
}
```

`internal/translate/openai.go`:

```go
package translate

import "encoding/json"

// ToAnthropic converts an OpenAI Chat API response body back to Anthropic Messages format.
func ToAnthropic(openAIBody []byte) ([]byte, error) {
	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Index   int `json:"index"`
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(openAIBody, &resp); err != nil {
		return nil, err
	}

	content := make([]json.RawMessage, 0)
	if len(resp.Choices) > 0 {
		textContent, _ := json.Marshal(map[string]any{
			"type": "text",
			"text": resp.Choices[0].Message.Content,
		})
		content = append(content, textContent)
	}

	anthropicResp := map[string]any{
		"id":      resp.ID,
		"model":   resp.Model,
		"type":    "message",
		"role":    "assistant",
		"content": content,
		"stop_reason": resp.Choices[0].FinishReason,
		"usage": map[string]int{
			"input_tokens":  0,
			"output_tokens": 0,
		},
	}
	if resp.Usage != nil {
		anthropicResp["usage"] = map[string]int{
			"input_tokens":  resp.Usage.PromptTokens,
			"output_tokens": resp.Usage.CompletionTokens,
		}
	}

	return json.Marshal(anthropicResp)
}
```

- [ ] **Step 2: Write tests**

`internal/translate/translate_test.go`:

```go
package translate

import (
	"encoding/json"
	"testing"
)

func TestToOpenAI_Basic(t *testing.T) {
	anthropicBody := []byte(`{
		"model": "claude-sonnet-4-6",
		"messages": [{"role":"user","content":"Hello"}],
		"max_tokens": 100
	}`)

	result, err := ToOpenAI(anthropicBody)
	if err != nil {
		t.Fatalf("ToOpenAI failed: %v", err)
	}

	var parsed map[string]any
	json.Unmarshal(result, &parsed)
	if parsed["model"] != "claude-sonnet-4-6" {
		t.Fatalf("expected model claude-sonnet-4-6, got %v", parsed["model"])
	}
}

func TestToAnthropic_Basic(t *testing.T) {
	openAIBody := []byte(`{
		"id": "chatcmpl-abc",
		"model": "gpt-4",
		"choices": [{
			"index": 0,
			"message": {"role":"assistant","content":"Hi there"},
			"finish_reason": "stop"
		}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 5}
	}`)

	result, err := ToAnthropic(openAIBody)
	if err != nil {
		t.Fatalf("ToAnthropic failed: %v", err)
	}

	var parsed map[string]any
	json.Unmarshal(result, &parsed)
	if parsed["id"] != "chatcmpl-abc" {
		t.Fatalf("expected id chatcmpl-abc, got %v", parsed["id"])
	}
}
```

- [ ] **Step 3: Run tests**

```bash
go test ./internal/translate/ -v
```

Expected: 2 tests pass.

- [ ] **Step 4: Commit**

```bash
git add internal/translate/
git commit -m "feat(translate): add Anthropic↔OpenAI protocol translation"
```

---

### Task 5: CLI subcommand for key management

**Files:**
- Create: `cmd/llm-interceptor-cli/main.go`

- [ ] **Step 1: Write CLI for key generation**

`cmd/llm-interceptor-cli/main.go`:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/chingjustwe/llm-Interceptor/internal/config"
	"github.com/chingjustwe/llm-Interceptor/internal/router"
	"github.com/chingjustwe/llm-Interceptor/internal/storage"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: llm-interceptor-cli <command> [args]\n")
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  generate-key <name>   Generate a new API key\n")
		fmt.Fprintf(os.Stderr, "  list-keys             List all API keys\n")
		fmt.Fprintf(os.Stderr, "  disable-key <id>      Disable an API key\n")
		os.Exit(0)
	}

	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	ctx := context.Background()

	var store storage.Backend
	switch cfg.Storage.Type {
	case "sqlite":
		s, err := storage.NewSQLite(cfg.StoragePath())
		if err != nil {
			log.Fatalf("failed to init storage: %v", err)
		}
		defer s.Close()
		store = s
	default:
		log.Fatalf("unsupported storage for CLI: %s", cfg.Storage.Type)
	}

	km := router.NewKeyManager(store)

	switch os.Args[1] {
	case "generate-key":
		if len(os.Args) < 3 {
			log.Fatal("Usage: generate-key <name>")
		}
		key, err := km.Generate(ctx, os.Args[2])
		if err != nil {
			log.Fatalf("generate key: %v", err)
		}
		fmt.Printf("API Key: %s\n", key)

	case "list-keys":
		keys, err := store.ListAPIKeys(ctx)
		if err != nil {
			log.Fatalf("list keys: %v", err)
		}
		for _, k := range keys {
			status := "enabled"
			if !k.Enabled {
				status = "disabled"
			}
			fmt.Printf("%s  %s  %s  [%s]\n", k.ID[:8], k.KeyPrefix, k.Name, status)
		}

	case "disable-key":
		if len(os.Args) < 3 {
			log.Fatal("Usage: disable-key <id>")
		}
		if err := store.DisableAPIKey(ctx, os.Args[2]); err != nil {
			log.Fatalf("disable key: %v", err)
		}
		fmt.Printf("Key %s disabled\n", os.Args[2])

	default:
		log.Fatalf("unknown command: %s", os.Args[1])
	}
}
```

- [ ] **Step 2: Build CLI**

```bash
go build ./cmd/llm-interceptor-cli/
echo "build ok"
```

- [ ] **Step 3: Commit**

```bash
git add cmd/
git commit -m "feat(cli): add API key management CLI"
```

---

### Task 6: Integrate router mode into main proxy handler

**Files:**
- Modify: `cmd/llm-interceptor/main.go`
- Modify: `internal/config/config.go`

- [ ] **Step 1: Add router config and wire into main.go**

Initialize the router and key manager before the HTTP server setup:

```go
// Initialize router and key manager
var routerCfg routerCfg
var keyManager *router.KeyManager
if cfg.Router.Enabled {
    keyManager = router.NewKeyManager(store)

    var providers []router.Provider
    for _, pc := range cfg.Router.Providers {
        providers = append(providers, router.NewHTTPProvider(
            pc.Name, pc.BaseURL, pc.ModelGlob, pc.APIKey,
        ))
    }
    routerCfg.enabled = true
    routerCfg.defaultProvider = cfg.Upstream
}
```

In the HTTP handler, add router logic BEFORE the existing passthrough logic:
```go
r.Post("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
    // ... (existing body read, stream detection, plugin dispatch) ...

    // Router mode: if enabled and key is managed, validate and select provider
    if routerCfg.enabled {
        mode := routerCfg.modeDetector.DetectMode(apiKey)
        if mode == "router" {
            valid, err := keyManager.Validate(r.Context(), apiKey)
            if err != nil || !valid {
                http.Error(w, "invalid API key", http.StatusUnauthorized)
                return
            }
            // Override upstream target with selected provider
            provider := routerCfg.router.SelectProvider(rb.Model)
            if provider != nil {
                target = provider  // use provider's target instead of default proxy
            }
        }
    }

    // ... (existing proxy forward logic unchanged) ...
})
```

Key design: The router layer does NOT replace the proxy pipeline. It only resolves the correct upstream target. All plugin hooks, streaming, and governance still apply.

- [ ] **Step 2: Add config.example.yaml updates**

```yaml
router:
  enabled: false
  providers:
    - name: anthropic
      base_url: "https://api.anthropic.com"
      model_glob: "claude-*"
      api_key: "${ANTHROPIC_API_KEY}"
    - name: openai
      base_url: "https://api.openai.com"
      model_glob: "gpt-*"
      api_key: "${OPENAI_API_KEY}"
```

- [ ] **Step 3: Build and verify**

```bash
go build ./cmd/llm-interceptor/
echo "build succeeded"
```

- [ ] **Step 4: Commit**

```bash
git add cmd/ internal/config/ config.example.yaml
git commit -m "feat: integrate router mode into main proxy handler"
```

---

### Self-Review

**1. Spec coverage:**
- Mode detection (passthrough vs router) ✓
- API key management (generate, hash, validate) ✓
- Provider routing engine (model glob → provider) ✓
- Multi-provider configuration ✓
- Protocol translation (Anthropic ↔ OpenAI) ✓
- CLI for key management ✓

**2. Security:**
- API keys hashed with bcrypt before storage
- `sk-lli-` prefix for owned keys, `sk-ant-`/`sk-proj-` pass through
- Router mode validates key on every request

**3. Backward compatibility:**
- Router disabled by default (`router.enabled: false`)
- Existing Phase 1-3 configs unchanged
- Passthrough mode identical to Phase 1 behavior

**4. Protocol translation scope (Phase 4):**
- Text-only messages (user/assistant roles) with system prompt translation ✓
- Tool/function calling, multimodal content, cache control — deferred to future phases
- Streaming SSE translation is not implemented (Anthropic and OpenAI SSE formats differ significantly)
- Clients should use provider-native models where possible to avoid translation overhead

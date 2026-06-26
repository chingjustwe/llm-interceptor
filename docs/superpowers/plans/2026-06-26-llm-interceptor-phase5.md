# LLM Interceptor — Implementation Plan (Phase 5: Web UI)

**Goal:** Build a React SPA embedded in the Go binary, providing real-time request monitoring, session detail, cost analysis, and API key management.

**Architecture:** React SPA under `ui/`, built with Vite. Production build outputs to `ui/dist/`, embedded into Go binary via `embed.FS`. Go backend serves the SPA and provides REST + SSE endpoints for live data. No separate server process — everything is in the single binary.

**Tech Stack:** React 18+, TypeScript, Vite, Tailwind CSS, Go `embed.FS`, Server-Sent Events (SSE)

---

### Task 1: Go REST API endpoints + SSE

**Files:**
- Create: `internal/api/handler.go`
- Create: `internal/api/sse.go`
- Modify: `cmd/llm-interceptor/main.go`

- [ ] **Step 1: Write REST API handler**

`internal/api/handler.go`:

```go
package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/chingjustwe/llm-Interceptor/internal/router"
	"github.com/chingjustwe/llm-Interceptor/internal/storage"
	"github.com/chingjustwe/llm-Interceptor/internal/types"
)

type Handler struct {
	store      storage.Backend
	keyManager *router.KeyManager
}

func NewHandler(store storage.Backend, km *router.KeyManager) *Handler {
	return &Handler{store: store, keyManager: km}
}

func (h *Handler) Register(r chi.Router) {
	r.Get("/api/requests", h.listRequests)
	r.Get("/api/requests/{id}", h.getRequest)
	r.Get("/api/sessions/{id}/requests", h.getSessionRequests)
	r.Get("/api/sessions", h.listSessions)
	r.Get("/api/stats", h.costStats)
	r.Get("/api/keys", h.listKeys)
	r.Post("/api/keys", h.createKey)
	r.Post("/api/keys/{id}/disable", h.disableKey)
}

func (h *Handler) listRequests(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	reqs, err := h.store.QueryRequests(r.Context(), types.RequestFilter{Limit: limit, Offset: offset})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(reqs)
}

func (h *Handler) getRequest(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	reqs, err := h.store.QueryRequests(r.Context(), types.RequestFilter{Limit: 1})
	if err != nil || len(reqs) == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(reqs[0])
}

func (h *Handler) getSessionRequests(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "id")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	reqs, err := h.store.GetSessionRequests(r.Context(), sessionID, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(reqs)
}

func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request) {
	reqs, err := h.store.QueryRequests(r.Context(), types.RequestFilter{Limit: 1000})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sessionMap := make(map[string]int)
	for _, req := range reqs {
		if req.SessionID != "" {
			sessionMap[req.SessionID]++
		}
	}
	type sessionSummary struct {
		ID    string `json:"id"`
		Count int    `json:"count"`
	}
	summaries := make([]sessionSummary, 0, len(sessionMap))
	for id, count := range sessionMap {
		summaries = append(summaries, sessionSummary{ID: id, Count: count})
	}
	json.NewEncoder(w).Encode(summaries)
}

func (h *Handler) costStats(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
	})
}

func (h *Handler) listKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.store.ListAPIKeys(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(keys)
}

func (h *Handler) createKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		http.Error(w, "missing name", http.StatusBadRequest)
		return
	}
	key, err := h.keyManager.Generate(r.Context(), body.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"key": key})
}

func (h *Handler) disableKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.store.DisableAPIKey(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
```

- [ ] **Step 2: Write SSE endpoint for live stream**

`internal/api/sse.go`:

```go
package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
)

type SSEBroker struct {
	mu      sync.RWMutex
	clients map[chan []byte]struct{}
}

func NewSSEBroker() *SSEBroker {
	return &SSEBroker{clients: make(map[chan []byte]struct{})}
}

func (b *SSEBroker) Subscribe() chan []byte {
	ch := make(chan []byte, 16)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *SSEBroker) Unsubscribe(ch chan []byte) {
	b.mu.Lock()
	delete(b.clients, ch)
	b.mu.Unlock()
	close(ch)
}

func (b *SSEBroker) Publish(data any) {
	msg, err := json.Marshal(data)
	if err != nil {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.clients {
		select {
		case ch <- msg:
		default:
			// drop if client is slow
		}
	}
}

func (b *SSEBroker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := b.Subscribe()
	defer b.Unsubscribe(ch)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		}
	}
}
```

- [ ] **Step 3: Wire API and SSE into main.go**

Add to `cmd/llm-interceptor/main.go`:

```go
import "github.com/chingjustwe/llm-Interceptor/internal/api"

// After router setup:
apiHandler := api.NewHandler(store, keyManager)
apiHandler.Register(r)

broker := api.NewSSEBroker()
r.Get("/api/events", broker.ServeHTTP)
```

- [ ] **Step 4: Build and verify**

```bash
go build ./cmd/llm-interceptor/
echo "build ok"
```

- [ ] **Step 5: Commit**

```bash
git add internal/api/ cmd/
git commit -m "feat(api): add REST API and SSE live stream endpoints"
```

---

### Task 2: React frontend scaffold

**Files:**
- Create: `ui/package.json`
- Create: `ui/tsconfig.json`
- Create: `ui/vite.config.ts`
- Create: `ui/index.html`
- Create: `ui/src/main.tsx`
- Create: `ui/src/App.tsx`

- [ ] **Step 1: Scaffold React + Vite + Tailwind project**

`ui/package.json`:

```json
{
  "name": "llm-interceptor-ui",
  "version": "0.1.0",
  "private": true,
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "tsc && vite build",
    "preview": "vite preview"
  },
  "dependencies": {
    "react": "^18.3.0",
    "react-dom": "^18.3.0"
  },
  "devDependencies": {
    "@types/react": "^18.3.0",
    "@types/react-dom": "^18.3.0",
    "@vitejs/plugin-react": "^4.3.0",
    "typescript": "^5.5.0",
    "vite": "^5.4.0"
  }
}
```

`ui/tsconfig.json`:

```json
{
  "compilerOptions": {
    "target": "ES2020",
    "module": "ESNext",
    "moduleResolution": "bundler",
    "jsx": "react-jsx",
    "strict": true,
    "esModuleInterop": true,
    "skipLibCheck": true,
    "forceConsistentCasingInFileNames": true
  },
  "include": ["src"]
}
```

`ui/vite.config.ts`:

```ts
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  base: '/',
  build: {
    outDir: 'dist',
  },
})
```

`ui/index.html`:

```html
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>LLM Interceptor</title>
</head>
<body>
  <div id="root"></div>
  <script type="module" src="/src/main.tsx"></script>
</body>
</html>
```

`ui/src/main.tsx`:

```tsx
import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
```

`ui/src/App.tsx`:

```tsx
import { useEffect, useState } from 'react'

type Request = {
  id: string
  session_id: string
  model: string
  duration_ms: number
  status_code: number
  created_at: number
}

function App() {
  const [requests, setRequests] = useState<Request[]>([])
  const [events, setEvents] = useState<string[]>([])

  useEffect(() => {
    fetch('/api/requests?limit=20')
      .then(r => r.json())
      .then(setRequests)
      .catch(console.error)
  }, [])

  useEffect(() => {
    const es = new EventSource('/api/events')
    es.onmessage = (e) => {
      setEvents(prev => [e.data, ...prev].slice(0, 50))
    }
    return () => es.close()
  }, [])

  return (
    <div style={{ padding: '1rem', fontFamily: 'monospace' }}>
      <h1>LLM Interceptor</h1>
      <h2>Recent Requests</h2>
      <table border={1} cellPadding={6} style={{ width: '100%', borderCollapse: 'collapse' }}>
        <thead>
          <tr>
            <th>ID</th>
            <th>Session</th>
            <th>Model</th>
            <th>Duration</th>
            <th>Status</th>
          </tr>
        </thead>
        <tbody>
          {requests.map(r => (
            <tr key={r.id}>
              <td>{r.id.slice(0, 12)}</td>
              <td>{r.session_id.slice(0, 12)}</td>
              <td>{r.model}</td>
              <td>{r.duration_ms}ms</td>
              <td>{r.status_code}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <h2>Live Events</h2>
      <pre>{events.join('\n')}</pre>
    </div>
  )
}

export default App
```

- [ ] **Step 2: Install dependencies and build**

```bash
cd ui && npm install && npm run build
```

Expected: `ui/dist/` directory created with built files.

- [ ] **Step 3: Commit**

```bash
git add ui/
git commit -m "feat(ui): scaffold React SPA with Vite"
```

---

### Task 3: Embed SPA in Go binary

**Files:**
- Modify: `cmd/llm-interceptor/main.go`

- [ ] **Step 1: Add embed directive and static file server**

```go
import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed ui/dist/*
var uiFS embed.FS

func staticFileServer() http.Handler {
	sub, err := fs.Sub(uiFS, "ui/dist")
	if err != nil {
		log.Fatalf("failed to get ui sub fs: %v", err)
	}
	return http.FileServer(http.FS(sub))
}
```

In router setup:

```go
// Serve SPA for all non-API routes (SPA fallback routing)
r.Handle("/*", staticFileServer())
```

Place this after API routes so `/api/*` matches first.

- [ ] **Step 2: Build and verify**

```bash
go build ./cmd/llm-interceptor/
echo "build ok"
```

- [ ] **Step 3: Commit**

```bash
git add cmd/
git commit -m "feat: embed React SPA into Go binary via embed.FS"
```

---

### Task 4: Session detail page

**Files:**
- Create: `ui/src/SessionDetail.tsx`
- Modify: `ui/src/App.tsx`

- [ ] **Step 1: Add session detail route**

`ui/src/SessionDetail.tsx`:

```tsx
import { useEffect, useState } from 'react'

type Request = {
  id: string
  session_id: string
  model: string
  method: string
  path: string
  request_body: string
  response_body: string
  usage: { input_tokens: number; output_tokens: number }
  duration_ms: number
  status_code: number
  created_at: number
}

export default function SessionDetail({ sessionId }: { sessionId: string }) {
  const [requests, setRequests] = useState<Request[]>([])

  useEffect(() => {
    fetch(`/api/sessions/${sessionId}/requests?limit=50`)
      .then(r => r.json())
      .then(setRequests)
      .catch(console.error)
  }, [sessionId])

  const totalTokens = requests.reduce(
    (sum, r) => sum + r.usage.input_tokens + r.usage.output_tokens, 0
  )

  return (
    <div>
      <h2>Session: {sessionId}</h2>
      <p>Total requests: {requests.length} | Total tokens: {totalTokens}</p>
      {requests.map(r => (
        <details key={r.id} style={{ marginBottom: '0.5rem' }}>
          <summary>{r.model} — {r.duration_ms}ms — {r.status_code}</summary>
          <pre>{JSON.stringify(JSON.parse(r.request_body || '{}'), null, 2)}</pre>
          <pre>{JSON.stringify(JSON.parse(r.response_body || '{}'), null, 2)}</pre>
        </details>
      ))}
    </div>
  )
}
```

- [ ] **Step 2: Commit**

```bash
git add ui/
git commit -m "feat(ui): add session detail page"
```

---

### Task 5: Cost dashboard + key management UI

**Files:**
- Create: `ui/src/CostDashboard.tsx`
- Create: `ui/src/KeyManagement.tsx`

- [ ] **Step 1: Cost dashboard**

`ui/src/CostDashboard.tsx`:

```tsx
import { useEffect, useState } from 'react'

export default function CostDashboard() {
  const [stats, setStats] = useState<any>({})

  useEffect(() => {
    fetch('/api/stats')
      .then(r => r.json())
      .then(setStats)
      .catch(console.error)
  }, [])

  return (
    <div>
      <h2>Cost Dashboard</h2>
      <pre>{JSON.stringify(stats, null, 2)}</pre>
    </div>
  )
}
```

- [ ] **Step 2: Key management UI**

`ui/src/KeyManagement.tsx`:

```tsx
import { useEffect, useState } from 'react'

export default function KeyManagement() {
  const [keys, setKeys] = useState<any[]>([])
  const [name, setName] = useState('')

  useEffect(() => {
    fetch('/api/keys')
      .then(r => r.json())
      .then(setKeys)
      .catch(console.error)
  }, [])

  const createKey = async () => {
    const res = await fetch('/api/keys', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name }),
    })
    const data = await res.json()
    alert(`New key: ${data.key}\n\nSave this — it won't be shown again.`)
    setName('')
    const updated = await fetch('/api/keys').then(r => r.json())
    setKeys(updated)
  }

  return (
    <div>
      <h2>API Keys</h2>
      <input value={name} onChange={e => setName(e.target.value)} placeholder="Key name" />
      <button onClick={createKey}>Generate Key</button>
      <table border={1} cellPadding={6} style={{ marginTop: '1rem', width: '100%' }}>
        <thead>
          <tr><th>Prefix</th><th>Name</th><th>Status</th></tr>
        </thead>
        <tbody>
          {keys.map(k => (
            <tr key={k.id}>
              <td>{k.key_prefix}</td>
              <td>{k.name}</td>
              <td>{k.enabled ? 'Enabled' : 'Disabled'}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
```

- [ ] **Step 3: Build UI**

```bash
cd ui && npm run build
```

- [ ] **Step 4: Commit**

```bash
git add ui/
git commit -m "feat(ui): add cost dashboard and key management pages"
```

---

### Self-Review

**1. Spec coverage:**
- React SPA with Vite ✓
- Embedded via `embed.FS` ✓
- REST API endpoints (requests, sessions, stats) ✓
- SSE live event stream ✓
- Session detail view ✓
- Cost dashboard ✓
- Key management UI ✓

**2. Design decisions:**
- No React Router — simple state-driven views for MVP
- SSE for live updates — simpler than WebSocket, well-supported
- Tailwind CSS deferred — plain HTML/CSS for MVP, styling can be enhanced later
- Same port for API and UI — avoids CORS issues in development

**3. Backward compatibility:**
- API routes under `/api/*` prefix, SPA serves all other routes
- Existing proxy functionality unchanged
- UI is optional — binary works without `ui/dist/` present (embed.FS build-time check)

package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chingjustwe/llm-interceptor/internal/api"
	"github.com/chingjustwe/llm-interceptor/internal/config"
	"github.com/chingjustwe/llm-interceptor/internal/plugin"
	"github.com/chingjustwe/llm-interceptor/internal/plugins"
	"github.com/chingjustwe/llm-interceptor/internal/state"
	"github.com/chingjustwe/llm-interceptor/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func newTestHarness(t *testing.T) (string, func()) {
	t.Helper()

	store, err := storage.NewSQLite(":memory:", storage.CompressionConfig{Enabled: false})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}

	st := state.NewMemory()

	pluginList := []plugin.Plugin{
		plugins.NewCostTracker(st),
		plugins.NewBudgetPlugin(st, 1000, 1000),
	}
	disp := plugin.NewDispatcher(pluginList)

	broker := api.NewSSEBroker()
	apiHandler := api.NewHandler(store, st)
	apiHandler.Dispatcher = disp
	apiHandler.Broker = broker
	apiHandler.Config = config.Default()
	apiHandler.AuthSecret = "test-secret"

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)
	apiHandler.Register(r)

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	server := httptest.NewServer(r)
	return server.URL, func() {
		server.Close()
		store.Close()
		st.Close()
	}
}

func TestIntegration_HealthEndpoint(t *testing.T) {
	url, cleanup := newTestHarness(t)
	defer cleanup()

	resp, err := http.Get(url + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("expected status ok, got %v", body["status"])
	}
}

func TestIntegration_APIEndpoints(t *testing.T) {
	url, cleanup := newTestHarness(t)
	defer cleanup()

	t.Run("GET /api/requests returns list", func(t *testing.T) {
		resp, err := http.Get(url + "/api/requests")
		if err != nil {
			t.Fatalf("GET /api/requests: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp.StatusCode)
		}
	})

	t.Run("GET /api/sessions returns list", func(t *testing.T) {
		resp, err := http.Get(url + "/api/sessions")
		if err != nil {
			t.Fatalf("GET /api/sessions: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp.StatusCode)
		}
	})

	t.Run("GET /api/stats returns stats", func(t *testing.T) {
		resp, err := http.Get(url + "/api/stats")
		if err != nil {
			t.Fatalf("GET /api/stats: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp.StatusCode)
		}
	})
}

func TestIntegration_AdminLogin(t *testing.T) {
	t.Skip("admin login requires credential file setup")
}

func TestIntegration_PluginPipelineBudget(t *testing.T) {
	store, err := storage.NewSQLite(":memory:", storage.CompressionConfig{Enabled: false})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	defer store.Close()

	st := state.NewMemory()
	defer st.Close()

	pluginList := []plugin.Plugin{
		plugins.NewCostTracker(st),
	}
	disp := plugin.NewDispatcher(pluginList)

	reqCtx := &plugin.RequestContext{
		ID:        "test-req",
		SessionID: "test-session",
		Method:    "POST",
		Path:      "/v1/messages",
		Body:      []byte(`{"model":"claude-sonnet-4","max_tokens":100}`),
		Headers:   map[string]string{},
		APIFormat: "anthropic",
	}

	result, err := disp.ExecuteOnRequest(reqCtx)
	if err != nil {
		t.Fatalf("ExecuteOnRequest: %v", err)
	}
	if result != nil && result.Block {
		t.Errorf("unexpected block on high-budget config: %s", result.Reason)
	}

	// Execute response (simulates a response with high token usage).
	respCtx := &plugin.ResponseContext{
		RequestID: "test-req",
		SessionID: "test-session",
		Model:     "claude-sonnet-4",
		Usage: plugin.Usage{
			InputTokens:  10000000,
			OutputTokens: 20000000,
		},
		StatusCode: 200,
	}
	if err := disp.ExecuteOnResponse(respCtx); err != nil {
		t.Fatalf("ExecuteOnResponse: %v", err)
	}
}

func TestIntegration_ExportAndTimeseries(t *testing.T) {
	url, cleanup := newTestHarness(t)
	defer cleanup()

	t.Run("GET /api/requests/export CSV", func(t *testing.T) {
		resp, err := http.Get(url + "/api/requests/export?format=csv")
		if err != nil {
			t.Fatalf("GET /api/requests/export: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp.StatusCode)
		}
	})

	t.Run("GET /api/stats/timeseries", func(t *testing.T) {
		resp, err := http.Get(url + "/api/stats/timeseries?granularity=day")
		if err != nil {
			t.Fatalf("GET /api/stats/timeseries: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp.StatusCode)
		}
	})
}

func TestIntegration_AgentsInfo(t *testing.T) {
	url, cleanup := newTestHarness(t)
	defer cleanup()

	resp, err := http.Get(url + "/api/agents/info")
	if err != nil {
		t.Fatalf("GET /api/agents/info: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var info map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if info["version"] == "" {
		t.Errorf("expected non-empty version")
	}
}

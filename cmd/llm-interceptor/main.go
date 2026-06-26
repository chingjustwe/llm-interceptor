// Package main is the entry point for the LLM Interceptor binary.
// It initializes configuration, storage, state store, plugins, and the HTTP
// server, then listens for incoming LLM proxy requests and API calls.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/chingjustwe/llm-interceptor/internal/api"
	"github.com/chingjustwe/llm-interceptor/internal/config"
	"github.com/chingjustwe/llm-interceptor/internal/plugin"
	"github.com/chingjustwe/llm-interceptor/internal/plugins"
	"github.com/chingjustwe/llm-interceptor/internal/proxy"
	"github.com/chingjustwe/llm-interceptor/internal/state"
	"github.com/chingjustwe/llm-interceptor/internal/storage"
	"github.com/chingjustwe/llm-interceptor/internal/types"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

//go:embed ui/dist/index.html
//go:embed ui/dist/assets
var uiFS embed.FS

func staticFileServer() http.Handler {
	sub, err := fs.Sub(uiFS, "ui/dist")
	if err != nil {
		log.Fatalf("failed to get ui sub fs: %v", err)
	}
	return http.FileServer(http.FS(sub))
}

func main() {
	cfgPath := "config.yaml"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

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
	case "postgres":
		if cfg.Storage.Postgres == nil {
			log.Fatalf("postgres storage requires a 'postgres' config block")
		}
		s, err := storage.NewPostgres(cfg.Storage.Postgres.ConnectionString)
		if err != nil {
			log.Fatalf("failed to init postgres storage: %v", err)
		}
		store = s
		defer store.Close()
	default:
		log.Fatalf("unknown storage type: %s", cfg.Storage.Type)
	}
	// Initialize state store
	var st state.Backend
	switch cfg.StateStore.Type {
	case "memory":
		st = state.NewMemory()
		defer st.Close()
	case "redis":
		if cfg.StateStore.Redis == nil {
			log.Fatalf("redis state store requires a 'redis' config block")
		}
		s, err := state.NewRedis(cfg.StateStore.Redis.URL)
		if err != nil {
			log.Fatalf("failed to init redis state: %v", err)
		}
		st = s
		defer st.Close()
	default:
		log.Fatalf("unknown state store type: %s", cfg.StateStore.Type)
	}
	// Initialize plugins
	ctx := context.Background()
	var pluginList []plugin.Plugin
	if cfg.Plugins.OTelExporter.Enabled {
		exporter, err := plugins.NewOTelExporter(ctx, plugins.OTelExporterConfig{
			Endpoint:     cfg.Plugins.OTelExporter.Endpoint,
			Headers:      cfg.Plugins.OTelExporter.Headers,
			MetricPrefix: cfg.MetricPrefix,
			MaxAttrLen:   cfg.Plugins.OTelExporter.MaxAttrLen,
		})
		if err != nil {
			log.Fatalf("failed to init otel exporter: %v", err)
		}
		pluginList = append(pluginList, exporter)
		defer exporter.Shutdown(ctx)
	}
	if cfg.Plugins.CostTracker.Enabled {
		pluginList = append(pluginList, plugins.NewCostTracker(st))
	}
	if cfg.Plugins.Budget.MaxCostPerSession > 0 || cfg.Plugins.Budget.MaxCostPerDay > 0 {
		pluginList = append(pluginList, plugins.NewBudgetPlugin(st,
			cfg.Plugins.Budget.MaxCostPerSession,
			cfg.Plugins.Budget.MaxCostPerDay,
		))
	}
	if cfg.Plugins.RateLimit.RequestsPerMinute > 0 || cfg.Plugins.RateLimit.TokensPerMinute > 0 {
		pluginList = append(pluginList, plugins.NewRateLimitPlugin(st,
			cfg.Plugins.RateLimit.RequestsPerMinute,
			cfg.Plugins.RateLimit.TokensPerMinute,
		))
	}
	var toolPolicy *plugins.ToolPolicyPlugin
	if len(cfg.Plugins.ToolPolicy.BlockedTools) > 0 || len(cfg.Plugins.ToolPolicy.AllowedTools) > 0 {
		toolPolicy = plugins.NewToolPolicyPlugin(
			cfg.Plugins.ToolPolicy.BlockedTools,
			cfg.Plugins.ToolPolicy.AllowedTools,
		)
		pluginList = append(pluginList, toolPolicy)
	}
	disp := plugin.NewDispatcher(pluginList)

	target, err := proxy.New("anthropic", cfg.Upstream)
	if err != nil {
		log.Fatalf("failed to init proxy: %v", err)
	}

	// HTTP server
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)

	// API routes
	apiHandler := api.NewHandler(store, st)
	apiHandler.Register(r)

	broker := api.NewSSEBroker()
	r.Get("/api/events", broker.ServeHTTP)

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
		respCtx.Context = r.Context()
		respCtx.RequestID = reqCtx.ID
		respCtx.SessionID = reqCtx.SessionID
		respCtx.Metadata = reqCtx.Metadata

		var model struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(body, &model) == nil {
			respCtx.Model = model.Model
		}

		// Build the tool blocking function for the proxy layer.
		var isToolBlocked func(name string) bool
		if toolPolicy != nil {
			isToolBlocked = toolPolicy.IsBlocked
		}

		if isStream {
			respBody, usage, toolCalls, stopReason, duration, err := target.HandleRequestStream(body, reqCtx.Headers, w, isToolBlocked)
			if err != nil {
				log.Printf("proxy stream error: %v", err)
				return
			}
			respCtx.Body = respBody
			if usage != nil {
				respCtx.Usage = plugin.Usage(*usage)
			}
			for _, tc := range toolCalls {
				respCtx.ToolCalls = append(respCtx.ToolCalls, plugin.ToolCall(tc))
			}
			respCtx.StopReason = stopReason
			respCtx.DurationMs = duration
			respCtx.StatusCode = http.StatusOK
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
			respCtx.Body = pr.Body

			// Apply tool policy to the response body before writing to client.
			bodyToWrite := pr.Body
			if isToolBlocked != nil {
				bodyToWrite = interceptBlockedTools(pr.Body, isToolBlocked)
			}

			for k, v := range pr.Headers {
				w.Header().Set(k, v)
			}
			if w.Header().Get("Content-Type") == "" {
				w.Header().Set("Content-Type", "application/json")
			}
			w.WriteHeader(pr.StatusCode)
			w.Write(bodyToWrite)
		}

		if err := disp.ExecuteOnResponse(&respCtx); err != nil {
			log.Printf("plugin response error: %v", err)
		}

		// Save request to storage and notify SSE clients (async, best-effort)
		go func() {
			storedReq := &types.StoredRequest{
				ID:        reqCtx.ID,
				SessionID: reqCtx.SessionID,
				Model:     respCtx.Model,
				Method:    reqCtx.Method,
				Path:      reqCtx.Path,
				Request:   string(body),
				Response:  string(respCtx.Body),
				Usage: types.TokenUsage{
					InputTokens:         respCtx.Usage.InputTokens,
					OutputTokens:        respCtx.Usage.OutputTokens,
					CacheReadTokens:     respCtx.Usage.CacheReadTokens,
					CacheCreationTokens: respCtx.Usage.CacheCreationTokens,
				},
				DurationMs: respCtx.DurationMs,
				StatusCode: respCtx.StatusCode,
				CreatedAt:  time.Now().UnixMilli(),
			}
			if err := store.SaveRequest(context.Background(), storedReq); err != nil {
				log.Printf("failed to save request: %v", err)
			}
			// Publish request data to SSE clients
			broker.Publish(storedReq)
		}()
	})

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	// SPA fallback for all non-API routes
	r.Handle("/*", staticFileServer())

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

// interceptBlockedTools checks a non-streaming LLM response body for tool_use
// content blocks whose names match the isBlocked predicate. Matching blocks
// are replaced with a text block saying the tool was blocked, and
// stop_reason is changed from "tool_use" to "end_turn". Returns the
// original body unchanged if no tools are blocked.
func interceptBlockedTools(body []byte, isBlocked func(name string) bool) []byte {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}
	content, ok := raw["content"].([]any)
	if !ok {
		return body
	}
	blocked := false
	for i, c := range content {
		block, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if block["type"] != "tool_use" {
			continue
		}
		name, ok := block["name"].(string)
		if !ok || !isBlocked(name) {
			continue
		}
		// Replace tool_use with a text block.
		content[i] = map[string]any{
			"type": "text",
			"text": fmt.Sprintf("Tool '%s' is blocked by interceptor policy. You cannot use this tool in this session.", name),
		}
		blocked = true
	}
	if !blocked {
		return body
	}
	raw["content"] = content
	raw["stop_reason"] = "end_turn"
	modified, err := json.Marshal(raw)
	if err != nil {
		return body
	}
	return modified
}

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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/chingjustwe/llm-interceptor/internal/api"
	"github.com/chingjustwe/llm-interceptor/internal/config"
	"github.com/chingjustwe/llm-interceptor/internal/plugin"
	"github.com/chingjustwe/llm-interceptor/internal/plugins"
	"github.com/chingjustwe/llm-interceptor/internal/proxy"
	"github.com/chingjustwe/llm-interceptor/internal/router"
	"github.com/chingjustwe/llm-interceptor/internal/state"
	"github.com/chingjustwe/llm-interceptor/internal/storage"
	"github.com/chingjustwe/llm-interceptor/internal/types"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// anthropicErrorType maps an HTTP status code to the corresponding Anthropic
// API error type string. Plugins use custom status codes; this normalizes them
// to the upstream API format so clients (Claude Code, etc.) can display
// meaningful error messages instead of blindly retrying.
func anthropicErrorType(statusCode int) string {
	switch statusCode {
	case 429:
		return "rate_limit_error"
	case 400:
		return "invalid_request_error"
	case 401:
		return "authentication_error"
	case 403:
		return "permission_error"
	case 404:
		return "not_found_error"
	case 500:
		return "api_error"
	case 529:
		return "overloaded_error"
	default:
		return "api_error"
	}
}

// writeAnthropicError writes an error response in Anthropic API JSON format
// so that Anthropic SDK clients can parse the error and display a meaningful
// message to the user. If retryAfterSec > 0, a Retry-After header is set so
// the client can back off before retrying (used by rate-limit, NOT by budget).
// If errorType is empty, the type is derived from statusCode via anthropicErrorType.
func writeAnthropicError(w http.ResponseWriter, message string, statusCode int, retryAfterSec int, errorType string) {
	w.Header().Set("Content-Type", "application/json")
	if retryAfterSec > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSec))
	}
	w.WriteHeader(statusCode)
	if errorType == "" {
		errorType = anthropicErrorType(statusCode)
	}
	json.NewEncoder(w).Encode(map[string]any{
		"type": "error",
		"error": map[string]string{
			"type":    errorType,
			"message": message,
		},
	})
}

//go:embed ui/dist/*
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

	// routerMode bundles all router-mode state. It is nil when router mode is
	// disabled, and the handler falls through to the default passthrough proxy.
	type routerMode struct {
		enabled    bool
		keyManager *router.KeyManager
		rt         *router.Router
		proxies    map[string]*proxy.Proxy // per-provider proxy keyed by provider name
		apiKeys    map[string]string       // provider name → upstream API key
	}
	var rm *routerMode
	if cfg.Router.Enabled {
		km := router.NewKeyManager(store)
		var providers []router.Provider
		providerProxies := make(map[string]*proxy.Proxy)
		providerKeys := make(map[string]string)
		for _, pc := range cfg.Router.Providers {
			hp := router.NewHTTPProvider(pc.Name, pc.BaseURL, pc.ModelGlob, pc.APIKey)
			providers = append(providers, hp)
			// Create a dedicated proxy per provider so each targets its own upstream.
			pp, err := proxy.New(pc.Name, hp.BaseURL())
			if err != nil {
				log.Fatalf("failed to init proxy for provider %s: %v", pc.Name, err)
			}
			providerProxies[pc.Name] = pp
			providerKeys[pc.Name] = hp.APIKey()
		}
		rm = &routerMode{
			enabled:    true,
			keyManager: km,
			rt:         router.New(providers, cfg.Upstream),
			proxies:    providerProxies,
			apiKeys:    providerKeys,
		}
		log.Printf("Router mode enabled with %d provider(s)", len(providers))
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
	var calculateCost api.CostCalculator
	if cfg.Plugins.CostTracker.Enabled {
		ct := plugins.NewCostTracker(st)

		pricingURL := cfg.Plugins.CostTracker.PricingURL
		if pricingURL == "" {
			pricingURL = "https://models.dev/api.json"
		}

		// Config-level overrides must be stored before the first fetch so
		// the refresh goroutine can re-apply them on each cycle.
		if len(cfg.Plugins.CostTracker.Prices) > 0 {
			prices := make(map[string]plugins.PriceEntry, len(cfg.Plugins.CostTracker.Prices))
			for model, p := range cfg.Plugins.CostTracker.Prices {
				prices[model] = plugins.PriceEntry{InputPerM: p.InputPerM, OutputPerM: p.OutputPerM}
			}
			ct.SetConfigPrices(prices)
		}

		// Initial fetch (best-effort, merges into static defaults).
		if onlinePrices, err := plugins.FetchOnlinePricing(pricingURL); err != nil {
			log.Printf("cost: initial pricing unavailable (%v), using static defaults", err)
		} else {
			ct.MergePrices(onlinePrices)
		}
		// Re-apply config overrides so they always win.
		ct.MergePrices(ct.ConfigPrices())

		// Periodic refresh every 10 minutes.
		refreshCtx, cancelRefresh := context.WithCancel(context.Background())
		ct.StartPricingRefresh(refreshCtx, pricingURL, 10*time.Minute)
		defer cancelRefresh()

		pluginList = append(pluginList, ct)
		calculateCost = ct.CalculateCost
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
	apiHandler.CalculateCostFn = calculateCost
	if rm != nil {
		apiHandler.KeyManager = rm.keyManager
	}
	apiHandler.Register(r)

	broker := api.NewSSEBroker()
	r.Get("/api/events", broker.ServeHTTP)

	llmHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		switch {
		case strings.HasSuffix(r.URL.Path, "/v1/messages"):
			reqCtx.APIFormat = "anthropic"
		case strings.HasSuffix(r.URL.Path, "/v1/chat/completions"):
			reqCtx.APIFormat = "openai"
		}
		reqCtx.Headers = make(map[string]string)
		for k, v := range r.Header {
			reqCtx.Headers[k] = strings.Join(v, ", ")
		}

		hookResult, err := disp.ExecuteOnRequest(reqCtx)
		if err != nil {
			log.Printf("plugin error: %v [req=%s session=%s agent=%s]", err, reqCtx.ID, reqCtx.SessionID, reqCtx.AgentID)
			writeAnthropicError(w, "internal error", http.StatusInternalServerError, 0, "")
			return
		}
		if hookResult != nil && hookResult.Block {
			log.Printf("request blocked: %s (status=%d) [req=%s session=%s agent=%s]",
				hookResult.Reason, hookResult.StatusCode,
				reqCtx.ID, reqCtx.SessionID, reqCtx.AgentID)
			writeAnthropicError(w, hookResult.Reason, hookResult.StatusCode, hookResult.RetryAfterSec, hookResult.ErrorType)
			return
		}

		var respCtx plugin.ResponseContext
		respCtx.Context = r.Context()
		respCtx.RequestID = reqCtx.ID
		respCtx.SessionID = reqCtx.SessionID
		respCtx.Metadata = reqCtx.Metadata
		respCtx.APIFormat = reqCtx.APIFormat

		var model struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(body, &model) == nil {
			respCtx.Model = model.Model
		}

		// Use the request body after plugins may have modified it.
		body = reqCtx.Body

		// Router mode: if enabled and the client key is a managed key,
		// validate it and select the appropriate provider proxy. The router
		// does NOT replace the proxy pipeline — it only resolves the correct
		// upstream target. All plugin hooks, streaming, and governance still apply.
		activeTarget := target
		if rm != nil {
			apiKey := r.Header.Get("x-api-key")
			if mode := rm.rt.DetectMode(apiKey); mode == "router" {
				valid, err := rm.keyManager.Validate(r.Context(), apiKey)
				if err != nil || !valid {
					writeAnthropicError(w, "invalid API key", http.StatusUnauthorized, 0, "")
					return
				}
				// Select provider based on model name.
				provider := rm.rt.SelectProvider(respCtx.Model)
				if provider != nil {
					if pp, ok := rm.proxies[provider.Name()]; ok {
						activeTarget = pp
						// Inject the provider's upstream API key into the request
						// headers so the proxy forwards it to the correct backend.
						if key, exists := rm.apiKeys[provider.Name()]; exists {
							reqCtx.Headers["x-api-key"] = key
							reqCtx.Headers["authorization"] = "Bearer " + key
						}
					}
				}
			}
		}

		var reqTTFTMs *int64
		if isStream {
			var isToolBlocked func(name string) bool
			if toolPolicy != nil {
				isToolBlocked = toolPolicy.IsBlocked
			}
			respBody, usage, toolCalls, stopReason, ttftMs, duration, err := activeTarget.HandleRequestStream(body, reqCtx.Headers, w, r.URL.Path, isToolBlocked)
			reqTTFTMs = &ttftMs
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
			pr, err := activeTarget.HandleRequest(body, reqCtx.Headers, r.URL.Path)
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
			storedReq.SystemPrompt = proxy.ExtractSystemPrompt(body)
			if respCtx.StopReason != "" {
				storedReq.StopReason = &respCtx.StopReason
			}
			if respCtx.StatusCode >= 400 {
				eType, eMsg := proxy.ExtractError(respCtx.Body)
				if eType != "" {
					storedReq.ErrorType = &eType
				}
				if eMsg != "" {
					storedReq.ErrorMessage = &eMsg
				}
			}
			if isStream {
				storedReq.TTFTMs = reqTTFTMs
			}
			params := proxy.ExtractRequestParams(body)
			if params != nil {
				if t, ok := params["temperature"].(float64); ok {
					storedReq.Temperature = &t
				}
				if tp, ok := params["top_p"].(float64); ok {
					storedReq.TopP = &tp
				}
				if pJSON, err := json.Marshal(params); err == nil {
					pStr := string(pJSON)
					storedReq.RequestParams = &pStr
				}
			}
			if err := store.SaveRequest(context.Background(), storedReq); err != nil {
				log.Printf("failed to save request: %v", err)
			}
			// Publish request data to SSE clients
			broker.Publish(storedReq)
		}()
	})
	r.Post("/v1/chat/completions", llmHandler)

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



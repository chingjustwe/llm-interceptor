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
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"log/slog"

	_ "embed"

	// "github.com/chingjustwe/llm-interceptor/internal/alerting"
	"github.com/chingjustwe/llm-interceptor/internal/api"
	"github.com/chingjustwe/llm-interceptor/internal/auth"
	"github.com/chingjustwe/llm-interceptor/internal/config"
	"github.com/chingjustwe/llm-interceptor/internal/log"
	"github.com/chingjustwe/llm-interceptor/internal/metrics"
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

//go:embed openapi.yaml
var openAPISpec []byte

func staticFileServer() http.Handler {
	sub, err := fs.Sub(uiFS, "ui/dist")
	if err != nil {
		slog.Error("failed to get ui sub fs", "error", err)
		os.Exit(1)
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
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Configure structured logging.
	log.Setup(cfg.Log)
	slog.Info("config loaded", "path", cfgPath)

	// Initialize admin credentials
	authSecret := cfg.Admin.JWTSecret
	if authSecret == "" {
		authSecret = auth.GenerateRandomString(32)
		slog.Info("auth: generated random JWT secret (set admin.jwt_secret in config to control)")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		slog.Error("failed to get home directory", "error", err)
		os.Exit(1)
	}
	credsFile := filepath.Join(home, ".llm-interceptor", "admin.credentials")

	credUsername := cfg.Admin.Username
	credPassword := cfg.Admin.Password
	if credUsername != "" && credPassword != "" {
		credHash, err := auth.HashPassword(credPassword)
		if err != nil {
			slog.Error("failed to hash admin password", "error", err)
			os.Exit(1)
		}
		if err := auth.SaveCredentials(credsFile, credUsername, credHash); err != nil {
			slog.Error("failed to save admin credentials", "error", err)
			os.Exit(1)
		}
		slog.Info("auth: using config-provided admin credentials", "user", credUsername)
	} else if _, err := os.Stat(credsFile); err == nil {
		slog.Info("auth: loaded admin credentials", "file", credsFile)
	} else {
		genUser, genPass := auth.GenerateDefaultCredentials()
		credHash, err := auth.HashPassword(genPass)
		if err != nil {
			slog.Error("failed to hash generated password", "error", err)
			os.Exit(1)
		}
		if err := auth.SaveCredentials(credsFile, genUser, credHash); err != nil {
			slog.Error("failed to save generated credentials", "error", err)
			os.Exit(1)
		}
		auth.PrintCredentialsToStdout(genUser, genPass)
	}

	// Convert compression config to storage format.
	compressor := storage.CompressionConfig{
		Enabled:   cfg.Storage.Compression.Enabled,
		Algorithm: cfg.Storage.Compression.Algorithm,
		MinSize:   cfg.Storage.Compression.MinSize,
	}
	if compressor.MinSize <= 0 {
		compressor.MinSize = 1024
	}
	if compressor.Algorithm == "" {
		compressor.Algorithm = "gzip"
	}

	// Initialize storage
	var store storage.Backend
	switch cfg.Storage.Type {
	case "sqlite":
		s, err := storage.NewSQLite(cfg.StoragePath(), compressor)
		if err != nil {
			slog.Error("failed to init storage", "error", err)
			os.Exit(1)
		}
		store = s
		defer store.Close()
	case "postgres":
		if cfg.Storage.Postgres == nil {
			slog.Error("postgres storage requires a 'postgres' config block")
			os.Exit(1)
		}
		s, err := storage.NewPostgres(cfg.Storage.Postgres.ConnectionString, compressor)
		if err != nil {
			slog.Error("failed to init postgres storage", "error", err)
			os.Exit(1)
		}
		store = s
		defer store.Close()
	default:
		slog.Error("unknown storage type", "type", cfg.Storage.Type)
		os.Exit(1)
	}
	// Initialize state store
	var st state.Backend
	switch cfg.StateStore.Type {
	case "memory":
		st = state.NewMemory()
		defer st.Close()
	case "redis":
		if cfg.StateStore.Redis == nil {
			slog.Error("redis state store requires a 'redis' config block")
			os.Exit(1)
		}
		s, err := state.NewRedis(cfg.StateStore.Redis.URL)
		if err != nil {
			slog.Error("failed to init redis state", "error", err)
			os.Exit(1)
		}
		st = s
		defer st.Close()
	default:
		slog.Error("unknown state store type", "type", cfg.StateStore.Type)
		os.Exit(1)
	}

	// Overlay runtime configuration from the database on top of YAML config.
	// DB values take precedence and can override any section at startup.
	if runtimeEntries, err := store.ListConfig(context.Background()); err == nil && len(runtimeEntries) > 0 {
		runtimeMap := make(map[string]json.RawMessage, len(runtimeEntries))
		for _, e := range runtimeEntries {
			runtimeMap[e.Key] = e.Value
		}
		cfg.OverlayRuntimeConfig(runtimeMap)
		slog.Info("config: overlaid runtime config entries from database", "count", len(runtimeEntries))
	} else if err != nil {
		slog.Warn("config: failed to load runtime config (continuing with YAML-only)", "error", err)
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
				slog.Error("failed to init proxy for provider", "name", pc.Name, "error", err)
				os.Exit(1)
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
		slog.Info("Router mode enabled", "providers", len(providers))
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
			slog.Error("failed to init otel exporter", "error", err)
			os.Exit(1)
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
			slog.Warn("cost: initial pricing unavailable, using static defaults", "error", err)
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
		slog.Error("failed to init proxy", "error", err)
		os.Exit(1)
	}

	// Prometheus metrics registry.
	metricsRegistry := metrics.NewMetrics()

	// HTTP server
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)

	// Prometheus metrics endpoint (no auth).
	r.Get("/metrics", metricsRegistry.Handler().ServeHTTP)

	// OpenAPI spec endpoint.
	r.Get("/api/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-yaml")
		w.Write(openAPISpec)
	})

	// Swagger UI at /api/docs (no auth).
	r.Get("/api/docs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<!DOCTYPE html>
<html>
<head><title>API Docs</title>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui.css">
</head>
<body>
<div id="swagger-ui"></div>
<script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
<script>SwaggerUIBundle({url: "/api/openapi.yaml", dom_id: "#swagger-ui"})</script>
</body>
</html>`))
	})

	// SSE event broker for live updates to the web UI.
	broker := api.NewSSEBroker()

	// API routes
	apiHandler := api.NewHandler(store, st)
	apiHandler.CalculateCostFn = calculateCost
	apiHandler.Config = cfg
	apiHandler.AuthSecret = authSecret
	apiHandler.CredsFile = credsFile
	apiHandler.Dispatcher = disp
	apiHandler.Broker = broker
	if rm != nil {
		apiHandler.KeyManager = rm.keyManager
	}
	apiHandler.Register(r)
	r.Get("/api/events", broker.ServeHTTP)

	// WaitGroup to track in-flight requests for graceful shutdown.
	var wg sync.WaitGroup

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

		wg.Add(1)
		logger := slog.With("request_id", reqCtx.ID, "session_id", reqCtx.SessionID)

		hookResult, err := disp.ExecuteOnRequest(reqCtx)
		if err != nil {
			logger.Error("plugin error", "error", err)
			writeAnthropicError(w, "internal error", http.StatusInternalServerError, 0, "")
			wg.Done()
			return
		}
		if hookResult != nil && hookResult.Block {
			logger.Warn("request blocked",
				"reason", hookResult.Reason,
				"status_code", hookResult.StatusCode,
			)
			writeAnthropicError(w, hookResult.Reason, hookResult.StatusCode, hookResult.RetryAfterSec, hookResult.ErrorType)
			wg.Done()
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
				logger.Error("proxy stream error", "error", err)
				wg.Done()
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
				logger.Error("proxy error", "error", err)
				http.Error(w, "upstream error", http.StatusBadGateway)
				wg.Done()
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
			logger.Error("plugin response error", "error", err)
		}

		// Save request to storage, record metrics, and notify SSE clients (async, best-effort)
		go func() {
			defer wg.Done()
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
			// Calculate cost if cost tracker is available.
			var costUSD float64
			if calculateCost != nil {
				costUSD = calculateCost(respCtx.Model, respCtx.Usage.InputTokens, respCtx.Usage.OutputTokens)
			}
			if err := store.SaveRequest(context.Background(), storedReq); err != nil {
				logger.Error("failed to save request", "error", err)
			}
			// Record Prometheus metrics (non-blocking).
			metricsRegistry.RecordRequest(context.Background(), storedReq, costUSD)
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

	// Graceful shutdown with configurable timeout.
	shutdownTimeout := time.Duration(cfg.ShutdownTimeoutSec) * time.Second
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		slog.Info("shutting down", "signal", sig.String(), "timeout", shutdownTimeout)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		// Stop accepting new connections and drain existing ones.
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("server shutdown error", "error", err)
		}

		// Wait for in-flight requests to complete (with remaining timeout).
		slog.Info("waiting for in-flight requests to complete")
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()
		select {
		case <-done:
			slog.Info("all in-flight requests completed")
		case <-shutdownCtx.Done():
			slog.Warn("shutdown timeout waiting for in-flight requests")
		}

		slog.Info("shutdown complete")
	}()

	slog.Info("server starting",
		"listen", cfg.Listen,
		"upstream", cfg.Upstream,
	)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}



// Package api implements the REST API and SSE broker for the LLM Interceptor
// web UI. It provides endpoints for querying stored requests, sessions, and
// statistics, as well as real-time event streaming.
package api

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chingjustwe/llm-interceptor/internal/auth"
	"github.com/chingjustwe/llm-interceptor/internal/config"
	"github.com/chingjustwe/llm-interceptor/internal/proxy"
	"github.com/chingjustwe/llm-interceptor/internal/router"
	"github.com/chingjustwe/llm-interceptor/internal/state"
	"github.com/chingjustwe/llm-interceptor/internal/storage"
	"github.com/chingjustwe/llm-interceptor/internal/types"
	"github.com/go-chi/chi/v5"
)

// ConfigChangeEvent is published via SSE when a runtime config value is updated.
type ConfigChangeEvent struct {
	Type      string `json:"type"`
	Key       string `json:"key"`
	UpdatedAt int64  `json:"updated_at"`
}

// CostCalculator computes the USD cost of an LLM request from model and tokens.
// If nil, the handler falls back to a simple static estimate.
type CostCalculator func(model string, inputTokens, outputTokens int) float64

// Handler provides HTTP endpoints for the web UI to query stored requests,
// sessions, and aggregate statistics, as well as manage API keys.
type Handler struct {
	store           storage.Backend
	st              state.Backend
	Config          *config.Config   // gateway configuration for agent info
	KeyManager      *router.KeyManager // nil when router mode is disabled
	CalculateCostFn CostCalculator
	AuthSecret      string // HMAC-SHA256 key for signing admin JWT tokens
	CredsFile       string // path to the admin credentials file
	Broker          *SSEBroker // for publishing config change events (optional)
}

// NewHandler creates an API handler backed by the given storage and state backends.
func NewHandler(store storage.Backend, st state.Backend) *Handler {
	return &Handler{store: store, st: st}
}

// Register mounts all API routes on the given chi router:
//
//	GET  /api/requests            — list requests with pagination
//	GET  /api/requests/{id}       — get a single request
//	GET  /api/sessions/{id}/requests — list requests for a session
//	GET  /api/sessions            — list all sessions
//	GET  /api/stats               — aggregate statistics
//	POST /api/keys                — generate a new API key
//	GET  /api/keys                — list all API keys
//	PATCH /api/keys/{id}/disable  — disable an API key
//	GET  /api/agents/info         — agent platform integration info
func (h *Handler) Register(r chi.Router) {
	r.Get("/api/requests", h.listRequests)
	r.Get("/api/requests/export", h.exportRequests)
	r.Get("/api/requests/{id}", h.getRequest)
	r.Get("/api/sessions/{id}/requests", h.getSessionRequests)
	r.Get("/api/sessions", h.listSessions)
	r.Get("/api/stats", h.costStats)
	r.Get("/api/stats/timeseries", h.timeseriesStats)

	// API key management (requires router mode to be enabled).
	r.Post("/api/keys", h.generateKey)
	r.Get("/api/keys", h.listKeys)
	r.Patch("/api/keys/{id}/disable", h.disableKey)

	// Agent platform integration.
	r.Get("/api/agents/info", h.agentInfo)

	// Admin console — login is unauthenticated; all other admin routes
	// are behind the requireAuth middleware.
	r.Post("/api/admin/login", h.loginHandler)
	r.Mount("/api/admin", h.adminRouter())
}

// listRequests returns a paginated list of stored requests, ordered by
// creation time descending. Accepts optional query params: limit, offset,
// model (exact match), and session_id (exact match).
func (h *Handler) listRequests(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	filter := types.RequestFilter{Limit: limit, Offset: offset}
	if m := r.URL.Query().Get("model"); m != "" {
		filter.Model = &m
	}
	if s := r.URL.Query().Get("session_id"); s != "" {
		filter.SessionID = &s
	}
	if sr := r.URL.Query().Get("stop_reason"); sr != "" {
		filter.StopReason = &sr
	}
	if et := r.URL.Query().Get("error_type"); et != "" {
		filter.ErrorType = &et
	}
	if md := r.URL.Query().Get("min_duration"); md != "" {
		if v, err := strconv.ParseInt(md, 10, 64); err == nil {
			filter.MinDuration = &v
		}
	}
	if md := r.URL.Query().Get("max_duration"); md != "" {
		if v, err := strconv.ParseInt(md, 10, 64); err == nil {
			filter.MaxDuration = &v
		}
	}
	if sc := r.URL.Query()["status_code"]; len(sc) > 0 {
		for _, s := range sc {
			if v, err := strconv.Atoi(s); err == nil {
				filter.StatusCodes = append(filter.StatusCodes, v)
			}
		}
	}
	reqs, err := h.store.QueryRequests(r.Context(), filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(reqs)
}

// exportRequests returns filtered requests as a downloadable CSV or JSON file.
// Accepts the same query params as listRequests, plus format (csv|json).
func (h *Handler) exportRequests(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	filter := types.RequestFilter{Limit: limit}
	if m := r.URL.Query().Get("model"); m != "" {
		filter.Model = &m
	}
	if s := r.URL.Query().Get("session_id"); s != "" {
		filter.SessionID = &s
	}
	if sr := r.URL.Query().Get("stop_reason"); sr != "" {
		filter.StopReason = &sr
	}
	if et := r.URL.Query().Get("error_type"); et != "" {
		filter.ErrorType = &et
	}
	if md := r.URL.Query().Get("min_duration"); md != "" {
		if v, err := strconv.ParseInt(md, 10, 64); err == nil {
			filter.MinDuration = &v
		}
	}
	if md := r.URL.Query().Get("max_duration"); md != "" {
		if v, err := strconv.ParseInt(md, 10, 64); err == nil {
			filter.MaxDuration = &v
		}
	}
	if sc := r.URL.Query()["status_code"]; len(sc) > 0 {
		for _, s := range sc {
			if v, err := strconv.Atoi(s); err == nil {
				filter.StatusCodes = append(filter.StatusCodes, v)
			}
		}
	}
	reqs, err := h.store.QueryRequests(r.Context(), filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	format := r.URL.Query().Get("format")
	if format == "json" {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="requests.json"`)
		json.NewEncoder(w).Encode(reqs)
		return
	}

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="requests.csv"`)
	wr := csv.NewWriter(w)
	wr.Write([]string{
		"id", "session_id", "model", "method", "path",
		"status_code", "duration_ms", "created_at",
		"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens",
		"stop_reason", "error_type", "error_message",
		"ttft_ms", "temperature", "top_p",
		"system_prompt", "request_params",
	})
	for _, req := range reqs {
		row := []string{
			req.ID,
			req.SessionID,
			req.Model,
			req.Method,
			req.Path,
			strconv.Itoa(req.StatusCode),
			strconv.FormatInt(req.DurationMs, 10),
			strconv.FormatInt(req.CreatedAt, 10),
			strconv.Itoa(req.Usage.InputTokens),
			strconv.Itoa(req.Usage.OutputTokens),
			strconv.Itoa(req.Usage.CacheReadTokens),
			strconv.Itoa(req.Usage.CacheCreationTokens),
			ptrStr(req.StopReason),
			ptrStr(req.ErrorType),
			ptrStr(req.ErrorMessage),
			ptrInt64Str(req.TTFTMs),
			ptrFloat64Str(req.Temperature),
			ptrFloat64Str(req.TopP),
			ptrStr(req.SystemPrompt),
			ptrStr(req.RequestParams),
		}
		wr.Write(row)
	}
	wr.Flush()
}

// ptrStr returns the string value of a *string, or "" if nil.
func ptrStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ptrInt64Str returns the formatted value of an *int64, or "" if nil.
func ptrInt64Str(n *int64) string {
	if n == nil {
		return ""
	}
	return strconv.FormatInt(*n, 10)
}

// ptrFloat64Str returns the formatted value of a *float64, or "" if nil.
func ptrFloat64Str(f *float64) string {
	if f == nil {
		return ""
	}
	return strconv.FormatFloat(*f, 'f', -1, 64)
}

// getRequest returns a single stored request by its ID.
func (h *Handler) getRequest(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	reqs, err := h.store.QueryRequests(r.Context(), types.RequestFilter{Limit: 100})
	if err != nil || len(reqs) == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	for _, req := range reqs {
		if req.ID == id {
			json.NewEncoder(w).Encode(req)
			return
		}
	}
	http.Error(w, "not found", http.StatusNotFound)
}

// getSessionRequests returns all requests belonging to a session, with
// pagination via limit and offset query parameters.
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

// listSessions aggregates stored requests by session ID and returns
// a list of session summaries (ID + request count). Accepts optional
// model query param to filter sessions by model.
func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request) {
	filter := types.RequestFilter{Limit: 1000}
	if m := r.URL.Query().Get("model"); m != "" {
		filter.Model = &m
	}
	reqs, err := h.store.QueryRequests(r.Context(), filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type sessionAgg struct {
		Count       int
		TotalTokens int64
		TotalCost   float64
		DurationSum int64
		ModelSet    map[string]struct{}
		ErrorCount  int
	}
	sessionMap := make(map[string]*sessionAgg)
	for _, req := range reqs {
		if req.SessionID == "" {
			continue
		}
		agg, ok := sessionMap[req.SessionID]
		if !ok {
			agg = &sessionAgg{ModelSet: make(map[string]struct{})}
			sessionMap[req.SessionID] = agg
		}
		agg.Count++
		tokens := int64(req.Usage.InputTokens + req.Usage.OutputTokens +
			req.Usage.CacheReadTokens + req.Usage.CacheCreationTokens)
		agg.TotalTokens += tokens
		agg.DurationSum += req.DurationMs
		agg.ModelSet[req.Model] = struct{}{}
		if h.CalculateCostFn != nil {
			agg.TotalCost += h.CalculateCostFn(req.Model, req.Usage.InputTokens, req.Usage.OutputTokens)
		} else {
			total := float64(req.Usage.InputTokens + req.Usage.OutputTokens)
			agg.TotalCost += total / 1_000_000 * 2.0
		}
		if req.StatusCode >= 400 {
			agg.ErrorCount++
		}
	}

	type sessionSummary struct {
		ID          string   `json:"id"`
		Count       int      `json:"count"`
		TotalTokens int64    `json:"total_tokens"`
		TotalCost   float64  `json:"total_cost"`
		AvgDuration float64  `json:"avg_duration"`
		ModelCount  int      `json:"model_count"`
		Models      []string `json:"models"`
		ErrorCount  int      `json:"error_count"`
	}
	summaries := make([]sessionSummary, 0, len(sessionMap))
	for id, agg := range sessionMap {
		var avgDuration float64
		if agg.Count > 0 {
			avgDuration = float64(agg.DurationSum) / float64(agg.Count)
		}
		avgDuration = float64(int64(avgDuration*10+0.5)) / 10.0
		models := make([]string, 0, len(agg.ModelSet))
		for m := range agg.ModelSet {
			models = append(models, m)
		}
		summaries = append(summaries, sessionSummary{
			ID:          id,
			Count:       agg.Count,
			TotalTokens: agg.TotalTokens,
			TotalCost:   round2(agg.TotalCost),
			AvgDuration: avgDuration,
			ModelCount:  len(agg.ModelSet),
			Models:      models,
			ErrorCount:  agg.ErrorCount,
		})
	}
	json.NewEncoder(w).Encode(summaries)
}

// costStats returns aggregate cost and usage statistics, including daily/total
// cost (in USD) from the state store and per-model breakdowns from the storage
// backend. Costs are stored as microdollars and converted to dollars here.
func (h *Handler) costStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	today := time.Now().Format("2006-01-02")
	dailyKey := "cost:daily:" + today

	dailyCostMicro, _ := h.st.Get(ctx, dailyKey)
	totalCostMicro, _ := h.st.Get(ctx, "cost:total")

	reqs, err := h.store.QueryRequests(ctx, types.RequestFilter{Limit: 10000})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var totalTokens int64
	type modelStat struct {
		Requests   int     `json:"requests"`
		Tokens     int64   `json:"tokens"`
		CostUSD    float64 `json:"cost_usd"`
		ErrorCount int     `json:"-"`
	}
	modelStats := make(map[string]*modelStat)
	for _, req := range reqs {
		tokens := int64(req.Usage.InputTokens + req.Usage.OutputTokens +
			req.Usage.CacheReadTokens + req.Usage.CacheCreationTokens)
		totalTokens += tokens

		entry := modelStats[req.Model]
		if entry == nil {
			entry = &modelStat{}
			modelStats[req.Model] = entry
		}
		entry.Requests++
		entry.Tokens += tokens
		if req.StatusCode >= 400 {
			entry.ErrorCount++
		}

		if h.CalculateCostFn != nil {
			entry.CostUSD += h.CalculateCostFn(req.Model, req.Usage.InputTokens, req.Usage.OutputTokens)
		} else {
			total := float64(req.Usage.InputTokens + req.Usage.OutputTokens)
			entry.CostUSD += total / 1_000_000 * 2.0
		}
	}

	var durations []int64
	var sumDuration int64
	for _, req := range reqs {
		if req.DurationMs > 0 {
			durations = append(durations, req.DurationMs)
			sumDuration += req.DurationMs
		}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })

	var avgLatency float64
	var p50, p95, p99 int64
	if len(durations) > 0 {
		avgLatency = float64(sumDuration) / float64(len(durations))
		p50 = percentile(durations, 0.5)
		p95 = percentile(durations, 0.95)
		p99 = percentile(durations, 0.99)
	}

	errorCount := 0
	errorCounts := make(map[string]int)
	for _, req := range reqs {
		if req.StatusCode >= 400 {
			errorCount++
			if req.ErrorType != nil {
				errorCounts[*req.ErrorType]++
			} else if req.Response != "" {
				eType, _ := proxy.ExtractError([]byte(req.Response))
				if eType != "" {
					errorCounts[eType]++
				}
			}
		}
	}

	perModel := make([]map[string]any, 0, len(modelStats))
	for model, stats := range modelStats {
		perModel = append(perModel, map[string]any{
			"model":      model,
			"requests":   stats.Requests,
			"tokens":     stats.Tokens,
			"cost_usd":   round2(stats.CostUSD),
			"error_rate": round2(float64(stats.ErrorCount) / float64(stats.Requests)),
		})
	}

	var errorRate float64
	if len(reqs) > 0 {
		errorRate = float64(errorCount) / float64(len(reqs))
	}
	json.NewEncoder(w).Encode(map[string]any{
		"daily_cost":     microToDollar(dailyCostMicro),
		"total_cost":     microToDollar(totalCostMicro),
		"total_requests": len(reqs),
		"total_tokens":   totalTokens,
		"per_model":      perModel,
		"error_rate":     round2(errorRate),
		"error_counts":   errorCounts,
		"avg_latency_ms": avgLatency,
		"p50_latency":    p50,
		"p95_latency":    p95,
		"p99_latency":    p99,

	})
}

// timeseriesStats returns per-bucket request count, token count, cost, and
// error count for a given time range. Buckets are aligned to the specified
// granularity (minute, hour, or day).
func (h *Handler) timeseriesStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := time.Now().UnixMilli()

	from := now - 24*60*60*1000 // default: 24 hours ago
	if f := r.URL.Query().Get("from"); f != "" {
		if v, err := strconv.ParseInt(f, 10, 64); err == nil {
			from = v
		}
	}
	to := now
	if t := r.URL.Query().Get("to"); t != "" {
		if v, err := strconv.ParseInt(t, 10, 64); err == nil {
			to = v
		}
	}

	if from >= to {
		http.Error(w, `{"error":"from must be before to"}`, http.StatusBadRequest)
		return
	}

	granularity := r.URL.Query().Get("granularity")
	if granularity == "" {
		granularity = "hour"
	}

	var bucketMs int64
	switch granularity {
	case "minute":
		bucketMs = 60 * 1000
	case "hour":
		bucketMs = 60 * 60 * 1000
	case "day":
		bucketMs = 24 * 60 * 60 * 1000
	default:
		http.Error(w, `{"error":"invalid granularity"}`, http.StatusBadRequest)
		return
	}

	filter := types.RequestFilter{
		From:  &from,
		To:    &to,
		Limit: 100000,
	}
	reqs, err := h.store.QueryRequests(ctx, filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type bucket struct {
		timestamp int64
		requests  int
		tokens    int64
		cost      float64
		errors    int
	}
	buckets := make(map[int64]*bucket)
	for _, req := range reqs {
		ts := (req.CreatedAt / bucketMs) * bucketMs
		b := buckets[ts]
		if b == nil {
			b = &bucket{timestamp: ts}
			buckets[ts] = b
		}
		b.requests++
		tokens := int64(req.Usage.InputTokens + req.Usage.OutputTokens +
			req.Usage.CacheReadTokens + req.Usage.CacheCreationTokens)
		b.tokens += tokens

		if h.CalculateCostFn != nil {
			b.cost += h.CalculateCostFn(req.Model, req.Usage.InputTokens, req.Usage.OutputTokens)
		} else {
			total := float64(req.Usage.InputTokens + req.Usage.OutputTokens)
			b.cost += total / 1_000_000 * 2.0
		}

		if req.StatusCode >= 400 {
			b.errors++
		}
	}

	sorted := make([]*bucket, 0, len(buckets))
	for _, b := range buckets {
		sorted = append(sorted, b)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].timestamp < sorted[j].timestamp
	})

	type point struct {
		Timestamp int64   `json:"timestamp"`
		Requests  int     `json:"requests"`
		Tokens    int64   `json:"tokens"`
		CostUSD   float64 `json:"cost_usd"`
		Errors    int     `json:"errors"`
	}
	points := make([]point, len(sorted))
	for i, b := range sorted {
		points[i] = point{
			Timestamp: b.timestamp,
			Requests:  b.requests,
			Tokens:    b.tokens,
			CostUSD:   round2(b.cost),
			Errors:    b.errors,
		}
	}

	json.NewEncoder(w).Encode(map[string]any{
		"granularity": granularity,
		"points":      points,
	})
}

// generateKey creates a new managed API key and returns the plaintext key
// in the response. The plaintext is only available at generation time.
// Request body: {"name": "my-key"}.
func (h *Handler) generateKey(w http.ResponseWriter, r *http.Request) {
	if h.KeyManager == nil {
		http.Error(w, `{"error":"router mode not enabled"}`, http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, `{"error":"name is required"}`, http.StatusBadRequest)
		return
	}
	key, err := h.KeyManager.Generate(r.Context(), req.Name)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"key":        key,
		"key_prefix": key[:12],
		"name":       req.Name,
	})
}

// listKeys returns all stored API keys with their prefix, name, and status.
// The full key and hash are never exposed.
func (h *Handler) listKeys(w http.ResponseWriter, r *http.Request) {
	if h.KeyManager == nil {
		http.Error(w, `{"error":"router mode not enabled"}`, http.StatusServiceUnavailable)
		return
	}
	keys, err := h.store.ListAPIKeys(r.Context())
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	// Strip sensitive fields before sending to the client.
	type safeKey struct {
		ID        string `json:"id"`
		KeyPrefix string `json:"key_prefix"`
		Name      string `json:"name"`
		Enabled   bool   `json:"enabled"`
		CreatedAt int64  `json:"created_at"`
	}
	result := make([]safeKey, len(keys))
	for i, k := range keys {
		result[i] = safeKey{
			ID:        k.ID,
			KeyPrefix: k.KeyPrefix,
			Name:      k.Name,
			Enabled:   k.Enabled,
			CreatedAt: k.CreatedAt,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// disableKey marks an API key as disabled so it can no longer authenticate
// requests. The key record is preserved for audit purposes.
func (h *Handler) disableKey(w http.ResponseWriter, r *http.Request) {
	if h.KeyManager == nil {
		http.Error(w, `{"error":"router mode not enabled"}`, http.StatusServiceUnavailable)
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, `{"error":"key id is required"}`, http.StatusBadRequest)
		return
	}
	if err := h.store.DisableAPIKey(r.Context(), id); err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// agentInfo returns information about the gateway's supported protocols,
// available models, and connection details for agent platform integration.
func (h *Handler) agentInfo(w http.ResponseWriter, r *http.Request) {
	var protocols []string
	models := make([]string, 0)
	routerEnabled := false

	if h.Config != nil && h.Config.Router.Enabled {
		routerEnabled = true
		protocolSet := make(map[string]struct{})
		for _, p := range h.Config.Router.Providers {
			switch p.Name {
			case "anthropic", "claude":
				protocolSet["anthropic"] = struct{}{}
			default:
				protocolSet["openai"] = struct{}{}
			}
			if p.ModelGlob != "" {
				models = append(models, p.ModelGlob)
			}
		}
		for k := range protocolSet {
			protocols = append(protocols, k)
		}
		sort.Strings(protocols)
	}

	if len(protocols) == 0 {
		protocols = []string{"anthropic"}
	}

	baseURL := "http://" + "127.0.0.1:8080"
	if h.Config != nil && h.Config.Listen != "" {
		baseURL = "http://" + h.Config.Listen
	}

	resp := map[string]any{
		"supported_protocols": protocols,
		"models":              models,
		"router_enabled":      routerEnabled,
		"version":             "0.3.0",
		"default_base_url":    baseURL,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// listConfig returns all runtime configuration entries.
func (h *Handler) listConfig(w http.ResponseWriter, r *http.Request) {
	entries, err := h.store.ListConfig(r.Context())
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []types.ConfigEntry{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

// getConfig returns a single runtime configuration entry by key.
func (h *Handler) getConfig(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if key == "" {
		http.Error(w, `{"error":"key is required"}`, http.StatusBadRequest)
		return
	}
	entry, err := h.store.GetConfig(r.Context(), key)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	if entry == nil {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entry)
}

// putConfig upserts a runtime configuration entry. The body must contain the
// JSON value directly, e.g. {"value": {"max_cost_per_session": 1.0}}.
func (h *Handler) putConfig(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if key == "" {
		http.Error(w, `{"error":"key is required"}`, http.StatusBadRequest)
		return
	}
	var req struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if len(req.Value) == 0 {
		http.Error(w, `{"error":"value is required"}`, http.StatusBadRequest)
		return
	}
	claims := auth.UserFromContext(r.Context())
	updatedBy := ""
	if claims != nil {
		updatedBy = claims.Username
	}
	entry := &types.ConfigEntry{
		Key:       key,
		Value:     req.Value,
		UpdatedAt: time.Now().UnixMilli(),
		UpdatedBy: updatedBy,
	}
	if err := h.store.SaveConfig(r.Context(), entry); err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	// Publish config change event via SSE if broker is available.
	if h.Broker != nil {
		h.Broker.Publish(ConfigChangeEvent{
			Type:      "config_reload",
			Key:       key,
			UpdatedAt: entry.UpdatedAt,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entry)
}

// deleteConfig removes a runtime configuration entry.
func (h *Handler) deleteConfig(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if key == "" {
		http.Error(w, `{"error":"key is required"}`, http.StatusBadRequest)
		return
	}
	claims := auth.UserFromContext(r.Context())
	updatedBy := ""
	if claims != nil {
		updatedBy = claims.Username
	}
	// Read old value before deleting (for audit, will be used in Step 5).
	if err := h.store.DeleteConfig(r.Context(), key); err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	// Publish config change event via SSE if broker is available.
	if h.Broker != nil {
		h.Broker.Publish(ConfigChangeEvent{
			Type:      "config_delete",
			Key:       key,
			UpdatedAt: time.Now().UnixMilli(),
		})
	}
	// Track deletion in context metadata for future audit logging.
	_ = updatedBy
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// loginHandler authenticates an admin user and returns a signed JWT token.
// POST /api/admin/login
// Request:  {"username":"...","password":"..."}
// Response: {"token":"...","expires_at":"..."}
func (h *Handler) loginHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.Username == "" || req.Password == "" {
		http.Error(w, `{"error":"username and password are required"}`, http.StatusBadRequest)
		return
	}
	credUsername, credHash, err := auth.LoadCredentials(h.CredsFile)
	if err != nil {
		http.Error(w, `{"error":"server configuration error"}`, http.StatusInternalServerError)
		return
	}
	if req.Username != credUsername || !auth.CheckPassword(req.Password, credHash) {
		http.Error(w, `{"error":"invalid credentials"}`, http.StatusUnauthorized)
		return
	}
	token, exp, err := auth.GenerateToken(req.Username, "admin", h.AuthSecret, 24*time.Hour)
	if err != nil {
		http.Error(w, `{"error":"failed to generate token"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"token":      token,
		"expires_at": exp.Format(time.RFC3339),
	})
}

// requireAuth is HTTP middleware that validates a Bearer JWT token from the
// Authorization header. On success, the parsed claims are injected into the
// request context via auth.ContextWithUser.
func (h *Handler) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if header == "" || !strings.HasPrefix(header, "Bearer ") {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		tokenStr := strings.TrimPrefix(header, "Bearer ")
		claims, err := auth.ValidateToken(tokenStr, h.AuthSecret)
		if err != nil {
			http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
			return
		}
		ctx := auth.ContextWithUser(r.Context(), claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// adminRouter creates a chi sub-router with the requireAuth middleware. All
// admin console endpoints (except login) should be mounted on this router.
func (h *Handler) adminRouter() chi.Router {
	r := chi.NewRouter()
	r.Use(h.requireAuth)

	// Runtime configuration CRUD
	r.Get("/config", h.listConfig)
	r.Get("/config/{key}", h.getConfig)
	r.Put("/config/{key}", h.putConfig)
	r.Delete("/config/{key}", h.deleteConfig)

	return r
}

// microToDollar converts microdollars to dollars (μ$ ÷ 1,000,000).
func microToDollar(micro int64) float64 {
	return float64(micro) / 1_000_000
}

// round2 rounds f to 2 decimal places.
func round2(f float64) float64 {
	return float64(int64(f*100+0.5)) / 100
}

// percentile returns the p-th percentile value from a sorted slice of int64
// durations. Returns 0 if the slice is empty.
func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	if idx < 0 {
		idx = 0
	}
	return sorted[idx]
}

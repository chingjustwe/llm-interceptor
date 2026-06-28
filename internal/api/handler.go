// Package api implements the REST API and SSE broker for the LLM Interceptor
// web UI. It provides endpoints for querying stored requests, sessions, and
// statistics, as well as real-time event streaming.
package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/chingjustwe/llm-interceptor/internal/proxy"
	"github.com/chingjustwe/llm-interceptor/internal/router"
	"github.com/chingjustwe/llm-interceptor/internal/state"
	"github.com/chingjustwe/llm-interceptor/internal/storage"
	"github.com/chingjustwe/llm-interceptor/internal/types"
	"github.com/go-chi/chi/v5"
)

// CostCalculator computes the USD cost of an LLM request from model and tokens.
// If nil, the handler falls back to a simple static estimate.
type CostCalculator func(model string, inputTokens, outputTokens int) float64

// Handler provides HTTP endpoints for the web UI to query stored requests,
// sessions, and aggregate statistics, as well as manage API keys.
type Handler struct {
	store           storage.Backend
	st              state.Backend
	KeyManager      *router.KeyManager // nil when router mode is disabled
	CalculateCostFn CostCalculator
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
func (h *Handler) Register(r chi.Router) {
	r.Get("/api/requests", h.listRequests)
	r.Get("/api/requests/{id}", h.getRequest)
	r.Get("/api/sessions/{id}/requests", h.getSessionRequests)
	r.Get("/api/sessions", h.listSessions)
	r.Get("/api/stats", h.costStats)

	// API key management (requires router mode to be enabled).
	r.Post("/api/keys", h.generateKey)
	r.Get("/api/keys", h.listKeys)
	r.Patch("/api/keys/{id}/disable", h.disableKey)
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

// microToDollar converts microdollars to dollars (μ$ ÷ 1,000,000).
func microToDollar(micro int64) float64 {
	return float64(micro) / 1_000_000
}

// round2 rounds f to 2 decimal places.
func round2(f float64) float64 {
	return float64(int64(f*100+0.5)) / 100
}

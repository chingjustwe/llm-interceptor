// Package api implements the REST API and SSE broker for the LLM Interceptor
// web UI. It provides endpoints for querying stored requests, sessions, and
// statistics, as well as real-time event streaming.
package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/chingjustwe/llm-interceptor/internal/state"
	"github.com/chingjustwe/llm-interceptor/internal/storage"
	"github.com/chingjustwe/llm-interceptor/internal/types"
	"github.com/go-chi/chi/v5"
)

// defaultPrices is a minimal fallback pricing table used by the stats endpoint
// to estimate per-model cost from stored token usage. Values are $/1M tokens.
// This is a best-effort estimate; the accurate per-request cost is persisted
// in the state store by the cost-tracker plugin.
var defaultPrices = map[string][2]float64{
	"claude-sonnet-4-6":          {3.0, 15.0},
	"claude-sonnet-4-20250506":   {3.0, 15.0},
	"claude-3-5-sonnet-20241022": {3.0, 15.0},
	"claude-3-opus-20240229":     {15.0, 75.0},
	"claude-3-haiku-20240307":    {0.25, 1.25},
	"claude-3-5-haiku-20241022":  {0.25, 1.25},
	"deepseek-v4-flash":          {0.14, 0.28},
}

// Handler provides HTTP endpoints for the web UI to query stored requests,
// sessions, and aggregate statistics.
type Handler struct {
	store storage.Backend
	st    state.Backend
}

// NewHandler creates an API handler backed by the given storage and state backends.
func NewHandler(store storage.Backend, st state.Backend) *Handler {
	return &Handler{store: store, st: st}
}

// Register mounts all API routes on the given chi router:
//
//	GET /api/requests        — list requests with pagination
//	GET /api/requests/{id}   — get a single request
//	GET /api/sessions/{id}/requests — list requests for a session
//	GET /api/sessions        — list all sessions
//	GET /api/stats           — aggregate statistics
func (h *Handler) Register(r chi.Router) {
	r.Get("/api/requests", h.listRequests)
	r.Get("/api/requests/{id}", h.getRequest)
	r.Get("/api/sessions/{id}/requests", h.getSessionRequests)
	r.Get("/api/sessions", h.listSessions)
	r.Get("/api/stats", h.costStats)
}

// listRequests returns a paginated list of all stored requests, ordered by
// creation time descending. Supports limit and offset query parameters.
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

// listSessions aggregates all stored requests by session ID and returns
// a list of session summaries (ID + request count).
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
		Requests int     `json:"requests"`
		Tokens   int64   `json:"tokens"`
		CostUSD  float64 `json:"cost_usd"`
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

		p, ok := defaultPrices[req.Model]
		inputCost := float64(req.Usage.InputTokens) / 1_000_000 * p[0]
		outputCost := float64(req.Usage.OutputTokens) / 1_000_000 * p[1]
		if !ok {
			total := float64(req.Usage.InputTokens + req.Usage.OutputTokens)
			inputCost = total / 1_000_000 * 2.0
			outputCost = 0
		}
		entry.CostUSD += inputCost + outputCost
	}

	perModel := make([]map[string]any, 0, len(modelStats))
	for model, stats := range modelStats {
		perModel = append(perModel, map[string]any{
			"model":    model,
			"requests": stats.Requests,
			"tokens":   stats.Tokens,
			"cost_usd": round2(stats.CostUSD),
		})
	}

	json.NewEncoder(w).Encode(map[string]any{
		"daily_cost":     microToDollar(dailyCostMicro),
		"total_cost":     microToDollar(totalCostMicro),
		"total_requests": len(reqs),
		"total_tokens":   totalTokens,
		"per_model":      perModel,
	})
}

// microToDollar converts microdollars to dollars (μ$ ÷ 1,000,000).
func microToDollar(micro int64) float64 {
	return float64(micro) / 1_000_000
}

// round2 rounds f to 2 decimal places.
func round2(f float64) float64 {
	return float64(int64(f*100+0.5)) / 100
}

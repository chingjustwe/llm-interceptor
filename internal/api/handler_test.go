// Package api tests the REST handler with a mock storage backend.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chingjustwe/llm-interceptor/internal/auth"
	"github.com/chingjustwe/llm-interceptor/internal/config"
	"github.com/chingjustwe/llm-interceptor/internal/state"
	"github.com/chingjustwe/llm-interceptor/internal/storage"
	"github.com/chingjustwe/llm-interceptor/internal/types"
	"github.com/go-chi/chi/v5"
)

// mockStore is an in-memory storage backend for handler tests.
type mockStore struct {
	requests []types.StoredRequest
	config   map[string]*types.ConfigEntry
}

func newMockStore(reqs []types.StoredRequest) *mockStore {
	return &mockStore{requests: reqs, config: make(map[string]*types.ConfigEntry)}
}

func (m *mockStore) SaveRequest(_ context.Context, _ *types.StoredRequest) error { return nil }

func (m *mockStore) GetSessionRequests(_ context.Context, sessionID string, limit, offset int) ([]types.StoredRequest, error) {
	var result []types.StoredRequest
	for _, r := range m.requests {
		if r.SessionID == sessionID {
			result = append(result, r)
		}
	}
	// Apply pagination
	if offset > len(result) {
		return nil, nil
	}
	result = result[offset:]
	if limit > 0 && limit < len(result) {
		result = result[:limit]
	}
	return result, nil
}

// QueryRequests simulates the LIKE filtering now done at the storage layer.
func (m *mockStore) QueryRequests(_ context.Context, filter types.RequestFilter) ([]types.StoredRequest, error) {
	var result []types.StoredRequest
	for _, r := range m.requests {
		if filter.SessionID != nil {
			if !strings.Contains(r.SessionID, *filter.SessionID) {
				continue
			}
		}
		if filter.Model != nil {
			if !strings.Contains(r.Model, *filter.Model) {
				continue
			}
		}
		if filter.From != nil && r.CreatedAt < *filter.From {
			continue
		}
		if filter.To != nil && r.CreatedAt > *filter.To {
			continue
		}
		if filter.StopReason != nil {
			if r.StopReason == nil || *r.StopReason != *filter.StopReason {
				continue
			}
		}
		if filter.ErrorType != nil {
			if r.ErrorType == nil || *r.ErrorType != *filter.ErrorType {
				continue
			}
		}
		if filter.MinDuration != nil && r.DurationMs < *filter.MinDuration {
			continue
		}
		if filter.MaxDuration != nil && r.DurationMs > *filter.MaxDuration {
			continue
		}
		if len(filter.StatusCodes) > 0 {
			match := false
			for _, sc := range filter.StatusCodes {
				if r.StatusCode == sc {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		result = append(result, r)
	}
	// Apply offset & limit
	if filter.Offset > len(result) {
		return nil, nil
	}
	result = result[filter.Offset:]
	if filter.Limit > 0 && filter.Limit < len(result) {
		result = result[:filter.Limit]
	}
	return result, nil
}

func (m *mockStore) SaveAPIKey(_ context.Context, _ *storage.APIKey) error { return nil }
func (m *mockStore) GetAPIKeyByPrefix(_ context.Context, _ string) (*storage.APIKey, error) {
	return nil, nil
}
func (m *mockStore) ListAPIKeys(_ context.Context) ([]storage.APIKey, error) { return nil, nil }
func (m *mockStore) DisableAPIKey(_ context.Context, _ string) error        { return nil }
func (m *mockStore) SaveConfig(_ context.Context, entry *types.ConfigEntry) error {
	m.config[entry.Key] = &types.ConfigEntry{
		Key:       entry.Key,
		Value:     append(json.RawMessage{}, entry.Value...),
		UpdatedAt: entry.UpdatedAt,
		UpdatedBy: entry.UpdatedBy,
	}
	return nil
}
func (m *mockStore) GetConfig(_ context.Context, key string) (*types.ConfigEntry, error) {
	e, ok := m.config[key]
	if !ok {
		return nil, nil
	}
	return &types.ConfigEntry{
		Key:       e.Key,
		Value:     append(json.RawMessage{}, e.Value...),
		UpdatedAt: e.UpdatedAt,
		UpdatedBy: e.UpdatedBy,
	}, nil
}
func (m *mockStore) ListConfig(_ context.Context) ([]types.ConfigEntry, error) {
	var entries []types.ConfigEntry
	for _, e := range m.config {
		entries = append(entries, *e)
	}
	return entries, nil
}
func (m *mockStore) DeleteConfig(_ context.Context, key string) error {
	delete(m.config, key)
	return nil
}
func (m *mockStore) Close() error                                           { return nil }

// mockState is a no-op state backend for handler tests.
type mockState struct{}

func (m *mockState) Increment(_ context.Context, _ string, _ int64) (int64, error) { return 0, nil }
func (m *mockState) Get(_ context.Context, _ string) (int64, error)                { return 0, nil }
func (m *mockState) Reset(_ context.Context, _ string) error                       { return nil }
func (m *mockState) IncrementWithTTL(_ context.Context, _ string, _ int64, _ int64) (int64, error) {
	return 0, nil
}
func (m *mockState) GetMany(_ context.Context, _ []string) (map[string]int64, error) {
	return nil, nil
}
func (m *mockState) Close() error { return nil }

var _ state.Backend = (*mockState)(nil)

func setupTestHandler(reqs []types.StoredRequest) *Handler {
	return NewHandler(newMockStore(reqs), &mockState{})
}

func TestListRequests_NoFilter(t *testing.T) {
	reqs := []types.StoredRequest{
		{ID: "r1", Model: "claude-sonnet-4", SessionID: "sess_a", DurationMs: 100, StatusCode: 200, CreatedAt: 1000},
		{ID: "r2", Model: "gpt-4o", SessionID: "sess_b", DurationMs: 200, StatusCode: 200, CreatedAt: 2000},
		{ID: "r3", Model: "claude-haiku", SessionID: "sess_a", DurationMs: 50, StatusCode: 429, CreatedAt: 3000},
	}
	h := setupTestHandler(reqs)

	r := httptest.NewRequest("GET", "/api/requests", nil)
	w := httptest.NewRecorder()
	h.listRequests(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var got []types.StoredRequest
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(got))
	}
}

func TestListRequests_FilterByModel(t *testing.T) {
	reqs := []types.StoredRequest{
		{ID: "r1", Model: "claude-sonnet-4", SessionID: "sess_a"},
		{ID: "r2", Model: "gpt-4o", SessionID: "sess_b"},
		{ID: "r3", Model: "claude-haiku", SessionID: "sess_a"},
	}
	h := setupTestHandler(reqs)

	r := httptest.NewRequest("GET", "/api/requests?model=claude", nil)
	w := httptest.NewRecorder()
	h.listRequests(w, r)

	var got []types.StoredRequest
	json.NewDecoder(w.Body).Decode(&got)
	if len(got) != 2 {
		t.Fatalf("expected 2 claude requests, got %d", len(got))
	}
	for _, req := range got {
		if !strings.Contains(req.Model, "claude") {
			t.Errorf("unexpected model %q in filtered results", req.Model)
		}
	}
}

func TestListRequests_FilterBySessionID(t *testing.T) {
	reqs := []types.StoredRequest{
		{ID: "r1", Model: "claude-sonnet-4", SessionID: "session_alpha"},
		{ID: "r2", Model: "gpt-4o", SessionID: "session_beta"},
		{ID: "r3", Model: "claude-haiku", SessionID: "session_alpha"},
	}
	h := setupTestHandler(reqs)

	r := httptest.NewRequest("GET", "/api/requests?session_id=alpha", nil)
	w := httptest.NewRecorder()
	h.listRequests(w, r)

	var got []types.StoredRequest
	json.NewDecoder(w.Body).Decode(&got)
	if len(got) != 2 {
		t.Fatalf("expected 2 requests for alpha session, got %d", len(got))
	}
	for _, req := range got {
		if req.SessionID != "session_alpha" {
			t.Errorf("unexpected session %q in filtered results", req.SessionID)
		}
	}
}

func TestListRequests_FilterByModelAndSession(t *testing.T) {
	reqs := []types.StoredRequest{
		{ID: "r1", Model: "claude-sonnet-4", SessionID: "session_alpha"},
		{ID: "r2", Model: "gpt-4o", SessionID: "session_alpha"},
		{ID: "r3", Model: "claude-haiku", SessionID: "session_beta"},
		{ID: "r4", Model: "claude-opus", SessionID: "session_beta"},
	}
	h := setupTestHandler(reqs)

	r := httptest.NewRequest("GET", "/api/requests?model=claude&session_id=beta", nil)
	w := httptest.NewRecorder()
	h.listRequests(w, r)

	var got []types.StoredRequest
	json.NewDecoder(w.Body).Decode(&got)
	if len(got) != 2 {
		t.Fatalf("expected 2 requests (r3 claude-haiku, r4 claude-opus both in beta), got %d", len(got))
	}
}

func TestListRequests_NoMatch(t *testing.T) {
	reqs := []types.StoredRequest{
		{ID: "r1", Model: "claude-sonnet-4", SessionID: "sess_a"},
	}
	h := setupTestHandler(reqs)

	r := httptest.NewRequest("GET", "/api/requests?model=nonexistent", nil)
	w := httptest.NewRecorder()
	h.listRequests(w, r)

	var got []types.StoredRequest
	json.NewDecoder(w.Body).Decode(&got)
	if len(got) != 0 {
		t.Fatalf("expected 0 results, got %d", len(got))
	}
}

func TestListSessions_FilterByModel(t *testing.T) {
	reqs := []types.StoredRequest{
		{ID: "r1", Model: "claude-sonnet-4", SessionID: "sess_a"},
		{ID: "r2", Model: "gpt-4o", SessionID: "sess_b"},
		{ID: "r3", Model: "claude-haiku", SessionID: "sess_a"},
		{ID: "r4", Model: "gpt-4o-mini", SessionID: "sess_c"},
	}
	h := setupTestHandler(reqs)

	r := httptest.NewRequest("GET", "/api/sessions?model=gpt", nil)
	w := httptest.NewRecorder()
	h.listSessions(w, r)

	var got []struct {
		ID    string `json:"id"`
		Count int    `json:"count"`
	}
	json.NewDecoder(w.Body).Decode(&got)

	// Only sess_b (gpt-4o) and sess_c (gpt-4o-mini) should match.
	if len(got) != 2 {
		t.Fatalf("expected 2 sessions, got %d: %+v", len(got), got)
	}
	// sess_a should not appear (its models don't contain "gpt")
	for _, s := range got {
		if s.ID == "sess_a" {
			t.Errorf("sess_a should not appear in gpt-filtered sessions")
		}
	}
}

func TestListRequests_FilterByStopReason(t *testing.T) {
	reqs := []types.StoredRequest{
		{ID: "r1", Model: "claude", StopReason: strPtr("end_turn"), DurationMs: 100, StatusCode: 200},
		{ID: "r2", Model: "gpt-4", StopReason: strPtr("max_tokens"), DurationMs: 200, StatusCode: 200},
		{ID: "r3", Model: "claude", StopReason: strPtr("end_turn"), DurationMs: 50, StatusCode: 200},
	}
	h := setupTestHandler(reqs)

	r := httptest.NewRequest("GET", "/api/requests?stop_reason=end_turn", nil)
	w := httptest.NewRecorder()
	h.listRequests(w, r)

	var got []types.StoredRequest
	json.NewDecoder(w.Body).Decode(&got)
	if len(got) != 2 {
		t.Fatalf("expected 2 requests with stop_reason=end_turn, got %d", len(got))
	}
}

func TestListRequests_FilterByErrorType(t *testing.T) {
	reqs := []types.StoredRequest{
		{ID: "r1", Model: "claude", ErrorType: strPtr("rate_limit_error"), StatusCode: 429},
		{ID: "r2", Model: "gpt-4", ErrorType: strPtr("invalid_request_error"), StatusCode: 400},
		{ID: "r3", Model: "claude", StatusCode: 200},
	}
	h := setupTestHandler(reqs)

	r := httptest.NewRequest("GET", "/api/requests?error_type=rate_limit_error", nil)
	w := httptest.NewRecorder()
	h.listRequests(w, r)

	var got []types.StoredRequest
	json.NewDecoder(w.Body).Decode(&got)
	if len(got) != 1 {
		t.Fatalf("expected 1 request with error_type=rate_limit_error, got %d", len(got))
	}
}

func TestListRequests_FilterByDurationRange(t *testing.T) {
	reqs := []types.StoredRequest{
		{ID: "r1", Model: "claude", DurationMs: 50, StatusCode: 200},
		{ID: "r2", Model: "gpt-4", DurationMs: 150, StatusCode: 200},
		{ID: "r3", Model: "claude", DurationMs: 300, StatusCode: 200},
	}
	h := setupTestHandler(reqs)

	r := httptest.NewRequest("GET", "/api/requests?min_duration=100&max_duration=200", nil)
	w := httptest.NewRecorder()
	h.listRequests(w, r)

	var got []types.StoredRequest
	json.NewDecoder(w.Body).Decode(&got)
	if len(got) != 1 {
		t.Fatalf("expected 1 request in duration range 100-200, got %d", len(got))
	}
}

func TestListRequests_FilterByStatusCodes(t *testing.T) {
	reqs := []types.StoredRequest{
		{ID: "r1", Model: "claude", StatusCode: 200},
		{ID: "r2", Model: "gpt-4", StatusCode: 429},
		{ID: "r3", Model: "claude", StatusCode: 400},
		{ID: "r4", Model: "gpt-4", StatusCode: 200},
	}
	h := setupTestHandler(reqs)

	r := httptest.NewRequest("GET", "/api/requests?status_code=200&status_code=429", nil)
	w := httptest.NewRecorder()
	h.listRequests(w, r)

	var got []types.StoredRequest
	json.NewDecoder(w.Body).Decode(&got)
	if len(got) != 3 {
		t.Fatalf("expected 3 requests with status 200 or 429, got %d", len(got))
	}
}

func TestCostStats_ErrorTracking(t *testing.T) {
	stopReason := "end_turn"
	reqs := []types.StoredRequest{
		{ID: "r1", Model: "claude", Usage: types.TokenUsage{InputTokens: 10, OutputTokens: 20}, StatusCode: 200, CreatedAt: 1000},
		{ID: "r2", Model: "claude", Usage: types.TokenUsage{InputTokens: 5, OutputTokens: 5}, StatusCode: 429, CreatedAt: 2000},
		{ID: "r3", Model: "gpt-4", Usage: types.TokenUsage{InputTokens: 10, OutputTokens: 10}, StatusCode: 400, CreatedAt: 3000},
		{ID: "r4", Model: "gpt-4", Usage: types.TokenUsage{InputTokens: 10, OutputTokens: 10}, StatusCode: 200, CreatedAt: 4000, StopReason: &stopReason},
	}
	h := setupTestHandler(reqs)

	r := httptest.NewRequest("GET", "/api/stats", nil)
	w := httptest.NewRecorder()
	h.costStats(w, r)

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	errorRate, ok := resp["error_rate"].(float64)
	if !ok {
		t.Fatal("expected error_rate in response")
	}
	if errorRate != 0.5 {
		t.Fatalf("expected error_rate 0.5, got %f", errorRate)
	}
	errorCounts, ok := resp["error_counts"].(map[string]any)
	if !ok {
		t.Fatal("expected error_counts in response")
	}
	if len(errorCounts) != 0 {
		t.Fatalf("expected empty error_counts (no ErrorType set), got %v", errorCounts)
	}
	perModel, ok := resp["per_model"].([]any)
	if !ok {
		t.Fatal("expected per_model in response")
	}
	for _, pm := range perModel {
		entry := pm.(map[string]any)
		er, ok := entry["error_rate"].(float64)
		if !ok {
			t.Fatal("expected error_rate per model")
		}
		if entry["model"] == "claude" && er != 0.5 {
			t.Fatalf("expected claude error_rate 0.5, got %f", er)
		}
	}
}

func TestCostStats_ErrorTrackingWithTypes(t *testing.T) {
	errType1 := "rate_limit_error"
	errType2 := "invalid_request_error"
	reqs := []types.StoredRequest{
		{ID: "r1", Model: "claude", StatusCode: 429, ErrorType: &errType1, CreatedAt: 1000},
		{ID: "r2", Model: "claude", StatusCode: 400, ErrorType: &errType2, CreatedAt: 2000},
		{ID: "r3", Model: "claude", StatusCode: 200, CreatedAt: 3000},
	}
	h := setupTestHandler(reqs)

	r := httptest.NewRequest("GET", "/api/stats", nil)
	w := httptest.NewRecorder()
	h.costStats(w, r)

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	errorCounts, ok := resp["error_counts"].(map[string]any)
	if !ok {
		t.Fatal("expected error_counts in response")
	}
	if errorCounts["rate_limit_error"] != 1.0 || errorCounts["invalid_request_error"] != 1.0 {
		t.Fatalf("expected rate_limit_error=1 and invalid_request_error=1, got %v", errorCounts)
	}
}

func TestListSessions_Aggregates(t *testing.T) {
	reqs := []types.StoredRequest{
		{
			ID: "r1", SessionID: "sess_a", Model: "claude-sonnet-4",
			Usage: types.TokenUsage{InputTokens: 100, OutputTokens: 50},
			DurationMs: 200, StatusCode: 200,
		},
		{
			ID: "r2", SessionID: "sess_a", Model: "claude-sonnet-4",
			Usage: types.TokenUsage{InputTokens: 30, OutputTokens: 20, CacheReadTokens: 10},
			DurationMs: 300, StatusCode: 200,
		},
		{
			ID: "r3", SessionID: "sess_a", Model: "gpt-4o",
			Usage: types.TokenUsage{InputTokens: 50, OutputTokens: 50, CacheCreationTokens: 25},
			DurationMs: 500, StatusCode: 429,
		},
		{
			ID: "r4", SessionID: "sess_b", Model: "claude-haiku",
			Usage: types.TokenUsage{InputTokens: 10, OutputTokens: 10},
			DurationMs: 100, StatusCode: 200,
		},
	}
	h := setupTestHandler(reqs)

	r := httptest.NewRequest("GET", "/api/sessions", nil)
	w := httptest.NewRecorder()
	h.listSessions(w, r)

	var got []struct {
		ID          string   `json:"id"`
		Count       int      `json:"count"`
		TotalTokens int64    `json:"total_tokens"`
		TotalCost   float64  `json:"total_cost"`
		AvgDuration float64  `json:"avg_duration"`
		ModelCount  int      `json:"model_count"`
		Models      []string `json:"models"`
		ErrorCount  int      `json:"error_count"`
	}
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(got))
	}

	// Find sess_a
	var sessA, sessB any
	for _, s := range got {
		switch s.ID {
		case "sess_a":
			sessA = s
		case "sess_b":
			sessB = s
		}
	}
	if sessA == nil {
		t.Fatal("sess_a not found")
	}
	if sessB == nil {
		t.Fatal("sess_b not found")
	}

	a := got[0]
	if got[0].ID != "sess_a" {
		// swap so a is sess_a
		for i := range got {
			if got[i].ID == "sess_a" {
				got[0], got[i] = got[i], got[0]
				break
			}
		}
		a = got[0]
		b := got[1]
		if a.ID != "sess_a" || b.ID != "sess_b" {
			t.Fatalf("expected sess_a and sess_b, got %s and %s", a.ID, b.ID)
		}
		_ = b
	}

	// sess_a: 3 requests
	// tokens: (100+50+0+0)=150 + (30+20+10+0)=60 + (50+50+0+25)=125 = 335
	// cost: (150/1e6)*2=0.0003 + (50/1e6)*2=0.0001 + (100/1e6)*2=0.0002 = 0.0006
	// duration: avg(200,300,500) = 1000/3 = 333.333... → 333.3
	// models: claude-sonnet-4, gpt-4o → count=2
	// errors: 1 (r3, status 429)
	if a.Count != 3 {
		t.Errorf("sess_a count: expected 3, got %d", a.Count)
	}
	if a.TotalTokens != 335 {
		t.Errorf("sess_a total_tokens: expected 335, got %d", a.TotalTokens)
	}
	if a.TotalCost != 0.0 {
		t.Errorf("sess_a total_cost: expected 0.0, got %f", a.TotalCost)
	}
	if a.AvgDuration != 333.3 {
		t.Errorf("sess_a avg_duration: expected 333.3, got %f", a.AvgDuration)
	}
	if a.ModelCount != 2 {
		t.Errorf("sess_a model_count: expected 2, got %d", a.ModelCount)
	}
	if a.ErrorCount != 1 {
		t.Errorf("sess_a error_count: expected 1, got %d", a.ErrorCount)
	}

	// sess_b: 1 request
	b := got[1]
	if b.ID != "sess_b" {
		b = got[0]
	}
	if b.Count != 1 {
		t.Errorf("sess_b count: expected 1, got %d", b.Count)
	}
	if b.TotalTokens != 20 {
		t.Errorf("sess_b total_tokens: expected 20, got %d", b.TotalTokens)
	}
	if b.AvgDuration != 100.0 {
		t.Errorf("sess_b avg_duration: expected 100.0, got %f", b.AvgDuration)
	}
	if b.ModelCount != 1 {
		t.Errorf("sess_b model_count: expected 1, got %d", b.ModelCount)
	}
	if b.ErrorCount != 0 {
		t.Errorf("sess_b error_count: expected 0, got %d", b.ErrorCount)
	}
}

func TestTimeseriesStats(t *testing.T) {
	reqs := []types.StoredRequest{
		{ID: "r1", Model: "claude", Usage: types.TokenUsage{InputTokens: 10, OutputTokens: 20}, StatusCode: 200, CreatedAt: 1000},
		{ID: "r2", Model: "claude", Usage: types.TokenUsage{InputTokens: 5, OutputTokens: 5}, StatusCode: 429, CreatedAt: 3700},
		{ID: "r3", Model: "gpt-4", Usage: types.TokenUsage{InputTokens: 10, OutputTokens: 10}, StatusCode: 200, CreatedAt: 7200},
	}
	h := setupTestHandler(reqs)

	r := httptest.NewRequest("GET", "/api/stats/timeseries?from=0&to=10000&granularity=hour", nil)
	w := httptest.NewRecorder()
	h.timeseriesStats(w, r)

	var resp struct {
		Granularity string `json:"granularity"`
		Points      []struct {
			Timestamp int64   `json:"timestamp"`
			Requests  int     `json:"requests"`
			Tokens    int64   `json:"tokens"`
			CostUSD   float64 `json:"cost_usd"`
			Errors    int     `json:"errors"`
		} `json:"points"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Granularity != "hour" {
		t.Fatalf("expected granularity hour, got %s", resp.Granularity)
	}
	// Hour bucket = 3600000ms. Requests at 1000, 3700, 7200 → all in bucket 0.
	if len(resp.Points) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(resp.Points))
	}
	p := resp.Points[0]
	if p.Requests != 3 {
		t.Errorf("expected 3 requests, got %d", p.Requests)
	}
	if p.Tokens != 60 {
		t.Errorf("expected 60 tokens, got %d", p.Tokens)
	}
	if p.Errors != 1 {
		t.Errorf("expected 1 error, got %d", p.Errors)
	}
}

func TestTimeseriesStats_FromAfterTo(t *testing.T) {
	h := setupTestHandler(nil)

	r := httptest.NewRequest("GET", "/api/stats/timeseries?from=1000&to=500", nil)
	w := httptest.NewRecorder()
	h.timeseriesStats(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestExportRequests_CSV(t *testing.T) {
	reqs := []types.StoredRequest{
		{ID: "r1", Model: "claude", Method: "POST", Path: "/v1/messages", StatusCode: 200, DurationMs: 100, CreatedAt: 1000, Usage: types.TokenUsage{InputTokens: 10, OutputTokens: 20}},
	}
	h := setupTestHandler(reqs)

	r := httptest.NewRequest("GET", "/api/requests/export", nil)
	w := httptest.NewRecorder()
	h.exportRequests(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if ct != "text/csv" {
		t.Fatalf("expected Content-Type text/csv, got %s", ct)
	}
	cd := w.Header().Get("Content-Disposition")
	if cd != `attachment; filename="requests.csv"` {
		t.Fatalf("expected Content-Disposition attachment, got %s", cd)
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, "id,session_id") {
		t.Fatalf("expected CSV header row, got %s", body[:20])
	}
	if !strings.Contains(body, "r1") {
		t.Fatalf("expected r1 in CSV body")
	}
}

func TestExportRequests_JSON(t *testing.T) {
	reqs := []types.StoredRequest{
		{ID: "r1", Model: "claude", Method: "POST", Path: "/v1/messages", StatusCode: 200, DurationMs: 100, CreatedAt: 1000, Usage: types.TokenUsage{InputTokens: 10, OutputTokens: 20}},
		{ID: "r2", Model: "gpt-4", Method: "POST", Path: "/v1/chat/completions", StatusCode: 200, DurationMs: 200, CreatedAt: 2000, Usage: types.TokenUsage{InputTokens: 5, OutputTokens: 15}},
	}
	h := setupTestHandler(reqs)

	r := httptest.NewRequest("GET", "/api/requests/export?format=json", nil)
	w := httptest.NewRecorder()
	h.exportRequests(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %s", ct)
	}
	cd := w.Header().Get("Content-Disposition")
	if cd != `attachment; filename="requests.json"` {
		t.Fatalf("expected Content-Disposition attachment, got %s", cd)
	}
	var got []types.StoredRequest
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(got))
	}
}

func TestAgentInfo_RouterEnabled(t *testing.T) {
	h := setupTestHandler(nil)
	h.Config = &config.Config{
		Listen: "127.0.0.1:9090",
		Router: config.RouterConfig{
			Enabled: true,
			Providers: []config.ProviderConfig{
				{Name: "anthropic", ModelGlob: "claude-*"},
				{Name: "openai", ModelGlob: "gpt-*"},
			},
		},
	}

	r := httptest.NewRequest("GET", "/api/agents/info", nil)
	w := httptest.NewRecorder()
	h.agentInfo(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	protocols, _ := resp["supported_protocols"].([]any)
	if len(protocols) != 2 {
		t.Fatalf("expected 2 protocols, got %d", len(protocols))
	}

	models, _ := resp["models"].([]any)
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}

	if resp["router_enabled"] != true {
		t.Fatal("expected router_enabled true")
	}
	if resp["default_base_url"] != "http://127.0.0.1:9090" {
		t.Fatalf("expected base URL http://127.0.0.1:9090, got %v", resp["default_base_url"])
	}
	if resp["version"] != "0.3.0" {
		t.Fatalf("expected version 0.3.0, got %v", resp["version"])
	}
}

func TestAgentInfo_RouterDisabled(t *testing.T) {
	h := setupTestHandler(nil)
	h.Config = &config.Config{
		Listen: "127.0.0.1:8080",
	}

	r := httptest.NewRequest("GET", "/api/agents/info", nil)
	w := httptest.NewRecorder()
	h.agentInfo(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)

	if resp["router_enabled"] != false {
		t.Fatal("expected router_enabled false")
	}
	protocols, _ := resp["supported_protocols"].([]any)
	if len(protocols) != 1 || protocols[0] != "anthropic" {
		t.Fatalf("expected [anthropic] fallback, got %v", protocols)
	}
}

func TestAgentInfo_NilConfig(t *testing.T) {
	h := setupTestHandler(nil)
	// Config is nil (not set)

	r := httptest.NewRequest("GET", "/api/agents/info", nil)
	w := httptest.NewRecorder()
	h.agentInfo(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)

	if resp["router_enabled"] != false {
		t.Fatal("expected router_enabled false when config is nil")
	}
}

// strPtr is a helper to create a *string literal.
func strPtr(s string) *string { return &s }

// Test with chi router to ensure routes are wired correctly.
func TestHandlerRegister_RoutesExist(t *testing.T) {
	h := setupTestHandler(nil)
	r := chi.NewRouter()
	h.Register(r)

	paths := []string{
		"/api/requests",
		"/api/sessions",
		"/api/stats",
		"/api/keys",
	}
	for _, p := range paths {
		req := httptest.NewRequest("GET", p, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		// The handler returns 200 even with empty data; we just check the route exists.
		if w.Code == http.StatusNotFound {
			t.Errorf("route %s returned 404", p)
		}
	}
}

func TestLoginHandler_Success(t *testing.T) {
	dir := t.TempDir()
	credsFile := filepath.Join(dir, "admin.credentials")
	hash, err := auth.HashPassword("correct-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveCredentials(credsFile, "admin_test", hash); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(newMockStore(nil), &mockState{})
	h.AuthSecret = "test-secret-key-min-32-chars!!!!!!!!"
	h.CredsFile = credsFile

	body := `{"username":"admin_test","password":"correct-password"}`
	r := httptest.NewRequest("POST", "/api/admin/login", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.loginHandler(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["token"] == "" {
		t.Fatal("expected non-empty token")
	}
	if resp["expires_at"] == "" {
		t.Fatal("expected expires_at")
	}
}

func TestLoginHandler_WrongPassword(t *testing.T) {
	dir := t.TempDir()
	credsFile := filepath.Join(dir, "admin.credentials")
	hash, _ := auth.HashPassword("correct-password")
	auth.SaveCredentials(credsFile, "admin_test", hash)
	h := NewHandler(newMockStore(nil), &mockState{})
	h.AuthSecret = "test-secret-key-min-32-chars!!!!!!!!"
	h.CredsFile = credsFile

	body := `{"username":"admin_test","password":"wrong-password"}`
	r := httptest.NewRequest("POST", "/api/admin/login", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.loginHandler(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestLoginHandler_MissingFields(t *testing.T) {
	h := NewHandler(newMockStore(nil), &mockState{})
	h.AuthSecret = "test-secret-key-min-32-chars!!!!!!!!"

	body := `{}`
	r := httptest.NewRequest("POST", "/api/admin/login", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.loginHandler(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestRequireAuth_ValidToken(t *testing.T) {
	h := &Handler{AuthSecret: "test-secret-key-min-32-chars!!!!!!!!"}
	token, _, err := auth.GenerateToken("admin", "admin", h.AuthSecret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	called := false
	handler := h.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		claims := auth.UserFromContext(r.Context())
		if claims == nil || claims.Username != "admin" {
			t.Error("expected admin claims in context")
		}
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("GET", "/api/admin/config", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if !called {
		t.Error("expected handler to be called")
	}
}

func TestRequireAuth_NoHeader(t *testing.T) {
	h := &Handler{AuthSecret: "test-secret-key-min-32-chars!!!!!!!!"}
	handler := h.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called")
	}))

	r := httptest.NewRequest("GET", "/api/admin/config", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestRequireAuth_BadToken(t *testing.T) {
	h := &Handler{AuthSecret: "test-secret-key-min-32-chars!!!!!!!!"}
	handler := h.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called")
	}))

	r := httptest.NewRequest("GET", "/api/admin/config", nil)
	r.Header.Set("Authorization", "Bearer garbage-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestRequireAuth_WrongSecret(t *testing.T) {
	token, _, _ := auth.GenerateToken("admin", "admin", "different-secret-key-32-chars!!!!!!", time.Hour)
	h := &Handler{AuthSecret: "test-secret-key-min-32-chars!!!!!!!!"}
	handler := h.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called")
	}))

	r := httptest.NewRequest("GET", "/api/admin/config", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestHandlerRegister_AdminRoutes(t *testing.T) {
	h := setupTestHandler(nil)
	h.AuthSecret = "test-secret-key-min-32-chars!!!!!!!!"
	h.CredsFile = filepath.Join(t.TempDir(), "admin.credentials")
	hash, _ := auth.HashPassword("pass")
	auth.SaveCredentials(h.CredsFile, "admin", hash)

	r := chi.NewRouter()
	h.Register(r)

	// Login should be public (no auth)
	body := `{"username":"admin","password":"pass"}`
	req := httptest.NewRequest("POST", "/api/admin/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("login should be public, got %d: %s", w.Code, w.Body.String())
	}
}

func TestConfigCRUD_AsAdmin(t *testing.T) {
	dir := t.TempDir()
	credsFile := filepath.Join(dir, "admin.credentials")
	hash, _ := auth.HashPassword("pass")
	auth.SaveCredentials(credsFile, "admin", hash)
	h := NewHandler(newMockStore(nil), &mockState{})
	h.AuthSecret = "test-secret-key-min-32-chars!!!!!!!!"
	h.CredsFile = credsFile

	// Generate a valid token
	token, _, _ := auth.GenerateToken("admin", "admin", h.AuthSecret, time.Hour)

	// Create a chi router with admin routes
	r := chi.NewRouter()
	r.Post("/api/admin/login", h.loginHandler)
	r.Mount("/api/admin", h.adminRouter())

	// Put a config value
	putBody := `{"value": {"max_cost_per_session": 1.5}}`
	req := httptest.NewRequest("PUT", "/api/admin/config/budget", strings.NewReader(putBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT config expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Get the config value back
	req = httptest.NewRequest("GET", "/api/admin/config/budget", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET config expected 200, got %d", w.Code)
	}
	var entry types.ConfigEntry
	if err := json.NewDecoder(w.Body).Decode(&entry); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if entry.Key != "budget" {
		t.Errorf("expected key budget, got %s", entry.Key)
	}

	// List config
	req = httptest.NewRequest("GET", "/api/admin/config", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("LIST config expected 200, got %d", w.Code)
	}
	var entries []types.ConfigEntry
	json.NewDecoder(w.Body).Decode(&entries)
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}

	// Delete config
	req = httptest.NewRequest("DELETE", "/api/admin/config/budget", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE config expected 200, got %d", w.Code)
	}
}

func TestConfigCRUD_Unauthenticated(t *testing.T) {
	h := NewHandler(newMockStore(nil), &mockState{})
	h.AuthSecret = "test-secret-key-min-32-chars!!!!!!!!"
	r := chi.NewRouter()
	r.Mount("/api/admin", h.adminRouter())

	// Without auth token, all admin endpoints should return 401
	for _, path := range []string{"/api/admin/config", "/api/admin/config/budget"} {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s expected 401, got %d", path, w.Code)
		}
	}
}

func TestListRequests_Pagination(t *testing.T) {
	reqs := make([]types.StoredRequest, 10)
	for i := 0; i < 10; i++ {
		reqs[i] = types.StoredRequest{
			ID:    fmt.Sprintf("r%d", i+1),
			Model: fmt.Sprintf("model-%d", i+1),
		}
	}
	h := setupTestHandler(reqs)

	r := httptest.NewRequest("GET", "/api/requests?limit=3&offset=5", nil)
	w := httptest.NewRecorder()
	h.listRequests(w, r)

	var got []types.StoredRequest
	json.NewDecoder(w.Body).Decode(&got)
	if len(got) != 3 {
		t.Fatalf("expected 3 requests (limit=3 offset=5), got %d", len(got))
	}
}

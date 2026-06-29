package storage

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/chingjustwe/llm-interceptor/internal/types"
)

func TestSQLiteBackend_ConfigCRUD(t *testing.T) {
	s, err := NewSQLite(":memory:")
	if err != nil {
		t.Fatalf("NewSQLite failed: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	// Save a config entry
	entry := &types.ConfigEntry{
		Key:       "budget",
		Value:     json.RawMessage(`{"max_cost_per_session":1.0,"max_cost_per_day":5.0}`),
		UpdatedAt: 1000,
		UpdatedBy: "admin_test",
	}
	if err := s.SaveConfig(ctx, entry); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	// Get the config entry back
	got, err := s.GetConfig(ctx, "budget")
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil entry")
	}
	if got.Key != "budget" {
		t.Errorf("expected key budget, got %s", got.Key)
	}
	if got.UpdatedBy != "admin_test" {
		t.Errorf("expected updated_by admin_test, got %s", got.UpdatedBy)
	}

	// Update the entry
	entry.Value = json.RawMessage(`{"max_cost_per_session":2.0,"max_cost_per_day":10.0}`)
	entry.UpdatedAt = 2000
	if err := s.SaveConfig(ctx, entry); err != nil {
		t.Fatalf("SaveConfig update failed: %v", err)
	}
	got, _ = s.GetConfig(ctx, "budget")
	if got == nil {
		t.Fatal("expected non-nil entry after update")
	}
	if string(got.Value) != `{"max_cost_per_session":2.0,"max_cost_per_day":10.0}` {
		t.Errorf("unexpected value after update: %s", string(got.Value))
	}
	if got.UpdatedAt != 2000 {
		t.Errorf("expected updated_at 2000, got %d", got.UpdatedAt)
	}

	// List config
	entry2 := &types.ConfigEntry{Key: "rate-limit", Value: json.RawMessage(`{"requests_per_minute":30}`), UpdatedAt: 3000, UpdatedBy: "admin_test"}
	s.SaveConfig(ctx, entry2)
	entries, err := s.ListConfig(ctx)
	if err != nil {
		t.Fatalf("ListConfig failed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// Delete config
	if err := s.DeleteConfig(ctx, "budget"); err != nil {
		t.Fatalf("DeleteConfig failed: %v", err)
	}
	got, _ = s.GetConfig(ctx, "budget")
	if got != nil {
		t.Fatal("expected nil after delete")
	}
	entries, _ = s.ListConfig(ctx)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after delete, got %d", len(entries))
	}

	// Get non-existent key
	got, err = s.GetConfig(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetConfig nonexistent failed: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil for nonexistent key")
	}
}

func TestSQLiteBackend_SaveAndQueryWithNewFields(t *testing.T) {
	s, err := NewSQLite(":memory:")
	if err != nil {
		t.Fatalf("NewSQLite failed: %v", err)
	}
	defer s.Close()

	temp := 0.7
	topP := 0.9
	ttft := int64(150)
	sr := "end_turn"
	sp := "You are helpful."
	rp := `{"max_tokens":100}`

	req := &types.StoredRequest{
		ID: "test1", Model: "claude", Method: "POST", Path: "/v1/messages",
		Request: "{}", Response: "{}", DurationMs: 500, StatusCode: 200, CreatedAt: 1000,
		Temperature: &temp, TopP: &topP, TTFTMs: &ttft, StopReason: &sr,
		SystemPrompt: &sp, RequestParams: &rp,
	}
	if err := s.SaveRequest(context.Background(), req); err != nil {
		t.Fatalf("SaveRequest failed: %v", err)
	}

	// Query with stop_reason filter
	results, err := s.QueryRequests(context.Background(), types.RequestFilter{
		StopReason: &sr, Limit: 10,
	})
	if err != nil {
		t.Fatalf("QueryRequests with stop_reason failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].StopReason == nil || *results[0].StopReason != "end_turn" {
		t.Fatal("stop_reason not stored correctly")
	}
	if results[0].Temperature == nil || *results[0].Temperature != 0.7 {
		t.Fatal("temperature not stored correctly")
	}
	if results[0].TTFTMs == nil || *results[0].TTFTMs != 150 {
		t.Fatal("ttft_ms not stored correctly")
	}

	// Query with status codes filter
	results, err = s.QueryRequests(context.Background(), types.RequestFilter{
		StatusCodes: []int{200, 201}, Limit: 10,
	})
	if err != nil {
		t.Fatalf("QueryRequests with status_codes failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for status filter, got %d", len(results))
	}
}

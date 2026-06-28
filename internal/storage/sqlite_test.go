package storage

import (
	"context"
	"testing"

	"github.com/chingjustwe/llm-interceptor/internal/types"
)

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

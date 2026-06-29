package storage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/chingjustwe/llm-interceptor/internal/types"
)

func TestCursorPagination(t *testing.T) {
	s, err := NewSQLite(":memory:", CompressionConfig{Enabled: false})
	if err != nil {
		t.Fatalf("NewSQLite failed: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	now := time.Now().UnixMilli()

	// Create 50 requests with staggered created_at descending by 100ms.
	for i := 0; i < 50; i++ {
		req := &types.StoredRequest{
			ID:        fmt.Sprintf("cursor-req-%d", i),
			Model:     "test-model",
			Method:    "POST",
			Path:      "/v1/messages",
			Request:   `{"test":true}`,
			Response:  `{"ok":true}`,
			DurationMs: int64(100 + i),
			StatusCode: 200,
			CreatedAt:  now - int64(i)*100,
		}
		if err := s.SaveRequest(ctx, req); err != nil {
			t.Fatalf("SaveRequest[%d] failed: %v", i, err)
		}
	}

	t.Run("offset-based still works", func(t *testing.T) {
		results, err := s.QueryRequests(ctx, types.RequestFilter{Limit: 10, Offset: 0})
		if err != nil {
			t.Fatalf("QueryRequests failed: %v", err)
		}
		if len(results) != 10 {
			t.Errorf("expected 10 results, got %d", len(results))
		}
	})

	t.Run("cursor pagination no duplicates", func(t *testing.T) {
		pageSize := 10
		seen := make(map[string]bool)
		var cursor *int64
		total := 0

		for {
			var results []types.StoredRequest
			var err error
			if cursor != nil {
				results, err = s.QueryRequests(ctx, types.RequestFilter{
					Limit:  pageSize,
					Cursor: cursor,
				})
			} else {
				results, err = s.QueryRequests(ctx, types.RequestFilter{
					Limit: pageSize,
				})
			}
			if err != nil {
				t.Fatalf("QueryRequests failed: %v", err)
			}
			if len(results) == 0 {
				break
			}
			for _, r := range results {
				if seen[r.ID] {
					t.Errorf("duplicate result: %s", r.ID)
				}
				seen[r.ID] = true
				total++
			}
			if len(results) < pageSize {
				break
			}
			last := results[len(results)-1].CreatedAt
			cursor = &last
		}

		if total != 50 {
			t.Errorf("expected 50 total results, got %d", total)
		}
	})

	t.Run("cursor with filter", func(t *testing.T) {
		model := "test-model"
		cursor := now - 2000
		results, err := s.QueryRequests(ctx, types.RequestFilter{
			Model:  &model,
			Limit:  100,
			Cursor: &cursor,
		})
		if err != nil {
			t.Fatalf("QueryRequests with cursor+filter failed: %v", err)
		}
		// Should return results with created_at < cursor (2000ms before latest)
		for _, r := range results {
			if r.CreatedAt >= cursor {
				t.Errorf("result %s has created_at %d >= cursor %d", r.ID, r.CreatedAt, cursor)
			}
		}
	})
}

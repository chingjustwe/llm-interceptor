package plugins

import (
	"context"
	"testing"

	"github.com/chingjustwe/llm-interceptor/internal/plugin"
	"github.com/chingjustwe/llm-interceptor/internal/state"
)

func TestRateLimitPlugin_RequestLimit_Blocks(t *testing.T) {
	st := state.NewMemory()
	defer st.Close()

	rp := NewRateLimitPlugin(st, 1, 0) // 1 request/min
	ctx := &plugin.RequestContext{Context: context.Background()}

	// First request: under limit, should not block.
	hook, err := rp.OnRequest(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hook != nil {
		t.Fatalf("expected no block on first request, got Block=%v", hook.Block)
	}

	// Second request: over limit, should block with 429 + RetryAfterSec 60.
	hook, err = rp.OnRequest(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hook == nil {
		t.Fatal("expected block on second request, got nil")
	}
	if !hook.Block {
		t.Fatal("expected Block=true")
	}
	if hook.StatusCode != 429 {
		t.Fatalf("expected status 429, got %d", hook.StatusCode)
	}
	if hook.RetryAfterSec != 60 {
		t.Fatalf("expected RetryAfterSec=60, got %d", hook.RetryAfterSec)
	}
	if hook.ErrorType != "" {
		t.Fatalf("expected empty ErrorType, got %q", hook.ErrorType)
	}
}

func TestRateLimitPlugin_RequestLimit_Zero(t *testing.T) {
	st := state.NewMemory()
	defer st.Close()

	rp := NewRateLimitPlugin(st, 0, 0) // unlimited
	for i := 0; i < 5; i++ {
		hook, err := rp.OnRequest(&plugin.RequestContext{Context: context.Background()})
		if err != nil {
			t.Fatalf("unexpected error on attempt %d: %v", i, err)
		}
		if hook != nil {
			t.Fatalf("expected no block with zero limit (attempt %d), got Block=%v", i, hook.Block)
		}
	}
}

func TestRateLimitPlugin_TokenLimit_Blocks(t *testing.T) {
	st := state.NewMemory()
	defer st.Close()

	// Pre-seed the token counter to exceed the limit.
	st.Increment(context.Background(), "ratelimit:tokens:global", 1500)

	rp := NewRateLimitPlugin(st, 0, 1000) // 1000 tokens/min
	ctx := &plugin.RequestContext{Context: context.Background()}

	hook, err := rp.OnRequest(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hook == nil {
		t.Fatal("expected block, got nil")
	}
	if !hook.Block {
		t.Fatal("expected Block=true")
	}
	if hook.StatusCode != 429 {
		t.Fatalf("expected status 429, got %d", hook.StatusCode)
	}
	if hook.RetryAfterSec != 60 {
		t.Fatalf("expected RetryAfterSec=60, got %d", hook.RetryAfterSec)
	}
}

func TestRateLimitPlugin_TokenLimit_Under(t *testing.T) {
	st := state.NewMemory()
	defer st.Close()

	// Pre-seed the token counter below the limit.
	st.Increment(context.Background(), "ratelimit:tokens:global", 500)

	rp := NewRateLimitPlugin(st, 0, 1000)
	hook, err := rp.OnRequest(&plugin.RequestContext{Context: context.Background()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hook != nil {
		t.Fatalf("expected no block, got Block=%v reason=%q", hook.Block, hook.Reason)
	}
}

func TestRateLimitPlugin_OnResponse_AccumulatesTokens(t *testing.T) {
	st := state.NewMemory()
	defer st.Close()

	rp := NewRateLimitPlugin(st, 0, 1000)

	// First response: 200 tokens.
	err := rp.OnResponse(&plugin.ResponseContext{
		Usage: plugin.Usage{InputTokens: 100, OutputTokens: 100},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the token counter increased.
	val, _ := st.Get(context.Background(), "ratelimit:tokens:global")
	if val != 200 {
		t.Fatalf("expected 200 tokens, got %d", val)
	}
}

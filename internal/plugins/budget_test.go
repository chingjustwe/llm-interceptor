package plugins

import (
	"context"
	"testing"
	"time"

	"github.com/chingjustwe/llm-interceptor/internal/plugin"
	"github.com/chingjustwe/llm-interceptor/internal/state"
)

func todayKey() string {
	return "cost:daily:" + time.Now().UTC().Format("2006-01-02")
}

func TestBudgetPlugin_SessionLimit_Blocks(t *testing.T) {
	st := state.NewMemory()
	defer st.Close()

	st.Increment(context.Background(), "cost:session:sess_1", 1_500_000) // $1.50

	bp := NewBudgetPlugin(st, 1.0, 0) // $1.00 session limit
	hook, err := bp.OnRequest(&plugin.RequestContext{
		Context:   context.Background(),
		SessionID: "sess_1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hook == nil {
		t.Fatal("expected block, got nil")
	}
	if !hook.Block {
		t.Fatal("expected Block=true")
	}
	if hook.StatusCode != 403 {
		t.Fatalf("expected status 403, got %d", hook.StatusCode)
	}
	if hook.Reason != "session budget exceeded (max $1.00)" {
		t.Fatalf("unexpected reason: %q", hook.Reason)
	}
}

func TestBudgetPlugin_SessionLimit_Under(t *testing.T) {
	st := state.NewMemory()
	defer st.Close()

	st.Increment(context.Background(), "cost:session:sess_1", 500_000) // $0.50

	bp := NewBudgetPlugin(st, 1.0, 0)
	hook, err := bp.OnRequest(&plugin.RequestContext{
		Context:   context.Background(),
		SessionID: "sess_1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hook != nil {
		t.Fatalf("expected no block, got Block=%v reason=%q", hook.Block, hook.Reason)
	}
}

func TestBudgetPlugin_DailyLimit_Blocks(t *testing.T) {
	st := state.NewMemory()
	defer st.Close()

	st.Increment(context.Background(), todayKey(), 3_000_000) // $3.00

	bp := NewBudgetPlugin(st, 0, 2.0) // $2.00 daily limit
	hook, err := bp.OnRequest(&plugin.RequestContext{
		Context:   context.Background(),
		SessionID: "sess_1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hook == nil {
		t.Fatal("expected block, got nil")
	}
	if !hook.Block {
		t.Fatal("expected Block=true")
	}
	if hook.StatusCode != 403 {
		t.Fatalf("expected status 403, got %d", hook.StatusCode)
	}
	if hook.Reason != "daily budget exceeded (max $2.00)" {
		t.Fatalf("unexpected reason: %q", hook.Reason)
	}
}

func TestBudgetPlugin_DailyLimit_Under(t *testing.T) {
	st := state.NewMemory()
	defer st.Close()

	st.Increment(context.Background(), todayKey(), 1_000_000) // $1.00

	bp := NewBudgetPlugin(st, 0, 2.0) // $2.00 daily limit
	hook, err := bp.OnRequest(&plugin.RequestContext{
		Context:   context.Background(),
		SessionID: "sess_1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hook != nil {
		t.Fatalf("expected no block, got Block=%v reason=%q", hook.Block, hook.Reason)
	}
}

func TestBudgetPlugin_ZeroLimits_NoBlock(t *testing.T) {
	st := state.NewMemory()
	defer st.Close()

	bp := NewBudgetPlugin(st, 0, 0) // unlimited
	hook, err := bp.OnRequest(&plugin.RequestContext{
		Context:   context.Background(),
		SessionID: "sess_any",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hook != nil {
		t.Fatalf("expected no block with zero limits, got %v", hook)
	}
}

func TestBudgetPlugin_OnResponse_NoOp(t *testing.T) {
	bp := NewBudgetPlugin(state.NewMemory(), 1.0, 1.0)
	err := bp.OnResponse(&plugin.ResponseContext{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

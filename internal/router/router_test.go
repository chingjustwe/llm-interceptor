package router

import (
	"net/http"
	"testing"
)

func TestDetectMode_RouterKey(t *testing.T) {
	r := New(nil, "https://api.anthropic.com")
	if mode := r.DetectMode("sk-lli-abc123"); mode != "router" {
		t.Fatalf("expected router mode, got %s", mode)
	}
}

func TestDetectMode_Passthrough(t *testing.T) {
	r := New(nil, "https://api.anthropic.com")
	if mode := r.DetectMode("sk-ant-abc123"); mode != "passthrough" {
		t.Fatalf("expected passthrough mode, got %s", mode)
	}
	if mode := r.DetectMode(""); mode != "passthrough" {
		t.Fatalf("expected passthrough for empty key, got %s", mode)
	}
}

func TestDetectMode_ShortKey(t *testing.T) {
	r := New(nil, "https://api.anthropic.com")
	// A key shorter than the prefix should not panic and should return passthrough.
	if mode := r.DetectMode("sk-lli"); mode != "passthrough" {
		t.Fatalf("expected passthrough for short key, got %s", mode)
	}
}

func TestDetectMode_OpenAIKey(t *testing.T) {
	r := New(nil, "https://api.anthropic.com")
	if mode := r.DetectMode("sk-proj-abc123xyz"); mode != "passthrough" {
		t.Fatalf("expected passthrough for OpenAI key, got %s", mode)
	}
}

// mockProvider implements both Provider and ModelMatcher for testing.
type mockProvider struct {
	name      string
	modelGlob string
}

func (m *mockProvider) Name() string { return m.name }

func (m *mockProvider) RoundTrip(_ *http.Request) (*http.Response, error) {
	return nil, nil
}

func (m *mockProvider) MatchModel(model string) bool {
	if m.modelGlob == "" || m.modelGlob == "*" {
		return true
	}
	// Simple prefix matching for tests: strip trailing '*' and compare prefix.
	prefix := m.modelGlob
	if len(prefix) > 0 && prefix[len(prefix)-1] == '*' {
		prefix = prefix[:len(prefix)-1]
	}
	return len(model) >= len(prefix) && model[:len(prefix)] == prefix
}

func TestSelectProvider_MatchesModel(t *testing.T) {
	openai := &mockProvider{name: "openai", modelGlob: "gpt-*"}
	anthropic := &mockProvider{name: "anthropic", modelGlob: "claude-*"}
	r := New([]Provider{openai, anthropic}, "https://api.anthropic.com")

	p := r.SelectProvider("gpt-4")
	if p == nil || p.Name() != "openai" {
		t.Fatalf("expected openai provider, got %v", p)
	}

	p = r.SelectProvider("claude-sonnet-4-6")
	if p == nil || p.Name() != "anthropic" {
		t.Fatalf("expected anthropic provider, got %v", p)
	}
}

func TestSelectProvider_NoMatch(t *testing.T) {
	openai := &mockProvider{name: "openai", modelGlob: "gpt-*"}
	r := New([]Provider{openai}, "https://api.anthropic.com")

	p := r.SelectProvider("llama-3")
	if p != nil {
		t.Fatalf("expected nil provider for unmatched model, got %v", p.Name())
	}
}

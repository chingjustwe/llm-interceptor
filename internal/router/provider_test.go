package router

import (
	"testing"
)

func TestHTTPProvider_MatchModel(t *testing.T) {
	tests := []struct {
		glob  string
		model string
		want  bool
	}{
		{"gpt-*", "gpt-4", true},
		{"gpt-*", "gpt-4o-mini", true},
		{"gpt-*", "claude-3", false},
		{"claude-*", "claude-sonnet-4-6", true},
		{"claude-*", "gpt-4", false},
		{"*", "any-model", true},
		{"", "any-model", true},
	}
	for _, tt := range tests {
		p := NewHTTPProvider("test", "https://api.example.com", tt.glob, "key")
		if got := p.MatchModel(tt.model); got != tt.want {
			t.Errorf("MatchModel(%q) with glob %q = %v, want %v", tt.model, tt.glob, got, tt.want)
		}
	}
}

func TestHTTPProvider_Name(t *testing.T) {
	p := NewHTTPProvider("openai", "https://api.openai.com", "gpt-*", "sk-xxx")
	if p.Name() != "openai" {
		t.Fatalf("expected name openai, got %s", p.Name())
	}
}

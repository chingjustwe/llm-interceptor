package plugins

import (
	"encoding/json"
	"testing"

	"github.com/chingjustwe/llm-interceptor/internal/plugin"
)

func TestToolPolicyPlugin_OnRequest_BlocksToolResult(t *testing.T) {
	p := NewToolPolicyPlugin([]string{"Bash"}, nil)

	body := mkBody(t, map[string]any{
		"model": "claude-sonnet-4-6",
		"messages": []map[string]any{
			{"role": "user", "content": "list files"},
			{"role": "assistant", "content": []map[string]any{
				{"type": "text", "text": "I'll use Bash"},
				{"type": "tool_use", "id": "toolu_001", "name": "Bash", "input": map[string]string{"command": "ls"}},
			}},
			{"role": "user", "content": []map[string]any{
				{"type": "tool_result", "tool_use_id": "toolu_001", "content": "file1.txt\nfile2.txt"},
			}},
		},
	})

	ctx := &plugin.RequestContext{Body: body}
	result, err := p.OnRequest(ctx)
	if err != nil {
		t.Fatalf("OnRequest failed: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil HookResult, got %v", result)
	}

	var req map[string]any
	if err := json.Unmarshal(ctx.Body, &req); err != nil {
		t.Fatalf("unmarshal modified body: %v", err)
	}
	messages := req["messages"].([]any)
	userMsg := messages[2].(map[string]any)
	content := userMsg["content"].([]any)
	toolResult := content[0].(map[string]any)

	if toolResult["type"] != "tool_result" {
		t.Fatalf("expected tool_result type, got %s", toolResult["type"])
	}
	expectedContent := "Tool 'Bash' is blocked by interceptor policy and cannot be used."
	if toolResult["content"] != expectedContent {
		t.Fatalf("expected content %q, got %q", expectedContent, toolResult["content"])
	}
}

func TestToolPolicyPlugin_OnRequest_NonBlockedToolPasses(t *testing.T) {
	p := NewToolPolicyPlugin([]string{"Bash"}, nil)

	body := mkBody(t, map[string]any{
		"model": "claude-sonnet-4-6",
		"messages": []map[string]any{
			{"role": "user", "content": "read file"},
			{"role": "assistant", "content": []map[string]any{
				{"type": "text", "text": "I'll read the file"},
				{"type": "tool_use", "id": "toolu_002", "name": "Read", "input": map[string]string{"path": "main.go"}},
			}},
			{"role": "user", "content": []map[string]any{
				{"type": "tool_result", "tool_use_id": "toolu_002", "content": "package main"},
			}},
		},
	})

	ctx := &plugin.RequestContext{Body: body}
	result, err := p.OnRequest(ctx)
	if err != nil {
		t.Fatalf("OnRequest failed: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil HookResult, got %v", result)
	}

	var req map[string]any
	if err := json.Unmarshal(ctx.Body, &req); err != nil {
		t.Fatalf("unmarshal modified body: %v", err)
	}
	messages := req["messages"].([]any)
	userMsg := messages[2].(map[string]any)
	content := userMsg["content"].([]any)
	toolResult := content[0].(map[string]any)

	if toolResult["content"] != "package main" {
		t.Fatalf("expected original content, got %q", toolResult["content"])
	}
}

func TestToolPolicyPlugin_OnRequest_NoToolUseInAssistant(t *testing.T) {
	// When the assistant hasn't used any tool, OnRequest should not modify
	// the body even if blocked tools are configured.
	p := NewToolPolicyPlugin([]string{"Bash"}, nil)

	body := mkBody(t, map[string]any{
		"model": "claude-sonnet-4-6",
		"messages": []map[string]any{
			{"role": "user", "content": "hello"},
			{"role": "assistant", "content": "Hi there!"},
		},
	})

	ctx := &plugin.RequestContext{Body: body}
	result, err := p.OnRequest(ctx)
	if err != nil {
		t.Fatalf("OnRequest failed: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil HookResult, got %v", result)
	}

	var req map[string]any
	if err := json.Unmarshal(ctx.Body, &req); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	messages := req["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
}

func mkBody(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// Package plugins provides built-in plugin implementations for the
// LLM Interceptor.

package plugins

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chingjustwe/llm-interceptor/internal/plugin"
)

// ToolPolicyPlugin checks tool_use blocks in LLM responses against a blocklist
// or allowlist. Blocked tools are intercepted at the proxy layer — their
// tool_use content blocks are replaced with a text message saying the tool
// is not available. The request body is never modified. It operates in one of
// two mutually exclusive modes:
//   - blocklist: blocks only the explicitly listed tools (all others pass)
//   - allowlist: blocks any tool not in the allowed set
type ToolPolicyPlugin struct {
	blockedTools map[string]bool
	allowedTools map[string]bool
	mode         string // "blocklist" or "allowlist"
}

// NewToolPolicyPlugin creates a ToolPolicyPlugin with the given blocked and
// allowed tool name lists. If allowed is non-empty, the plugin operates in
// allowlist mode; otherwise it operates in blocklist mode.
func NewToolPolicyPlugin(blocked, allowed []string) *ToolPolicyPlugin {
	p := &ToolPolicyPlugin{
		blockedTools: make(map[string]bool, len(blocked)),
		allowedTools: make(map[string]bool, len(allowed)),
		mode:         "blocklist",
	}
	for _, t := range blocked {
		p.blockedTools[strings.ToLower(t)] = true
	}
	if len(allowed) > 0 {
		p.mode = "allowlist"
		for _, t := range allowed {
			p.allowedTools[strings.ToLower(t)] = true
		}
	}
	return p
}

// Name returns "tool-policy" as the plugin identifier.
func (t *ToolPolicyPlugin) Name() string { return "tool-policy" }

// OnRequest intercepts outgoing requests to the LLM. When a request contains
// tool_result blocks whose corresponding tool_use names are blocked by policy,
// the tool_result content is replaced with a message explaining the tool is
// blocked. This lets the LLM receive feedback that the tool was blocked and
// adapt its strategy, rather than aborting the turn.
func (t *ToolPolicyPlugin) OnRequest(ctx *plugin.RequestContext) (*plugin.HookResult, error) {
	if len(ctx.Body) == 0 {
		return nil, nil
	}

	var req map[string]any
	if err := json.Unmarshal(ctx.Body, &req); err != nil {
		return nil, nil
	}
	messages, ok := req["messages"].([]any)
	if !ok {
		return nil, nil
	}

	// Build tool_use_id → tool_name mapping from assistant messages.
	idToName := make(map[string]string)
	for _, msg := range messages {
		m, ok := msg.(map[string]any)
		if !ok || m["role"] != "assistant" {
			continue
		}
		content, ok := m["content"].([]any)
		if !ok {
			continue
		}
		for _, block := range content {
			b, ok := block.(map[string]any)
			if !ok || b["type"] != "tool_use" {
				continue
			}
			id, _ := b["id"].(string)
			name, _ := b["name"].(string)
			if id != "" && name != "" {
				idToName[id] = name
			}
		}
	}

	if len(idToName) == 0 {
		return nil, nil
	}

	// Check tool_results against the blocked policy.
	modified := false
	for _, msg := range messages {
		m, ok := msg.(map[string]any)
		if !ok || m["role"] != "user" {
			continue
		}
		content, ok := m["content"].([]any)
		if !ok {
			continue
		}
		for i, block := range content {
			b, ok := block.(map[string]any)
			if !ok || b["type"] != "tool_result" {
				continue
			}
			toolUseID, _ := b["tool_use_id"].(string)
			toolName, ok := idToName[toolUseID]
			if !ok || !t.IsBlocked(toolName) {
				continue
			}
			content[i] = map[string]any{
				"type":        "tool_result",
				"tool_use_id": toolUseID,
				"content":     fmt.Sprintf("Tool '%s' is blocked by interceptor policy and cannot be used.", toolName),
			}
			modified = true
		}
	}

	if !modified {
		return nil, nil
	}

	modifiedBody, err := json.Marshal(req)
	if err != nil {
		return nil, nil
	}
	ctx.Body = modifiedBody
	return nil, nil
}

// OnResponse is a no-op — tool policy enforcement happens in OnRequest
// by intercepting tool_result blocks before they reach the LLM.
func (t *ToolPolicyPlugin) OnResponse(ctx *plugin.ResponseContext) error {
	return nil
}

// IsBlocked returns true if the given tool name (case-insensitive) is blocked
// by policy. Used by the proxy to intercept tool_use content blocks in SSE
// streams and non-streaming responses.
func (t *ToolPolicyPlugin) IsBlocked(name string) bool {
	lower := strings.ToLower(name)
	if t.blockedTools[lower] {
		return true
	}
	if t.mode == "allowlist" && !t.allowedTools[lower] {
		return true
	}
	return false
}

// Package plugins provides built-in plugin implementations for the
// LLM Interceptor.

package plugins

import (
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

// OnRequest is a no-op — tool policy enforcement happens in the proxy layer
// during response streaming. The request body is forwarded unchanged so the
// LLM has full context.
func (t *ToolPolicyPlugin) OnRequest(ctx *plugin.RequestContext) (*plugin.HookResult, error) {
	return nil, nil
}

// OnResponse is a no-op — tool policy enforcement happens in the proxy layer
// during response streaming.
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

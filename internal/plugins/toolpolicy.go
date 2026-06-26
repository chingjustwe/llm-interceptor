// Package plugins provides built-in plugin implementations for the
// LLM Interceptor.

package plugins

import (
	"encoding/json"
	"strings"

	"github.com/chingjustwe/llm-interceptor/internal/plugin"
)

// ToolPolicyPlugin inspects incoming request bodies and removes tool
// definitions that violate policy — either tools on a blocklist or tools
// not on an allowlist. Instead of blocking the request, it silently strips
// the restricted tools from the tools array so the LLM never sees them as
// available options. It operates in one of two mutually exclusive modes:
//   - blocklist: removes only the explicitly listed tools
//   - allowlist: removes any tool not in the allowed set
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

// OnRequest strips blocked or disallowed tools from the request body's tools
// array. The modified body is set back on ctx.Body so the proxy forwards the
// sanitised version to the upstream LLM. Returns nil to let the request
// continue — this plugin never blocks.
func (t *ToolPolicyPlugin) OnRequest(ctx *plugin.RequestContext) (*plugin.HookResult, error) {
	// Parse the body as a generic map to preserve all fields.
	var body map[string]any
	if err := json.Unmarshal(ctx.Body, &body); err != nil {
		return nil, nil
	}

	rawTools, ok := body["tools"].([]any)
	if !ok || len(rawTools) == 0 {
		return nil, nil
	}

	filtered := make([]any, 0, len(rawTools))
	for _, raw := range rawTools {
		tool, ok := raw.(map[string]any)
		if !ok {
			filtered = append(filtered, raw)
			continue
		}
		nameAny, ok := tool["name"]
		if !ok {
			filtered = append(filtered, raw)
			continue
		}
		name, ok := nameAny.(string)
		if !ok {
			filtered = append(filtered, raw)
			continue
		}

		lower := strings.ToLower(name)
		if t.blockedTools[lower] {
			continue // strip this tool
		}
		if t.mode == "allowlist" && !t.allowedTools[lower] {
			continue // strip this tool
		}
		filtered = append(filtered, raw)
	}

	// Only re-marshal if we actually removed something.
	if len(filtered) == len(rawTools) {
		return nil, nil
	}
	body["tools"] = filtered
	modified, err := json.Marshal(body)
	if err != nil {
		return nil, nil
	}
	ctx.Body = modified
	return nil, nil
}

// OnResponse is a no-op for the tool policy plugin; all enforcement happens
// on request.
func (t *ToolPolicyPlugin) OnResponse(ctx *plugin.ResponseContext) error {
	return nil
}

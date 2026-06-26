// Package plugins provides built-in plugin implementations for the
// LLM Interceptor.

package plugins

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chingjustwe/llm-interceptor/internal/plugin"
)

// ToolPolicyPlugin inspects incoming request bodies for tool definitions and
// enforces governance by blocking tools on a blocklist or requiring tools to
// be on an allowlist. It operates in one of two mutually exclusive modes:
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

// OnRequest inspects the request body for tool_use blocks in the conversation
// messages and checks each tool name against the blocklist or allowlist.
// Only actual tool invocations (not tool definitions) are checked, so simple
// messages that don't call any tools always pass. Returns a blocking
// HookResult with HTTP 403 if a tool violates policy.
func (t *ToolPolicyPlugin) OnRequest(ctx *plugin.RequestContext) (*plugin.HookResult, error) {
	var body struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(ctx.Body, &body); err != nil {
		return nil, nil
	}
	for _, msg := range body.Messages {
		result := t.checkContent(msg.Content)
		if result != nil {
			return result, nil
		}
	}
	return nil, nil
}

// checkContent parses a message content field and checks any tool_use blocks
// against the policy. Content may be a plain string or an array of content
// blocks. Returns a blocking HookResult if a tool violates policy.
func (t *ToolPolicyPlugin) checkContent(content json.RawMessage) *plugin.HookResult {
	if len(content) == 0 {
		return nil
	}
	// Try as an array of content blocks first (Anthropic format).
	var blocks []struct {
		Type string `json:"type"`
		Name string `json:"name,omitempty"`
	}
	if err := json.Unmarshal(content, &blocks); err == nil {
		for _, block := range blocks {
			if block.Type != "tool_use" {
				continue
			}
			name := strings.ToLower(block.Name)
			if t.blockedTools[name] {
				return &plugin.HookResult{
					Block:      true,
					Reason:     fmt.Sprintf("tool '%s' is blocked by policy", block.Name),
					StatusCode: 403,
				}
			}
			if t.mode == "allowlist" && !t.allowedTools[name] {
				return &plugin.HookResult{
					Block:      true,
					Reason:     fmt.Sprintf("tool '%s' is not in the allowed list", block.Name),
					StatusCode: 403,
				}
			}
		}
	}
	return nil
}

// OnResponse is a no-op for the tool policy plugin; all enforcement happens
// on request.
func (t *ToolPolicyPlugin) OnResponse(ctx *plugin.ResponseContext) error {
	return nil
}

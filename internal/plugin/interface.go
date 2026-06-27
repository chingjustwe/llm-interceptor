// Package plugin defines the plugin interface for intercepting and extending
// LLM request/response lifecycle. Plugins implement OnRequest for pre-processing
// (e.g., auth, rate-limiting) and OnResponse for post-processing (e.g., logging,
// cost tracking, metrics export).
package plugin

import "context"

// RequestContext holds all request-scoped information available to plugins
// during the pre-processing phase. Plugins can read or modify the body,
// headers, and metadata, or block the request entirely via HookResult.
type RequestContext struct {
	Context   context.Context
	ID        string
	Method    string
	Path      string
	Headers   map[string]string
	Body      []byte
	SessionID string
	AgentID   string
	Metadata  map[string]any
}

// HookResult allows a plugin to signal that a request should be blocked.
// When Block is true, the request is rejected with the given StatusCode and Reason.
// If RetryAfterSec > 0, a Retry-After header is set on the response.
type HookResult struct {
	Block         bool
	Reason        string
	StatusCode    int
	RetryAfterSec int
}

// Usage holds token consumption metrics extracted from an LLM response.
type Usage struct {
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
}

// ToolCall represents a tool invocation parsed from an LLM response.
type ToolCall struct {
	ID    string
	Name  string
	Input map[string]any
}

// ResponseContext holds all information available to plugins during the
// post-processing phase, including parsed usage, tool calls, and the
// original response body for further inspection.
type ResponseContext struct {
	Context    context.Context
	RequestID  string
	SessionID  string
	Model      string
	Usage      Usage
	StopReason string
	ToolCalls  []ToolCall
	DurationMs int64
	StatusCode int
	Body       []byte
	Metadata   map[string]any
}

// Plugin is the interface that all plugins must implement.
//   - OnRequest is called before forwarding the request to the upstream provider.
//     Return a non-nil HookResult with Block=true to reject the request.
//   - OnResponse is called after receiving the upstream response (or stream end).
//   - Plugins are executed in registration order for OnRequest and reverse order
//     for OnResponse, forming a LIFO-style middleware chain.
type Plugin interface {
	Name() string
	OnRequest(ctx *RequestContext) (*HookResult, error)
	OnResponse(ctx *ResponseContext) error
}

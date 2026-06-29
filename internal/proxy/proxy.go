// Package proxy implements an HTTP passthrough proxy that forwards LLM API requests
// to upstream providers. It supports both synchronous and streaming (SSE) modes,
// and provides utilities for extracting usage data and tool calls from responses.
package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// PluginResponse wraps the upstream provider's response, including status code,
// body, headers, measured duration, and parsed usage data.
type PluginResponse struct {
	StatusCode int
	Body       []byte
	Headers    map[string]string
	DurationMs int64
	Usage      UsageData
}

// ToolCall represents a tool invocation parsed from an LLM response.
type ToolCall struct {
	ID    string
	Name  string
	Input map[string]any
}

// ContentBlock represents a single item in the LLM response's content array.
// It is either a text block or a tool_use block.
type ContentBlock struct {
	Type      string         // "text" or "tool_use"
	Text      string         // for text blocks
	ToolUseID string         // for tool_use blocks
	Name      string         // for tool_use blocks
	Input     map[string]any // for tool_use blocks
}

// UsageData holds token usage counts parsed from an LLM response.
type UsageData struct {
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
}

// Proxy is an HTTP client wrapper that forwards requests to a configured upstream LLM provider.
// It handles both synchronous and streaming request/response flows.
type Proxy struct {
	name     string
	upstream string
	client   *http.Client
}

// New creates a new Proxy targeting the given upstream URL. It validates the URL
// and sets a default 120-second timeout on the underlying HTTP client.
func New(name, upstreamURL string) (*Proxy, error) {
	if _, err := url.Parse(upstreamURL); err != nil {
		return nil, fmt.Errorf("invalid upstream URL: %w", err)
	}
	return &Proxy{
		name:     name,
		upstream: upstreamURL,
		client:   &http.Client{Timeout: 120 * time.Second},
	}, nil
}

// HandleRequest sends a synchronous request to the upstream provider and returns
// the full response body, status code, headers, and measured round-trip duration.
// The path parameter specifies the upstream path; if empty, "/v1/messages" is used.
func (p *Proxy) HandleRequest(body []byte, headers map[string]string, path string) (*PluginResponse, error) {
	start := time.Now()

	if path == "" {
		path = "/v1/messages"
	}
	if path[0] != '/' {
		path = "/" + path
	}
	req, err := http.NewRequest("POST", p.upstream+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	pr := &PluginResponse{
		StatusCode: resp.StatusCode,
		Body:       respBody,
		Headers:    make(map[string]string, len(resp.Header)),
		DurationMs: time.Since(start).Milliseconds(),
	}
	for k, v := range resp.Header {
		pr.Headers[k] = v[0]
	}
	return pr, nil
}

// ExtractUsage parses an upstream JSON response body to extract token usage counts,
// tool calls, and the stop reason. Returns zero values if parsing fails.
// Supports both Anthropic format (stop_reason, content[], usage.input_tokens)
// and OpenAI format (choices[].finish_reason, choices[].message.tool_calls, usage.prompt_tokens).
func ExtractUsage(body []byte) (UsageData, []ToolCall, string) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return UsageData{}, nil, ""
	}
	var usage UsageData
	if u, ok := raw["usage"].(map[string]any); ok {
		if v, ok := u["input_tokens"].(float64); ok {
			usage.InputTokens = int(v)
		}
		if v, ok := u["output_tokens"].(float64); ok {
			usage.OutputTokens = int(v)
		}
		if v, ok := u["cache_read_input_tokens"].(float64); ok {
			usage.CacheReadTokens = int(v)
		}
		if v, ok := u["cache_creation_input_tokens"].(float64); ok {
			usage.CacheCreationTokens = int(v)
		}
		// Fallback to OpenAI token names if no Anthropic tokens found.
		if usage.InputTokens == 0 {
			if v, ok := u["prompt_tokens"].(float64); ok {
				usage.InputTokens = int(v)
			}
		}
		if usage.OutputTokens == 0 {
			if v, ok := u["completion_tokens"].(float64); ok {
				usage.OutputTokens = int(v)
			}
		}
		if usage.CacheReadTokens == 0 {
			if details, ok := u["prompt_tokens_details"].(map[string]any); ok {
				if v, ok := details["cached_tokens"].(float64); ok {
					usage.CacheReadTokens = int(v)
				}
			}
		}
	}
	var stopReason string
	if sr, ok := raw["stop_reason"].(string); ok {
		stopReason = sr
	}
	// Fallback to OpenAI format: choices[0].finish_reason
	if stopReason == "" {
		if choices, ok := raw["choices"].([]any); ok && len(choices) > 0 {
			if first, ok := choices[0].(map[string]any); ok {
				if fr, ok := first["finish_reason"].(string); ok {
					stopReason = fr
				}
			}
		}
	}
	var toolCalls []ToolCall
	// Try Anthropic format: content[] blocks with type "tool_use"
	if content, ok := raw["content"].([]any); ok {
		for _, c := range content {
			if block, ok := c.(map[string]any); ok && block["type"] == "tool_use" {
				var tc ToolCall
				if id, ok := block["id"].(string); ok {
					tc.ID = id
				}
				if name, ok := block["name"].(string); ok {
					tc.Name = name
				}
				if input, ok := block["input"].(map[string]any); ok {
					tc.Input = input
				}
				toolCalls = append(toolCalls, tc)
			}
		}
	}
	// Fallback to OpenAI format: choices[0].message.tool_calls
	if len(toolCalls) == 0 {
		if choices, ok := raw["choices"].([]any); ok && len(choices) > 0 {
			if first, ok := choices[0].(map[string]any); ok {
				if msg, ok := first["message"].(map[string]any); ok {
					if tcs, ok := msg["tool_calls"].([]any); ok {
						for _, tcAny := range tcs {
							if tcMap, ok := tcAny.(map[string]any); ok {
								var tc ToolCall
								if id, ok := tcMap["id"].(string); ok {
									tc.ID = id
								}
								if fn, ok := tcMap["function"].(map[string]any); ok {
									if name, ok := fn["name"].(string); ok {
										tc.Name = name
									}
									if args, ok := fn["arguments"].(string); ok {
										var input map[string]any
										if json.Unmarshal([]byte(args), &input) == nil {
											tc.Input = input
										}
									}
								}
								toolCalls = append(toolCalls, tc)
							}
						}
					}
				}
			}
		}
	}
	return usage, toolCalls, stopReason
}

// Forward is a generic passthrough handler that proxies any HTTP request to the
// upstream provider while preserving the original method, path, headers, and body.
func (p *Proxy) Forward(w http.ResponseWriter, r *http.Request) {
	target := p.upstream + r.URL.Path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}

	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
		r.Body.Close()
	}

	req, err := http.NewRequest(r.Method, target, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	for k, v := range r.Header {
		req.Header[k] = v
	}

	resp, err := p.client.Do(req)
	if err != nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// HandleRequestStream sends a streaming (SSE) request to the upstream provider.
// It relays Server-Sent Events directly to the client, and collects aggregated
// usage data, tool calls, stop reason, and response body.
//
// If isToolBlocked is non-nil, the response is inspected for tool_use blocks
// whose name returns true. When a blocked tool is found, the method
// transparently synthesises a follow-up request: it sends a tool_result back
// to the LLM saying the tool was blocked, and forwards the LLM's subsequent
// response (after the LLM adapts) to the client instead of the original.
// A follow-up budget of 3 prevents infinite recursion if the LLM keeps
// returning a blocked tool in its adaptive responses.
// The path parameter specifies the upstream path; if empty, "/v1/messages" is used.
// The extra int64 return is ttftMs (time-to-first-token in milliseconds).
func (p *Proxy) HandleRequestStream(body []byte, headers map[string]string, w http.ResponseWriter, path string, isToolBlocked func(name string) bool) ([]byte, *UsageData, []ToolCall, string, int64, int64, error) {
	return p.handleRequestStream(body, headers, w, path, isToolBlocked, 3)
}

// handleRequestStream is the internal implementation with a follow-up budget
// to prevent infinite recursion when the LLM repeatedly returns blocked tools.
func (p *Proxy) handleRequestStream(body []byte, headers map[string]string, w http.ResponseWriter, path string, isToolBlocked func(name string) bool, followUpBudget int) ([]byte, *UsageData, []ToolCall, string, int64, int64, error) {
	start := time.Now()

	if path == "" {
		path = "/v1/messages"
	}
	if path[0] != '/' {
		path = "/" + path
	}
	req, err := http.NewRequest("POST", p.upstream+path, bytes.NewReader(body))
	if err != nil {
		return nil, nil, nil, "", 0, 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "text/event-stream")
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, nil, nil, "", 0, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		errBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, nil, nil, "", 0, 0, fmt.Errorf("read error body: %w", err)
		}
		w.Write(errBody)
		return errBody, nil, nil, "", 0, time.Since(start).Milliseconds(), nil
	}

	// Collect the SSE stream into a buffer (don't forward to client yet).
	sseText, contentBlocks, respBody, usage, tools, stopReason, ttftMs, duration, err := collectSSE(resp)
	_ = duration // total time for the upstream round-trip; follow-up resets it
	if err != nil {
		return nil, nil, nil, "", 0, 0, err
	}

	// If any tool_use is blocked and we still have budget, synthesise a
	// follow-up request. We pass the same isToolBlocked so the follow-up
	// response is also checked (the LLM may try the same tool again).
	if isToolBlocked != nil && followUpBudget > 0 {
		for _, tc := range tools {
			if isToolBlocked(tc.Name) {
				slog.Info("tool-policy: blocked tool_use in response", "tool", tc.Name)
				newBody := buildFollowUpRequest(body, contentBlocks, tools, isToolBlocked)
				if newBody != nil {
					return p.handleRequestStream(newBody, headers, w, path, isToolBlocked, followUpBudget-1)
				}
				break
			}
		}
	}

	// No blocking needed — forward the buffered SSE to the client.
	if isToolBlocked != nil && followUpBudget == 0 {
		for _, tc := range tools {
			if isToolBlocked(tc.Name) {
				slog.Warn("tool-policy: follow-up budget exhausted, allowing blocked tool_use", "tool", tc.Name)
			}
		}
	}
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	w.Write([]byte(sseText))

	return respBody, &usage, tools, stopReason, ttftMs, time.Since(start).Milliseconds(), nil
}

// buildFollowUpRequest builds a new LLM request body that appends the assistant's
// original response (text + tool_use) and a tool_result for each blocked tool
// to the message history. The LLM receives this as feedback that the tool was
// blocked and can adapt its strategy.
func buildFollowUpRequest(origBody []byte, contentBlocks []ContentBlock, tools []ToolCall, isToolBlocked func(name string) bool) []byte {
	var req map[string]any
	if err := json.Unmarshal(origBody, &req); err != nil {
		return nil
	}
	messages, ok := req["messages"].([]any)
	if !ok {
		return nil
	}

	// Build the assistant content array from SSE content blocks.
	var assistantContent []map[string]any
	for _, block := range contentBlocks {
		switch block.Type {
		case "text":
			assistantContent = append(assistantContent, map[string]any{
				"type": "text",
				"text": block.Text,
			})
		case "tool_use":
			assistantContent = append(assistantContent, map[string]any{
				"type":  "tool_use",
				"id":    block.ToolUseID,
				"name":  block.Name,
				"input": block.Input,
			})
		}
	}

	// Build tool_result blocks for blocked tools.
	var toolResultBlocks []map[string]any
	for _, tc := range tools {
		if isToolBlocked(tc.Name) {
			toolResultBlocks = append(toolResultBlocks, map[string]any{
				"type":        "tool_result",
				"tool_use_id": tc.ID,
				"content":     fmt.Sprintf("Tool '%s' is blocked by interceptor policy and cannot be used.", tc.Name),
			})
		}
	}

	// Append assistant message and tool_result user message.
	messages = append(messages, map[string]any{
		"role":    "assistant",
		"content": assistantContent,
	})
	messages = append(messages, map[string]any{
		"role":    "user",
		"content": toolResultBlocks,
	})

	req["messages"] = messages
	modified, err := json.Marshal(req)
	if err != nil {
		return nil
	}
	return modified
}

// ExtractRequestParams extracts request configuration parameters from the
// request body, excluding messages, stream, and model. Returns a flat
// JSON-serializable map suitable for the request_params column.
func ExtractRequestParams(body []byte) map[string]any {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil
	}
	delete(raw, "messages")
	delete(raw, "stream")
	delete(raw, "model")
	return raw
}

// ExtractSystemPrompt extracts the system prompt from an LLM request body.
// It first checks the Anthropic top-level "system" field, then falls back to
// OpenAI-style messages with role "system" or "developer".
func ExtractSystemPrompt(body []byte) *string {
	var raw struct {
		System   *string            `json:"system,omitempty"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil
	}
	if raw.System != nil {
		return raw.System
	}
	for _, m := range raw.Messages {
		var msg struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if json.Unmarshal(m, &msg) == nil && (msg.Role == "system" || msg.Role == "developer") {
			return &msg.Content
		}
	}
	return nil
}

// ExtractError parses an upstream error response body to extract the error
// type and message. Supports both OpenAI format ({"error":{"type":"...","message":"..."}})
// and Anthropic format ({"type":"error","error":{"type":"...","message":"..."}}).
func ExtractError(body []byte) (errorType, errorMessage string) {
	var raw struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", ""
	}
	if raw.Error.Type != "" {
		return raw.Error.Type, raw.Error.Message
	}
	// Try nested Anthropic format: {"type":"error","error":{"type":"...","message":"..."}}
	var anthropicRaw struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &anthropicRaw); err != nil {
		return "", ""
	}
	return anthropicRaw.Error.Type, anthropicRaw.Error.Message
}

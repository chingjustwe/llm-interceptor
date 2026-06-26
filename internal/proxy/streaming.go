// Package proxy implements an HTTP passthrough proxy that forwards LLM API requests
// to upstream providers. It supports both synchronous and streaming (SSE) modes,
// and provides utilities for extracting usage data and tool calls from responses.
package proxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// writeSSE writes a single SSE event line to the response writer and flushes.
func writeSSE(w http.ResponseWriter, flusher http.Flusher, line string) {
	_, _ = fmt.Fprintf(w, "%s\n", line)
	flusher.Flush()
}

// streamAndCollect reads Server-Sent Events from the upstream response, relays each
// event line to the caller via the ResponseWriter, and aggregates usage data, tool
// calls, stop reason, and response body from the event stream for post-processing
// by plugins.
//
// If isToolBlocked is non-nil, it is called for each tool_use content block. When
// a tool is blocked, the tool_use events are replaced with a text content block
// saying the tool was blocked by policy, and the stop_reason is overridden to
// "end_turn".
func streamAndCollect(upstreamResp *http.Response, w http.ResponseWriter, isToolBlocked func(name string) bool) ([]byte, UsageData, []ToolCall, string, int64, error) {
	start := time.Now()
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, UsageData{}, nil, "", 0, fmt.Errorf("response writer does not support flushing")
	}

	var finalUsage UsageData
	var finalToolCalls []ToolCall
	var stopReason string
	var respBody strings.Builder

	// Track the content block index of a blocked tool_use we're currently
	// suppressing (-1 = none).
	blockedIdx := -1

	scanner := bufio.NewScanner(upstreamResp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			writeSSE(w, flusher, line)
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		var raw map[string]any
		if err := json.Unmarshal([]byte(data), &raw); err != nil {
			writeSSE(w, flusher, line)
			continue
		}
		evtType, _ := raw["type"].(string)

		switch evtType {
		case "content_block_start":
			block, ok := raw["content_block"].(map[string]any)
			if !ok {
				writeSSE(w, flusher, line)
				continue
			}
			if block["type"] == "tool_use" {
				var tc ToolCall
				if name, ok := block["name"].(string); ok {
					tc.Name = name
				}
				if input, ok := block["input"].(map[string]any); ok {
					tc.Input = input
				}
				finalToolCalls = append(finalToolCalls, tc)

				// Check if this tool is blocked by policy.
				if isToolBlocked != nil && isToolBlocked(tc.Name) {
					// Suppress this tool_use block: don't forward its events.
					// We'll inject a text replacement after block_stop.
					if idx, ok := raw["index"].(float64); ok {
						blockedIdx = int(idx)
					}
					continue
				}
			}
			writeSSE(w, flusher, line)

		case "content_block_delta":
			if blockedIdx >= 0 {
				if idx, ok := raw["index"].(float64); ok && int(idx) == blockedIdx {
					continue // suppress events for the blocked block
				}
			}
			if delta, ok := raw["delta"].(map[string]any); ok {
				if delta["type"] == "text_delta" {
					if text, ok := delta["text"].(string); ok {
						respBody.WriteString(text)
					}
				}
			}
			writeSSE(w, flusher, line)

		case "content_block_stop":
			if blockedIdx >= 0 {
				if idx, ok := raw["index"].(float64); ok && int(idx) == blockedIdx {
					// The blocked tool_use stream just ended. Replace with
					// a text block explaining the policy.
					writeSSE(w, flusher, fmt.Sprintf(`event: content_block_start`))
					writeSSE(w, flusher, fmt.Sprintf(`data: {"type":"content_block_start","index":%d,"content_block":{"type":"text","text":"Tool call blocked by interceptor policy — the tool you attempted to use is not available in this session."}}`, blockedIdx))
					writeSSE(w, flusher, fmt.Sprintf(`event: content_block_stop`))
					writeSSE(w, flusher, fmt.Sprintf(`data: {"type":"content_block_stop","index":%d}`, blockedIdx))
					blockedIdx = -1
					continue
				}
			}
			writeSSE(w, flusher, line)

		case "message_delta":
			if blockedIdx >= 0 {
				// We blocked a tool_use, but the stop_reason from upstream
				// is still "tool_use". Override it to "end_turn". We replace
				// the delta's stop_reason in the forwarded event.
				if d, ok := raw["delta"].(map[string]any); ok {
					d["stop_reason"] = "end_turn"
				}
				if u, ok := raw["usage"].(map[string]any); ok {
					if v, ok := u["input_tokens"].(float64); ok {
						finalUsage.InputTokens = int(v)
					}
					if v, ok := u["output_tokens"].(float64); ok {
						finalUsage.OutputTokens = int(v)
					}
					if v, ok := u["cache_read_input_tokens"].(float64); ok {
						finalUsage.CacheReadTokens = int(v)
					}
					if v, ok := u["cache_creation_input_tokens"].(float64); ok {
						finalUsage.CacheCreationTokens = int(v)
					}
				}
				// Re-marshal and write the modified message_delta.
				modified, err := json.Marshal(raw)
				if err == nil {
					writeSSE(w, flusher, "data: "+string(modified))
				}
				stopReason = "end_turn"
				continue
			}
			// Normal message_delta: extract data and forward.
			if delta, ok := raw["delta"].(map[string]any); ok {
				if sr, ok := delta["stop_reason"].(string); ok {
					stopReason = sr
				}
			}
			if u, ok := raw["usage"].(map[string]any); ok {
				if v, ok := u["input_tokens"].(float64); ok {
					finalUsage.InputTokens = int(v)
				}
				if v, ok := u["output_tokens"].(float64); ok {
					finalUsage.OutputTokens = int(v)
				}
				if v, ok := u["cache_read_input_tokens"].(float64); ok {
					finalUsage.CacheReadTokens = int(v)
				}
				if v, ok := u["cache_creation_input_tokens"].(float64); ok {
					finalUsage.CacheCreationTokens = int(v)
				}
			}
			writeSSE(w, flusher, line)
		default:
			writeSSE(w, flusher, line)
		}
	}
	duration := time.Since(start).Milliseconds()
	return []byte(respBody.String()), finalUsage, finalToolCalls, stopReason, duration, scanner.Err()
}

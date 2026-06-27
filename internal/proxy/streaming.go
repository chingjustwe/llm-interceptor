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
	anyBlocked := false
	// pendingEvent buffers an "event: xxx" line until its paired "data: "
	// line is processed. This ensures we never emit an event: line without
	// its corresponding data: line, which would confuse SSE clients.
	pendingEvent := ""

	// writePendingAndData writes any buffered event line followed by the
	// given data line, then clears the buffer.
	writePendingAndData := func(dataLine string) {
		if pendingEvent != "" {
			writeSSE(w, flusher, pendingEvent)
			pendingEvent = ""
		}
		line := dataLine
		if !strings.HasPrefix(line, "data: ") {
			line = "data: " + line
		}
		writeSSE(w, flusher, line)
	}

	// flushPending writes any pending event line as an orphan (no paired
	// data). This should only happen for non-data lines like blank
	// separators between events.
	flushPending := func() {
		if pendingEvent != "" {
			writeSSE(w, flusher, pendingEvent)
			pendingEvent = ""
		}
	}

	// discardPending drops the buffered event line without writing it.
	discardPending := func() {
		pendingEvent = ""
	}

	scanner := bufio.NewScanner(upstreamResp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "event: ") {
			// Buffer this event line. If we already have one buffered,
			// flush it first (it was orphaned, e.g. event without data).
			flushPending()
			pendingEvent = line
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			// Non-data, non-event line (e.g. blank separator).
			flushPending()
			writeSSE(w, flusher, line)
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		var raw map[string]any
		if err := json.Unmarshal([]byte(data), &raw); err != nil {
			writePendingAndData(line)
			continue
		}
		evtType, _ := raw["type"].(string)

		switch evtType {
		case "content_block_start":
			block, ok := raw["content_block"].(map[string]any)
			if !ok {
				writePendingAndData(line)
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
					if idx, ok := raw["index"].(float64); ok {
						blockedIdx = int(idx)
					}
					anyBlocked = true
					discardPending()
					continue
				}
			}
			writePendingAndData(line)

		case "content_block_delta":
			if blockedIdx >= 0 {
				if idx, ok := raw["index"].(float64); ok && int(idx) == blockedIdx {
					discardPending()
					continue
				}
			}
			if delta, ok := raw["delta"].(map[string]any); ok {
				if delta["type"] == "text_delta" {
					if text, ok := delta["text"].(string); ok {
						respBody.WriteString(text)
					}
				}
			}
			writePendingAndData(line)

		case "content_block_stop":
			if blockedIdx >= 0 {
				if idx, ok := raw["index"].(float64); ok && int(idx) == blockedIdx {
					// The blocked tool_use stream just ended. Replace with
					// a text block explaining the policy.
					blockedMsg := "Tool call blocked by interceptor policy — the tool you attempted to use is not available in this session."
					discardPending()
					writeSSE(w, flusher, "event: content_block_start")
					writeSSE(w, flusher, fmt.Sprintf(`data: {"type":"content_block_start","index":%d,"content_block":{"type":"text","text":%q}}`, blockedIdx, blockedMsg))
					writeSSE(w, flusher, "") // blank line = SSE event separator
					writeSSE(w, flusher, "event: content_block_stop")
					writeSSE(w, flusher, fmt.Sprintf(`data: {"type":"content_block_stop","index":%d}`, blockedIdx))
					respBody.WriteString(blockedMsg)
					blockedIdx = -1
					continue
				}
			}
			writePendingAndData(line)

		case "message_delta":
			if anyBlocked {
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
					discardPending()
					writeSSE(w, flusher, "event: message_delta")
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
			writePendingAndData(line)
		default:
			writePendingAndData(line)
		}
	}
	flushPending()
	duration := time.Since(start).Milliseconds()
	return []byte(respBody.String()), finalUsage, finalToolCalls, stopReason, duration, scanner.Err()
}

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

// collectSSE reads Server-Sent Events from the upstream response and buffers them
// entirely without forwarding to any client. It returns the raw SSE text (for later
// forwarding if no blocking is needed), the parsed content blocks (for constructing
// follow-up requests), and aggregated metadata (usage, tool calls, stop reason,
// response body text).
func collectSSE(upstreamResp *http.Response) (sseText string, contentBlocks []ContentBlock, respBody []byte, usage UsageData, tools []ToolCall, stopReason string, ttftMs int64, durationMs int64, err error) {
	start := time.Now()
	var ttftSet bool

	var buf strings.Builder
	var finalUsage UsageData
	var finalTools []ToolCall
	var finalStopReason string
	var respText strings.Builder
	var finalBlocks []ContentBlock

	var pendingEvent string

	writeLine := func(line string) {
		buf.WriteString(line)
		buf.WriteByte('\n')
	}

	scanner := bufio.NewScanner(upstreamResp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "event: ") {
			if pendingEvent != "" {
				writeLine(pendingEvent)
			}
			pendingEvent = line
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			if pendingEvent != "" {
				writeLine(pendingEvent)
				pendingEvent = ""
			}
			writeLine(line)
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		var raw map[string]any
		if err := json.Unmarshal([]byte(data), &raw); err != nil {
			if pendingEvent != "" {
				writeLine(pendingEvent)
				pendingEvent = ""
			}
			writeLine(line)
			continue
		}
		evtType, _ := raw["type"].(string)

		switch evtType {
		case "content_block_start":
			block, ok := raw["content_block"].(map[string]any)
			if ok {
				var cb ContentBlock
				if t, _ := block["type"].(string); t == "tool_use" {
					cb.Type = "tool_use"
					cb.ToolUseID, _ = block["id"].(string)
					cb.Name, _ = block["name"].(string)
					cb.Input, _ = block["input"].(map[string]any)
					if !ttftSet {
						ttftMs = time.Since(start).Milliseconds()
						ttftSet = true
					}
					var tc ToolCall
					tc.ID = cb.ToolUseID
					tc.Name = cb.Name
					tc.Input = cb.Input
					finalTools = append(finalTools, tc)
				} else {
					cb.Type = "text"
					if !ttftSet {
						ttftMs = time.Since(start).Milliseconds()
						ttftSet = true
					}
				}
				finalBlocks = append(finalBlocks, cb)
			}
			if pendingEvent != "" {
				writeLine(pendingEvent)
				pendingEvent = ""
			}
			writeLine(line)

		case "content_block_delta":
			if delta, ok := raw["delta"].(map[string]any); ok {
				if delta["type"] == "text_delta" {
					if !ttftSet {
						ttftMs = time.Since(start).Milliseconds()
						ttftSet = true
					}
					if text, ok := delta["text"].(string); ok {
						respText.WriteString(text)
						// Accumulate text into the last text content block.
						if len(finalBlocks) > 0 && finalBlocks[len(finalBlocks)-1].Type == "text" {
							finalBlocks[len(finalBlocks)-1].Text += text
						}
					}
				}
			}
			if pendingEvent != "" {
				writeLine(pendingEvent)
				pendingEvent = ""
			}
			writeLine(line)

		case "content_block_stop":
			if pendingEvent != "" {
				writeLine(pendingEvent)
				pendingEvent = ""
			}
			writeLine(line)

		case "message_delta":
			if delta, ok := raw["delta"].(map[string]any); ok {
				if sr, ok := delta["stop_reason"].(string); ok {
					finalStopReason = sr
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
			if pendingEvent != "" {
				writeLine(pendingEvent)
				pendingEvent = ""
			}
			writeLine(line)

		default:
			// OpenAI streaming format: no event type, data contains choices[].delta.
			if choices, ok := raw["choices"].([]any); ok && len(choices) > 0 {
				if first, ok := choices[0].(map[string]any); ok {
					if delta, ok := first["delta"].(map[string]any); ok {
						if content, ok := delta["content"].(string); ok {
							if !ttftSet {
								ttftMs = time.Since(start).Milliseconds()
								ttftSet = true
							}
							respText.WriteString(content)
						}
					}
					if fr, ok := first["finish_reason"].(string); ok && fr != "" && fr != "null" {
						finalStopReason = fr
					}
				}
			}
			if u, ok := raw["usage"].(map[string]any); ok {
				if v, ok := u["prompt_tokens"].(float64); ok {
					finalUsage.InputTokens = int(v)
				}
				if v, ok := u["completion_tokens"].(float64); ok {
					finalUsage.OutputTokens = int(v)
				}
				if details, ok := u["prompt_tokens_details"].(map[string]any); ok {
					if v, ok := details["cached_tokens"].(float64); ok {
						finalUsage.CacheReadTokens = int(v)
					}
				}
			}
			if pendingEvent != "" {
				writeLine(pendingEvent)
				pendingEvent = ""
			}
			writeLine(line)
		}
	}
	if pendingEvent != "" {
		writeLine(pendingEvent)
	}

	durationMs = time.Since(start).Milliseconds()
	return buf.String(), finalBlocks, []byte(respText.String()), finalUsage, finalTools, finalStopReason, ttftMs, durationMs, scanner.Err()
}

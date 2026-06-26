package proxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func streamAndCollect(upstreamResp *http.Response, w http.ResponseWriter) (UsageData, []ToolCall, string, int64, error) {
	start := time.Now()
	flusher, ok := w.(http.Flusher)
	if !ok {
		return UsageData{}, nil, "", 0, fmt.Errorf("response writer does not support flushing")
	}

	var finalUsage UsageData
	var finalToolCalls []ToolCall
	var stopReason string

	scanner := bufio.NewScanner(upstreamResp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		_, _ = fmt.Fprintf(w, "%s\n", line)
		flusher.Flush()

		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			var raw map[string]any
			if err := json.Unmarshal([]byte(data), &raw); err != nil {
				continue
			}
			evtType, _ := raw["type"].(string)

			switch evtType {
			case "message_delta":
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
			case "content_block_start":
				if block, ok := raw["content_block"].(map[string]any); ok {
					if block["type"] == "tool_use" {
						var tc ToolCall
						if name, ok := block["name"].(string); ok {
							tc.Name = name
						}
						if input, ok := block["input"].(map[string]any); ok {
							tc.Input = input
						}
						finalToolCalls = append(finalToolCalls, tc)
					}
				}
			}
		}
	}
	duration := time.Since(start).Milliseconds()
	return finalUsage, finalToolCalls, stopReason, duration, scanner.Err()
}

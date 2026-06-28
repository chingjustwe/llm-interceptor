// Package translate provides protocol translation between the Anthropic
// Messages API and the OpenAI Chat Completions API.
package translate

import "encoding/json"

// ToOpenAI converts an Anthropic Messages API request body to OpenAI Chat
// Completions format. It maps system, messages, max_tokens, temperature,
// top_p, stop_sequences, tools, tool_choice, and metadata fields.
func ToOpenAI(anthropicBody []byte) ([]byte, error) {
	var req struct {
		Model        string              `json:"model"`
		Messages     []json.RawMessage   `json:"messages"`
		System       *string             `json:"system,omitempty"`
		MaxTokens    *int                `json:"max_tokens,omitempty"`
		Stream       bool                `json:"stream,omitempty"`
		Temperature  *float64            `json:"temperature,omitempty"`
		TopP         *float64            `json:"top_p,omitempty"`
		StopSeq      []string            `json:"stop_sequences,omitempty"`
		Tools        []json.RawMessage   `json:"tools,omitempty"`
		ToolChoice   any                 `json:"tool_choice,omitempty"`
		Metadata     *struct {
			UserID string `json:"user_id,omitempty"`
		} `json:"metadata,omitempty"`
		Thinking     *struct {
			Type string `json:"type"`
		} `json:"thinking,omitempty"`
	}
	if err := json.Unmarshal(anthropicBody, &req); err != nil {
		return nil, err
	}

	out := map[string]any{
		"model":  req.Model,
		"stream": req.Stream,
	}

	var msgs []map[string]any
	if req.System != nil {
		msgs = append(msgs, map[string]any{"role": "system", "content": *req.System})
	}
	for _, m := range req.Messages {
		var msg struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		}
		if err := json.Unmarshal(m, &msg); err != nil {
			continue
		}
		if content, ok := msg.Content.([]any); ok && msg.Role == "user" {
			isToolResult := false
			for _, c := range content {
				if block, ok := c.(map[string]any); ok {
					if block["type"] == "tool_result" {
						isToolResult = true
						break
					}
				}
			}
			if isToolResult {
				msgs = append(msgs, map[string]any{"role": "tool", "content": msg.Content})
				continue
			}
		}
		msgs = append(msgs, map[string]any{"role": msg.Role, "content": msg.Content})
	}
	out["messages"] = msgs

	if req.MaxTokens != nil {
		out["max_completion_tokens"] = *req.MaxTokens
	}
	if req.Temperature != nil {
		out["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		out["top_p"] = *req.TopP
	}
	if len(req.StopSeq) > 0 {
		out["stop"] = req.StopSeq
	}

	if len(req.Tools) > 0 {
		var openAITools []map[string]any
		for _, t := range req.Tools {
			var tool struct {
				Name        string `json:"name"`
				Description string `json:"description,omitempty"`
				InputSchema any    `json:"input_schema,omitempty"`
			}
			if err := json.Unmarshal(t, &tool); err != nil {
				continue
			}
			openAITools = append(openAITools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        tool.Name,
					"description": tool.Description,
					"parameters":  tool.InputSchema,
				},
			})
		}
		out["tools"] = openAITools
	}

	if req.ToolChoice != nil {
		if tc, ok := req.ToolChoice.(map[string]any); ok {
			if tc["type"] == "any" {
				out["tool_choice"] = "required"
			} else {
				out["tool_choice"] = tc
			}
		} else {
			out["tool_choice"] = req.ToolChoice
		}
	}

	if req.Metadata != nil && req.Metadata.UserID != "" {
		out["user"] = req.Metadata.UserID
	}

	return json.Marshal(out)
}

// AnthropicToOpenAIResponse converts an Anthropic Messages API response body
// to OpenAI Chat Completions response format. It maps content blocks (text
// and tool_use) and stop_reason.
func AnthropicToOpenAIResponse(anthropicBody []byte) ([]byte, error) {
	var resp struct {
		ID         string              `json:"id"`
		Model      string              `json:"model"`
		Type       string              `json:"type"`
		Role       string              `json:"role"`
		Content    []json.RawMessage   `json:"content"`
		StopReason string              `json:"stop_reason"`
		Usage      *struct {
			InputTokens         int `json:"input_tokens"`
			OutputTokens        int `json:"output_tokens"`
			CacheReadTokens     int `json:"cache_read_input_tokens,omitempty"`
			CacheCreationTokens int `json:"cache_creation_input_tokens,omitempty"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(anthropicBody, &resp); err != nil {
		return nil, err
	}

	var textParts []string
	var toolCalls []map[string]any

	for _, c := range resp.Content {
		var block struct {
			Type  string `json:"type"`
			Text  string `json:"text,omitempty"`
			ID    string `json:"id,omitempty"`
			Name  string `json:"name,omitempty"`
			Input any    `json:"input,omitempty"`
		}
		if err := json.Unmarshal(c, &block); err != nil {
			continue
		}
		switch block.Type {
		case "text":
			textParts = append(textParts, block.Text)
		case "tool_use":
			toolCalls = append(toolCalls, map[string]any{
				"id":   block.ID,
				"type": "function",
				"function": map[string]any{
					"name":      block.Name,
					"arguments": block.Input,
				},
			})
		}
	}

	content := ""
	for _, t := range textParts {
		content += t
	}

	finishReason := ""
	switch resp.StopReason {
	case "end_turn":
		finishReason = "stop"
	case "max_tokens":
		finishReason = "length"
	case "tool_use":
		finishReason = "tool_calls"
	case "refusal":
		finishReason = "content_filter"
	default:
		finishReason = resp.StopReason
	}

	choice := map[string]any{
		"index": 0,
		"message": map[string]any{
			"role":    "assistant",
			"content": content,
		},
		"finish_reason": finishReason,
	}
	if len(toolCalls) > 0 {
		choice["message"].(map[string]any)["tool_calls"] = toolCalls
	}

	out := map[string]any{
		"id":      resp.ID,
		"object":  "chat.completion",
		"model":   resp.Model,
		"choices": []any{choice},
	}

	if resp.Usage != nil {
		usage := map[string]any{
			"prompt_tokens":     resp.Usage.InputTokens,
			"completion_tokens": resp.Usage.OutputTokens,
		}
		if resp.Usage.CacheReadTokens > 0 || resp.Usage.CacheCreationTokens > 0 {
			usage["prompt_tokens_details"] = map[string]any{
				"cached_tokens": resp.Usage.CacheReadTokens,
			}
		}
		out["usage"] = usage
	}

	return json.Marshal(out)
}

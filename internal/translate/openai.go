// Package translate provides protocol translation between the Anthropic
// Messages API and the OpenAI Chat Completions API.
package translate

import "encoding/json"

// ToAnthropic converts an OpenAI Chat Completions API request body to
// Anthropic Messages API format. It maps messages, max_tokens,
// temperature, top_p, stop, tools, tool_choice, response_format, and user.
func ToAnthropic(openAIBody []byte) ([]byte, error) {
	var req struct {
		Model               string            `json:"model"`
		Messages            []json.RawMessage `json:"messages"`
		MaxCompletionTokens *int              `json:"max_completion_tokens,omitempty"`
		MaxTokens           *int              `json:"max_tokens,omitempty"`
		Stream              bool              `json:"stream,omitempty"`
		Temperature         *float64          `json:"temperature,omitempty"`
		TopP                *float64          `json:"top_p,omitempty"`
		Stop                []string          `json:"stop,omitempty"`
		Tools               []json.RawMessage `json:"tools,omitempty"`
		ToolChoice          any               `json:"tool_choice,omitempty"`
		ResponseFormat      any               `json:"response_format,omitempty"`
		User                string            `json:"user,omitempty"`
	}
	if err := json.Unmarshal(openAIBody, &req); err != nil {
		return nil, err
	}

	out := map[string]any{
		"model":  req.Model,
		"stream": req.Stream,
	}

	var msgs []map[string]any
	var systemContent string
	for _, m := range req.Messages {
		var msg struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		}
		if err := json.Unmarshal(m, &msg); err != nil {
			continue
		}
		switch msg.Role {
		case "system":
			if s, ok := msg.Content.(string); ok {
				systemContent += s
			}
		case "tool":
			msgs = append(msgs, map[string]any{"role": "user", "content": msg.Content})
		case "assistant":
			msgs = append(msgs, map[string]any{"role": "assistant", "content": msg.Content})
		default:
			msgs = append(msgs, map[string]any{"role": msg.Role, "content": msg.Content})
		}
	}
	if systemContent != "" {
		out["system"] = systemContent
	}
	out["messages"] = msgs

	if req.MaxCompletionTokens != nil {
		out["max_tokens"] = *req.MaxCompletionTokens
	} else if req.MaxTokens != nil {
		out["max_tokens"] = *req.MaxTokens
	}

	if req.Temperature != nil {
		out["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		out["top_p"] = *req.TopP
	}
	if len(req.Stop) > 0 {
		out["stop_sequences"] = req.Stop
	}

	if len(req.Tools) > 0 {
		var anthropicTools []map[string]any
		for _, t := range req.Tools {
			var tool struct {
				Type     string `json:"type"`
				Function *struct {
					Name        string `json:"name"`
					Description string `json:"description,omitempty"`
					Parameters  any    `json:"parameters,omitempty"`
				} `json:"function,omitempty"`
			}
			if err := json.Unmarshal(t, &tool); err != nil {
				continue
			}
			if tool.Function == nil {
				continue
			}
			at := map[string]any{
				"name":        tool.Function.Name,
				"description": tool.Function.Description,
			}
			if tool.Function.Parameters != nil {
				at["input_schema"] = tool.Function.Parameters
			}
			anthropicTools = append(anthropicTools, at)
		}
		out["tools"] = anthropicTools
	}

	if req.ToolChoice != nil {
		out["tool_choice"] = req.ToolChoice
	}
	if req.ResponseFormat != nil {
		out["output_config"] = map[string]any{"format": req.ResponseFormat}
	}
	if req.User != "" {
		out["metadata"] = map[string]any{"user_id": req.User}
	}

	return json.Marshal(out)
}

// OpenAIToAnthropicResponse converts an OpenAI Chat Completions response
// body to Anthropic Messages API format. It maps tool_calls to tool_use
// content blocks and maps finish_reason.
func OpenAIToAnthropicResponse(openAIBody []byte) ([]byte, error) {
	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Index   int `json:"index"`
			Message struct {
				Role      string `json:"role"`
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls,omitempty"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens         int `json:"prompt_tokens"`
			CompletionTokens     int `json:"completion_tokens"`
			PromptTokensDetails  *struct {
				CachedTokens int `json:"cached_tokens,omitempty"`
			} `json:"prompt_tokens_details,omitempty"`
			CompletionTokensDetails *struct {
				ReasoningTokens int `json:"reasoning_tokens,omitempty"`
			} `json:"completion_tokens_details,omitempty"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(openAIBody, &resp); err != nil {
		return nil, err
	}

	var content []map[string]any
	var stopReason string

	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]
		if choice.Message.Content != "" {
			content = append(content, map[string]any{
				"type": "text",
				"text": choice.Message.Content,
			})
		}
		for _, tc := range choice.Message.ToolCalls {
			var input any
			json.Unmarshal([]byte(tc.Function.Arguments), &input)
			if input == nil {
				input = tc.Function.Arguments
			}
			content = append(content, map[string]any{
				"type":  "tool_use",
				"id":    tc.ID,
				"name":  tc.Function.Name,
				"input": input,
			})
		}

		switch choice.FinishReason {
		case "stop":
			stopReason = "end_turn"
		case "length":
			stopReason = "max_tokens"
		case "tool_calls":
			stopReason = "tool_use"
		case "content_filter":
			stopReason = "refusal"
		default:
			stopReason = choice.FinishReason
		}
	}

	if content == nil {
		content = []map[string]any{}
	}

	usage := map[string]any{
		"input_tokens":  0,
		"output_tokens": 0,
	}
	if resp.Usage != nil {
		usage["input_tokens"] = resp.Usage.PromptTokens
		usage["output_tokens"] = resp.Usage.CompletionTokens
		if resp.Usage.PromptTokensDetails != nil && resp.Usage.PromptTokensDetails.CachedTokens > 0 {
			usage["cache_read_input_tokens"] = resp.Usage.PromptTokensDetails.CachedTokens
		}
	}

	out := map[string]any{
		"id":          resp.ID,
		"model":       resp.Model,
		"type":        "message",
		"role":        "assistant",
		"content":     content,
		"stop_reason": stopReason,
		"usage":       usage,
	}
	return json.Marshal(out)
}

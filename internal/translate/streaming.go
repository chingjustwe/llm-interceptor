// Package translate provides protocol translation between the Anthropic
// Messages API and the OpenAI Chat Completions API, including streaming SSE
// event translation for bidirectional protocol interoperability.
package translate

import (
	"encoding/json"
	"strings"
)

// SSEEvent represents a single parsed Server-Sent Event from any LLM provider.
type SSEEvent struct {
	Event string           // event type ("message_start", "content_block_delta", etc.)
	Data  json.RawMessage  // event data as raw JSON
}

// StreamParser parses raw SSE lines from an upstream provider and emits
// complete SSEEvent values when a full event has been received.
type StreamParser interface {
	// ParseEvent processes a single SSE line and returns a complete event
	// if one has been assembled. The bool indicates if the event is complete
	// and ready for processing.
	ParseEvent(line string) (*SSEEvent, bool)
}

// StreamTranslator converts parsed SSE events between Anthropic and OpenAI
// streaming protocols. Each Translate method returns zero or more translated
// events for the target protocol.
type StreamTranslator interface {
	TranslateAnthropicToOpenAI(event *SSEEvent) []SSEEvent
	TranslateOpenAIToAnthropic(event *SSEEvent) []SSEEvent
}

// anthropicStreamParser implements StreamParser for Anthropic SSE format.
// Anthropic uses event: xxx lines followed by data: {...} lines.
type anthropicStreamParser struct {
	pendingEvent string
}

// NewAnthropicStreamParser creates a new parser for Anthropic SSE format.
func NewAnthropicStreamParser() StreamParser {
	return &anthropicStreamParser{}
}

func (p *anthropicStreamParser) ParseEvent(line string) (*SSEEvent, bool) {
	if strings.HasPrefix(line, "event: ") {
		p.pendingEvent = strings.TrimPrefix(line, "event: ")
		return nil, false
	}
	if strings.HasPrefix(line, "data: ") {
		data := strings.TrimPrefix(line, "data: ")
		evt := &SSEEvent{
			Event: p.pendingEvent,
			Data:  json.RawMessage(data),
		}
		p.pendingEvent = ""
		return evt, true
	}
	return nil, false
}

// openAIStreamParser implements StreamParser for OpenAI SSE format.
// OpenAI uses data: {...} lines with "data: [DONE]" as the termination signal.
type openAIStreamParser struct{}

// NewOpenAIStreamParser creates a new parser for OpenAI SSE format.
func NewOpenAIStreamParser() StreamParser {
	return &openAIStreamParser{}
}

func (p *openAIStreamParser) ParseEvent(line string) (*SSEEvent, bool) {
	if strings.HasPrefix(line, "data: ") {
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			return &SSEEvent{Event: "done", Data: json.RawMessage(`"[DONE]"`)}, true
		}
		return &SSEEvent{Event: "delta", Data: json.RawMessage(data)}, true
	}
	return nil, false
}

// streamTranslator implements StreamTranslator with bidirectional conversion
// between Anthropic and OpenAI streaming formats.
type streamTranslator struct{}

// NewStreamTranslator creates a new bidirectional stream translator.
func NewStreamTranslator() StreamTranslator {
	return &streamTranslator{}
}

// TranslateAnthropicToOpenAI converts an Anthropic SSE event to zero or more
// OpenAI SSE chunk events.
func (t *streamTranslator) TranslateAnthropicToOpenAI(event *SSEEvent) []SSEEvent {
	switch event.Event {
	case "message_start":
		return []SSEEvent{{
			Event: "delta",
			Data: mustMarshal(map[string]any{
				"choices": []any{map[string]any{
					"index": 0,
					"delta": map[string]any{"role": "assistant", "content": ""},
				}},
			}),
		}}

	case "content_block_start":
		var block struct {
			Index        int `json:"index"`
			ContentBlock *struct {
				Type string `json:"type"`
				Text string `json:"text,omitempty"`
				ID   string `json:"id,omitempty"`
				Name string `json:"name,omitempty"`
			} `json:"content_block"`
		}
		if err := json.Unmarshal(event.Data, &block); err != nil {
			return nil
		}
		if block.ContentBlock == nil {
			return nil
		}
		switch block.ContentBlock.Type {
		case "text":
			return nil
		case "tool_use":
			return []SSEEvent{{
				Event: "delta",
				Data: mustMarshal(map[string]any{
					"choices": []any{map[string]any{
						"index": 0,
						"delta": map[string]any{
							"tool_calls": []any{map[string]any{
								"index":    block.Index,
								"id":       block.ContentBlock.ID,
								"type":     "function",
								"function": map[string]any{"name": block.ContentBlock.Name},
							}},
						},
					}},
				}),
			}}
		default:
			return nil
		}

	case "content_block_delta":
		var raw struct {
			Index int             `json:"index"`
			Delta json.RawMessage `json:"delta"`
		}
		if err := json.Unmarshal(event.Data, &raw); err != nil {
			return nil
		}
		var deltaType struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw.Delta, &deltaType); err != nil {
			return nil
		}
		switch deltaType.Type {
		case "text_delta":
			var textDelta struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(raw.Delta, &textDelta); err != nil {
				return nil
			}
			return []SSEEvent{{
				Event: "delta",
				Data: mustMarshal(map[string]any{
					"choices": []any{map[string]any{
						"index": 0,
						"delta": map[string]any{"content": textDelta.Text},
					}},
				}),
			}}
		case "input_json_delta":
			var inputDelta struct {
				PartialJSON string `json:"partial_json"`
			}
			if err := json.Unmarshal(raw.Delta, &inputDelta); err != nil {
				return nil
			}
			return []SSEEvent{{
				Event: "delta",
				Data: mustMarshal(map[string]any{
					"choices": []any{map[string]any{
						"index": 0,
						"delta": map[string]any{
							"tool_calls": []any{map[string]any{
								"index":    raw.Index,
								"function": map[string]any{"arguments": inputDelta.PartialJSON},
							}},
						},
					}},
				}),
			}}
		default:
			return nil
		}

	case "content_block_stop":
		return nil

	case "message_delta":
		var msgDelta struct {
			Delta *struct {
				StopReason string `json:"stop_reason,omitempty"`
			} `json:"delta"`
			Usage *struct {
				InputTokens         int `json:"input_tokens,omitempty"`
				OutputTokens        int `json:"output_tokens,omitempty"`
				CacheReadTokens     int `json:"cache_read_input_tokens,omitempty"`
				CacheCreationTokens int `json:"cache_creation_input_tokens,omitempty"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(event.Data, &msgDelta); err != nil {
			return nil
		}

		finishReason := ""
		if msgDelta.Delta != nil {
			switch msgDelta.Delta.StopReason {
			case "end_turn":
				finishReason = "stop"
			case "max_tokens":
				finishReason = "length"
			case "tool_use":
				finishReason = "tool_calls"
			case "refusal":
				finishReason = "content_filter"
			default:
				finishReason = msgDelta.Delta.StopReason
			}
		}

		chunk := map[string]any{
			"choices": []any{map[string]any{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": finishReason,
			}},
		}

		if msgDelta.Usage != nil {
			usage := map[string]any{
				"prompt_tokens":     msgDelta.Usage.InputTokens,
				"completion_tokens": msgDelta.Usage.OutputTokens,
			}
			if msgDelta.Usage.CacheReadTokens > 0 {
				usage["prompt_tokens_details"] = map[string]any{
					"cached_tokens": msgDelta.Usage.CacheReadTokens,
				}
			}
			chunk["usage"] = usage
		}

		return []SSEEvent{{
			Event: "delta",
			Data:  mustMarshal(chunk),
		}}

	case "message_stop":
		return []SSEEvent{{
			Event: "delta",
			Data:  json.RawMessage(`"[DONE]"`),
		}}

	default:
		return nil
	}
}

// TranslateOpenAIToAnthropic converts an OpenAI SSE chunk to zero or more
// Anthropic SSE events.
func (t *streamTranslator) TranslateOpenAIToAnthropic(event *SSEEvent) []SSEEvent {
	if event.Event == "done" {
		return []SSEEvent{{
			Event: "message_stop",
			Data:  mustMarshal(map[string]any{"type": "message_stop"}),
		}}
	}

	var chunk struct {
		Choices []struct {
			Index        int             `json:"index"`
			Delta        json.RawMessage `json:"delta"`
			FinishReason string          `json:"finish_reason,omitempty"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(event.Data, &chunk); err != nil || len(chunk.Choices) == 0 {
		return nil
	}

	choice := chunk.Choices[0]

	var delta struct {
		Role      string `json:"role,omitempty"`
		Content   string `json:"content,omitempty"`
		ToolCalls []struct {
			Index    int    `json:"index"`
			ID       string `json:"id,omitempty"`
			Type     string `json:"type,omitempty"`
			Function *struct {
				Name      string `json:"name,omitempty"`
				Arguments string `json:"arguments,omitempty"`
			} `json:"function,omitempty"`
		} `json:"tool_calls,omitempty"`
	}
	if err := json.Unmarshal(choice.Delta, &delta); err != nil {
		return nil
	}

	var events []SSEEvent

	if delta.Role == "assistant" {
		events = append(events, SSEEvent{
			Event: "message_start",
			Data: mustMarshal(map[string]any{
				"type":    "message_start",
				"message": map[string]any{"role": "assistant"},
			}),
		})
	}

	if delta.Content != "" {
		events = append(events, SSEEvent{
			Event: "content_block_start",
			Data: mustMarshal(map[string]any{
				"type":  "content_block_start",
				"index": 0,
				"content_block": map[string]any{
					"type": "text",
					"text": "",
				},
			}),
		})
		events = append(events, SSEEvent{
			Event: "content_block_delta",
			Data: mustMarshal(map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{
					"type": "text_delta",
					"text": delta.Content,
				},
			}),
		})
		events = append(events, SSEEvent{
			Event: "content_block_stop",
			Data: mustMarshal(map[string]any{
				"type":  "content_block_stop",
				"index": 0,
			}),
		})
	}

	for _, tc := range delta.ToolCalls {
		if tc.ID != "" {
			events = append(events, SSEEvent{
				Event: "content_block_start",
				Data: mustMarshal(map[string]any{
					"type":  "content_block_start",
					"index": tc.Index,
					"content_block": map[string]any{
						"type": "tool_use",
						"id":   tc.ID,
						"name": tc.Function.Name,
					},
				}),
			})
		}
		if tc.Function != nil && tc.Function.Arguments != "" {
			events = append(events, SSEEvent{
				Event: "content_block_delta",
				Data: mustMarshal(map[string]any{
					"type":  "content_block_delta",
					"index": tc.Index,
					"delta": map[string]any{
						"type":         "input_json_delta",
						"partial_json": tc.Function.Arguments,
					},
				}),
			})
		}
	}

	if choice.FinishReason != "" {
		stopReason := ""
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

		events = append(events, SSEEvent{
			Event: "message_delta",
			Data: mustMarshal(map[string]any{
				"type":  "message_delta",
				"delta": map[string]any{"stop_reason": stopReason},
			}),
		})
	}

	return events
}

// mustMarshal is a helper that marshals v to JSON and returns a RawMessage.
// Panics on failure — only used internally with known-safe types.
func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic("translate: mustMarshal: " + err.Error())
	}
	return b
}

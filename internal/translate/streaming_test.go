package translate

import (
	"testing"
)

func TestAnthropicStreamParser(t *testing.T) {
	parser := NewAnthropicStreamParser()

	evt, ok := parser.ParseEvent("event: message_start")
	if ok || evt != nil {
		t.Fatal("expected incomplete event after event line")
	}

	evt, ok = parser.ParseEvent(`data: {"type":"message_start","message":{"role":"assistant"}}`)
	if !ok || evt == nil {
		t.Fatal("expected complete event")
	}
	if evt.Event != "message_start" {
		t.Fatalf("expected event message_start, got %s", evt.Event)
	}
}

func TestOpenAIStreamParser(t *testing.T) {
	parser := NewOpenAIStreamParser()

	evt, ok := parser.ParseEvent(`data: {"choices":[{"delta":{"role":"assistant"}}]}`)
	if !ok || evt == nil {
		t.Fatal("expected complete event")
	}
	if evt.Event != "delta" {
		t.Fatalf("expected event delta, got %s", evt.Event)
	}

	evt, ok = parser.ParseEvent("data: [DONE]")
	if !ok || evt == nil {
		t.Fatal("expected done event")
	}
	if evt.Event != "done" {
		t.Fatalf("expected event done, got %s", evt.Event)
	}
}

func TestStreamTranslate_AnthropicToOpenAI(t *testing.T) {
	translator := NewStreamTranslator()

	events := translator.TranslateAnthropicToOpenAI(&SSEEvent{
		Event: "message_start",
		Data: mustMarshal(map[string]any{
			"type":    "message_start",
			"message": map[string]any{"role": "assistant"},
		}),
	})
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Event != "delta" {
		t.Fatalf("expected delta event, got %s", events[0].Event)
	}

	events = translator.TranslateAnthropicToOpenAI(&SSEEvent{
		Event: "content_block_delta",
		Data: mustMarshal(map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{"type": "text_delta", "text": "Hello"},
		}),
	})
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	events = translator.TranslateAnthropicToOpenAI(&SSEEvent{
		Event: "message_delta",
		Data: mustMarshal(map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": "end_turn"},
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
		}),
	})
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	events = translator.TranslateAnthropicToOpenAI(&SSEEvent{
		Event: "message_stop",
		Data:  mustMarshal(map[string]any{"type": "message_stop"}),
	})
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
}

func TestStreamTranslate_OpenAIToAnthropic(t *testing.T) {
	translator := NewStreamTranslator()

	events := translator.TranslateOpenAIToAnthropic(&SSEEvent{
		Event: "delta",
		Data: mustMarshal(map[string]any{
			"choices": []any{map[string]any{
				"index": 0,
				"delta": map[string]any{"role": "assistant", "content": ""},
			}},
		}),
	})
	if len(events) < 1 {
		t.Fatalf("expected at least 1 event, got %d", len(events))
	}
	if events[0].Event != "message_start" {
		t.Fatalf("expected first event to be message_start, got %s", events[0].Event)
	}

	events = translator.TranslateOpenAIToAnthropic(&SSEEvent{
		Event: "delta",
		Data: mustMarshal(map[string]any{
			"choices": []any{map[string]any{
				"index": 0,
				"delta": map[string]any{"content": "Hello"},
			}},
		}),
	})
	if len(events) != 3 {
		t.Fatalf("expected 3 events (start, delta, stop), got %d", len(events))
	}

	events = translator.TranslateOpenAIToAnthropic(&SSEEvent{
		Event: "done",
		Data:  mustMarshal("[DONE]"),
	})
	if len(events) != 1 || events[0].Event != "message_stop" {
		t.Fatalf("expected message_stop event, got %v", events)
	}
}

func TestStreamTranslate_ToolCallsBidirectional(t *testing.T) {
	translator := NewStreamTranslator()

	events := translator.TranslateAnthropicToOpenAI(&SSEEvent{
		Event: "content_block_start",
		Data: mustMarshal(map[string]any{
			"type":  "content_block_start",
			"index": 0,
			"content_block": map[string]any{
				"type": "tool_use",
				"id":   "toolu_01",
				"name": "Bash",
			},
		}),
	})
	if len(events) != 1 {
		t.Fatalf("expected 1 event for tool start, got %d", len(events))
	}

	events = translator.TranslateOpenAIToAnthropic(&SSEEvent{
		Event: "delta",
		Data: mustMarshal(map[string]any{
			"choices": []any{map[string]any{
				"index": 0,
				"delta": map[string]any{
					"tool_calls": []any{map[string]any{
						"index":    0,
						"id":       "call_01",
						"type":     "function",
						"function": map[string]any{"name": "Bash", "arguments": `{"cmd":"ls"}`},
					}},
				},
			}},
		}),
	})
	if len(events) >= 1 {
		t.Logf("OpenAI→Anthropic tool_calls produced %d events", len(events))
	}
}

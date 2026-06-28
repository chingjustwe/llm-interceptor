package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToOpenAI_Basic(t *testing.T) {
	anthropicBody := []byte(`{
		"model": "claude-sonnet-4-6",
		"messages": [{"role":"user","content":"Hello"}],
		"max_tokens": 100
	}`)

	result, err := ToOpenAI(anthropicBody)
	if err != nil {
		t.Fatalf("ToOpenAI failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if parsed["model"] != "claude-sonnet-4-6" {
		t.Fatalf("expected model claude-sonnet-4-6, got %v", parsed["model"])
	}
	if parsed["max_completion_tokens"] != float64(100) {
		t.Fatalf("expected max_completion_tokens 100, got %v", parsed["max_completion_tokens"])
	}
	msgs := parsed["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["content"] != "Hello" {
		t.Fatalf("expected 1 user message 'Hello', got %+v", msgs)
	}
}

func TestToOpenAI_SystemField(t *testing.T) {
	anthropicBody := []byte(`{
		"model": "claude-sonnet-4-6",
		"system": "You are a helpful assistant.",
		"messages": [{"role":"user","content":"Hi"}],
		"max_tokens": 50
	}`)

	result, err := ToOpenAI(anthropicBody)
	if err != nil {
		t.Fatalf("ToOpenAI failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	msgs := parsed["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %d", len(msgs))
	}
	msg0 := msgs[0].(map[string]any)
	if msg0["role"] != "system" {
		t.Fatalf("expected first message role=system, got %s", msg0["role"])
	}
	if msg0["content"] != "You are a helpful assistant." {
		t.Fatalf("expected system content, got %s", msg0["content"])
	}
	msg1 := msgs[1].(map[string]any)
	if msg1["role"] != "user" {
		t.Fatalf("expected second message role=user, got %s", msg1["role"])
	}
}

func TestToOpenAI_NoMaxTokens(t *testing.T) {
	anthropicBody := []byte(`{
		"model": "claude-sonnet-4-6",
		"messages": [{"role":"user","content":"Hello"}]
	}`)

	result, err := ToOpenAI(anthropicBody)
	if err != nil {
		t.Fatalf("ToOpenAI failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if _, ok := parsed["max_completion_tokens"]; ok {
		t.Fatal("expected max_completion_tokens to be omitted")
	}
}

func TestToAnthropic_Basic(t *testing.T) {
	openAIBody := []byte(`{
		"model": "gpt-4",
		"messages": [{"role":"user","content":"Hello"}],
		"max_completion_tokens": 100
	}`)

	result, err := ToAnthropic(openAIBody)
	if err != nil {
		t.Fatalf("ToAnthropic failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if parsed["model"] != "gpt-4" {
		t.Fatalf("expected model gpt-4, got %v", parsed["model"])
	}
	if parsed["max_tokens"] != float64(100) {
		t.Fatalf("expected max_tokens 100, got %v", parsed["max_tokens"])
	}
	msgs := parsed["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
}

func TestToAnthropic_NoUsage(t *testing.T) {
	openAIBody := []byte(`{
		"model": "gpt-4",
		"messages": [{"role":"user","content":"Hello"}]
	}`)

	result, err := ToAnthropic(openAIBody)
	if err != nil {
		t.Fatalf("ToAnthropic failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if _, ok := parsed["max_tokens"]; ok {
		t.Fatal("expected max_tokens to be omitted when no max_tokens in input")
	}
}

func TestToOpenAI_InvalidJSON(t *testing.T) {
	_, err := ToOpenAI([]byte(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestToAnthropic_InvalidJSON(t *testing.T) {
	_, err := ToAnthropic([]byte(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestAnthropicToOpenAI_Response_Basic(t *testing.T) {
	anthropicBody := []byte(`{
		"id": "msg_123",
		"model": "claude-sonnet-4-6",
		"type": "message",
		"role": "assistant",
		"content": [
			{"type":"text","text":"Let me search."},
			{"type":"tool_use","id":"tu_1","name":"search","input":{"query":"weather"}}
		],
		"stop_reason": "tool_use",
		"usage": {"input_tokens":10,"output_tokens":20}
	}`)

	result, err := AnthropicToOpenAIResponse(anthropicBody)
	if err != nil {
		t.Fatalf("AnthropicToOpenAIResponse failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if parsed["id"] != "msg_123" {
		t.Fatalf("expected id msg_123, got %v", parsed["id"])
	}
	if parsed["object"] != "chat.completion" {
		t.Fatalf("expected object chat.completion, got %v", parsed["object"])
	}

	choices := parsed["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(choices))
	}
	choice := choices[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("expected finish_reason tool_calls, got %v", choice["finish_reason"])
	}

	msg := choice["message"].(map[string]any)
	if !strings.Contains(msg["content"].(string), "Let me search.") {
		t.Fatalf("expected content containing 'Let me search.', got %v", msg["content"])
	}

	toolCalls := msg["tool_calls"].([]any)
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool_call, got %d", len(toolCalls))
	}
	tc := toolCalls[0].(map[string]any)
	if tc["type"] != "function" {
		t.Fatalf("expected tool_call type function, got %v", tc["type"])
	}
	fn := tc["function"].(map[string]any)
	if fn["name"] != "search" {
		t.Fatalf("expected function name search, got %v", fn["name"])
	}

	usage := parsed["usage"].(map[string]any)
	if usage["prompt_tokens"] != float64(10) {
		t.Fatalf("expected prompt_tokens 10, got %v", usage["prompt_tokens"])
	}
	if usage["completion_tokens"] != float64(20) {
		t.Fatalf("expected completion_tokens 20, got %v", usage["completion_tokens"])
	}
}

func TestOpenAIToAnthropic_Response_ToolCalls(t *testing.T) {
	openAIBody := []byte(`{
		"id": "chatcmpl-abc",
		"model": "gpt-4",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "",
				"tool_calls": [{
					"id": "call_1",
					"type": "function",
					"function": {"name":"get_weather","arguments":"{\"city\":\"NYC\"}"}
				}]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {"prompt_tokens":10,"completion_tokens":5}
	}`)

	result, err := OpenAIToAnthropicResponse(openAIBody)
	if err != nil {
		t.Fatalf("OpenAIToAnthropicResponse failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	content := parsed["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(content))
	}
	block := content[0].(map[string]any)
	if block["type"] != "tool_use" {
		t.Fatalf("expected tool_use block, got %v", block["type"])
	}
	if block["id"] != "call_1" {
		t.Fatalf("expected id call_1, got %v", block["id"])
	}
	if block["name"] != "get_weather" {
		t.Fatalf("expected name get_weather, got %v", block["name"])
	}
	if parsed["stop_reason"] != "tool_use" {
		t.Fatalf("expected stop_reason tool_use, got %v", parsed["stop_reason"])
	}
}

func TestToOpenAI_Full(t *testing.T) {
	anthropicBody := []byte(`{
		"model": "claude-sonnet-4-6",
		"system": "Be helpful.",
		"messages": [{"role":"user","content":"What's the weather?"}],
		"max_tokens": 200,
		"stream": true,
		"temperature": 0.7,
		"top_p": 0.9,
		"stop_sequences": ["\n\n", "stop"],
		"tools": [{"name":"get_weather","description":"Get weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}],
		"tool_choice": {"type":"any"},
		"metadata": {"user_id": "user123"}
	}`)

	result, err := ToOpenAI(anthropicBody)
	if err != nil {
		t.Fatalf("ToOpenAI failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if parsed["model"] != "claude-sonnet-4-6" {
		t.Fatalf("expected model claude-sonnet-4-6, got %v", parsed["model"])
	}
	if parsed["stream"] != true {
		t.Fatalf("expected stream true, got %v", parsed["stream"])
	}
	if parsed["max_completion_tokens"] != float64(200) {
		t.Fatalf("expected max_completion_tokens 200, got %v", parsed["max_completion_tokens"])
	}
	if parsed["temperature"] != 0.7 {
		t.Fatalf("expected temperature 0.7, got %v", parsed["temperature"])
	}
	if parsed["top_p"] != 0.9 {
		t.Fatalf("expected top_p 0.9, got %v", parsed["top_p"])
	}

	stop := parsed["stop"].([]any)
	if len(stop) != 2 || stop[0] != "\n\n" || stop[1] != "stop" {
		t.Fatalf("expected stop [\\n\\n, stop], got %v", stop)
	}

	msgs := parsed["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	sysMsg := msgs[0].(map[string]any)
	if sysMsg["role"] != "system" || sysMsg["content"] != "Be helpful." {
		t.Fatalf("unexpected system message: %v", sysMsg)
	}

	tools := parsed["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "function" {
		t.Fatalf("expected function type, got %v", tool["type"])
	}
	fn := tool["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Fatalf("expected function name get_weather, got %v", fn["name"])
	}

	if parsed["tool_choice"] != "required" {
		t.Fatalf("expected tool_choice required, got %v", parsed["tool_choice"])
	}

	if parsed["user"] != "user123" {
		t.Fatalf("expected user user123, got %v", parsed["user"])
	}
}

func TestToAnthropic_Full(t *testing.T) {
	openAIBody := []byte(`{
		"model": "gpt-4",
		"messages": [
			{"role":"system","content":"Be helpful."},
			{"role":"user","content":"Hi"}
		],
		"max_completion_tokens": 200,
		"stream": true,
		"temperature": 0.7,
		"top_p": 0.9,
		"stop": ["\n\n"],
		"tools": [{
			"type": "function",
			"function": {
				"name": "get_weather",
				"description": "Get weather",
				"parameters": {"type":"object","properties":{"city":{"type":"string"}}}
			}
		}],
		"tool_choice": "auto",
		"response_format": {"type":"json_object"},
		"user": "user123"
	}`)

	result, err := ToAnthropic(openAIBody)
	if err != nil {
		t.Fatalf("ToAnthropic failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if parsed["model"] != "gpt-4" {
		t.Fatalf("expected model gpt-4, got %v", parsed["model"])
	}
	if parsed["stream"] != true {
		t.Fatalf("expected stream true, got %v", parsed["stream"])
	}
	if parsed["system"] != "Be helpful." {
		t.Fatalf("expected system 'Be helpful.', got %v", parsed["system"])
	}
	if parsed["max_tokens"] != float64(200) {
		t.Fatalf("expected max_tokens 200, got %v", parsed["max_tokens"])
	}
	if parsed["temperature"] != 0.7 {
		t.Fatalf("expected temperature 0.7, got %v", parsed["temperature"])
	}
	if parsed["top_p"] != 0.9 {
		t.Fatalf("expected top_p 0.9, got %v", parsed["top_p"])
	}

	stopSeqs := parsed["stop_sequences"].([]any)
	if len(stopSeqs) != 1 || stopSeqs[0] != "\n\n" {
		t.Fatalf("expected stop_sequences [\\n\\n], got %v", stopSeqs)
	}

	msgs := parsed["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	tools := parsed["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "get_weather" {
		t.Fatalf("expected tool name get_weather, got %v", tool["name"])
	}

	if parsed["tool_choice"] != "auto" {
		t.Fatalf("expected tool_choice auto, got %v", parsed["tool_choice"])
	}

	meta := parsed["metadata"].(map[string]any)
	if meta["user_id"] != "user123" {
		t.Fatalf("expected metadata.user_id user123, got %v", meta["user_id"])
	}
}

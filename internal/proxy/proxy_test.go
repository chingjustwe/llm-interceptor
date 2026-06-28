package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProxy_ForwardsRequestAndReturnsResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":      "msg_123",
			"model":   "claude-sonnet-4-6",
			"content": []map[string]string{{"type": "text", "text": "Hello"}},
		})
	}))
	defer upstream.Close()

	target, err := New("test-upstream", upstream.URL)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	pluginResp, err := target.HandleRequest([]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`), map[string]string{
		"x-api-key":    "test-key",
		"content-type": "application/json",
	}, "")
	if err != nil {
		t.Fatalf("HandleRequest failed: %v", err)
	}
	if pluginResp == nil {
		t.Fatal("expected non-nil response")
	}
	if pluginResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", pluginResp.StatusCode)
	}
}

func TestProxy_StreamingResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello there\"}}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"name\":\"Read\",\"input\":{\"path\":\"main.go\"}}}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"input_tokens\":10,\"output_tokens\":20}}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	target, err := New("test-stream", upstream.URL)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	rec := httptest.NewRecorder()
	respBody, usage, tools, stopReason, _, _, err := target.HandleRequestStream(
		[]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		map[string]string{"x-api-key": "test-key"},
		rec,
		"",
		nil,
	)
	if string(respBody) != "Hello there" {
		t.Fatalf("expected response body 'Hello there', got '%s'", string(respBody))
	}
	if err != nil {
		t.Fatalf("HandleRequestStream failed: %v", err)
	}
	if usage == nil {
		t.Fatal("expected non-nil usage")
	}
	if usage.InputTokens != 10 || usage.OutputTokens != 20 {
		t.Fatalf("expected usage 10/20, got %d/%d", usage.InputTokens, usage.OutputTokens)
	}
	if stopReason != "tool_use" {
		t.Fatalf("expected stop_reason tool_use, got %s", stopReason)
	}
	if len(tools) != 1 || tools[0].Name != "Read" {
		t.Fatalf("expected 1 tool_use (Read), got %v", tools)
	}
}

func TestStreamAndCollect_Passthrough(t *testing.T) {
	// Verify that the SSE stream is forwarded unchanged (no response-side
	// modification). Tool blocking now happens at the request level.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"name\":\"Read\",\"input\":{\"path\":\"main.go\"}}}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"input_tokens\":10,\"output_tokens\":20}}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	target, err := New("test-passthrough", upstream.URL)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	rec := httptest.NewRecorder()
	respBody, usage, tools, stopReason, _, _, err := target.HandleRequestStream(
		[]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		map[string]string{"x-api-key": "test-key"},
		rec,
		"",
		nil,
	)
	if err != nil {
		t.Fatalf("HandleRequestStream failed: %v", err)
	}
	if string(respBody) != "Hello" {
		t.Fatalf("expected respBody 'Hello', got '%s'", string(respBody))
	}
	if stopReason != "tool_use" {
		t.Fatalf("expected stop_reason tool_use, got %s", stopReason)
	}
	if len(tools) != 1 || tools[0].Name != "Read" {
		t.Fatalf("expected 1 tool_use (Read), got %v", tools)
	}
	if usage == nil {
		t.Fatal("expected non-nil usage")
	}
	if usage.InputTokens != 10 || usage.OutputTokens != 20 {
		t.Fatalf("expected usage 10/20, got %d/%d", usage.InputTokens, usage.OutputTokens)
	}
	// SSE formatting must be valid (event/data pairing, blank line separators).
	assertSSEValid(t, rec.Body.String())
	// Tool_use must still be present in output (response-side passthrough).
	if !strings.Contains(rec.Body.String(), `"type":"tool_use"`) {
		t.Error("passthrough SSE output should contain tool_use")
	}
}

// assertSSEValid checks raw SSE output for proper formatting per Anthropic's
// SSE convention. For each data: line, the very next line must be blank (event
// separator) or end-of-stream. This catches the bug where two events are
// emitted without a blank line between them, causing the client's SSE parser
// to merge them into a single event with concatenated (invalid) JSON.
func assertSSEValid(t *testing.T, raw string) {
	t.Helper()
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		if !strings.HasPrefix(trimmed, "data: ") {
			continue
		}
		// Each data: line must be valid JSON.
		data := strings.TrimPrefix(trimmed, "data: ")
		if !json.Valid([]byte(data)) {
			t.Errorf("line %d: invalid JSON: %s", i+1, data)
		}
		// Next line must be blank (SSE event separator) or end of stream.
		if i+1 >= len(lines) {
			continue
		}
		next := strings.TrimRight(lines[i+1], "\r")
		if next != "" {
			t.Errorf("line %d: data: immediately followed by non-blank line %q (missing blank line separator)", i+1, next)
		}
	}
}

func TestProxy_Forward_AnyPath(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"method": r.Method,
			"path":   r.URL.Path,
		})
	}))
	defer upstream.Close()

	target, err := New("test-catchall", upstream.URL)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	req := httptest.NewRequest("GET", "/v1/other-endpoint?q=test", nil)
	req.Header.Set("x-custom", "value")
	rec := httptest.NewRecorder()
	target.Forward(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if body["method"] != "GET" {
		t.Fatalf("expected method GET, got %s", body["method"])
	}
	if body["path"] != "/v1/other-endpoint" {
		t.Fatalf("expected path /v1/other-endpoint, got %s", body["path"])
	}
}

func TestProxy_Forward_PreservesBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"echo": string(body)})
	}))
	defer upstream.Close()

	target, err := New("test-body", upstream.URL)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	req := httptest.NewRequest("POST", "/v1/complete", io.NopCloser(bytes.NewReader([]byte(`{"hello":"world"}`))))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	target.Forward(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if body["echo"] != `{"hello":"world"}` {
		t.Fatalf("expected body to be echoed, got %s", body["echo"])
	}
}

func TestStreamAndCollect_OpenAIFormat(t *testing.T) {
	// Verify that OpenAI streaming format (without Anthropic event types) is
	// correctly parsed for stop_reason, text content, and usage.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		// OpenAI streaming chunks: no event: lines, just data: lines.
		data := `data: {"id":"1","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"id":"2","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"3","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}

data: {"id":"4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}

data: [DONE]

`
		fmt.Fprint(w, data)
		flusher.Flush()
	}))
	defer upstream.Close()

	target, err := New("test-openai-stream", upstream.URL)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	rec := httptest.NewRecorder()
	respBody, usage, tools, stopReason, _, _, err := target.HandleRequestStream(
		[]byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		map[string]string{"x-api-key": "test-key"},
		rec,
		"",
		nil,
	)
	if err != nil {
		t.Fatalf("HandleRequestStream failed: %v", err)
	}
	if string(respBody) != "Hello world" {
		t.Fatalf("expected respBody 'Hello world', got %q", string(respBody))
	}
	if stopReason != "stop" {
		t.Fatalf("expected stop_reason 'stop', got %q", stopReason)
	}
	if usage == nil {
		t.Fatal("expected non-nil usage")
	}
	if usage.InputTokens != 5 {
		t.Errorf("expected input_tokens=5, got %d", usage.InputTokens)
	}
	if usage.OutputTokens != 2 {
		t.Errorf("expected output_tokens=2, got %d", usage.OutputTokens)
	}
	if len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(tools))
	}
	// Verify SSE lines are forwarded unchanged.
	if !strings.Contains(rec.Body.String(), `"content":"Hello"`) {
		t.Error("forwarded SSE should contain content")
	}
}

func TestHandleRequestStream_ToolBlockedFollowUp(t *testing.T) {
	var requestCount int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requestCount++
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		if requestCount == 1 {
			// First request: return SSE with a blocked tool (Bash).
			fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			flusher.Flush()
			fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"I'll use Bash to list files\"}}\n\n")
			flusher.Flush()
			fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			flusher.Flush()
			fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_bash_001\",\"name\":\"Bash\",\"input\":{\"command\":\"ls\"}}}\n\n")
			flusher.Flush()
			fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}\n\n")
			flusher.Flush()
			fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			flusher.Flush()
			return
		}

		// Second request (follow-up): verify tool_result is present, then
		// return an LLM-adaptive text-only response.
		// The proxy's buildFollowUpRequest appends an assistant message
		// and a user message with tool_result. Verify the structure.
		if !strings.Contains(string(body), "tool_result") {
			t.Error("follow-up request should contain tool_result blocks")
		}
		if !strings.Contains(string(body), "blocked by interceptor") {
			t.Error("follow-up request should contain blocked message in tool_result")
		}

		fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"I cannot use Bash as it is blocked. Let me use Read instead.\"}}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_read_001\",\"name\":\"Read\",\"input\":{\"path\":\"main.go\"}}}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"input_tokens\":15,\"output_tokens\":8}}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	target, err := New("test-blocked-followup", upstream.URL)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	isToolBlocked := func(name string) bool {
		return name == "Bash"
	}

	rec := httptest.NewRecorder()
	respBody, usage, tools, stopReason, _, _, err := target.HandleRequestStream(
		[]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		map[string]string{"x-api-key": "test-key"},
		rec,
		"",
		isToolBlocked,
	)
	if err != nil {
		t.Fatalf("HandleRequestStream failed: %v", err)
	}
	// The client should receive the adaptive response (second LLM call),
	// not the original response with the blocked tool.
	expectedText := "I cannot use Bash as it is blocked. Let me use Read instead."
	if string(respBody) != expectedText {
		t.Fatalf("expected adaptive response %q, got %q", expectedText, string(respBody))
	}
	if stopReason != "tool_use" {
		t.Fatalf("expected stop_reason tool_use, got %s", stopReason)
	}
	if len(tools) != 1 || tools[0].Name != "Read" {
		t.Fatalf("expected 1 tool_use (Read), got %v", tools)
	}
	if usage == nil {
		t.Fatal("expected non-nil usage")
	}
	if usage.InputTokens != 15 || usage.OutputTokens != 8 {
		t.Fatalf("expected usage 15/8, got %d/%d", usage.InputTokens, usage.OutputTokens)
	}
	// The client should NOT receive the original blocked tool (Bash tool_use)
	// in the SSE output. The adaptive response mentions "Bash" in text but
	// should not have a Bash tool_use content block.
	if strings.Contains(rec.Body.String(), `"name":"Bash"`) {
		t.Error("SSE output should not contain a Bash tool_use content block")
	}
	if requestCount != 2 {
		t.Fatalf("expected 2 requests (original + follow-up), got %d", requestCount)
	}
	assertSSEValid(t, rec.Body.String())
}

func TestHandleRequestStream_FollowUpBudgetExhausted(t *testing.T) {
	var requestCount int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		// Always return a Bash tool_use — simulates an LLM that ignores
		// tool_result and keeps retrying the blocked tool.
		fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Let me try bash again\"}}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_bash_002\",\"name\":\"Bash\",\"input\":{\"command\":\"ls\"}}}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"input_tokens\":5,\"output_tokens\":5}}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	target, err := New("test-budget-exhausted", upstream.URL)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	isToolBlocked := func(name string) bool {
		return name == "Bash"
	}

	rec := httptest.NewRecorder()
	_, _, tools, _, _, _, err := target.HandleRequestStream(
		[]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		map[string]string{"x-api-key": "test-key"},
		rec,
		"",
		isToolBlocked,
	)
	if err != nil {
		t.Fatalf("HandleRequestStream failed: %v", err)
	}
	// Even with follow-up budget exhausted, the response body should still
	// be valid — it's the LLM's final response (which happens to contain
	// a blocked tool since the LLM wouldn't adapt).
	if len(tools) != 1 || tools[0].Name != "Bash" {
		t.Fatalf("expected final response to still contain Bash tool_use, got %v", tools)
	}
	// 4 requests: original + 3 follow-ups (budget=3, one per retry).
	if requestCount != 4 {
		t.Fatalf("expected 4 requests (original + 3 follow-ups), got %d", requestCount)
	}
}

func TestExtractRequestParams(t *testing.T) {
	body := []byte(`{"model":"claude","messages":[{"role":"user","content":"hi"}],"stream":true,"temperature":0.7,"top_p":0.9,"max_tokens":100}`)
	params := ExtractRequestParams(body)
	if params == nil {
		t.Fatal("expected non-nil params")
	}
	if _, ok := params["messages"]; ok {
		t.Error("messages should be removed")
	}
	if _, ok := params["stream"]; ok {
		t.Error("stream should be removed")
	}
	if _, ok := params["model"]; ok {
		t.Error("model should be removed")
	}
	if params["temperature"] != 0.7 {
		t.Errorf("expected temperature 0.7, got %v", params["temperature"])
	}
	if params["top_p"] != 0.9 {
		t.Errorf("expected top_p 0.9, got %v", params["top_p"])
	}
	if params["max_tokens"] != float64(100) {
		t.Errorf("expected max_tokens 100, got %v", params["max_tokens"])
	}
}

func TestExtractSystemPrompt(t *testing.T) {
	// Anthropic format
	body := []byte(`{"model":"claude","system":"You are helpful.","messages":[{"role":"user","content":"hi"}]}`)
	sp := ExtractSystemPrompt(body)
	if sp == nil || *sp != "You are helpful." {
		t.Fatalf("expected 'You are helpful.', got %v", sp)
	}

	// OpenAI format with system role
	body = []byte(`{"model":"gpt-4","messages":[{"role":"system","content":"You are GPT."},{"role":"user","content":"hi"}]}`)
	sp = ExtractSystemPrompt(body)
	if sp == nil || *sp != "You are GPT." {
		t.Fatalf("expected 'You are GPT.', got %v", sp)
	}

	// OpenAI format with developer role
	body = []byte(`{"model":"gpt-4","messages":[{"role":"developer","content":"You are dev."},{"role":"user","content":"hi"}]}`)
	sp = ExtractSystemPrompt(body)
	if sp == nil || *sp != "You are dev." {
		t.Fatalf("expected 'You are dev.', got %v", sp)
	}

	// No system prompt
	body = []byte(`{"model":"claude","messages":[{"role":"user","content":"hi"}]}`)
	sp = ExtractSystemPrompt(body)
	if sp != nil {
		t.Fatalf("expected nil, got %v", *sp)
	}
}

func TestExtractUsage_Anthropic(t *testing.T) {
	body := []byte(`{
		"id":"msg_123",
		"type":"message",
		"stop_reason":"end_turn",
		"content":[{"type":"text","text":"Hello"}],
		"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":5,"cache_creation_input_tokens":3}
	}`)
	usage, tools, stopReason := ExtractUsage(body)
	if stopReason != "end_turn" {
		t.Fatalf("expected stop_reason=end_turn, got %q", stopReason)
	}
	if usage.InputTokens != 10 {
		t.Errorf("expected input_tokens=10, got %d", usage.InputTokens)
	}
	if usage.OutputTokens != 20 {
		t.Errorf("expected output_tokens=20, got %d", usage.OutputTokens)
	}
	if usage.CacheReadTokens != 5 {
		t.Errorf("expected cache_read=5, got %d", usage.CacheReadTokens)
	}
	if usage.CacheCreationTokens != 3 {
		t.Errorf("expected cache_creation=3, got %d", usage.CacheCreationTokens)
	}
	if len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(tools))
	}
}

func TestExtractUsage_OpenAI(t *testing.T) {
	body := []byte(`{
		"id":"chatcmpl-123",
		"model":"gpt-4",
		"choices":[{
			"index":0,
			"finish_reason":"stop",
			"message":{"role":"assistant","content":"Hello"}
		}],
		"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}
	}`)
	usage, tools, stopReason := ExtractUsage(body)
	if stopReason != "stop" {
		t.Fatalf("expected stop_reason=stop, got %q", stopReason)
	}
	if usage.InputTokens != 10 {
		t.Errorf("expected input_tokens=10, got %d", usage.InputTokens)
	}
	if usage.OutputTokens != 20 {
		t.Errorf("expected output_tokens=20, got %d", usage.OutputTokens)
	}
	if len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(tools))
	}
}

func TestExtractUsage_OpenAI_WithToolCalls(t *testing.T) {
	body := []byte(`{
		"id":"chatcmpl-456",
		"choices":[{
			"index":0,
			"finish_reason":"tool_calls",
			"message":{"role":"assistant","content":null,"tool_calls":[
				{"id":"call_abc","type":"function","function":{"name":"get_weather","arguments":"{\"loc\":\"NYC\"}"}}
			]}
		}],
		"usage":{"prompt_tokens":5,"completion_tokens":10}
	}`)
	usage, tools, stopReason := ExtractUsage(body)
	if stopReason != "tool_calls" {
		t.Fatalf("expected stop_reason=tool_calls, got %q", stopReason)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].ID != "call_abc" {
		t.Errorf("expected id=call_abc, got %s", tools[0].ID)
	}
	if tools[0].Name != "get_weather" {
		t.Errorf("expected name=get_weather, got %s", tools[0].Name)
	}
	if tools[0].Input == nil || tools[0].Input["loc"] != "NYC" {
		t.Errorf("expected input.loc=NYC, got %v", tools[0].Input)
	}
	if usage.InputTokens != 5 {
		t.Errorf("expected input_tokens=5, got %d", usage.InputTokens)
	}
}

func TestExtractUsage_InvalidJSON(t *testing.T) {
	_, _, stopReason := ExtractUsage([]byte(`not json`))
	if stopReason != "" {
		t.Errorf("expected empty stop_reason for invalid JSON, got %q", stopReason)
	}
}

func TestExtractUsage_EmptyBody(t *testing.T) {
	_, _, stopReason := ExtractUsage([]byte(`{}`))
	if stopReason != "" {
		t.Errorf("expected empty stop_reason for empty body, got %q", stopReason)
	}
}

func TestExtractError(t *testing.T) {
	// OpenAI format
	body := []byte(`{"error":{"type":"invalid_request_error","message":"Bad request"}}`)
	eType, eMsg := ExtractError(body)
	if eType != "invalid_request_error" || eMsg != "Bad request" {
		t.Errorf("expected invalid_request_error/Bad request, got %s/%s", eType, eMsg)
	}

	// Anthropic format
	body = []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Too fast"}}`)
	eType, eMsg = ExtractError(body)
	if eType != "rate_limit_error" || eMsg != "Too fast" {
		t.Errorf("expected rate_limit_error/Too fast, got %s/%s", eType, eMsg)
	}

	// No error (success response)
	body = []byte(`{"id":"msg_123","type":"message","content":[]}`)
	eType, eMsg = ExtractError(body)
	if eType != "" || eMsg != "" {
		t.Errorf("expected empty strings, got %s/%s", eType, eMsg)
	}

	// Invalid JSON
	body = []byte(`not json`)
	eType, eMsg = ExtractError(body)
	if eType != "" || eMsg != "" {
		t.Errorf("expected empty strings for invalid json, got %s/%s", eType, eMsg)
	}
}

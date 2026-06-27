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
	})
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
	respBody, usage, tools, stopReason, _, err := target.HandleRequestStream(
		[]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		map[string]string{"x-api-key": "test-key"},
		rec,
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

func TestE2E_StreamingToolBlock_RawOutput(t *testing.T) {
	// End-to-end test: mock upstream Anthropic API → proxy → client.
	// Prints raw SSE output so the user can inspect validitiy.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		// Simulate Anthropic API: text block then tool_use.
		fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"I will list the files using Bash.\"}}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_abc123\",\"name\":\"Bash\",\"input\":{\"command\":\"ls -la\"}}}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"command\\\": \\\"ls -la\\\"}\"}}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\",\"stop_sequence\":null},\"usage\":{\"input_tokens\":25,\"output_tokens\":42}}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	target, err := New("e2e-test", upstream.URL)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	isToolBlocked := func(name string) bool { return name == "Bash" }

	rec := httptest.NewRecorder()
	respBody, usage, tools, stopReason, _, err := target.HandleRequestStream(
		[]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"list files"}],"stream":true}`),
		map[string]string{"x-api-key": "test-key"},
		rec,
		isToolBlocked,
	)
	if err != nil {
		t.Fatalf("HandleRequestStream failed: %v", err)
	}

	// Print the raw SSE output for visual inspection.
	rawSSE := rec.Body.String()
	t.Logf("=== RAW SSE OUTPUT ===\n%s", rawSSE)
	t.Logf("=== RETURNED VALUES ===")
	t.Logf("respBody:         %q", string(respBody))
	t.Logf("stopReason:       %q", stopReason)
	t.Logf("usage:            input=%d output=%d", usage.InputTokens, usage.OutputTokens)
	t.Logf("toolCalls:        %d (should still contain the blocked tool for logging)", len(tools))
	for i, tc := range tools {
		t.Logf("  tool[%d]: name=%q", i, tc.Name)
	}

	// Validate: SSE formatting (blank line separators) and valid JSON.
	assertSSEValid(t, rawSSE)
	// Validate: no tool_use in output, but blocked message present.
	if strings.Contains(rawSSE, `"type":"tool_use"`) {
		t.Error("FATAL: output still contains tool_use — client will get confused")
	}
	if !strings.Contains(rawSSE, "blocked by interceptor policy") {
		t.Error("FATAL: output missing blocked message")
	}
	if !strings.Contains(rawSSE, `"stop_reason":"end_turn"`) {
		t.Error("FATAL: output missing stop_reason end_turn")
	}

	// Also test the non-streaming path.
	t.Run("non-streaming", func(t *testing.T) {
		mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id":      "msg_456",
				"model":   "claude-sonnet-4-6",
				"stop_reason": "tool_use",
				"content": []map[string]any{
					{"type": "text", "text": "I will list files."},
					{"type": "tool_use", "id": "toolu_def456", "name": "Bash", "input": map[string]string{"command": "ls"}},
				},
				"usage": map[string]any{"input_tokens": 10, "output_tokens": 20},
			})
		}))
		defer mock.Close()

		target2, err := New("e2e-nonstream", mock.URL)
		if err != nil {
			t.Fatalf("New failed: %v", err)
		}
		pr, err := target2.HandleRequest([]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"ls"}]}`),
			map[string]string{"x-api-key": "test-key"},
		)
		if err != nil {
			t.Fatalf("HandleRequest failed: %v", err)
		}
		blocked := InterceptBlockedTools(pr.Body, func(name string) bool { return name == "Bash" })
		t.Logf("=== NON-STREAMING BLOCKED RESPONSE ===\n%s", string(blocked))
		if strings.Contains(string(blocked), `"type":"tool_use"`) {
			t.Error("non-streaming: output still contains tool_use")
		}
		if !strings.Contains(string(blocked), "blocked by interceptor policy") {
			t.Error("non-streaming: output missing blocked message")
		}
		if !strings.Contains(string(blocked), `"stop_reason":"end_turn"`) {
			t.Error("non-streaming: output missing stop_reason end_turn")
		}
	})
}

func TestStreamAndCollect_BlocksToolUse(t *testing.T) {
	tests := []struct {
		name        string
		isBlocked   func(name string) bool
		wantStop    string
		wantBody    string
		wantTools   int
		checkOutput func(t *testing.T, output string)
	}{
		{
			name:      "blocked_tool_replaced_with_text",
			isBlocked: func(name string) bool { return name == "Bash" },
			wantStop:  "end_turn",
			wantBody:  "HelloTool call blocked by interceptor policy — the tool you attempted to use is not available in this session.",
			wantTools: 1,
	checkOutput: func(t *testing.T, output string) {
			assertSSEValid(t, output)
			// Must NOT contain tool_use blocks.
			if strings.Contains(output, `"type":"tool_use"`) {
				t.Errorf("output should not contain tool_use, got:\n%s", output)
			}
			// Must contain the blocked message.
			if !strings.Contains(output, "blocked by interceptor policy") {
				t.Errorf("output should contain blocked message")
			}
			// Must have event: lines paired with data: lines.
			if !strings.Contains(output, "event: content_block_start") {
				t.Errorf("output should contain event: content_block_start")
			}
			if !strings.Contains(output, "event: content_block_stop") {
				t.Errorf("output should contain event: content_block_stop")
			}
			if !strings.Contains(output, `"stop_reason":"end_turn"`) {
				t.Errorf("output should contain stop_reason end_turn")
			}
		},
		},
		{
			name:      "non_blocked_tool_passthrough",
			isBlocked: func(name string) bool { return false },
			wantStop:  "tool_use",
			wantBody:  "Hello",
			wantTools: 1,
			checkOutput: func(t *testing.T, output string) {
				if !strings.Contains(output, `"type":"tool_use"`) {
					t.Errorf("non-blocked output should contain tool_use")
				}
				if !strings.Contains(output, `"stop_reason":"tool_use"`) {
					t.Errorf("non-blocked output should have stop_reason tool_use")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				flusher, _ := w.(http.Flusher)

				// Text block at index 0: "Hello"
				fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
				flusher.Flush()
				fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n")
				flusher.Flush()
				fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
				flusher.Flush()

				// Tool_use block at index 1: "Bash" (may be blocked)
				fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"name\":\"Bash\",\"input\":{\"command\":\"ls\"}}}\n\n")
				flusher.Flush()
				fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\n")
				flusher.Flush()
				fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n")
				flusher.Flush()

				// Message delta
				fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"input_tokens\":10,\"output_tokens\":20}}\n\n")
				flusher.Flush()
				fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
				flusher.Flush()
			}))
			defer upstream.Close()

			target, err := New("test-block", upstream.URL)
			if err != nil {
				t.Fatalf("New failed: %v", err)
			}

			rec := httptest.NewRecorder()
			respBody, usage, tools, stopReason, _, err := target.HandleRequestStream(
				[]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":true}`),
				map[string]string{"x-api-key": "test-key"},
				rec,
				tt.isBlocked,
			)
			if err != nil {
				t.Fatalf("HandleRequestStream failed: %v", err)
			}
			if string(respBody) != tt.wantBody {
				t.Fatalf("expected respBody %q, got %q", tt.wantBody, string(respBody))
			}
			if stopReason != tt.wantStop {
				t.Fatalf("expected stop_reason %q, got %q", tt.wantStop, stopReason)
			}
			if len(tools) != tt.wantTools {
				t.Fatalf("expected %d tools, got %d", tt.wantTools, len(tools))
			}
			if usage == nil {
				t.Fatal("expected non-nil usage")
			}
			if usage.InputTokens != 10 || usage.OutputTokens != 20 {
				t.Fatalf("expected usage 10/20, got %d/%d", usage.InputTokens, usage.OutputTokens)
			}
			tt.checkOutput(t, rec.Body.String())
		})
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

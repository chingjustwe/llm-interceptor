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
	respBody, usage, tools, stopReason, _, err := target.HandleRequestStream(
		[]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		map[string]string{"x-api-key": "test-key"},
		rec,
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

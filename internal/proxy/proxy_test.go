package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
		fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"name\":\"Read\",\"input\":{\"path\":\"main.go\"}}}\n\n")
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
	usage, tools, stopReason, _, err := target.HandleRequestStream(
		[]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		map[string]string{"x-api-key": "test-key"},
		rec,
	)
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

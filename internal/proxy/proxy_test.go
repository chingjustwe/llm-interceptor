package proxy

import (
	"encoding/json"
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

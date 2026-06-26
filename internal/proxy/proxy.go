package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type PluginResponse struct {
	StatusCode int
	Body       []byte
	Headers    map[string]string
	DurationMs int64
	Usage      UsageData
}

type ToolCall struct {
	Name  string
	Input map[string]any
}

type UsageData struct {
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
}

type Proxy struct {
	name     string
	upstream string
	client   *http.Client
}

func New(name, upstreamURL string) (*Proxy, error) {
	if _, err := url.Parse(upstreamURL); err != nil {
		return nil, fmt.Errorf("invalid upstream URL: %w", err)
	}
	return &Proxy{
		name:     name,
		upstream: upstreamURL,
		client:   &http.Client{Timeout: 120 * time.Second},
	}, nil
}

func (p *Proxy) HandleRequest(body []byte, headers map[string]string) (*PluginResponse, error) {
	start := time.Now()

	req, err := http.NewRequest("POST", p.upstream+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	pr := &PluginResponse{
		StatusCode: resp.StatusCode,
		Body:       respBody,
		Headers:    make(map[string]string, len(resp.Header)),
		DurationMs: time.Since(start).Milliseconds(),
	}
	for k, v := range resp.Header {
		pr.Headers[k] = v[0]
	}
	return pr, nil
}

func ExtractUsage(body []byte) (UsageData, []ToolCall, string) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return UsageData{}, nil, ""
	}
	var usage UsageData
	if u, ok := raw["usage"].(map[string]any); ok {
		if v, ok := u["input_tokens"].(float64); ok {
			usage.InputTokens = int(v)
		}
		if v, ok := u["output_tokens"].(float64); ok {
			usage.OutputTokens = int(v)
		}
		if v, ok := u["cache_read_input_tokens"].(float64); ok {
			usage.CacheReadTokens = int(v)
		}
		if v, ok := u["cache_creation_input_tokens"].(float64); ok {
			usage.CacheCreationTokens = int(v)
		}
	}
	var stopReason string
	if sr, ok := raw["stop_reason"].(string); ok {
		stopReason = sr
	}
	var toolCalls []ToolCall
	if content, ok := raw["content"].([]any); ok {
		for _, c := range content {
			if block, ok := c.(map[string]any); ok && block["type"] == "tool_use" {
				var tc ToolCall
				if name, ok := block["name"].(string); ok {
					tc.Name = name
				}
				if input, ok := block["input"].(map[string]any); ok {
					tc.Input = input
				}
				toolCalls = append(toolCalls, tc)
			}
		}
	}
	return usage, toolCalls, stopReason
}

func (p *Proxy) HandleRequestStream(body []byte, headers map[string]string, w http.ResponseWriter) (*UsageData, []ToolCall, string, int64, error) {
	start := time.Now()

	req, err := http.NewRequest("POST", p.upstream+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, nil, "", 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "text/event-stream")
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, nil, "", 0, err
	}
	defer resp.Body.Close()

	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, nil, "", 0, fmt.Errorf("read error body: %w", err)
		}
		w.Write(body)
		return nil, nil, "", time.Since(start).Milliseconds(), nil
	}

	usage, tools, stopReason, duration, err := streamAndCollect(resp, w)
	if err != nil {
		return nil, nil, "", duration, err
	}
	return &usage, tools, stopReason, duration, nil
}

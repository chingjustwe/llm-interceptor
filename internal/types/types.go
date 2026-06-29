// Package types defines shared data structures used across the LLM Interceptor
// codebase, including request/response models, token usage, and storage types.
package types

import "encoding/json"

// TokenUsage tracks token consumption for an LLM request, including cache
// metrics separately from regular input/output tokens.
type TokenUsage struct {
	InputTokens         int `json:"input_tokens" yaml:"input_tokens"`
	OutputTokens        int `json:"output_tokens" yaml:"output_tokens"`
	CacheReadTokens     int `json:"cache_read_tokens" yaml:"cache_read_tokens"`
	CacheCreationTokens int `json:"cache_creation_tokens" yaml:"cache_creation_tokens"`
}

// ToolCall represents a tool invocation parsed from an LLM response content block.
type ToolCall struct {
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

// RequestBody represents the JSON structure of an incoming LLM request body.
// It is used to extract common fields such as model, streaming mode, and tools.
type RequestBody struct {
	Model     string  `json:"model"`
	Messages  []any   `json:"messages"`
	System    *string `json:"system,omitempty"`
	Tools     []any   `json:"tools,omitempty"`
	MaxTokens *int    `json:"max_tokens,omitempty"`
	Stream    bool    `json:"stream,omitempty"`
}

// StoredRequest is the database model for a persisted LLM request. It contains
// the full request/response bodies, metadata, and parsed token usage data.
type StoredRequest struct {
	ID         string     `json:"id"`
	SessionID  string     `json:"session_id"`
	Model      string     `json:"model"`
	Method     string     `json:"method"`
	Path       string     `json:"path"`
	Request    string     `json:"request"`
	Response   string     `json:"response"`
	Usage      TokenUsage `json:"usage"`
	DurationMs int64      `json:"duration_ms"`
	StatusCode int        `json:"status_code"`
	CreatedAt  int64      `json:"created_at"`

	SystemPrompt    *string  `json:"system_prompt,omitempty"`
	StopReason      *string  `json:"stop_reason,omitempty"`
	ErrorType       *string  `json:"error_type,omitempty"`
	ErrorMessage    *string  `json:"error_message,omitempty"`
	TTFTMs          *int64   `json:"ttft_ms,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"top_p,omitempty"`
	RequestParams   *string  `json:"request_params,omitempty"`
}

// ConfigEntry represents a single runtime configuration entry stored in the
// database. Values are JSON-encoded for flexibility. The UpdatedBy field records
// which admin user made the change, sourced from JWT claims.
type ConfigEntry struct {
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
	UpdatedAt int64           `json:"updated_at"`
	UpdatedBy string          `json:"updated_by"`
}

// RequestFilter defines the available filter parameters for querying stored
// requests. Nil fields are omitted from the query, providing flexible filtering
// by session, model, time range, and pagination.
type RequestFilter struct {
	SessionID *string
	Model     *string
	From      *int64
	To        *int64
	Limit     int
	Offset    int

	StopReason  *string
	ErrorType   *string
	MinDuration *int64
	MaxDuration *int64
	StatusCodes []int
}

package types

type TokenUsage struct {
	InputTokens         int `json:"input_tokens" yaml:"input_tokens"`
	OutputTokens        int `json:"output_tokens" yaml:"output_tokens"`
	CacheReadTokens     int `json:"cache_read_tokens" yaml:"cache_read_tokens"`
	CacheCreationTokens int `json:"cache_creation_tokens" yaml:"cache_creation_tokens"`
}

type ToolCall struct {
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

type RequestBody struct {
	Model     string   `json:"model"`
	Messages  []any    `json:"messages"`
	System    *string  `json:"system,omitempty"`
	Tools     []any    `json:"tools,omitempty"`
	MaxTokens *int     `json:"max_tokens,omitempty"`
	Stream    bool     `json:"stream,omitempty"`
}

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
}

type RequestFilter struct {
	SessionID *string
	Model     *string
	From      *int64
	To        *int64
	Limit     int
	Offset    int
}

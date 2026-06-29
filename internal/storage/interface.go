// Package storage defines the storage abstraction for persisting LLM request
// and response data, as well as API key records for the router mode.
// Implementations include SQLite (embedded, dev-friendly) and PostgreSQL
// (production).
package storage

import (
	"context"

	"github.com/chingjustwe/llm-interceptor/internal/types"
)

// APIKey represents a managed API key stored in the gateway. Keys are hashed
// with bcrypt before storage; only the hash and a short prefix are persisted.
type APIKey struct {
	ID        string `json:"id"`
	KeyHash   string `json:"key_hash"`
	KeyPrefix string `json:"key_prefix"` // first 12 chars for identification
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`
}

// Backend is the persistence interface for storing and querying LLM request
// records and managed API keys. Each request is stored with its metadata,
// usage data, and the original request/response bodies for audit and
// debugging.
type Backend interface {
	// Request storage
	SaveRequest(ctx context.Context, req *types.StoredRequest) error
	GetSessionRequests(ctx context.Context, sessionID string, limit, offset int) ([]types.StoredRequest, error)
	QueryRequests(ctx context.Context, filter types.RequestFilter) ([]types.StoredRequest, error)

	// API key management
	SaveAPIKey(ctx context.Context, key *APIKey) error
	GetAPIKeyByPrefix(ctx context.Context, prefix string) (*APIKey, error)
	ListAPIKeys(ctx context.Context) ([]APIKey, error)
	DisableAPIKey(ctx context.Context, id string) error

	// Runtime configuration storage
	SaveConfig(ctx context.Context, entry *types.ConfigEntry) error
	GetConfig(ctx context.Context, key string) (*types.ConfigEntry, error)
	ListConfig(ctx context.Context) ([]types.ConfigEntry, error)
	DeleteConfig(ctx context.Context, key string) error

	Close() error
}

// Package router implements the LLM routing layer that enables multi-provider
// support. It detects whether a request should be handled in "router" mode
// (managed API keys, provider selection) or "passthrough" mode (original
// Phase 1 behavior, upstream proxy with client-supplied credentials).
package router

import (
	"net/http"
)

// keyPrefix is the distinctive prefix that identifies a managed API key.
// When a request carries this prefix, the gateway enters router mode.
const keyPrefix = "sk-lli-"

// keyPrefixLen is the byte length of keyPrefix, used for fast prefix checks.
const keyPrefixLen = len(keyPrefix)

// Provider is the interface that every upstream LLM provider must implement.
// The router uses providers to forward requests to the correct backend API.
type Provider interface {
	// Name returns a human-readable identifier for this provider (e.g. "openai").
	Name() string

	// RoundTrip sends the HTTP request to the provider's API endpoint and
	// returns the raw HTTP response. Implementations are responsible for
	// rewriting the URL and injecting provider-specific authentication.
	RoundTrip(req *http.Request) (*http.Response, error)
}

// Router orchestrates mode detection and provider selection for incoming LLM
// requests. It holds the configured providers and a fallback URL for
// passthrough mode.
type Router struct {
	providers  []Provider
	defaultURL string // fallback upstream URL when router mode is not active
}

// New creates a Router with the given providers and a default upstream URL.
// The defaultURL is used when the request is in passthrough mode.
func New(providers []Provider, defaultURL string) *Router {
	return &Router{providers: providers, defaultURL: defaultURL}
}

// DetectMode inspects the API key and returns "router" if it belongs to a
// managed key (starts with the sk-lli- prefix), or "passthrough" otherwise.
// Passthrough mode preserves the original Phase 1 proxy behavior.
func (r *Router) DetectMode(apiKey string) string {
	if len(apiKey) > keyPrefixLen && apiKey[:keyPrefixLen] == keyPrefix {
		return "router"
	}
	return "passthrough"
}

// ModelMatcher is an optional interface that providers can implement to
// declare which model name patterns they handle. The router uses this to
// select the correct provider for a given model.
type ModelMatcher interface {
	MatchModel(model string) bool
}

// SelectProvider iterates over configured providers and returns the first one
// whose model pattern matches the given model name. Returns nil if no provider
// matches, which signals the caller to fall back to the default upstream.
func (r *Router) SelectProvider(model string) Provider {
	for _, p := range r.providers {
		if mp, ok := p.(ModelMatcher); ok && mp.MatchModel(model) {
			return p
		}
	}
	return nil
}

// DefaultURL returns the fallback upstream URL for passthrough mode.
func (r *Router) DefaultURL() string {
	return r.defaultURL
}

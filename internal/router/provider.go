// Package router implements the LLM routing layer that enables multi-provider
// support. This file contains the HTTPProvider, a concrete Provider that
// forwards requests to an upstream LLM API over HTTP.
package router

import (
	"net/http"
	"strings"
	"time"
)

// httpProviderTimeout is the maximum time allowed for a single provider
// round-trip, including connection establishment and response reading.
const httpProviderTimeout = 120 * time.Second

// HTTPProvider is a Provider implementation that forwards requests to an
// upstream LLM API endpoint. It rewrites the request URL to the provider's
// base URL and injects the provider's API key for authentication.
type HTTPProvider struct {
	name      string
	baseURL   string
	modelGlob string
	apiKey    string
	client    *http.Client
}

// NewHTTPProvider creates an HTTPProvider with the given configuration.
// The baseURL has its trailing slash stripped for consistent URL construction.
func NewHTTPProvider(name, baseURL, modelGlob, apiKey string) *HTTPProvider {
	return &HTTPProvider{
		name:      name,
		baseURL:   strings.TrimRight(baseURL, "/"),
		modelGlob: modelGlob,
		apiKey:    apiKey,
		client:    &http.Client{Timeout: httpProviderTimeout},
	}
}

// Name returns the human-readable identifier for this provider.
func (p *HTTPProvider) Name() string { return p.name }

// MatchModel checks whether the given model name matches this provider's glob
// pattern. An empty pattern or "*" matches all models. Otherwise, the pattern
// is treated as a prefix match with an optional trailing wildcard.
func (p *HTTPProvider) MatchModel(model string) bool {
	if p.modelGlob == "" || p.modelGlob == "*" {
		return true
	}
	// Strip trailing '*' for prefix matching: "gpt-*" → prefix "gpt-".
	prefix := strings.TrimSuffix(p.modelGlob, "*")
	return strings.HasPrefix(model, prefix)
}

// RoundTrip rewrites the request to target this provider's base URL and
// injects the provider's API key before sending the request. The caller's
// original request URL path is preserved; only the scheme and host are replaced.
func (p *HTTPProvider) RoundTrip(req *http.Request) (*http.Response, error) {
	// Rewrite URL to the provider's endpoint.
	req.URL.Scheme = "https"
	// Strip the scheme from baseURL to get host+path for URL.Host.
	host := p.baseURL
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	req.URL.Host = host

	// Inject provider's API key. The header name depends on the provider;
	// for now we set both common headers to support Anthropic and OpenAI.
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	return p.client.Do(req)
}

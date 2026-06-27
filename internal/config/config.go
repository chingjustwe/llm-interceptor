// Package config handles loading, validating, and providing default values for
// the LLM Interceptor YAML configuration file.
package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration structure for the LLM Interceptor.
// It specifies the listen address, upstream provider, storage, state store,
// and plugin settings.
type Config struct {
	Listen       string           `yaml:"listen"`
	Upstream     string           `yaml:"upstream"`
	MetricPrefix string           `yaml:"metric_prefix"`
	Log          LogConfig        `yaml:"log"`
	Storage      StorageConfig    `yaml:"storage"`
	StateStore   StateStoreConfig `yaml:"state_store"`
	Plugins      PluginConfig     `yaml:"plugins"`
}

// PluginConfig holds configuration for all built-in plugins.
type PluginConfig struct {
	OTelExporter OTelExporterConfig      `yaml:"otel-exporter"`
	CostTracker  CostTrackerPluginConfig `yaml:"cost-tracker"`
	Budget       BudgetPluginConfig      `yaml:"budget"`
	RateLimit    RateLimitPluginConfig   `yaml:"rate-limit"`
	ToolPolicy   ToolPolicyPluginConfig  `yaml:"tool-policy"`
}

// PriceConfig defines per-million-token pricing for a single model.
type PriceConfig struct {
	InputPerM  float64 `yaml:"input_per_m"`
	OutputPerM float64 `yaml:"output_per_m"`
}

// CostTrackerPluginConfig enables the cost-tracking plugin and optionally
// overrides per-model pricing. The key is the model name as reported in
// the LLM response (e.g. "deepseek-v4-flash").
type CostTrackerPluginConfig struct {
	Enabled bool                  `yaml:"enabled"`
	Prices  map[string]PriceConfig `yaml:"prices,omitempty"`
}

// BudgetPluginConfig sets per-session and per-day cost limits in USD.
// Zero values disable the corresponding limit.
type BudgetPluginConfig struct {
	MaxCostPerSession float64 `yaml:"max_cost_per_session"`
	MaxCostPerDay     float64 `yaml:"max_cost_per_day"`
}

// RateLimitPluginConfig sets request and token rate limits per minute.
// Zero values disable the corresponding limit.
type RateLimitPluginConfig struct {
	RequestsPerMinute int `yaml:"requests_per_minute"`
	TokensPerMinute   int `yaml:"tokens_per_minute"`
}

// ToolPolicyPluginConfig defines tool access rules for the tool-policy plugin.
type ToolPolicyPluginConfig struct {
	BlockedTools []string `yaml:"blocked_tools"`
	AllowedTools []string `yaml:"allowed_tools"`
}

// OTelExporterConfig configures the OpenTelemetry OTLP exporter plugin.
type OTelExporterConfig struct {
	Enabled    bool              `yaml:"enabled"`
	Endpoint   string            `yaml:"endpoint"`
	Headers    map[string]string `yaml:"headers,omitempty"`
	MaxAttrLen int               `yaml:"max_attr_len,omitempty"`
}

// LogConfig controls whether request and response bodies are logged.
type LogConfig struct {
	RequestBody  bool `yaml:"request_body"`
	ResponseBody bool `yaml:"response_body"`
}

// StorageConfig selects the storage backend (sqlite or postgres) and
// provides backend-specific connection parameters.
type StorageConfig struct {
	Type     string          `yaml:"type"`
	SQLite   *SQLiteConfig   `yaml:"sqlite,omitempty"`
	Postgres *PostgresConfig `yaml:"postgres,omitempty"`
}

// SQLiteConfig specifies the filesystem path for the SQLite database file.
type SQLiteConfig struct {
	Path string `yaml:"path"`
}

// PostgresConfig specifies the PostgreSQL connection string.
type PostgresConfig struct {
	ConnectionString string `yaml:"connection_string"`
}

// StateStoreConfig selects the state store backend (memory or redis) and
// provides backend-specific connection parameters.
type StateStoreConfig struct {
	Type   string        `yaml:"type"`
	Memory *MemoryConfig `yaml:"memory,omitempty"`
	Redis  *RedisConfig  `yaml:"redis,omitempty"`
}

// MemoryConfig is a placeholder for in-memory state store configuration.
type MemoryConfig struct{}

// RedisConfig specifies the Redis server URL for the state store.
type RedisConfig struct {
	URL string `yaml:"url"`
}

// Default returns a configuration with sensible defaults: listen on 127.0.0.1:8080,
// proxy to Anthropic, SQLite storage at ~/.llm-interceptor/data.db, and in-memory
// state store.
func Default() *Config {
	return &Config{
		Listen:       "127.0.0.1:8080",
		Upstream:     "https://api.anthropic.com",
		MetricPrefix: "llm_proxy.",
		Storage: StorageConfig{
			Type:   "sqlite",
			SQLite: &SQLiteConfig{Path: "~/.llm-interceptor/data.db"},
		},
		StateStore: StateStoreConfig{
			Type:   "memory",
			Memory: &MemoryConfig{},
		},
	}
}

// Load reads a YAML config file from the given path, merges it with defaults,
// and validates the result. Returns an error if the file cannot be read or
// validation fails.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := Default()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks required configuration fields. It sets a default metric prefix
// if omitted, and returns an error if listen address or upstream URL is empty.
func (c *Config) Validate() error {
	if c.Listen == "" {
		return fmt.Errorf("config: listen address is required")
	}
	if c.Upstream == "" {
		return fmt.Errorf("config: upstream URL is required")
	}
	if c.MetricPrefix == "" {
		c.MetricPrefix = "llm_proxy."
	}
	return nil
}

// expandHome replaces a leading "~/" prefix with the user's home directory path.
// Returns the original path if home directory lookup fails.
func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return home + path[1:]
	}
	return path
}

// StoragePath returns the expanded filesystem path for the SQLite database.
// Returns empty string if SQLite is not configured.
func (c *Config) StoragePath() string {
	if c.Storage.SQLite == nil {
		return ""
	}
	return expandHome(c.Storage.SQLite.Path)
}

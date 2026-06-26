package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen       string           `yaml:"listen"`
	Upstream     string           `yaml:"upstream"`
	MetricPrefix string           `yaml:"metric_prefix"`
	Log          LogConfig        `yaml:"log"`
	Storage      StorageConfig    `yaml:"storage"`
	StateStore   StateStoreConfig `yaml:"state_store"`
	Plugins      PluginConfig     `yaml:"plugins"`
}

type PluginConfig struct {
	OTelExporter OTelExporterConfig `yaml:"otel-exporter"`
}

type OTelExporterConfig struct {
	Enabled  bool              `yaml:"enabled"`
	Endpoint string            `yaml:"endpoint"`
	Headers  map[string]string `yaml:"headers,omitempty"`
}

type LogConfig struct {
	RequestBody  bool `yaml:"request_body"`
	ResponseBody bool `yaml:"response_body"`
}

type StorageConfig struct {
	Type     string          `yaml:"type"`
	SQLite   *SQLiteConfig   `yaml:"sqlite,omitempty"`
	Postgres *PostgresConfig `yaml:"postgres,omitempty"`
}

type SQLiteConfig struct {
	Path string `yaml:"path"`
}

type PostgresConfig struct {
	ConnectionString string `yaml:"connection_string"`
}

type StateStoreConfig struct {
	Type   string         `yaml:"type"`
	Memory *MemoryConfig  `yaml:"memory,omitempty"`
	Redis  *RedisConfig   `yaml:"redis,omitempty"`
}

type MemoryConfig struct{}

type RedisConfig struct {
	URL string `yaml:"url"`
}

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

func (c *Config) StoragePath() string {
	if c.Storage.SQLite == nil {
		return ""
	}
	return expandHome(c.Storage.SQLite.Path)
}

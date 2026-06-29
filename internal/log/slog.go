// Package log provides structured logging setup for the LLM Interceptor.
// It wraps Go 1.21+ log/slog with JSON and text handlers, and configures
// the global default logger from application config.
package log

import (
	"github.com/chingjustwe/llm-interceptor/internal/config"
	"log/slog"
	"os"
)

// Setup configures the global slog default logger based on the application
// LogConfig. Returns the configured logger for use in the main function.
func Setup(cfg config.LogConfig) *slog.Logger {
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	var handler slog.Handler
	opts := &slog.HandlerOptions{Level: level}

	switch cfg.Format {
	case "json":
		handler = slog.NewJSONHandler(os.Stdout, opts)
	default:
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	logger := slog.New(handler)
	slog.SetDefault(logger)
	return logger
}

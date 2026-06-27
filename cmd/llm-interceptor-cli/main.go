// Package main provides a CLI tool for managing LLM Interceptor API keys.
// It supports generating new keys, listing existing keys, and disabling keys.
// The CLI connects directly to the gateway's storage backend (SQLite or
// PostgreSQL) without requiring the gateway to be running.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/chingjustwe/llm-interceptor/internal/config"
	"github.com/chingjustwe/llm-interceptor/internal/router"
	"github.com/chingjustwe/llm-interceptor/internal/storage"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(0)
	}

	cfgPath := "config.yaml"
	// Allow overriding config path with --config flag.
	for i, arg := range os.Args {
		if arg == "--config" && i+1 < len(os.Args) {
			cfgPath = os.Args[i+1]
			// Remove the flag and its value from os.Args for simpler parsing below.
			os.Args = append(os.Args[:i], os.Args[i+2:]...)
			break
		}
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	store, err := initStorage(cfg)
	if err != nil {
		log.Fatalf("failed to init storage: %v", err)
	}
	defer store.Close()

	km := router.NewKeyManager(store)
	ctx := context.Background()

	switch os.Args[1] {
	case "generate-key":
		if len(os.Args) < 3 {
			log.Fatal("Usage: llm-interceptor-cli generate-key <name>")
		}
		key, err := km.Generate(ctx, os.Args[2])
		if err != nil {
			log.Fatalf("generate key: %v", err)
		}
		fmt.Printf("API Key: %s\n", key)
		fmt.Println("Save this key now — it cannot be retrieved later.")

	case "list-keys":
		keys, err := store.ListAPIKeys(ctx)
		if err != nil {
			log.Fatalf("list keys: %v", err)
		}
		if len(keys) == 0 {
			fmt.Println("No API keys found.")
			return
		}
		fmt.Printf("%-24s  %-14s  %-20s  %s\n", "ID", "PREFIX", "NAME", "STATUS")
		fmt.Println("------------------------------------------------------------------------------------")
		for _, k := range keys {
			status := "enabled"
			if !k.Enabled {
				status = "disabled"
			}
			fmt.Printf("%-24s  %-14s  %-20s  %s\n", k.ID, k.KeyPrefix, k.Name, status)
		}

	case "disable-key":
		if len(os.Args) < 3 {
			log.Fatal("Usage: llm-interceptor-cli disable-key <id>")
		}
		if err := store.DisableAPIKey(ctx, os.Args[2]); err != nil {
			log.Fatalf("disable key: %v", err)
		}
		fmt.Printf("Key %s disabled.\n", os.Args[2])

	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

// printUsage prints the CLI help message to stderr.
func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage: llm-interceptor-cli [--config path] <command> [args]\n\n")
	fmt.Fprintf(os.Stderr, "Commands:\n")
	fmt.Fprintf(os.Stderr, "  generate-key <name>   Generate a new API key\n")
	fmt.Fprintf(os.Stderr, "  list-keys             List all API keys\n")
	fmt.Fprintf(os.Stderr, "  disable-key <id>      Disable an API key by its ID\n")
}

// initStorage creates a storage backend based on the configuration.
func initStorage(cfg *config.Config) (storage.Backend, error) {
	switch cfg.Storage.Type {
	case "sqlite":
		return storage.NewSQLite(cfg.StoragePath())
	case "postgres":
		if cfg.Storage.Postgres == nil {
			return nil, fmt.Errorf("postgres storage requires a 'postgres' config block")
		}
		return storage.NewPostgres(cfg.Storage.Postgres.ConnectionString)
	default:
		return nil, fmt.Errorf("unsupported storage type: %s", cfg.Storage.Type)
	}
}

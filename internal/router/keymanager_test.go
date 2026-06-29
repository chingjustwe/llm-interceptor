package router

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/chingjustwe/llm-interceptor/internal/storage"
	"github.com/chingjustwe/llm-interceptor/internal/types"
)

// mockStorage is a minimal in-memory implementation of storage.Backend that
// supports only the API key methods needed for KeyManager testing.
type mockStorage struct {
	keys map[string]*storage.APIKey // keyed by prefix
	byID map[string]*storage.APIKey // keyed by ID
}

func newMockStorage() *mockStorage {
	return &mockStorage{
		keys: make(map[string]*storage.APIKey),
		byID: make(map[string]*storage.APIKey),
	}
}

func (m *mockStorage) SaveAPIKey(_ context.Context, key *storage.APIKey) error {
	// Assign an ID if not set (mirrors what real storage would do).
	if key.ID == "" {
		key.ID = fmt.Sprintf("key_%d", time.Now().UnixNano())
	}
	m.keys[key.KeyPrefix] = key
	m.byID[key.ID] = key
	return nil
}

func (m *mockStorage) GetAPIKeyByPrefix(_ context.Context, prefix string) (*storage.APIKey, error) {
	k, ok := m.keys[prefix]
	if !ok {
		return nil, nil
	}
	return k, nil
}

func (m *mockStorage) ListAPIKeys(_ context.Context) ([]storage.APIKey, error) {
	var result []storage.APIKey
	for _, k := range m.byID {
		result = append(result, *k)
	}
	return result, nil
}

func (m *mockStorage) DisableAPIKey(_ context.Context, id string) error {
	if k, ok := m.byID[id]; ok {
		k.Enabled = false
		return nil
	}
	return fmt.Errorf("key not found: %s", id)
}

// Stub methods for the request storage part of Backend — not used in these tests.

func (m *mockStorage) SaveRequest(_ context.Context, _ *types.StoredRequest) error { return nil }
func (m *mockStorage) GetSessionRequests(_ context.Context, _ string, _, _ int) ([]types.StoredRequest, error) {
	return nil, nil
}
func (m *mockStorage) QueryRequests(_ context.Context, _ types.RequestFilter) ([]types.StoredRequest, error) {
	return nil, nil
}
func (m *mockStorage) SaveConfig(_ context.Context, _ *types.ConfigEntry) error { return nil }
func (m *mockStorage) GetConfig(_ context.Context, _ string) (*types.ConfigEntry, error) { return nil, nil }
func (m *mockStorage) ListConfig(_ context.Context) ([]types.ConfigEntry, error) { return nil, nil }
func (m *mockStorage) DeleteConfig(_ context.Context, _ string) error { return nil }
func (m *mockStorage) Close() error { return nil }

func TestKeyManager_GenerateAndValidate(t *testing.T) {
	store := newMockStorage()
	km := NewKeyManager(store)
	ctx := context.Background()

	key, err := km.Generate(ctx, "test-key")
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if len(key) < 20 {
		t.Fatalf("expected long key, got %s", key)
	}
	if key[:7] != "sk-lli-" {
		t.Fatalf("expected sk-lli- prefix, got %s", key[:7])
	}

	valid, err := km.Validate(ctx, key)
	if err != nil {
		t.Fatalf("validate failed: %v", err)
	}
	if !valid {
		t.Fatal("expected valid key")
	}
}

func TestKeyManager_InvalidKey(t *testing.T) {
	store := newMockStorage()
	km := NewKeyManager(store)
	ctx := context.Background()

	// Generate a real key to populate the store.
	key, err := km.Generate(ctx, "test-key")
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	// Construct a wrong key with the same prefix but different body.
	wrongKey := key[:12] + "0000000000000000000000000000000000000000000000000000"
	valid, err := km.Validate(ctx, wrongKey)
	if err != nil {
		t.Fatalf("validate error: %v", err)
	}
	if valid {
		t.Fatal("expected invalid key")
	}
}

func TestKeyManager_DisabledKey(t *testing.T) {
	store := newMockStorage()
	km := NewKeyManager(store)
	ctx := context.Background()

	key, err := km.Generate(ctx, "to-disable")
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	// Find the stored key ID and disable it.
	prefix := key[:12]
	stored, _ := store.GetAPIKeyByPrefix(ctx, prefix)
	if stored == nil {
		t.Fatal("expected stored key")
	}
	if err := store.DisableAPIKey(ctx, stored.ID); err != nil {
		t.Fatalf("disable failed: %v", err)
	}

	valid, err := km.Validate(ctx, key)
	if err != nil {
		t.Fatalf("validate error: %v", err)
	}
	if valid {
		t.Fatal("expected disabled key to be invalid")
	}
}

func TestKeyManager_ShortKey(t *testing.T) {
	store := newMockStorage()
	km := NewKeyManager(store)
	ctx := context.Background()

	// A key shorter than the prefix length should not panic.
	valid, err := km.Validate(ctx, "short")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if valid {
		t.Fatal("expected short key to be invalid")
	}
}

func TestKeyManager_UnknownPrefix(t *testing.T) {
	store := newMockStorage()
	km := NewKeyManager(store)
	ctx := context.Background()

	valid, err := km.Validate(ctx, "sk-lli-unknown1234567890abcdef")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if valid {
		t.Fatal("expected unknown prefix key to be invalid")
	}
}

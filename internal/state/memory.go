package state

import (
	"context"
	"sync"
	"time"
)

type memoryEntry struct {
	value  int64
	expiry int64
}

type MemoryBackend struct {
	mu       sync.RWMutex
	counters map[string]*memoryEntry
	stopCh   chan struct{}
}

func NewMemory() *MemoryBackend {
	m := &MemoryBackend{
		counters: make(map[string]*memoryEntry),
		stopCh:   make(chan struct{}),
	}
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.cleanExpired()
			case <-m.stopCh:
				return
			}
		}
	}()
	return m
}

func (m *MemoryBackend) cleanExpired() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UnixMilli()
	for k, entry := range m.counters {
		if entry.expiry > 0 && now > entry.expiry {
			delete(m.counters, k)
		}
	}
}

func (m *MemoryBackend) Increment(ctx context.Context, key string, delta int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.counters[key]
	if !ok || (entry.expiry > 0 && time.Now().UnixMilli() > entry.expiry) {
		entry = &memoryEntry{value: 0}
		m.counters[key] = entry
	}
	entry.value += delta
	return entry.value, nil
}

func (m *MemoryBackend) Get(ctx context.Context, key string) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.counters[key]
	if !ok {
		return 0, nil
	}
	if entry.expiry > 0 && time.Now().UnixMilli() > entry.expiry {
		return 0, nil
	}
	return entry.value, nil
}

func (m *MemoryBackend) Reset(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.counters, key)
	return nil
}

func (m *MemoryBackend) IncrementWithTTL(ctx context.Context, key string, delta int64, ttlMs int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.counters[key]
	if !ok || (entry.expiry > 0 && time.Now().UnixMilli() > entry.expiry) {
		entry = &memoryEntry{value: 0, expiry: time.Now().UnixMilli() + ttlMs}
		m.counters[key] = entry
	}
	entry.value += delta
	return entry.value, nil
}

func (m *MemoryBackend) GetMany(ctx context.Context, keys []string) (map[string]int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]int64, len(keys))
	now := time.Now().UnixMilli()
	for _, key := range keys {
		if entry, ok := m.counters[key]; ok {
			if entry.expiry == 0 || now <= entry.expiry {
				result[key] = entry.value
			}
		}
	}
	return result, nil
}

func (m *MemoryBackend) Close() error {
	close(m.stopCh)
	return nil
}

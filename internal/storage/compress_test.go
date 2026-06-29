package storage

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

func TestCompressBody_SmallPayload(t *testing.T) {
	cfg := DefaultCompressionConfig()
	data := []byte("small")
	result, err := CompressBody(data, cfg)
	if err != nil {
		t.Fatalf("CompressBody failed: %v", err)
	}
	// Small payloads (< min_size 1024) should be returned as-is.
	if !bytes.Equal(result, data) {
		t.Errorf("expected small payload unchanged, got different bytes")
	}
	if IsCompressed(result) {
		t.Errorf("small payload should not be compressed")
	}
}

func TestCompressBody_LargePayload(t *testing.T) {
	cfg := CompressionConfig{
		Enabled:   true,
		Algorithm: "gzip",
		MinSize:   1, // compress everything
	}
	data := []byte(strings.Repeat("hello world ", 1000))
	result, err := CompressBody(data, cfg)
	if err != nil {
		t.Fatalf("CompressBody failed: %v", err)
	}
	if !IsCompressed(result) {
		t.Errorf("large payload should be compressed")
	}
	if len(result) >= len(data) {
		t.Errorf("compressed data should be smaller than original (got %d >= %d)", len(result), len(data))
	}
}

func TestRoundTrip(t *testing.T) {
	cfg := CompressionConfig{
		Enabled:   true,
		Algorithm: "gzip",
		MinSize:   1,
	}
	original := []byte(`{"model":"claude-sonnet-4","messages":[{"role":"user","content":"Hello, world!"}]}`)

	compressed, err := CompressBody(original, cfg)
	if err != nil {
		t.Fatalf("CompressBody failed: %v", err)
	}

	decompressed, err := DecompressBody(compressed)
	if err != nil {
		t.Fatalf("DecompressBody failed: %v", err)
	}

	if !bytes.Equal(decompressed, original) {
		t.Errorf("round-trip failed: got %s, want %s", decompressed, original)
	}
}

func TestDecompressBody_RawData(t *testing.T) {
	// Raw (not compressed) data should be returned as-is.
	raw := []byte(`{"key": "value"}`)
	result, err := DecompressBody(raw)
	if err != nil {
		t.Fatalf("DecompressBody failed: %v", err)
	}
	if !bytes.Equal(result, raw) {
		t.Errorf("expected raw data unchanged")
	}
}

func TestCompressBody_Disabled(t *testing.T) {
	cfg := CompressionConfig{
		Enabled:   false,
		Algorithm: "gzip",
		MinSize:   1,
	}
	data := []byte(strings.Repeat("large payload ", 1000))
	result, err := CompressBody(data, cfg)
	if err != nil {
		t.Fatalf("CompressBody failed: %v", err)
	}
	if !bytes.Equal(result, data) {
		t.Errorf("expected unchanged when compression disabled")
	}
}

func TestCompressBody_MinSize(t *testing.T) {
	cfg := CompressionConfig{
		Enabled:   true,
		Algorithm: "gzip",
		MinSize:   5000,
	}
	data := []byte(strings.Repeat("medium ", 200)) // 1200 bytes
	result, err := CompressBody(data, cfg)
	if err != nil {
		t.Fatalf("CompressBody failed: %v", err)
	}
	// 1200 < 5000, so should not compress
	if IsCompressed(result) {
		t.Errorf("payload below min_size should not compress")
	}
}

func TestConcurrentCompress(t *testing.T) {
	cfg := CompressionConfig{
		Enabled:   true,
		Algorithm: "gzip",
		MinSize:   1,
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			data := []byte(strings.Repeat("concurrent test ", n+1))
			compressed, err := CompressBody(data, cfg)
			if err != nil {
				t.Errorf("CompressBody failed: %v", err)
				return
			}
			decompressed, err := DecompressBody(compressed)
			if err != nil {
				t.Errorf("DecompressBody failed: %v", err)
				return
			}
			if !bytes.Equal(decompressed, data) {
				t.Errorf("round-trip mismatch for iteration %d", n)
			}
		}(i)
	}
	wg.Wait()
}

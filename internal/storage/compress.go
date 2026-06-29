package storage

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
)

// CompressionConfig controls body compression behavior for stored requests.
type CompressionConfig struct {
	Enabled   bool   `yaml:"enabled"`
	Algorithm string `yaml:"algorithm"`  // "gzip" (default)
	MinSize   int    `yaml:"min_size"`   // minimum body length to compress, default 1024
}

// DefaultCompressionConfig returns sensible defaults for body compression.
func DefaultCompressionConfig() CompressionConfig {
	return CompressionConfig{
		Enabled:   true,
		Algorithm: "gzip",
		MinSize:   1024,
	}
}

// CompressBody compresses data with gzip if it meets the minimum size threshold.
// Returns the original data unchanged for small payloads below CompressMinSize.
func CompressBody(data []byte, cfg CompressionConfig) ([]byte, error) {
	if !cfg.Enabled || len(data) < cfg.MinSize {
		return data, nil
	}

	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, gzip.DefaultCompression)
	if err != nil {
		return nil, fmt.Errorf("gzip writer: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return nil, fmt.Errorf("gzip write: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("gzip close: %w", err)
	}
	return buf.Bytes(), nil
}

// IsCompressed checks if data was compressed with gzip by looking for the
// gzip magic number (0x1f, 0x8b) at the start of the payload.
func IsCompressed(data []byte) bool {
	return len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b
}

// DecompressBody decompresses gzip-compressed data back to the original.
// If the data does not start with the gzip magic number, it is returned as-is.
func DecompressBody(data []byte) ([]byte, error) {
	if !IsCompressed(data) {
		return data, nil
	}

	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("gzip reader: %w", err)
	}
	defer r.Close()

	out, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("gzip read: %w", err)
	}
	return out, nil
}

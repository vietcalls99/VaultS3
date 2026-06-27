package server

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/Kodiqa-Solutions/VaultS3/internal/config"
	"github.com/Kodiqa-Solutions/VaultS3/internal/metadata"
	"github.com/Kodiqa-Solutions/VaultS3/internal/storage"
	"github.com/Kodiqa-Solutions/VaultS3/internal/vector"
)

// vectorIndexableType reports whether a content type is text we can embed.
func vectorIndexableType(ct string) bool {
	ct = strings.ToLower(ct)
	if strings.HasPrefix(ct, "text/") {
		return true
	}
	return strings.Contains(ct, "json") ||
		strings.Contains(ct, "xml") ||
		strings.Contains(ct, "csv") ||
		strings.Contains(ct, "yaml") ||
		strings.Contains(ct, "markdown") ||
		strings.Contains(ct, "javascript")
}

// shouldVectorIndex decides whether an object qualifies for auto vector indexing.
func shouldVectorIndex(cfg config.VectorConfig, key string, meta *metadata.ObjectMeta) bool {
	if meta.DeleteMarker {
		return false
	}
	maxBytes := cfg.MaxObjectBytes
	if maxBytes <= 0 {
		maxBytes = 1 << 20 // 1 MB default
	}
	if meta.Size > maxBytes {
		return false
	}
	if !vectorIndexableType(meta.ContentType) {
		return false
	}
	if len(cfg.IndexPrefixes) > 0 {
		for _, p := range cfg.IndexPrefixes {
			if strings.HasPrefix(key, p) {
				return true
			}
		}
		return false
	}
	return true
}

// indexObjectVector reads an object's text and indexes it. It runs asynchronously
// off the upload path; all failures are logged, never propagated (best-effort
// indexing must never break or slow a PUT).
func indexObjectVector(mgr *vector.Manager, engine storage.Engine, bucket, key string, cfg config.VectorConfig) {
	maxBytes := cfg.MaxObjectBytes
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	rc, _, err := engine.GetObject(bucket, key)
	if err != nil {
		slog.Warn("vector: read object for indexing failed", "bucket", bucket, "key", key, "error", err)
		return
	}
	data, err := io.ReadAll(io.LimitReader(rc, maxBytes))
	rc.Close()
	if err != nil {
		slog.Warn("vector: read object body failed", "bucket", bucket, "key", key, "error", err)
		return
	}

	// Prepend the key so filename terms also contribute to the embedding.
	text := key + "\n" + string(data)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := mgr.IndexText(ctx, bucket, key, text); err != nil {
		slog.Warn("vector: index object failed", "bucket", bucket, "key", key, "error", err)
	}
}

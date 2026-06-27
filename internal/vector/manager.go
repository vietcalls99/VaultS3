package vector

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// Manager ties an Embedder to an Index and owns persistence. It is the entry
// point the rest of VaultS3 uses: index object text, query by natural language,
// and remove on delete.
type Manager struct {
	embedder Embedder
	index    *Index
	path     string // persistence file (empty = in-memory only)
	saveMu   sync.Mutex
}

// NewManager creates a manager. If persistPath is non-empty and exists, the
// index is loaded from it.
func NewManager(embedder Embedder, index *Index, persistPath string) *Manager {
	m := &Manager{embedder: embedder, index: index, path: persistPath}
	if persistPath != "" {
		if err := m.load(); err != nil && !os.IsNotExist(err) {
			slog.Warn("vector: failed to load persisted index", "path", persistPath, "error", err)
		}
	}
	return m
}

// IndexText embeds text and upserts it under bucket/key.
func (m *Manager) IndexText(ctx context.Context, bucket, key, text string) error {
	if text == "" {
		return nil
	}
	embs, err := m.embedder.Embed(ctx, []string{text})
	if err != nil {
		return err
	}
	if len(embs) != 1 {
		return fmt.Errorf("vector: expected 1 embedding, got %d", len(embs))
	}
	return m.index.Upsert(bucket, key, embs[0])
}

// Query embeds the natural-language query and returns the topK most similar
// objects. bucket "" searches all buckets.
func (m *Manager) Query(ctx context.Context, query string, topK int, bucket string) ([]Match, error) {
	if query == "" {
		return nil, fmt.Errorf("vector: empty query")
	}
	embs, err := m.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	if len(embs) != 1 {
		return nil, fmt.Errorf("vector: expected 1 embedding, got %d", len(embs))
	}
	return m.index.Search(embs[0], topK, bucket)
}

// Remove drops an object's vector.
func (m *Manager) Remove(bucket, key string) {
	m.index.Remove(bucket, key)
}

// Count returns the number of indexed vectors.
func (m *Manager) Count() int { return m.index.Count() }

// Save persists the index atomically (temp file + rename). No-op without a path.
func (m *Manager) Save() error {
	if m.path == "" {
		return nil
	}
	m.saveMu.Lock()
	defer m.saveMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(m.path), 0755); err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := m.index.Save(f); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, m.path)
}

func (m *Manager) load() error {
	f, err := os.Open(m.path)
	if err != nil {
		return err
	}
	defer f.Close()
	return m.index.Load(f)
}

package vector

import (
	"context"
	"hash/fnv"
	"path/filepath"
	"testing"
)

// fakeEmbedder produces deterministic vectors from text so manager tests need no
// network. Identical text → identical vector; it spreads tokens across 8 dims so
// semantically-overlapping strings land near each other.
type fakeEmbedder struct{}

func (fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, 8)
		for _, word := range splitWords(t) {
			h := fnv.New32a()
			h.Write([]byte(word))
			v[h.Sum32()%8] += 1
		}
		out[i] = v
	}
	return out, nil
}

func splitWords(s string) []string {
	var words []string
	cur := ""
	for _, r := range s {
		if r == ' ' {
			if cur != "" {
				words = append(words, cur)
				cur = ""
			}
		} else {
			cur += string(r)
		}
	}
	if cur != "" {
		words = append(words, cur)
	}
	return words
}

func TestManagerIndexAndQuery(t *testing.T) {
	m := NewManager(fakeEmbedder{}, NewIndex(0, 0), "")
	ctx := context.Background()

	docs := map[string]string{
		"cats.txt":    "the cat sat on the mat",
		"dogs.txt":    "the dog ran in the park",
		"finance.txt": "quarterly revenue and profit report",
	}
	for k, v := range docs {
		if err := m.IndexText(ctx, "b", k, v); err != nil {
			t.Fatalf("IndexText %s: %v", k, err)
		}
	}
	if m.Count() != 3 {
		t.Fatalf("count = %d, want 3", m.Count())
	}

	// Query overlapping the cat document most.
	got, err := m.Query(ctx, "the cat sat on the mat", 1, "")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 || got[0].Key != "cats.txt" {
		t.Fatalf("query returned %+v, want cats.txt first", got)
	}
}

func TestManagerRemove(t *testing.T) {
	m := NewManager(fakeEmbedder{}, NewIndex(0, 0), "")
	ctx := context.Background()
	m.IndexText(ctx, "b", "k", "hello world")
	m.Remove("b", "k")
	if m.Count() != 0 {
		t.Fatalf("count = %d after remove, want 0", m.Count())
	}
}

func TestManagerPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vectors.gob")
	ctx := context.Background()

	m1 := NewManager(fakeEmbedder{}, NewIndex(0, 0), path)
	m1.IndexText(ctx, "b", "doc", "persisted document text")
	if err := m1.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// A fresh manager pointed at the same path loads the vectors on startup.
	m2 := NewManager(fakeEmbedder{}, NewIndex(0, 0), path)
	if m2.Count() != 1 {
		t.Fatalf("reloaded count = %d, want 1", m2.Count())
	}
	got, err := m2.Query(ctx, "persisted document text", 1, "")
	if err != nil {
		t.Fatalf("Query after reload: %v", err)
	}
	if len(got) != 1 || got[0].Key != "doc" {
		t.Fatalf("reloaded query returned %+v", got)
	}
}

func TestManagerEmptyText(t *testing.T) {
	m := NewManager(fakeEmbedder{}, NewIndex(0, 0), "")
	if err := m.IndexText(context.Background(), "b", "k", ""); err != nil {
		t.Fatalf("empty text should be a no-op, got %v", err)
	}
	if m.Count() != 0 {
		t.Fatal("empty text should not index anything")
	}
}

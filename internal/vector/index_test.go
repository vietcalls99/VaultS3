package vector

import (
	"bytes"
	"math"
	"testing"
)

func TestIndexSearchRanksByCosine(t *testing.T) {
	ix := NewIndex(0, 0)
	// All in the same 2D plane; query points along +x.
	mustUpsert(t, ix, "b", "near", []float32{1, 0.1})
	mustUpsert(t, ix, "b", "mid", []float32{1, 1})
	mustUpsert(t, ix, "b", "far", []float32{0, 1})
	mustUpsert(t, ix, "b", "opposite", []float32{-1, 0})

	got, err := ix.Search([]float32{1, 0}, 3, "")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d results, want 3", len(got))
	}
	if got[0].Key != "near" {
		t.Fatalf("nearest = %q, want near", got[0].Key)
	}
	// Scores must be descending.
	for i := 1; i < len(got); i++ {
		if got[i].Score > got[i-1].Score {
			t.Fatalf("results not sorted by score: %+v", got)
		}
	}
	// Cosine of [1,0]·[1,0.1] normalized ≈ 0.995.
	if math.Abs(float64(got[0].Score)-0.995) > 0.01 {
		t.Fatalf("near score = %f, want ~0.995", got[0].Score)
	}
}

func TestIndexUpsertReplaces(t *testing.T) {
	ix := NewIndex(0, 0)
	mustUpsert(t, ix, "b", "k", []float32{1, 0})
	mustUpsert(t, ix, "b", "k", []float32{0, 1}) // replace
	if ix.Count() != 1 {
		t.Fatalf("count = %d, want 1 after replace", ix.Count())
	}
	got, _ := ix.Search([]float32{0, 1}, 1, "")
	if got[0].Score < 0.99 {
		t.Fatalf("replaced vector not used: score %f", got[0].Score)
	}
}

func TestIndexDimensionMismatch(t *testing.T) {
	ix := NewIndex(0, 0)
	mustUpsert(t, ix, "b", "k", []float32{1, 0, 0})
	if err := ix.Upsert("b", "k2", []float32{1, 0}); err == nil {
		t.Fatal("expected dimension-mismatch error")
	}
	if _, err := ix.Search([]float32{1, 0}, 1, ""); err == nil {
		t.Fatal("expected query dimension-mismatch error")
	}
}

func TestIndexRemove(t *testing.T) {
	ix := NewIndex(0, 0)
	mustUpsert(t, ix, "b", "k", []float32{1, 0})
	ix.Remove("b", "k")
	if ix.Count() != 0 {
		t.Fatalf("count = %d after remove, want 0", ix.Count())
	}
}

func TestIndexBucketFilter(t *testing.T) {
	ix := NewIndex(0, 0)
	mustUpsert(t, ix, "b1", "k", []float32{1, 0})
	mustUpsert(t, ix, "b2", "k", []float32{1, 0})
	got, _ := ix.Search([]float32{1, 0}, 10, "b2")
	if len(got) != 1 || got[0].Bucket != "b2" {
		t.Fatalf("bucket filter failed: %+v", got)
	}
}

func TestIndexEvictionHonorsCap(t *testing.T) {
	ix := NewIndex(2, 2) // cap of 2
	mustUpsert(t, ix, "b", "a", []float32{1, 0})
	mustUpsert(t, ix, "b", "b", []float32{0, 1})
	mustUpsert(t, ix, "b", "c", []float32{1, 1}) // evicts oldest ("a")

	if ix.Count() != 2 {
		t.Fatalf("count = %d, want 2 (capped)", ix.Count())
	}
	if ix.Evicted() != 1 {
		t.Fatalf("evicted = %d, want 1", ix.Evicted())
	}
	// "a" should be gone; searching its exact vector should not return it.
	got, _ := ix.Search([]float32{1, 0}, 10, "")
	for _, m := range got {
		if m.Key == "a" {
			t.Fatal("oldest vector should have been evicted")
		}
	}
}

func TestIndexPersistenceRoundTrip(t *testing.T) {
	ix := NewIndex(0, 0)
	mustUpsert(t, ix, "b", "one", []float32{1, 0, 0})
	mustUpsert(t, ix, "b", "two", []float32{0, 1, 0})

	var buf bytes.Buffer
	if err := ix.Save(&buf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	restored := NewIndex(0, 0)
	if err := restored.Load(&buf); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if restored.Count() != 2 {
		t.Fatalf("restored count = %d, want 2", restored.Count())
	}
	if restored.Dim() != 3 {
		t.Fatalf("restored dim = %d, want 3", restored.Dim())
	}
	got, _ := restored.Search([]float32{1, 0, 0}, 1, "")
	if got[0].Key != "one" {
		t.Fatalf("restored search wrong: %+v", got)
	}
}

func TestIndexEmptyInputs(t *testing.T) {
	ix := NewIndex(0, 0)
	if err := ix.Upsert("b", "k", nil); err == nil {
		t.Fatal("expected error on empty embedding")
	}
	if _, err := ix.Search(nil, 1, ""); err == nil {
		t.Fatal("expected error on empty query")
	}
}

func mustUpsert(t *testing.T, ix *Index, bucket, key string, v []float32) {
	t.Helper()
	if err := ix.Upsert(bucket, key, v); err != nil {
		t.Fatalf("Upsert %s: %v", key, err)
	}
}

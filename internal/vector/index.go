// Package vector provides an optional, dependency-free vector store for VaultS3:
// embeddings are indexed per object and queried by cosine similarity, enabling
// semantic search and RAG retrieval directly from the storage layer.
//
// The index is brute-force kNN on purpose. It needs no native libraries or
// external service, keeps VaultS3 a single binary, and is fast enough for the
// self-host / SMB scale this targets (tens to low-hundreds of thousands of
// vectors). Larger deployments can cap the index or point auto-indexing at a
// prefix.
package vector

import (
	"encoding/gob"
	"fmt"
	"io"
	"math"
	"sort"
	"sync"
)

// Match is a single nearest-neighbor result.
type Match struct {
	Bucket string  `json:"bucket"`
	Key    string  `json:"key"`
	Score  float32 `json:"score"` // cosine similarity in [-1, 1]
}

// vec is a stored, L2-normalized embedding for an object.
type vec struct {
	bucket string
	key    string
	embed  []float32 // normalized so cosine similarity == dot product
	seq    uint64    // insertion order, for FIFO eviction
}

// Index is a thread-safe in-memory cosine-similarity vector index.
type Index struct {
	mu       sync.RWMutex
	dim      int // 0 until the first vector pins it
	items    map[string]*vec
	seq      uint64
	maxItems int
	evicted  uint64 // count of vectors dropped to stay under the cap
}

const defaultMaxItems = 100000

// NewIndex creates an index. dim may be 0 (pinned on first Upsert); maxItems<=0
// uses the default cap.
func NewIndex(dim, maxItems int) *Index {
	if maxItems <= 0 {
		maxItems = defaultMaxItems
	}
	return &Index{
		dim:      dim,
		items:    make(map[string]*vec),
		maxItems: maxItems,
	}
}

func mapKey(bucket, key string) string { return bucket + "/" + key }

// Dim returns the index dimensionality (0 if no vectors yet).
func (ix *Index) Dim() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.dim
}

// Count returns the number of indexed vectors.
func (ix *Index) Count() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.items)
}

// Evicted returns how many vectors have been dropped to honor the cap.
func (ix *Index) Evicted() uint64 {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.evicted
}

// Upsert adds or replaces the embedding for an object. The embedding is
// L2-normalized into the index (the caller's slice is not modified). Returns an
// error on empty input or dimension mismatch.
func (ix *Index) Upsert(bucket, key string, embedding []float32) error {
	if len(embedding) == 0 {
		return fmt.Errorf("vector: empty embedding")
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()

	if ix.dim == 0 {
		ix.dim = len(embedding)
	}
	if len(embedding) != ix.dim {
		return fmt.Errorf("vector: dimension mismatch: got %d, want %d", len(embedding), ix.dim)
	}

	norm := normalize(embedding)
	mk := mapKey(bucket, key)
	if existing, ok := ix.items[mk]; ok {
		existing.embed = norm // replace in place, keep seq
		return nil
	}

	if len(ix.items) >= ix.maxItems {
		ix.evictOldestLocked()
	}
	ix.seq++
	ix.items[mk] = &vec{bucket: bucket, key: key, embed: norm, seq: ix.seq}
	return nil
}

// evictOldestLocked removes the lowest-seq (oldest) vector. Caller holds the lock.
func (ix *Index) evictOldestLocked() {
	var oldestKey string
	var oldestSeq uint64 = math.MaxUint64
	for mk, v := range ix.items {
		if v.seq < oldestSeq {
			oldestSeq = v.seq
			oldestKey = mk
		}
	}
	if oldestKey != "" {
		delete(ix.items, oldestKey)
		ix.evicted++
	}
}

// Remove deletes an object's vector if present.
func (ix *Index) Remove(bucket, key string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	delete(ix.items, mapKey(bucket, key))
}

// Search returns the topK most similar vectors to query, highest score first.
// An optional bucket filter ("" = all buckets) restricts the search.
func (ix *Index) Search(query []float32, topK int, bucket string) ([]Match, error) {
	if len(query) == 0 {
		return nil, fmt.Errorf("vector: empty query")
	}
	if topK <= 0 {
		topK = 10
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	if ix.dim != 0 && len(query) != ix.dim {
		return nil, fmt.Errorf("vector: query dimension mismatch: got %d, want %d", len(query), ix.dim)
	}

	q := normalize(query)
	matches := make([]Match, 0, len(ix.items))
	for _, v := range ix.items {
		if bucket != "" && v.bucket != bucket {
			continue
		}
		matches = append(matches, Match{Bucket: v.bucket, Key: v.key, Score: dot(q, v.embed)})
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		// Stable tie-break for deterministic output.
		if matches[i].Bucket != matches[j].Bucket {
			return matches[i].Bucket < matches[j].Bucket
		}
		return matches[i].Key < matches[j].Key
	})
	if len(matches) > topK {
		matches = matches[:topK]
	}
	return matches, nil
}

// --- persistence (gob; embeddings are expensive to recompute, so we persist) ---

type vecDTO struct {
	Bucket string
	Key    string
	Embed  []float32
	Seq    uint64
}

type snapshot struct {
	Dim   int
	Seq   uint64
	Items []vecDTO
}

// Save writes the index to w as a gob snapshot.
func (ix *Index) Save(w io.Writer) error {
	ix.mu.RLock()
	snap := snapshot{Dim: ix.dim, Seq: ix.seq, Items: make([]vecDTO, 0, len(ix.items))}
	for _, v := range ix.items {
		snap.Items = append(snap.Items, vecDTO{Bucket: v.bucket, Key: v.key, Embed: v.embed, Seq: v.seq})
	}
	ix.mu.RUnlock()
	return gob.NewEncoder(w).Encode(snap)
}

// Load replaces the index contents from a gob snapshot produced by Save.
func (ix *Index) Load(r io.Reader) error {
	var snap snapshot
	if err := gob.NewDecoder(r).Decode(&snap); err != nil {
		return err
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.dim = snap.Dim
	ix.seq = snap.Seq
	ix.items = make(map[string]*vec, len(snap.Items))
	for _, d := range snap.Items {
		ix.items[mapKey(d.Bucket, d.Key)] = &vec{bucket: d.Bucket, key: d.Key, embed: d.Embed, seq: d.Seq}
	}
	return nil
}

// --- math helpers (float32) ---

// normalize returns an L2-normalized copy of v. A zero vector is returned as-is.
func normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	out := make([]float32, len(v))
	if sum == 0 {
		copy(out, v)
		return out
	}
	inv := float32(1.0 / math.Sqrt(sum))
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}

// dot computes the dot product of two equal-length vectors.
func dot(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

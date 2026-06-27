//go:build !windows

package fuse

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gofuse "github.com/hanwen/go-fuse/v2/fuse"
)

// newS3Stub starts an in-memory S3-ish endpoint serving PUT / GET (with Range) /
// HEAD / DELETE for bucket "b". Returns the base URL and the backing store.
func newS3Stub(t *testing.T) (string, *sync.Map) {
	t.Helper()
	store := &sync.Map{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/b/")
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			store.Store(key, body)
			w.WriteHeader(http.StatusOK)
		case http.MethodHead:
			v, ok := store.Load(key)
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(v.([]byte))))
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			v, ok := store.Load(key)
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			data := v.([]byte)
			if rng := r.Header.Get("Range"); rng != "" {
				var start, end int64
				fmt.Sscanf(rng, "bytes=%d-%d", &start, &end)
				if start < 0 {
					start = 0
				}
				if end >= int64(len(data)) {
					end = int64(len(data)) - 1
				}
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
				w.WriteHeader(http.StatusPartialContent)
				w.Write(data[start : end+1])
				return
			}
			w.Write(data)
		case http.MethodDelete:
			store.Delete(key)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, store
}

func testCfg(endpoint string) MountConfig {
	return MountConfig{
		Endpoint:        endpoint,
		Bucket:          "b",
		Region:          "us-east-1",
		AccessKey:       "AKIATEST",
		SecretKey:       "secretkey",
		MetadataTTLSecs: 5,
	}
}

// TestFUSEWriteReadRoundTrip exercises the real FUSE I/O code paths: a write
// handle PUTs an object, and a read handle GETs it back byte-identically —
// without needing a kernel mount.
func TestFUSEWriteReadRoundTrip(t *testing.T) {
	endpoint, store := newS3Stub(t)
	cfg := testCfg(endpoint)
	client := &http.Client{Timeout: 10 * time.Second}
	ctx := context.Background()
	data := []byte("hello from the FUSE write path — round trip me please")

	// Write (Create → Write → Flush) maps to an S3 PUT.
	wh := &VaultWriteHandle{
		cfg:       cfg,
		client:    client,
		key:       "dir/hello.txt",
		metaCache: NewMetaCache(5*time.Second, time.Second),
	}
	if n, errno := wh.Write(ctx, data, 0); errno != 0 || int(n) != len(data) {
		t.Fatalf("Write: n=%d errno=%v", n, errno)
	}
	if errno := wh.Flush(ctx); errno != 0 {
		t.Fatalf("Flush: errno=%v", errno)
	}

	// The server received the exact bytes.
	got, ok := store.Load("dir/hello.txt")
	if !ok || !bytes.Equal(got.([]byte), data) {
		t.Fatal("server did not receive the written bytes")
	}

	// Read it back through the file handle (S3 GET Range).
	h := &VaultFileHandle{cfg: cfg, client: client, key: "dir/hello.txt", size: int64(len(data))}
	rr, errno := h.Read(ctx, make([]byte, len(data)), 0)
	if errno != 0 {
		t.Fatalf("Read: errno=%v", errno)
	}
	readBack, st := rr.Bytes(make([]byte, len(data)))
	if st != gofuse.OK {
		t.Fatalf("ReadResult.Bytes status=%v", st)
	}
	if !bytes.Equal(readBack, data) {
		t.Fatalf("round-trip mismatch:\n got %q\nwant %q", readBack, data)
	}
}

// TestFUSEReadRange verifies offset reads and reads past EOF.
func TestFUSEReadRange(t *testing.T) {
	endpoint, store := newS3Stub(t)
	store.Store("file.bin", []byte("0123456789"))
	cfg := testCfg(endpoint)
	h := &VaultFileHandle{cfg: cfg, client: &http.Client{Timeout: 5 * time.Second}, key: "file.bin", size: 10}

	rr, errno := h.Read(context.Background(), make([]byte, 4), 3)
	if errno != 0 {
		t.Fatalf("Read errno=%v", errno)
	}
	b, _ := rr.Bytes(make([]byte, 4))
	if string(b) != "3456" {
		t.Fatalf("offset read = %q, want 3456", b)
	}

	// Reading at/after EOF yields no bytes.
	rr2, errno := h.Read(context.Background(), make([]byte, 4), 100)
	if errno != 0 {
		t.Fatalf("Read past EOF errno=%v", errno)
	}
	if b2, _ := rr2.Bytes(make([]byte, 4)); len(b2) != 0 {
		t.Fatalf("read past EOF returned %q, want empty", b2)
	}
}

// TestFUSEUnlink confirms deleting a file issues an S3 DELETE.
func TestFUSEUnlink(t *testing.T) {
	endpoint, store := newS3Stub(t)
	store.Store("gone.txt", []byte("x"))
	v := &VaultFS{
		cfg:       testCfg(endpoint),
		client:    &http.Client{Timeout: 5 * time.Second},
		metaCache: NewMetaCache(time.Second, time.Second),
	}
	if errno := v.Unlink(context.Background(), "gone.txt"); errno != 0 {
		t.Fatalf("Unlink errno=%v", errno)
	}
	if _, ok := store.Load("gone.txt"); ok {
		t.Fatal("object was not deleted from the backend")
	}
}

// TestFUSEReadWithBlockCache verifies a cached read serves correct bytes and
// survives the backend going away (proving the second read came from cache).
func TestFUSEReadWithBlockCache(t *testing.T) {
	endpoint, store := newS3Stub(t)
	cfg := testCfg(endpoint)
	cfg.Bucket = "b"
	payload := bytes.Repeat([]byte("A"), 1000)
	store.Store("cached.bin", payload)

	cache := NewBlockCache(1 << 20)
	h := &VaultFileHandle{cfg: cfg, client: &http.Client{Timeout: 5 * time.Second}, key: "cached.bin", size: int64(len(payload)), blockCache: cache}

	// First read populates the cache.
	rr, errno := h.Read(context.Background(), make([]byte, len(payload)), 0)
	if errno != 0 {
		t.Fatalf("first read errno=%v", errno)
	}
	first, _ := rr.Bytes(make([]byte, len(payload)))
	if !bytes.Equal(first, payload) {
		t.Fatal("first read mismatch")
	}

	// Remove from backend; a correct cached read must still succeed.
	store.Delete("cached.bin")
	rr2, errno := h.Read(context.Background(), make([]byte, len(payload)), 0)
	if errno != 0 {
		t.Fatalf("cached read errno=%v", errno)
	}
	if second, _ := rr2.Bytes(make([]byte, len(payload))); !bytes.Equal(second, payload) {
		t.Fatal("cached read did not serve the original bytes")
	}
}

func TestBlockCacheLRUEviction(t *testing.T) {
	// Budget for ~2 blocks of 100 bytes.
	c := NewBlockCache(250)
	block := func(b byte) []byte { return bytes.Repeat([]byte{b}, 100) }

	c.Put("b", "k", 0, block('a'))
	c.Put("b", "k", 1, block('b'))
	// Touch block 0 so block 1 becomes the LRU victim.
	if c.Get("b", "k", 0) == nil {
		t.Fatal("block 0 should be cached")
	}
	c.Put("b", "k", 2, block('c')) // exceeds budget → evicts LRU (block 1)

	if c.Get("b", "k", 1) != nil {
		t.Fatal("block 1 should have been evicted as LRU")
	}
	if c.Get("b", "k", 0) == nil || c.Get("b", "k", 2) == nil {
		t.Fatal("most-recently-used blocks should survive eviction")
	}

	// Invalidate drops everything for the object.
	c.Invalidate("b", "k")
	if c.Get("b", "k", 0) != nil || c.Get("b", "k", 2) != nil {
		t.Fatal("Invalidate should remove all blocks for the object")
	}
}

func TestMetaCacheTTL(t *testing.T) {
	m := NewMetaCache(40*time.Millisecond, 40*time.Millisecond)

	m.PutHead("b", "k", 1234)
	if size, ok := m.GetHead("b", "k"); !ok || size != 1234 {
		t.Fatalf("GetHead = (%d,%v), want (1234,true)", size, ok)
	}

	time.Sleep(60 * time.Millisecond)
	if _, ok := m.GetHead("b", "k"); ok {
		t.Fatal("HEAD entry should have expired")
	}

	// InvalidateObject clears HEAD immediately.
	m.PutHead("b", "k2", 9)
	m.InvalidateObject("b", "k2")
	if _, ok := m.GetHead("b", "k2"); ok {
		t.Fatal("InvalidateObject should drop the HEAD entry")
	}
}

package s3

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/Kodiqa-Solutions/VaultS3/internal/metadata"
	"github.com/Kodiqa-Solutions/VaultS3/internal/storage"
)

// TestDrainGate covers the node drain gate (issue #31): when the shared writable
// flag is false, S3 object writes return 503 while reads still succeed; clearing
// the flag restores writes.
func TestDrainGate(t *testing.T) {
	dir := t.TempDir()
	store, err := metadata.NewStore(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	engine, err := storage.NewFileSystem(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatalf("NewFileSystem: %v", err)
	}

	auth := NewAuthenticator(testAccessKey, testSecretKey, store, nil, nil)
	handler := NewHandler(store, engine, auth, false, "", nil)
	writable := &atomic.Bool{}
	writable.Store(true)
	handler.SetWritableFlag(writable)

	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	bucket := "drainb"
	key := "obj.txt"
	body := []byte("hello drain")

	// Create bucket and seed an object while writable.
	doSigned(t, http.MethodPut, ts.URL+"/"+bucket, nil).Body.Close()
	if resp := doSigned(t, http.MethodPut, ts.URL+"/"+bucket+"/"+key, body); resp.StatusCode != http.StatusOK {
		t.Fatalf("seed PUT while writable: status %d", resp.StatusCode)
	}

	// Drain: writes rejected with 503, reads still served.
	writable.Store(false)

	resp := doSigned(t, http.MethodPut, ts.URL+"/"+bucket+"/new.txt", body)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("PUT while draining: status %d, want 503", resp.StatusCode)
	}
	resp = doSigned(t, http.MethodDelete, ts.URL+"/"+bucket+"/"+key, nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("DELETE while draining: status %d, want 503", resp.StatusCode)
	}
	resp = doSigned(t, http.MethodGet, ts.URL+"/"+bucket+"/"+key, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET while draining: status %d, want 200 (reads must continue)", resp.StatusCode)
	}
	if got := readBody(t, resp); got != string(body) {
		t.Fatalf("GET while draining returned %q, want %q", got, body)
	}

	// Undrain: writes succeed again.
	writable.Store(true)
	if resp := doSigned(t, http.MethodPut, ts.URL+"/"+bucket+"/after.txt", body); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT after undrain: status %d, want 200", resp.StatusCode)
	}
}

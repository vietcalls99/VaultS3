package bucketkeys

import (
	"path/filepath"
	"testing"

	"github.com/Kodiqa-Solutions/VaultS3/internal/metadata"
)

func TestStoreBackedKeyProvisioning(t *testing.T) {
	store := newTestStore(t)
	mk := make([]byte, 32)
	for i := range mk {
		mk[i] = byte(i + 1)
	}
	mgr, err := NewManager(store, mk)
	if err != nil || mgr == nil {
		t.Fatalf("NewManager: mgr=%v err=%v", mgr, err)
	}

	// Opting a bucket in provisions a wrapped DEK into its encryption config.
	if err := mgr.EnableBucket("tenant-a"); err != nil {
		t.Fatal(err)
	}
	cfg, err := store.GetEncryptionConfig("tenant-a")
	if err != nil || cfg == nil {
		t.Fatalf("GetEncryptionConfig: %v", err)
	}
	if cfg.KeyVersion != 1 || len(cfg.WrappedDEKs) != 1 || len(cfg.WrappedDEKs[1]) == 0 {
		t.Fatalf("expected one wrapped DEK at version 1, got %+v", cfg)
	}
	if cfg.SSEAlgorithm != "AES256" {
		t.Fatalf("SSE algorithm should default to AES256, got %q", cfg.SSEAlgorithm)
	}

	// Round-trip through the store-backed manager.
	enc, ok, err := mgr.Encrypt("tenant-a", []byte("ledger"))
	if err != nil || !ok {
		t.Fatalf("Encrypt ok=%v err=%v", ok, err)
	}

	// Persistence: a fresh manager (cold cache) over the same store + KEK reads it.
	mgr2, _ := NewManager(store, mk)
	got, err := mgr2.Decrypt("tenant-a", enc)
	if err != nil || string(got) != "ledger" {
		t.Fatalf("persisted decrypt: got=%q err=%v", got, err)
	}

	// Isolation: a different tenant's key cannot read it.
	mgr.EnableBucket("tenant-b")
	if _, err := mgr.Decrypt("tenant-b", enc); err == nil {
		t.Fatal("tenant B must not decrypt tenant A's object")
	}

	// Crypto-shred clears the key material from the config.
	if err := mgr.ShredBucket("tenant-a"); err != nil {
		t.Fatal(err)
	}
	cfg, _ = store.GetEncryptionConfig("tenant-a")
	if cfg != nil && (cfg.KeyVersion != 0 || len(cfg.WrappedDEKs) != 0) {
		t.Fatalf("shred should clear key material, got %+v", cfg)
	}
}

func TestNoMasterKeyDisablesManager(t *testing.T) {
	store := newTestStore(t)
	mgr, err := NewManager(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mgr != nil {
		t.Fatal("no master key should yield a nil manager (feature unavailable)")
	}
}

func newTestStore(t *testing.T) *metadata.Store {
	t.Helper()
	st, err := metadata.NewStore(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

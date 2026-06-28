package storage

import (
	"bytes"
	"compress/gzip"
	"io"
	"path/filepath"
	"testing"
)

func newCompressed(t *testing.T) (*CompressedEngine, *FileSystem) {
	t.Helper()
	fs, err := NewFileSystem(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("NewFileSystem: %v", err)
	}
	ce := NewCompressedEngine(fs)
	if err := ce.CreateBucketDir("b"); err != nil {
		t.Fatalf("CreateBucketDir: %v", err)
	}
	return ce, fs
}

// TestCompressedZstdRoundTrip verifies new objects are stored zstd-compressed
// (smaller, with the zstd magic) and read back byte-identical.
func TestCompressedZstdRoundTrip(t *testing.T) {
	ce, fs := newCompressed(t)
	plain := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog\n"), 500)

	if _, _, err := ce.PutObject("b", "file.txt", bytes.NewReader(plain), int64(len(plain))); err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	// On disk it must be zstd (magic 28 B5 2F FD) and smaller than the original.
	rawRC, _, err := fs.GetObject("b", "file.txt")
	if err != nil {
		t.Fatalf("inner GetObject: %v", err)
	}
	raw, _ := io.ReadAll(rawRC)
	rawRC.Close()
	if len(raw) < 4 || raw[0] != 0x28 || raw[1] != 0xB5 || raw[2] != 0x2F || raw[3] != 0xFD {
		t.Fatalf("stored object is not zstd (magic): % x", raw[:min(4, len(raw))])
	}
	if len(raw) >= len(plain) {
		t.Fatalf("zstd did not compress: stored %d >= original %d", len(raw), len(plain))
	}

	rc, n, err := ce.GetObject("b", "file.txt")
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if int64(len(plain)) != n || !bytes.Equal(got, plain) {
		t.Fatalf("round-trip mismatch: n=%d gotLen=%d equal=%v", n, len(got), bytes.Equal(got, plain))
	}
}

// TestCompressedReadsLegacyGzip is the backward-compatibility guard: objects
// written by an older (gzip) build must still decompress after the switch to zstd.
func TestCompressedReadsLegacyGzip(t *testing.T) {
	ce, fs := newCompressed(t)
	plain := []byte("legacy object compressed with gzip by an older version of VaultS3")

	var gzbuf bytes.Buffer
	gw := gzip.NewWriter(&gzbuf)
	if _, err := gw.Write(plain); err != nil {
		t.Fatal(err)
	}
	gw.Close()
	// Write the gzip blob raw to the inner engine (simulating an old object).
	if _, _, err := fs.PutObject("b", "old.txt", bytes.NewReader(gzbuf.Bytes()), int64(gzbuf.Len())); err != nil {
		t.Fatalf("inner PutObject: %v", err)
	}

	rc, _, err := ce.GetObject("b", "old.txt")
	if err != nil {
		t.Fatalf("GetObject legacy gzip: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, plain) {
		t.Fatalf("legacy gzip object not decoded: got %q", got)
	}
}

// TestCompressedReadsRawWhenNoMagic covers data written while compression was off
// (no codec magic) — it must be returned unchanged, not mangled.
func TestCompressedReadsRawWhenNoMagic(t *testing.T) {
	ce, fs := newCompressed(t)
	plain := []byte("stored raw while compression was disabled")
	if _, _, err := fs.PutObject("b", "raw.txt", bytes.NewReader(plain), int64(len(plain))); err != nil {
		t.Fatalf("inner PutObject: %v", err)
	}
	rc, _, err := ce.GetObject("b", "raw.txt")
	if err != nil {
		t.Fatalf("GetObject raw: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, plain) {
		t.Fatalf("raw passthrough failed: got %q", got)
	}
}

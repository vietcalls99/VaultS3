package storage

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newPacked(t *testing.T, maxObj, volMax int64) (*PackedEngine, string) {
	t.Helper()
	dir := t.TempDir()
	fs, err := NewFileSystem(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatalf("NewFileSystem: %v", err)
	}
	p, err := NewPackedEngine(fs, maxObj, volMax)
	if err != nil {
		t.Fatalf("NewPackedEngine: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	if err := p.CreateBucketDir("b"); err != nil {
		t.Fatalf("CreateBucketDir: %v", err)
	}
	return p, filepath.Join(dir, "data")
}

func get(t *testing.T, p *PackedEngine, bucket, key string) []byte {
	t.Helper()
	rc, _, err := p.GetObject(bucket, key)
	if err != nil {
		t.Fatalf("GetObject %s: %v", key, err)
	}
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	return b
}

// TestPackedRoundTrip: a small object is packed into a volume (not an individual
// file) and reads back byte-identical.
func TestPackedRoundTrip(t *testing.T) {
	p, dataDir := newPacked(t, 1024, 1<<20)
	plain := []byte("a small object that gets packed into a volume")

	n, etag, err := p.PutObject("b", "small.txt", bytes.NewReader(plain), int64(len(plain)))
	if err != nil || n != int64(len(plain)) || etag == "" {
		t.Fatalf("PutObject: n=%d etag=%q err=%v", n, etag, err)
	}
	// It must NOT be stored as an individual file in the bucket dir.
	if _, err := os.Stat(filepath.Join(dataDir, "b", "small.txt")); err == nil {
		t.Fatal("packed object should not be an individual file")
	}
	// A volume file must exist.
	if vols, _ := filepath.Glob(filepath.Join(dataDir, "_volumes", "vol-*.dat")); len(vols) == 0 {
		t.Fatal("no volume file created")
	}
	if got := get(t, p, "b", "small.txt"); !bytes.Equal(got, plain) {
		t.Fatalf("round-trip mismatch: %q", got)
	}
}

// TestPackedRoutesLargeToInner: objects over the threshold are stored as
// individual files by the inner engine, and still read back correctly.
func TestPackedRoutesLargeToInner(t *testing.T) {
	p, dataDir := newPacked(t, 64, 1<<20)
	big := bytes.Repeat([]byte("ABCD"), 100) // 400 bytes > 64

	if _, _, err := p.PutObject("b", "big.bin", bytes.NewReader(big), int64(len(big))); err != nil {
		t.Fatalf("PutObject big: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "b", "big.bin")); err != nil {
		t.Fatalf("large object should be an inner file: %v", err)
	}
	if got := get(t, p, "b", "big.bin"); !bytes.Equal(got, big) {
		t.Fatal("large round-trip mismatch")
	}
}

// TestPackedPersistsAcrossReopen: packed objects survive an engine close/reopen
// (index + volume persisted).
func TestPackedPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	fs, _ := NewFileSystem(filepath.Join(dir, "data"))
	p, _ := NewPackedEngine(fs, 1024, 1<<20)
	p.CreateBucketDir("b")
	objs := map[string][]byte{"a.txt": []byte("alpha"), "c/b.txt": []byte("bravo"), "c/d.txt": []byte("delta")}
	for k, v := range objs {
		if _, _, err := p.PutObject("b", k, bytes.NewReader(v), int64(len(v))); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}
	p.Close()

	fs2, _ := NewFileSystem(filepath.Join(dir, "data"))
	p2, err := NewPackedEngine(fs2, 1024, 1<<20)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer p2.Close()
	for k, want := range objs {
		if got := get(t, p2, "b", k); !bytes.Equal(got, want) {
			t.Fatalf("%s mismatch after reopen: %q", k, got)
		}
	}
}

func TestPackedDelete(t *testing.T) {
	p, _ := newPacked(t, 1024, 1<<20)
	p.PutObject("b", "x.txt", bytes.NewReader([]byte("data")), 4)
	if !p.ObjectExists("b", "x.txt") {
		t.Fatal("should exist")
	}
	if sz, _ := p.ObjectSize("b", "x.txt"); sz != 4 {
		t.Fatalf("ObjectSize = %d, want 4", sz)
	}
	if err := p.DeleteObject("b", "x.txt"); err != nil {
		t.Fatal(err)
	}
	if p.ObjectExists("b", "x.txt") {
		t.Fatal("should be deleted")
	}
	if _, _, err := p.GetObject("b", "x.txt"); err == nil {
		t.Fatal("get after delete should error")
	}
}

func TestPackedOverwrite(t *testing.T) {
	p, _ := newPacked(t, 1024, 1<<20)
	p.PutObject("b", "k.txt", bytes.NewReader([]byte("first")), 5)
	p.PutObject("b", "k.txt", bytes.NewReader([]byte("second-value")), 12)
	if got := get(t, p, "b", "k.txt"); string(got) != "second-value" {
		t.Fatalf("overwrite: got %q", got)
	}
}

// TestPackedVolumeRolling: a tiny volume cap forces multiple volumes, and every
// object across all volumes remains readable.
func TestPackedVolumeRolling(t *testing.T) {
	p, dataDir := newPacked(t, 1024, 100) // 100-byte volumes → rolls frequently
	for i := 0; i < 20; i++ {
		d := []byte(fmt.Sprintf("object-%02d-payload-data", i))
		if _, _, err := p.PutObject("b", fmt.Sprintf("k%02d", i), bytes.NewReader(d), int64(len(d))); err != nil {
			t.Fatalf("put k%02d: %v", i, err)
		}
	}
	if vols, _ := filepath.Glob(filepath.Join(dataDir, "_volumes", "vol-*.dat")); len(vols) < 2 {
		t.Fatalf("expected multiple volumes from rolling, got %d", len(vols))
	}
	for i := 0; i < 20; i++ {
		want := fmt.Sprintf("object-%02d-payload-data", i)
		if got := get(t, p, "b", fmt.Sprintf("k%02d", i)); string(got) != want {
			t.Fatalf("k%02d mismatch across volumes: %q", i, got)
		}
	}
}

func TestPackedListMergesPackedAndLarge(t *testing.T) {
	p, _ := newPacked(t, 64, 1<<20)
	p.PutObject("b", "small1.txt", bytes.NewReader([]byte("s1")), 2)
	p.PutObject("b", "small2.txt", bytes.NewReader([]byte("s2")), 2)
	big := bytes.Repeat([]byte("x"), 200)
	p.PutObject("b", "big.bin", bytes.NewReader(big), int64(len(big)))

	objs, _, err := p.ListObjects("b", "", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, o := range objs {
		keys = append(keys, o.Key)
	}
	if strings.Join(keys, ",") != "big.bin,small1.txt,small2.txt" {
		t.Fatalf("merged list = %v, want [big.bin small1.txt small2.txt]", keys)
	}
}

// TestPackedConcurrent stresses concurrent packs (with volume rolling) and reads;
// run with -race.
func TestPackedConcurrent(t *testing.T) {
	p, _ := newPacked(t, 4096, 8192)
	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("c/obj-%03d", i)
			data := []byte(fmt.Sprintf("concurrent-payload-number-%03d", i))
			if _, _, err := p.PutObject("b", key, bytes.NewReader(data), int64(len(data))); err != nil {
				t.Errorf("put %s: %v", key, err)
			}
		}(i)
	}
	wg.Wait()
	for i := 0; i < 60; i++ {
		key := fmt.Sprintf("c/obj-%03d", i)
		want := fmt.Sprintf("concurrent-payload-number-%03d", i)
		if got := get(t, p, "b", key); string(got) != want {
			t.Fatalf("%s mismatch: %q", key, got)
		}
	}
}

func sumVolBytes(t *testing.T, dataDir string) int64 {
	t.Helper()
	vols, _ := filepath.Glob(filepath.Join(dataDir, "_volumes", "vol-*.dat"))
	var s int64
	for _, v := range vols {
		if fi, err := os.Stat(v); err == nil {
			s += fi.Size()
		}
	}
	return s
}

// TestPackedCompactReclaims: deleting many objects then compacting shrinks the
// on-disk volumes while survivors stay readable and deleted keys stay gone.
func TestPackedCompactReclaims(t *testing.T) {
	p, dataDir := newPacked(t, 1024, 300) // small volumes → several sealed ones
	const n = 40
	for i := 0; i < n; i++ {
		d := []byte(fmt.Sprintf("payload-for-object-number-%03d-padding", i))
		if _, _, err := p.PutObject("b", fmt.Sprintf("k%02d", i), bytes.NewReader(d), int64(len(d))); err != nil {
			t.Fatalf("put k%02d: %v", i, err)
		}
	}
	before := sumVolBytes(t, dataDir)
	for i := 0; i < 30; i++ { // delete a contiguous early range → whole volumes go dead
		p.DeleteObject("b", fmt.Sprintf("k%02d", i))
	}
	reclaimed, err := p.Compact(0.5)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	after := sumVolBytes(t, dataDir)
	if reclaimed <= 0 || after >= before {
		t.Fatalf("compaction did not reclaim: reclaimed=%d before=%d after=%d", reclaimed, before, after)
	}
	for i := 30; i < n; i++ { // survivors intact
		want := fmt.Sprintf("payload-for-object-number-%03d-padding", i)
		if got := get(t, p, "b", fmt.Sprintf("k%02d", i)); string(got) != want {
			t.Fatalf("k%02d corrupted after compaction: %q", i, got)
		}
	}
	for i := 0; i < 30; i++ { // deleted stay gone
		if p.ObjectExists("b", fmt.Sprintf("k%02d", i)) {
			t.Fatalf("k%02d should be deleted", i)
		}
	}
}

// TestPackedCompactPreservesOverwrites: overwrites leave dead frames; after
// compaction the latest value is returned.
func TestPackedCompactPreservesOverwrites(t *testing.T) {
	p, _ := newPacked(t, 1024, 200)
	for i := 0; i < 10; i++ {
		d := []byte(fmt.Sprintf("OLD-VALUE-%d", i))
		p.PutObject("b", fmt.Sprintf("k%d", i), bytes.NewReader(d), int64(len(d)))
	}
	for i := 0; i < 10; i++ {
		d := []byte(fmt.Sprintf("NEW-VALUE-%d", i))
		p.PutObject("b", fmt.Sprintf("k%d", i), bytes.NewReader(d), int64(len(d)))
	}
	if _, err := p.Compact(0.3); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	for i := 0; i < 10; i++ {
		want := fmt.Sprintf("NEW-VALUE-%d", i)
		if got := get(t, p, "b", fmt.Sprintf("k%d", i)); string(got) != want {
			t.Fatalf("k%d: got %q want %q", i, got, want)
		}
	}
}

// TestPackedCompactConcurrent runs compaction alongside concurrent reads and
// writes; run with -race. All survivors and newly written objects must be intact.
func TestPackedCompactConcurrent(t *testing.T) {
	p, _ := newPacked(t, 1024, 256)
	const n = 60
	for i := 0; i < n; i++ {
		d := []byte(fmt.Sprintf("v-%03d-payload-data-here", i))
		p.PutObject("b", fmt.Sprintf("k%03d", i), bytes.NewReader(d), int64(len(d)))
	}
	for i := 0; i < n; i += 2 { // delete evens → dead space
		p.DeleteObject("b", fmt.Sprintf("k%03d", i))
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 5; j++ {
			p.Compact(0.1)
		}
	}()
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 1; j < n; j += 2 { // odd survivors
				rc, _, err := p.GetObject("b", fmt.Sprintf("k%03d", j))
				if err != nil {
					t.Errorf("get k%03d during compaction: %v", j, err)
					continue
				}
				got, _ := io.ReadAll(rc)
				rc.Close()
				if string(got) != fmt.Sprintf("v-%03d-payload-data-here", j) {
					t.Errorf("k%03d mismatch during compaction: %q", j, got)
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 20; j++ {
			d := []byte(fmt.Sprintf("new-%03d", j))
			p.PutObject("b", fmt.Sprintf("new%03d", j), bytes.NewReader(d), int64(len(d)))
		}
	}()
	wg.Wait()

	for j := 1; j < n; j += 2 {
		if got := get(t, p, "b", fmt.Sprintf("k%03d", j)); string(got) != fmt.Sprintf("v-%03d-payload-data-here", j) {
			t.Fatalf("k%03d final mismatch: %q", j, got)
		}
	}
	for j := 0; j < 20; j++ {
		if got := get(t, p, "b", fmt.Sprintf("new%03d", j)); string(got) != fmt.Sprintf("new-%03d", j) {
			t.Fatalf("new%03d final mismatch: %q", j, got)
		}
	}
}

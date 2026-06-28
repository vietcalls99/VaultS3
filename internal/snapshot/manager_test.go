package snapshot

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Kodiqa-Solutions/VaultS3/internal/metadata"
)

func newStore(t *testing.T) *metadata.Store {
	t.Helper()
	store, err := metadata.NewStore(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// putVer writes a version and makes it the latest pointer for key.
func putVer(t *testing.T, store *metadata.Store, bucket, key, versionID, etag string) {
	t.Helper()
	meta := metadata.ObjectMeta{
		Bucket: bucket, Key: key, VersionID: versionID, ETag: etag,
		Size: 10, IsLatest: true, LastModified: time.Now().Unix(),
	}
	if err := store.PutObjectVersion(meta); err != nil {
		t.Fatalf("PutObjectVersion %s@%s: %v", key, versionID, err)
	}
	if err := store.PutObjectMeta(meta); err != nil {
		t.Fatalf("PutObjectMeta %s: %v", key, err)
	}
}

// latest returns a key->versionID map of the bucket's current latest objects.
func latest(t *testing.T, store *metadata.Store, bucket string) map[string]string {
	t.Helper()
	objs, _, err := store.ListLatestObjects(bucket, "", "", 0)
	if err != nil {
		t.Fatalf("ListLatestObjects: %v", err)
	}
	out := map[string]string{}
	for _, o := range objs {
		out[o.Key] = o.VersionID
	}
	return out
}

func versionedBucket(t *testing.T, store *metadata.Store, name string) {
	t.Helper()
	if err := store.CreateBucket(name); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	if err := store.SetBucketVersioning(name, "Enabled"); err != nil {
		t.Fatalf("SetBucketVersioning: %v", err)
	}
}

func TestSnapshotRequiresVersioning(t *testing.T) {
	store := newStore(t)
	store.CreateBucket("plain") // no versioning
	m := NewManager(store)
	if _, err := m.Create("plain", "nope"); err == nil {
		t.Fatal("expected error: snapshots require versioning enabled")
	}
}

func TestSnapshotLifecycle(t *testing.T) {
	store := newStore(t)
	versionedBucket(t, store, "b")
	m := NewManager(store)

	// Initial state: a@v1, b@v1, c@v1
	putVer(t, store, "b", "a", "v1", "etag-a1")
	putVer(t, store, "b", "b", "v1", "etag-b1")
	putVer(t, store, "b", "c", "v1", "etag-c1")

	snap, err := m.Create("b", "before changes")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if snap.Objects != 3 {
		t.Fatalf("snapshot captured %d objects, want 3", snap.Objects)
	}

	// Mutate: modify a (v2), delete c, add d
	putVer(t, store, "b", "a", "v2", "etag-a2") // modify
	store.DeleteObjectMeta("b", "c")            // delete
	putVer(t, store, "b", "d", "v1", "etag-d1") // add

	// Diff vs snapshot: a modified, c removed, d added; b unchanged.
	diff, err := m.Diff("b", snap.ID)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if diff.Added != 1 || diff.Removed != 1 || diff.Modified != 1 {
		t.Fatalf("diff = +%d -%d ~%d, want +1 -1 ~1 (%+v)", diff.Added, diff.Removed, diff.Modified, diff.Changes)
	}

	// Restore back to the snapshot.
	rr, err := m.Restore("b", snap.ID)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if rr.Reverted != 3 || rr.Removed != 1 || rr.Skipped != 0 {
		t.Fatalf("restore = reverted %d, removed %d, skipped %d; want 3/1/0", rr.Reverted, rr.Removed, rr.Skipped)
	}

	// Live bucket must now match the snapshot exactly: a@v1, b@v1, c@v1, no d.
	got := latest(t, store, "b")
	want := map[string]string{"a": "v1", "b": "v1", "c": "v1"}
	if len(got) != len(want) {
		t.Fatalf("after restore have %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("after restore %s = %q, want %q", k, got[k], v)
		}
	}

	// And a fresh diff should be clean.
	diff2, _ := m.Diff("b", snap.ID)
	if diff2.Added != 0 || diff2.Removed != 0 || diff2.Modified != 0 {
		t.Fatalf("post-restore diff not clean: %+v", diff2)
	}
	if diff2.Changes == nil {
		t.Fatal("a clean diff must return an empty Changes slice, not nil (crashes the dashboard otherwise)")
	}
}

func TestSnapshotListAndDelete(t *testing.T) {
	store := newStore(t)
	versionedBucket(t, store, "b")
	putVer(t, store, "b", "x", "v1", "e")
	m := NewManager(store)

	s1, _ := m.Create("b", "one")
	time.Sleep(2 * time.Millisecond)
	s2, _ := m.Create("b", "two")

	list, err := m.List("b")
	if err != nil || len(list) != 2 {
		t.Fatalf("List returned %d snapshots, want 2 (err=%v)", len(list), err)
	}
	// Newest first.
	if list[0].ID != s2.ID {
		t.Fatalf("expected newest snapshot first")
	}

	if err := m.Delete("b", s1.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if list, _ := m.List("b"); len(list) != 1 {
		t.Fatalf("after delete have %d, want 1", len(list))
	}
}

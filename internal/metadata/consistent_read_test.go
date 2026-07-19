package metadata

import (
	"testing"
	"time"
)

// fakeRaft is a RaftApplier whose ReadBarrier can simulate a write becoming
// visible on this node (as replication would).
type fakeRaft struct {
	onBarrier func()
}

func (f *fakeRaft) Apply([]byte) error           { return nil }
func (f *fakeRaft) IsLeader() bool               { return false }
func (f *fakeRaft) ForwardToLeader([]byte) error { return nil }
func (f *fakeRaft) ReadBarrier(time.Duration) error {
	if f.onBarrier != nil {
		f.onBarrier()
	}
	return nil
}

// TestGetObjectMetaConsistentBarrierOnMiss covers the issue #37 read-side redesign:
// GetObjectMetaConsistent re-reads after a catch-up barrier when the local read
// misses, so a GET right after a PUT on another node doesn't spuriously 404 —
// without slowing the write path (which uses plain GetObjectMeta).
func TestGetObjectMetaConsistentBarrierOnMiss(t *testing.T) {
	store := newTestStore(t)
	if err := store.CreateBucket("b"); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	applied := false
	ds := NewDistributedStore(store, &fakeRaft{onBarrier: func() {
		// Simulate the just-PUT object replicating to this node during the barrier.
		if !applied {
			store.PutObjectMeta(ObjectMeta{Bucket: "b", Key: "k", Size: 5})
			applied = true
		}
	}})

	// Local miss → barrier makes the write visible → re-read hits.
	if meta, _ := ds.GetObjectMetaConsistent("b", "k"); meta == nil {
		t.Fatal("barrier-on-miss did not surface the just-written object")
	}
	// Now present locally → second read hits (the barrier need not have done more).
	if meta, _ := ds.GetObjectMetaConsistent("b", "k"); meta == nil {
		t.Fatal("second consistent read should hit")
	}

	// A genuine miss (nothing to surface) still returns not-found — no false hit.
	dsMiss := NewDistributedStore(store, &fakeRaft{})
	if meta, _ := dsMiss.GetObjectMetaConsistent("b", "does-not-exist"); meta != nil {
		t.Fatalf("genuine miss should return nil, got %+v", meta)
	}
}

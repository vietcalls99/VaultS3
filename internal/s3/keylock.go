package s3

import (
	"hash/fnv"
	"sync"
)

// Striped per-object locks. Conditional writes (If-Match / If-None-Match) must
// serialize their check-then-write against other writers to the same key so the
// compare-and-swap is atomic — otherwise two concurrent `If-None-Match: *` PUTs
// could both pass the precondition check and both succeed, breaking the guarantee
// that exactly one create wins (the basis for lock files and Iceberg commits).
//
// Locks are striped across a fixed array keyed by hash(bucket/key): different
// keys (almost always) map to different stripes and run fully in parallel, while
// the lock set stays bounded regardless of how many distinct keys exist.
const keyLockStripes = 256

var objectKeyLocks [keyLockStripes]sync.Mutex

// lockObjectKey acquires the stripe lock for a bucket/key and returns the unlock
// function (intended for `defer unlock()`).
func lockObjectKey(bucket, key string) func() {
	h := fnv.New32a()
	h.Write([]byte(bucket))
	h.Write([]byte{'/'})
	h.Write([]byte(key))
	idx := h.Sum32() % keyLockStripes
	objectKeyLocks[idx].Lock()
	return objectKeyLocks[idx].Unlock
}

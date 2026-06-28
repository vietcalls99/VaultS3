package metadata

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	bolt "go.etcd.io/bbolt"
)

var bucketSnapshotsBucket = []byte("bucket_snapshots")

// BucketSnapshotEntry records one object's captured version at snapshot time.
type BucketSnapshotEntry struct {
	Key       string `json:"key"`
	VersionID string `json:"versionId"`
	ETag      string `json:"etag"`
	Size      int64  `json:"size"`
}

// BucketSnapshot is an immutable, named capture of a bucket's state — the basis
// for "git-for-buckets" history, diff, and rollback. (Distinct from the Raft
// WriteSnapshot/RestoreSnapshot whole-DB snapshots used for clustering.)
type BucketSnapshot struct {
	ID            string                `json:"id"`
	Bucket        string                `json:"bucket"`
	Message       string                `json:"message"`
	CreatedAt     int64                 `json:"createdAt"`               // unix seconds (display)
	CreatedAtNano int64                 `json:"createdAtNano,omitempty"` // unix nanos (ordering)
	Objects       int                   `json:"objects"`
	Size          int64                 `json:"size"`
	Entries       []BucketSnapshotEntry `json:"entries,omitempty"`
}

func bucketSnapshotKey(bucket, id string) []byte { return []byte(bucket + "/" + id) }

// PutBucketSnapshot stores a snapshot (creating the bolt bucket lazily).
func (s *Store) PutBucketSnapshot(snap BucketSnapshot) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(bucketSnapshotsBucket)
		if err != nil {
			return err
		}
		data, err := json.Marshal(snap)
		if err != nil {
			return err
		}
		return b.Put(bucketSnapshotKey(snap.Bucket, snap.ID), data)
	})
}

// GetBucketSnapshot returns a snapshot with its full entry manifest.
func (s *Store) GetBucketSnapshot(bucket, id string) (*BucketSnapshot, error) {
	var snap BucketSnapshot
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSnapshotsBucket)
		if b == nil {
			return fmt.Errorf("snapshot not found")
		}
		data := b.Get(bucketSnapshotKey(bucket, id))
		if data == nil {
			return fmt.Errorf("snapshot not found")
		}
		return json.Unmarshal(data, &snap)
	})
	if err != nil {
		return nil, err
	}
	return &snap, nil
}

// ListBucketSnapshots returns a bucket's snapshots, newest first, without the
// (potentially large) entry manifests.
func (s *Store) ListBucketSnapshots(bucket string) ([]BucketSnapshot, error) {
	var out []BucketSnapshot
	prefix := bucket + "/"
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSnapshotsBucket)
		if b == nil {
			return nil
		}
		c := b.Cursor()
		for k, v := c.Seek([]byte(prefix)); k != nil && strings.HasPrefix(string(k), prefix); k, v = c.Next() {
			var snap BucketSnapshot
			if err := json.Unmarshal(v, &snap); err != nil {
				continue
			}
			snap.Entries = nil
			out = append(out, snap)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Newest first, using nanosecond resolution so rapid snapshots order
	// deterministically (falling back to seconds for older records).
	sort.Slice(out, func(i, j int) bool {
		ai, aj := out[i].CreatedAtNano, out[j].CreatedAtNano
		if ai == 0 {
			ai = out[i].CreatedAt * 1e9
		}
		if aj == 0 {
			aj = out[j].CreatedAt * 1e9
		}
		return ai > aj
	})
	return out, nil
}

// DeleteBucketSnapshot removes a snapshot.
func (s *Store) DeleteBucketSnapshot(bucket, id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSnapshotsBucket)
		if b == nil {
			return nil
		}
		return b.Delete(bucketSnapshotKey(bucket, id))
	})
}

package erasure

import (
	"bytes"
	"testing"
)

// makeData returns a deterministic byte slice of the given length.
func makeData(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + 3)
	}
	return b
}

func TestEncoderRoundTrip(t *testing.T) {
	enc, err := NewEncoder(4, 2)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}

	for _, size := range []int{1, 100, 4096, 65537} {
		data := makeData(size)
		shards, err := enc.Encode(data)
		if err != nil {
			t.Fatalf("Encode(%d): %v", size, err)
		}
		if len(shards) != 6 {
			t.Fatalf("size %d: expected 6 shards, got %d", size, len(shards))
		}

		got, err := enc.Decode(shards, int64(size))
		if err != nil {
			t.Fatalf("Decode(%d): %v", size, err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("size %d: round-trip mismatch", size)
		}
	}
}

// TestEncoderReconstructsUpToParity verifies that losing exactly `parity`
// shards (data or parity, in any position) is still recoverable.
func TestEncoderReconstructsUpToParity(t *testing.T) {
	enc, err := NewEncoder(4, 2)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	data := makeData(10000)

	// Drop every distinct pair of shards (2 == parity count) and confirm recovery.
	for a := 0; a < 6; a++ {
		for b := a + 1; b < 6; b++ {
			shards, err := enc.Encode(data)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			shards[a] = nil
			shards[b] = nil

			got, err := enc.Decode(shards, int64(len(data)))
			if err != nil {
				t.Fatalf("Decode with shards %d,%d missing: %v", a, b, err)
			}
			if !bytes.Equal(got, data) {
				t.Fatalf("shards %d,%d missing: data mismatch after reconstruct", a, b)
			}
		}
	}
}

// TestEncoderFailsBeyondParity verifies that losing more than `parity` shards
// fails loudly rather than returning corrupt data.
func TestEncoderFailsBeyondParity(t *testing.T) {
	enc, err := NewEncoder(4, 2)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	data := makeData(10000)
	shards, err := enc.Encode(data)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// 3 missing > 2 parity → unrecoverable.
	shards[0] = nil
	shards[1] = nil
	shards[2] = nil

	if _, err := enc.Decode(shards, int64(len(data))); err == nil {
		t.Fatal("expected error decoding with 3 missing shards, got nil")
	}
}

// TestEncoderDetectsCorruption verifies Verify catches a silently flipped byte.
func TestEncoderDetectsCorruption(t *testing.T) {
	enc, err := NewEncoder(4, 2)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	shards, err := enc.Encode(makeData(8192))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	ok, err := enc.Verify(shards)
	if err != nil || !ok {
		t.Fatalf("expected freshly encoded shards to verify (ok=%v err=%v)", ok, err)
	}

	// Flip a byte in a data shard.
	shards[0][0] ^= 0xFF
	ok, err = enc.Verify(shards)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if ok {
		t.Fatal("expected Verify to report corruption after byte flip")
	}
}

func TestNewEncoderInvalidParams(t *testing.T) {
	if _, err := NewEncoder(0, 2); err == nil {
		t.Fatal("expected error for 0 data shards")
	}
}

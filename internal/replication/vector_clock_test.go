package replication

import "testing"

func TestVectorClockIncrementAndGet(t *testing.T) {
	vc := NewVectorClock()
	if vc.Get("a") != 0 {
		t.Fatal("absent site should read 0")
	}
	vc.Increment("a")
	vc.Increment("a")
	if vc.Get("a") != 2 {
		t.Fatalf("Get(a) = %d, want 2", vc.Get("a"))
	}
}

func TestVectorClockCompare(t *testing.T) {
	tests := []struct {
		name   string
		a, b   VectorClock
		expect Ordering
	}{
		{"equal-empty", VectorClock{}, VectorClock{}, Equal},
		{"equal", VectorClock{"a": 2, "b": 1}, VectorClock{"a": 2, "b": 1}, Equal},
		{"before", VectorClock{"a": 1}, VectorClock{"a": 2}, HappenedBefore},
		{"before-missing-key", VectorClock{"a": 1}, VectorClock{"a": 1, "b": 1}, HappenedBefore},
		{"after", VectorClock{"a": 3, "b": 1}, VectorClock{"a": 2, "b": 1}, HappenedAfter},
		{"concurrent", VectorClock{"a": 2, "b": 1}, VectorClock{"a": 1, "b": 2}, Concurrent},
		{"concurrent-disjoint", VectorClock{"a": 1}, VectorClock{"b": 1}, Concurrent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.Compare(tt.b); got != tt.expect {
				t.Fatalf("Compare = %v, want %v", got, tt.expect)
			}
		})
	}
}

// TestVectorClockCompareAntisymmetry: if a happened-before b, then b
// happened-after a (and concurrency is symmetric).
func TestVectorClockCompareAntisymmetry(t *testing.T) {
	a := VectorClock{"x": 1, "y": 2}
	b := VectorClock{"x": 2, "y": 2}
	if a.Compare(b) != HappenedBefore {
		t.Fatal("a should be before b")
	}
	if b.Compare(a) != HappenedAfter {
		t.Fatal("b should be after a")
	}

	c := VectorClock{"x": 2, "y": 1}
	if a.Compare(c) != Concurrent || c.Compare(a) != Concurrent {
		t.Fatal("a and c should be mutually concurrent")
	}
}

func TestVectorClockMerge(t *testing.T) {
	a := VectorClock{"a": 3, "b": 1}
	b := VectorClock{"a": 1, "b": 5, "c": 2}
	m := a.Merge(b)

	want := VectorClock{"a": 3, "b": 5, "c": 2}
	for k, v := range want {
		if m[k] != v {
			t.Fatalf("merged[%s] = %d, want %d", k, m[k], v)
		}
	}

	// Merge must not mutate the inputs.
	if a.Get("b") != 1 || b.Get("a") != 1 {
		t.Fatal("Merge mutated an input clock")
	}

	// The merge dominates both parents.
	if m.Compare(a) != HappenedAfter {
		t.Fatal("merge should be after a")
	}
	if m.Compare(b) != HappenedAfter {
		t.Fatal("merge should be after b")
	}
}

func TestVectorClockCloneIsolation(t *testing.T) {
	a := VectorClock{"a": 1}
	c := a.Clone()
	c.Increment("a")
	if a.Get("a") != 1 {
		t.Fatal("Clone is not independent of the original")
	}
	if c.Get("a") != 2 {
		t.Fatal("clone increment did not take effect")
	}
}

func TestVectorClockBytesRoundTrip(t *testing.T) {
	vc := VectorClock{"site-1": 4, "site-2": 9}
	parsed, err := ParseVectorClock(vc.Bytes())
	if err != nil {
		t.Fatalf("ParseVectorClock: %v", err)
	}
	if parsed.Compare(vc) != Equal {
		t.Fatal("round-tripped clock differs from original")
	}

	// Empty input parses to an empty clock, not an error.
	empty, err := ParseVectorClock(nil)
	if err != nil {
		t.Fatalf("ParseVectorClock(nil): %v", err)
	}
	if len(empty) != 0 {
		t.Fatal("nil input should parse to empty clock")
	}
}

// TestVectorClockCausalChain models a real replication causal chain across two
// sites and asserts the ordering at each step.
func TestVectorClockCausalChain(t *testing.T) {
	// site-1 writes (a:1), site-2 receives + writes on top (a:1,b:1).
	v1 := NewVectorClock()
	v1.Increment("site-1") // {site-1:1}

	v2 := v1.Clone().Merge(NewVectorClock())
	v2.Increment("site-2") // {site-1:1, site-2:1}

	if v1.Compare(v2) != HappenedBefore {
		t.Fatal("v1 should causally precede v2")
	}

	// Meanwhile site-1 makes an independent second write → concurrent with v2.
	v3 := v1.Clone()
	v3.Increment("site-1") // {site-1:2}

	if v3.Compare(v2) != Concurrent {
		t.Fatalf("v3 and v2 should be concurrent, got %v", v3.Compare(v2))
	}
}

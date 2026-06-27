package cluster

import (
	"fmt"
	"testing"
)

func TestHashRingEmpty(t *testing.T) {
	h := NewHashRing(64)
	if got := h.GetNode("b", "k"); got != "" {
		t.Fatalf("empty ring GetNode = %q, want \"\"", got)
	}
	if nodes := h.GetNodes("b", "k", 3); nodes != nil {
		t.Fatalf("empty ring GetNodes = %v, want nil", nodes)
	}
}

func TestHashRingAddIsIdempotent(t *testing.T) {
	h := NewHashRing(64)
	h.AddNode("n1")
	h.AddNode("n1")
	if h.NodeCount() != 1 {
		t.Fatalf("NodeCount after double-add = %d, want 1", h.NodeCount())
	}
}

// TestHashRingDeterministic: the same key always maps to the same node.
func TestHashRingDeterministic(t *testing.T) {
	h := NewHashRing(128)
	for _, n := range []string{"n1", "n2", "n3"} {
		h.AddNode(n)
	}

	first := h.GetNode("bucket", "object-key")
	if first == "" {
		t.Fatal("expected a node for key")
	}
	for i := 0; i < 100; i++ {
		if got := h.GetNode("bucket", "object-key"); got != first {
			t.Fatalf("non-deterministic mapping: got %q, want %q", got, first)
		}
	}
}

// TestHashRingReplicasDistinct: GetNodes returns N distinct nodes, capped at the
// ring size.
func TestHashRingReplicasDistinct(t *testing.T) {
	h := NewHashRing(128)
	for _, n := range []string{"n1", "n2", "n3"} {
		h.AddNode(n)
	}

	for i := 0; i < 50; i++ {
		key := fmt.Sprintf("obj-%d", i)
		nodes := h.GetNodes("b", key, 3)
		if len(nodes) != 3 {
			t.Fatalf("key %s: got %d replicas, want 3", key, len(nodes))
		}
		seen := map[string]bool{}
		for _, n := range nodes {
			if seen[n] {
				t.Fatalf("key %s: duplicate replica %q in %v", key, n, nodes)
			}
			seen[n] = true
		}
	}

	// Requesting more replicas than nodes is capped, not padded.
	if got := h.GetNodes("b", "k", 10); len(got) != 3 {
		t.Fatalf("GetNodes(n=10) with 3 nodes = %d, want 3", len(got))
	}
}

// TestHashRingDistribution: keys spread across all nodes (no node gets ~nothing).
func TestHashRingDistribution(t *testing.T) {
	h := NewHashRing(256)
	nodes := []string{"n1", "n2", "n3", "n4"}
	for _, n := range nodes {
		h.AddNode(n)
	}

	const keys = 8000
	counts := map[string]int{}
	for i := 0; i < keys; i++ {
		counts[h.GetNode("b", fmt.Sprintf("key-%d", i))]++
	}

	for _, n := range nodes {
		// With 4 nodes the fair share is 25%; allow a generous spread but ensure
		// every node carries a meaningful fraction (>10%).
		if counts[n] < keys/10 {
			t.Fatalf("node %q got only %d/%d keys — distribution too skewed", n, counts[n], keys)
		}
	}
}

// TestHashRingConsistencyOnRemoval is the key consistent-hashing property:
// removing a node must only reassign keys that belonged to that node; every
// other key keeps its primary. This is what makes rebalancing cheap.
func TestHashRingConsistencyOnRemoval(t *testing.T) {
	h := NewHashRing(256)
	for _, n := range []string{"n1", "n2", "n3", "n4"} {
		h.AddNode(n)
	}

	const keys = 5000
	before := make(map[string]string, keys)
	for i := 0; i < keys; i++ {
		k := fmt.Sprintf("key-%d", i)
		before[k] = h.GetNode("b", k)
	}

	h.RemoveNode("n3")
	if h.HasNode("n3") {
		t.Fatal("n3 still present after RemoveNode")
	}

	moved := 0
	for k, prev := range before {
		now := h.GetNode("b", k)
		if prev == "n3" {
			if now == "n3" {
				t.Fatalf("key %s still mapped to removed node", k)
			}
			moved++
			continue
		}
		// Keys not owned by n3 must be untouched.
		if now != prev {
			t.Fatalf("key %s moved from %q to %q despite owner surviving", k, prev, now)
		}
	}

	if moved == 0 {
		t.Fatal("expected some keys to move off the removed node")
	}
}

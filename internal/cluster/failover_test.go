package cluster

import "testing"

// TestShouldProxyNeverServesReadFromNonHolder is the issue #37 read-after-write fix:
// with replica_count=1 the object's data lives on exactly one node, so a read must
// be routed to that owner. If a per-node failure detector marks the (healthy) owner
// down, the read must still be forwarded to it — never served locally by a node that
// holds no data, which would return a phantom "Object not found" for a live object.
func TestShouldProxyNeverServesReadFromNonHolder(t *testing.T) {
	ring := NewHashRing(64)
	for _, id := range []string{"n0", "n1", "n2", "n3", "n4"} {
		ring.AddNode(id)
	}
	bucket, key := "b", "some/object/key"
	order := ring.GetNodes(bucket, key, 5) // full ring preference order
	if len(order) < 3 {
		t.Fatalf("ring returned %d nodes, need >=3", len(order))
	}
	owner := order[0]  // the sole data holder at replica_count=1
	second := order[1] // NOT a holder at replica_count=1

	// The failure detector marks the true owner down (e.g. a flaky-probe false positive).
	det := NewFailureDetector("tester", DetectorConfig{SuspectAfter: 1, DownAfter: 1})
	det.AddNode(owner, "127.0.0.1:1") // unreachable → probe fails → declared down
	det.probeNode(det.nodes[owner])
	if !det.IsNodeDown(owner) {
		t.Fatalf("precondition: owner %s should be marked down", owner)
	}

	newFP := func(selfID string, replicas int) *FailoverProxy {
		return &FailoverProxy{
			Proxy: &Proxy{
				ring:      ring,
				node:      &Node{cfg: ClusterConfig{NodeID: selfID}},
				placement: PlacementConfig{ReplicaCount: replicas},
			},
			detector: det,
		}
	}

	// replica_count=1, request lands on the second-preference node (a non-holder):
	// it must forward to the owner, NOT serve locally (which 404s a live object).
	if got := newFP(second, 1).ShouldProxy(bucket, key); got != owner {
		t.Fatalf("replica_count=1, owner down, self=non-holder: ShouldProxy=%q, want owner %q (forward, not phantom-404 local serve)", got, owner)
	}

	// The owner always serves locally.
	if got := newFP(owner, 1).ShouldProxy(bucket, key); got != "" {
		t.Fatalf("owner should serve locally, got %q", got)
	}

	// replica_count=2: the second node IS a data holder, so a down primary
	// legitimately fails over to it (this failover is still valid).
	if got := newFP(order[2], 2).ShouldProxy(bucket, key); got != second {
		t.Fatalf("replica_count=2, primary down: want failover to holder %q, got %q", second, got)
	}
}

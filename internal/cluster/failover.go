package cluster

import (
	"log/slog"
	"net/http"
)

// FailoverProxy extends the basic Proxy with failure-aware routing.
// When the primary node is down, requests are forwarded to the next
// healthy replica in the hash ring.
type FailoverProxy struct {
	*Proxy
	detector *FailureDetector
}

// NewFailoverProxy creates a failover-aware proxy.
func NewFailoverProxy(proxy *Proxy, detector *FailureDetector) *FailoverProxy {
	return &FailoverProxy{
		Proxy:    proxy,
		detector: detector,
	}
}

// ShouldProxy returns the target node for a request, accounting for failed nodes.
// Returns empty string if this node should handle the request locally.
//
// Only the first replica_count nodes on the ring actually hold an object's data.
// A read served by any other node returns a phantom "Object not found" for a live
// object, so we route strictly within that holder set — we do NOT widen it for
// failover, because a node outside it has nothing to serve (issue #37). This is the
// crux of the read-after-write miss: a healthy owner that a per-node failure
// detector has (possibly wrongly) marked down must NOT cause the read to be
// answered by a data-less node; it is forwarded to the owner regardless.
func (f *FailoverProxy) ShouldProxy(bucket, key string) string {
	if bucket == "" {
		return ""
	}

	holders := f.ring.GetNodes(bucket, key, f.dataReplicas())
	if len(holders) == 0 {
		return ""
	}
	selfID := f.node.NodeID()

	// If this node holds the data, serve locally.
	for _, nodeID := range holders {
		if nodeID == selfID {
			return ""
		}
	}

	// Forward to the first healthy holder.
	for _, nodeID := range holders {
		if f.detector == nil || !f.detector.IsNodeDown(nodeID) {
			return nodeID
		}
		slog.Debug("failover: holder marked down", "node_id", nodeID, "bucket", bucket, "key", key)
	}

	// Every holder is marked down. Forward to the primary owner anyway rather than
	// answering from this node (which has no data): the "down" may be a false
	// positive — in which case the read succeeds — and if the owner really is down,
	// an honest upstream error beats a misleading 404 (issue #37).
	slog.Warn("failover: all data holders marked down, forwarding to primary owner",
		"owner", holders[0], "bucket", bucket, "key", key)
	return holders[0]
}

// dataReplicas is the number of ring nodes that hold an object's data — the set a
// read may legitimately be served from. Never less than 1.
func (f *FailoverProxy) dataReplicas() int {
	n := f.placement.ReplicaCount
	if n < 1 {
		n = 1
	}
	return n
}

// ForwardWithRetry attempts to forward a request to the primary node,
// falling back to replicas if the primary fails.
func (f *FailoverProxy) ForwardWithRetry(w http.ResponseWriter, r *http.Request, bucket, key string) bool {
	target := f.ShouldProxy(bucket, key)
	if target == "" {
		return false // handle locally
	}
	f.ForwardRequest(w, r, target)
	return true
}

// OnNodeDown is called when the detector declares a node as down.
// It updates the hash ring and proxy state.
func (f *FailoverProxy) OnNodeDown(nodeID string) {
	slog.Warn("failover: node down, traffic will route to replicas",
		"node_id", nodeID,
	)
	// Don't remove from ring — the ring is used to determine ownership.
	// The failover logic in ShouldProxy skips down nodes.
	// This preserves object placement for when the node recovers.
}

// OnNodeRecover is called when a previously down node comes back.
func (f *FailoverProxy) OnNodeRecover(nodeID string) {
	slog.Info("failover: node recovered, resuming normal routing",
		"node_id", nodeID,
	)
}

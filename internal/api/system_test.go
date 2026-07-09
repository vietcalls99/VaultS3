package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Kodiqa-Solutions/VaultS3/internal/sysinfo"
)

// TestClusterInfoAggregation covers the Tier-2 cluster rollup: the coordinator
// fetches each peer's /api/v1/system (via admin login) and aggregates capacity,
// marking unreachable peers without failing the whole call.
func TestClusterInfoAggregation(t *testing.T) {
	h, store := newTestAPI(t)
	if err := store.CreateBucket("b"); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	// A reachable peer that serves admin login + its system info.
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/auth/login"):
			w.Write([]byte(`{"token":"t"}`))
		case strings.HasSuffix(r.URL.Path, "/system"):
			json.NewEncoder(w).Encode(NodeSystemInfo{
				Version: "v9", OS: "linux", Arch: "amd64",
				Disk:        sysinfo.Disk{TotalBytes: 1000, UsedBytes: 400, FreeBytes: 600},
				ObjectBytes: 50, ObjectCount: 5, BucketCount: 2,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer peer.Close()
	peerAddr := strings.TrimPrefix(peer.URL, "http://")

	h.SetClusterInfo("node-a", func() map[string]string {
		return map[string]string{
			"node-a": "self:9000",   // self — computed locally, not fetched
			"node-b": peerAddr,      // reachable peer
			"node-c": "127.0.0.1:1", // unreachable
		}
	})

	rr := httptest.NewRecorder()
	h.handleClusterInfo(rr, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}

	var out struct {
		Clustered      bool             `json:"clustered"`
		NodeCount      int              `json:"nodeCount"`
		ReachableNodes int              `json:"reachableNodes"`
		Nodes          []NodeSystemInfo `json:"nodes"`
		Totals         struct {
			Disk sysinfo.Disk `json:"disk"`
		} `json:"totals"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !out.Clustered {
		t.Error("expected clustered=true")
	}
	if out.NodeCount != 3 {
		t.Errorf("nodeCount=%d, want 3", out.NodeCount)
	}
	if out.ReachableNodes != 2 {
		t.Errorf("reachableNodes=%d, want 2 (self + node-b)", out.ReachableNodes)
	}

	var b, c *NodeSystemInfo
	for i := range out.Nodes {
		switch out.Nodes[i].NodeID {
		case "node-b":
			b = &out.Nodes[i]
		case "node-c":
			c = &out.Nodes[i]
		}
	}
	if b == nil || !b.Reachable || b.Version != "v9" || b.Disk.TotalBytes != 1000 {
		t.Fatalf("reachable peer node-b not aggregated correctly: %+v", b)
	}
	if c == nil || c.Reachable {
		t.Fatalf("unreachable peer node-c should be marked down: %+v", c)
	}
	// Totals include the peer's 1000 plus this node's real disk.
	if out.Totals.Disk.TotalBytes < 1000 {
		t.Errorf("totals should include peer's 1000, got %d", out.Totals.Disk.TotalBytes)
	}
}

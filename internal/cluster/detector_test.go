package cluster

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func stateOf(d *FailureDetector, id string) NodeState {
	for _, nh := range d.NodeStates() {
		if nh.NodeID == id {
			return nh.State
		}
	}
	return NodeState(-1)
}

// TestFailureDetectorTransitions drives the healthy→suspect→down→healthy state
// machine deterministically by probing a server whose status code we control,
// and verifies the down/recover callbacks fire on the right transitions.
func TestFailureDetectorTransitions(t *testing.T) {
	var code atomic.Int32
	code.Store(http.StatusInternalServerError) // start unhealthy

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(code.Load()))
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	downCh := make(chan string, 1)
	recoverCh := make(chan string, 1)

	d := NewFailureDetector("self", DetectorConfig{
		SuspectAfter:     2,
		DownAfter:        3,
		ProbeTimeoutSecs: 1,
	})
	d.SetCallbacks(
		func(id string) { downCh <- id },
		func(id string) { recoverCh <- id },
	)
	d.AddNode("n1", addr)

	nh := d.nodes["n1"]
	if nh == nil {
		t.Fatal("node n1 not registered")
	}

	// Probe 1: first failure, still below suspect threshold (2).
	d.probeNode(nh)
	if got := stateOf(d, "n1"); got != NodeHealthy {
		t.Fatalf("after 1 failure: state %v, want healthy", got)
	}

	// Probe 2: reaches suspect threshold.
	d.probeNode(nh)
	if got := stateOf(d, "n1"); got != NodeSuspect {
		t.Fatalf("after 2 failures: state %v, want suspect", got)
	}

	// Probe 3: reaches down threshold → onNodeDown fires.
	d.probeNode(nh)
	if got := stateOf(d, "n1"); got != NodeDown {
		t.Fatalf("after 3 failures: state %v, want down", got)
	}
	if !d.IsNodeDown("n1") {
		t.Fatal("IsNodeDown should be true")
	}
	select {
	case id := <-downCh:
		if id != "n1" {
			t.Fatalf("down callback for %q, want n1", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("onNodeDown callback did not fire")
	}

	// Recover: server returns healthy again → onNodeRecover fires.
	code.Store(http.StatusOK)
	d.probeNode(nh)
	if got := stateOf(d, "n1"); got != NodeHealthy {
		t.Fatalf("after recovery probe: state %v, want healthy", got)
	}
	select {
	case id := <-recoverCh:
		if id != "n1" {
			t.Fatalf("recover callback for %q, want n1", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("onNodeRecover callback did not fire")
	}
}

// TestFailureDetectorIgnoresSelf: a detector never monitors its own ID.
func TestFailureDetectorIgnoresSelf(t *testing.T) {
	d := NewFailureDetector("self", DetectorConfig{})
	d.AddNode("self", "127.0.0.1:1")
	if len(d.NodeStates()) != 0 {
		t.Fatal("detector should not monitor itself")
	}
}

// TestFailureDetectorHealthyNodes: HealthyNodes always includes self and only
// nodes currently in the healthy state.
func TestFailureDetectorHealthyNodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	d := NewFailureDetector("self", DetectorConfig{SuspectAfter: 1, DownAfter: 1})
	d.AddNode("n1", addr)

	healthy := d.HealthyNodes()
	if !healthy["self"] {
		t.Fatal("self must always be healthy")
	}
	if !healthy["n1"] {
		t.Fatal("n1 should be healthy before any failed probe")
	}

	d.probeNode(d.nodes["n1"]) // one failure → down (thresholds are 1)
	if d.HealthyNodes()["n1"] {
		t.Fatal("n1 should be excluded from healthy set after going down")
	}
}

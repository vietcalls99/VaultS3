package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

func runCluster(args []string) {
	if len(args) == 0 {
		fmt.Println(`Usage: vaults3-cli cluster <subcommand>

Subcommands:
  status                       Show cluster members, leader, and drain state
  join <nodeId> <raftAddr>     Add a member (run against the leader)
  leave <nodeId>               Remove a member (run against the leader)
  drain [nodeId]               Stop a node accepting writes (defaults to the node served)
  undrain [nodeId]             Resume writes on a node
  rebalance                    Move objects to their correct owner after membership changes
  decommission <nodeId>        Drain + rebalance a node so it can be safely replaced`)
		os.Exit(1)
	}

	requireCreds()

	switch args[0] {
	case "status":
		clusterStatus()
	case "join":
		if len(args) < 3 {
			fatal("usage: vaults3-cli cluster join <nodeId> <raftAddr>")
		}
		clusterJoin(args[1], args[2])
	case "leave":
		if len(args) < 2 {
			fatal("usage: vaults3-cli cluster leave <nodeId>")
		}
		clusterLeave(args[1])
	case "drain":
		clusterDrain(argOrEmpty(args, 1), true)
	case "undrain":
		clusterDrain(argOrEmpty(args, 1), false)
	case "rebalance":
		clusterRebalance()
	case "decommission":
		if len(args) < 2 {
			fatal("usage: vaults3-cli cluster decommission <nodeId>")
		}
		clusterDecommission(args[1])
	default:
		fatal("unknown cluster subcommand: " + args[0])
	}
}

func argOrEmpty(args []string, i int) string {
	if len(args) > i {
		return args[i]
	}
	return ""
}

// clusterPost sends an admin POST with an optional JSON body and returns the
// decoded response, exiting on any error or non-2xx status.
func clusterPost(path string, body any) map[string]any {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	resp, err := apiRequest("POST", path, rdr)
	if err != nil {
		fatal(err.Error())
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fatal(fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(raw)))
	}
	var out map[string]any
	json.Unmarshal(raw, &out)
	return out
}

func clusterStatus() {
	resp, err := apiRequest("GET", "/cluster/status", nil)
	if err != nil {
		fatal(err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		fatal(fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(body)))
	}

	var st struct {
		Clustered bool   `json:"clustered"`
		SelfID    string `json:"selfId"`
		LeaderID  string `json:"leaderId"`
		IsLeader  bool   `json:"isLeader"`
		Writable  bool   `json:"writable"`
		Members   []struct {
			NodeID   string `json:"nodeId"`
			Address  string `json:"address"`
			Suffrage string `json:"suffrage"`
			Leader   bool   `json:"leader"`
		} `json:"members"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		fatal("parse response: " + err.Error())
	}

	if !st.Clustered {
		writable := "writable"
		if !st.Writable {
			writable = "draining (writes rejected)"
		}
		fmt.Printf("This node is running standalone (not clustered). Write state: %s\n", writable)
		return
	}

	fmt.Printf("Cluster: self=%s leader=%s  this node: %s\n", st.SelfID, st.LeaderID, writeState(st.Writable))
	headers := []string{"NODE ID", "RAFT ADDRESS", "SUFFRAGE", "ROLE"}
	var rows [][]string
	for _, m := range st.Members {
		role := "follower"
		if m.Leader {
			role = "leader"
		}
		rows = append(rows, []string{m.NodeID, m.Address, m.Suffrage, role})
	}
	printTable(headers, rows)
}

func writeState(writable bool) string {
	if writable {
		return "writable"
	}
	return "draining (writes rejected)"
}

func clusterJoin(nodeID, addr string) {
	out := clusterPost("/cluster/join", map[string]string{"nodeId": nodeID, "addr": addr})
	fmt.Println(msgOr(out, "node "+nodeID+" joined"))
}

func clusterLeave(nodeID string) {
	out := clusterPost("/cluster/leave", map[string]string{"nodeId": nodeID})
	fmt.Println(msgOr(out, "node "+nodeID+" removed"))
}

func clusterDrain(nodeID string, drain bool) {
	path := "/cluster/undrain"
	if drain {
		path = "/cluster/drain"
	}
	var body any
	if nodeID != "" {
		body = map[string]string{"nodeId": nodeID}
	}
	out := clusterPost(path, body)
	target := fmt.Sprintf("%v", out["nodeId"])
	if target == "" || target == "<nil>" {
		target = "this node"
	}
	if drain {
		fmt.Printf("Draining %s: writes are now rejected, reads continue.\n", target)
	} else {
		fmt.Printf("Resumed writes on %s.\n", target)
	}
}

func clusterRebalance() {
	out := clusterPost("/cluster/rebalance", nil)
	running := out["running"] == true
	fmt.Printf("Rebalance triggered (running=%v). Objects are moving to their correct owner in the background.\n", running)
}

func clusterDecommission(nodeID string) {
	fmt.Printf("Decommissioning %s: this drains the node and triggers a rebalance so its\n", nodeID)
	fmt.Println("data moves to the remaining members. It does NOT remove the node — verify the")
	fmt.Println("data has moved (vaults3-cli info / cluster status), then run:")
	fmt.Printf("  vaults3-cli cluster leave %s\n\n", nodeID)
	fmt.Println("Zero-data-loss requires replica_count >= 2 (replicas already exist elsewhere).")

	clusterPost("/cluster/drain", map[string]string{"nodeId": nodeID})
	fmt.Printf("- %s drained (no new writes)\n", nodeID)
	clusterPost("/cluster/rebalance", nil)
	fmt.Println("- rebalance triggered")
	fmt.Println("\nWatch progress, then leave the node when its data has moved.")
}

// msgOr returns the response "message" field, or a fallback.
func msgOr(out map[string]any, fallback string) string {
	if m, ok := out["message"].(string); ok && m != "" {
		return m
	}
	return fallback
}

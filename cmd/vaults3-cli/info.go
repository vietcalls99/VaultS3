package main

import (
	"encoding/json"
	"fmt"
	"io"
)

type diskInfo struct {
	TotalBytes uint64 `json:"totalBytes"`
	UsedBytes  uint64 `json:"usedBytes"`
	FreeBytes  uint64 `json:"freeBytes"`
}

// runInfo prints server version and storage capacity. In a cluster it shows the
// aggregate capacity and a per-node breakdown (like `mc admin info`).
func runInfo(_ []string) {
	requireCreds()

	resp, err := apiRequest("GET", "/cluster/info", nil)
	if err != nil {
		fatal(err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		fatal(fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(body)))
	}

	var ci struct {
		Clustered      bool `json:"clustered"`
		NodeCount      int  `json:"nodeCount"`
		ReachableNodes int  `json:"reachableNodes"`
		Nodes          []struct {
			NodeID      string   `json:"nodeId"`
			Reachable   bool     `json:"reachable"`
			Error       string   `json:"error"`
			Version     string   `json:"version"`
			OS          string   `json:"os"`
			Arch        string   `json:"arch"`
			Disk        diskInfo `json:"disk"`
			ObjectCount int64    `json:"objectCount"`
		} `json:"nodes"`
		Totals struct {
			Disk        diskInfo `json:"disk"`
			ObjectBytes int64    `json:"objectBytes"`
			ObjectCount int64    `json:"objectCount"`
		} `json:"totals"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ci); err != nil {
		fatal(err.Error())
	}

	var ver, osName, arch string
	if len(ci.Nodes) > 0 {
		ver, osName, arch = ci.Nodes[0].Version, ci.Nodes[0].OS, ci.Nodes[0].Arch
	}
	fmt.Printf("VaultS3 %s (%s/%s)\n", ver, osName, arch)
	fmt.Printf("Endpoint:   %s\n", endpoint)
	if ci.Clustered {
		fmt.Printf("Cluster:    %d nodes (%d reachable)\n", ci.NodeCount, ci.ReachableNodes)
	}
	fmt.Printf("Objects:    %d (%s logical)\n", ci.Totals.ObjectCount, humanBytes(uint64(ci.Totals.ObjectBytes)))

	var pct float64
	if ci.Totals.Disk.TotalBytes > 0 {
		pct = float64(ci.Totals.Disk.UsedBytes) / float64(ci.Totals.Disk.TotalBytes) * 100
	}
	label := "Disk capacity:"
	if ci.Clustered {
		label = "Cluster capacity:"
	}
	fmt.Println(label)
	fmt.Printf("  Used:     %s (%.1f%%)\n", humanBytes(ci.Totals.Disk.UsedBytes), pct)
	fmt.Printf("  Free:     %s\n", humanBytes(ci.Totals.Disk.FreeBytes))
	fmt.Printf("  Total:    %s\n", humanBytes(ci.Totals.Disk.TotalBytes))

	if ci.Clustered {
		fmt.Println("Nodes:")
		for _, n := range ci.Nodes {
			if n.Reachable {
				fmt.Printf("  %-24s %-10s %s / %s\n", n.NodeID, n.Version,
					humanBytes(n.Disk.UsedBytes), humanBytes(n.Disk.TotalBytes))
			} else {
				reason := n.Error
				if reason == "" {
					reason = "unreachable"
				}
				fmt.Printf("  %-24s unreachable (%s)\n", n.NodeID, reason)
			}
		}
	}
}

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

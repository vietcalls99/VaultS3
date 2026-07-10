package api

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"time"

	"github.com/Kodiqa-Solutions/VaultS3/internal/sysinfo"
)

// NodeSystemInfo is one node's version, capacity, and object usage. The cluster
// fields (NodeID/Address/Reachable) are omitted from the single-node
// /api/v1/system response and populated only in the cluster rollup.
type NodeSystemInfo struct {
	NodeID      string       `json:"nodeId,omitempty"`
	Address     string       `json:"address,omitempty"`
	Reachable   bool         `json:"reachable,omitempty"`
	Error       string       `json:"error,omitempty"` // why a peer is unreachable
	Version     string       `json:"version"`
	OS          string       `json:"os"`
	Arch        string       `json:"arch"`
	DataDirs    []string     `json:"dataDirs"`
	Disk        sysinfo.Disk `json:"disk"`
	ObjectBytes int64        `json:"objectBytes"`
	ObjectCount int64        `json:"objectCount"`
	BucketCount int          `json:"bucketCount"`
}

// clusterInfoClient fetches peers' /api/v1/system for the cluster rollup. Inter
// -node TLS is commonly self-signed, so certificate verification is skipped for
// these internal, admin-authenticated calls.
var clusterInfoClient = &http.Client{
	Timeout:   5 * time.Second,
	Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
}

// localSystemInfo gathers this node's version, on-disk capacity, and logical
// object usage.
func (h *APIHandler) localSystemInfo() NodeSystemInfo {
	var dirs []string
	if h.cfg != nil {
		dirs = append(dirs, h.cfg.Storage.DataDir, h.cfg.Storage.MetadataDir)
		if h.cfg.Tiering.Enabled && h.cfg.Tiering.ColdDataDir != "" {
			dirs = append(dirs, h.cfg.Tiering.ColdDataDir)
		}
		if h.cfg.Erasure.Enabled {
			dirs = append(dirs, h.cfg.Erasure.DataDirs...)
		}
	}

	var objectBytes, objectCount int64
	var bucketCount int
	if buckets, err := h.store.ListBuckets(); err == nil {
		bucketCount = len(buckets)
		for _, b := range buckets {
			size, count := h.bucketStatCounter(b.Name)
			objectBytes += size
			objectCount += count
		}
	}

	version := "dev"
	if h.updater != nil {
		if v := h.updater.LastStatus().Current; v != "" {
			version = v
		}
	}

	return NodeSystemInfo{
		Version:     version,
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		DataDirs:    uniqueNonEmpty(dirs),
		Disk:        sysinfo.DiskUsage(dirs),
		ObjectBytes: objectBytes,
		ObjectCount: objectCount,
		BucketCount: bucketCount,
	}
}

// handleSystemInfo handles GET /api/v1/system: this node's version, data
// directories, on-disk capacity (total/used/free), and logical object usage.
func (h *APIHandler) handleSystemInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.localSystemInfo())
}

// handleClusterInfo handles GET /api/v1/cluster/info: the version and capacity of
// every node in the cluster, plus aggregate totals — a cluster-wide equivalent of
// `mc admin info`. On a single node it returns just this node.
func (h *APIHandler) handleClusterInfo(w http.ResponseWriter, _ *http.Request) {
	self := h.localSystemInfo()
	self.NodeID = h.clusterSelfID
	self.Reachable = true
	nodes := []NodeSystemInfo{self}

	if h.clusterNodeAddrs != nil {
		for id, addr := range h.clusterNodeAddrs() {
			if id == h.clusterSelfID || addr == "" {
				continue
			}
			nodes = append(nodes, h.fetchPeerSystemInfo(id, addr))
		}
	}

	// Aggregate physical disk across reachable nodes (replicas legitimately use
	// disk on multiple nodes, so this is the true "how full is the cluster").
	var totalDisk sysinfo.Disk
	var objectBytes, objectCount int64
	reachable := 0
	for _, n := range nodes {
		if !n.Reachable {
			continue
		}
		reachable++
		totalDisk.TotalBytes += n.Disk.TotalBytes
		totalDisk.UsedBytes += n.Disk.UsedBytes
		totalDisk.FreeBytes += n.Disk.FreeBytes
		objectBytes += n.ObjectBytes
		objectCount += n.ObjectCount
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"clustered":      h.clusterSelfID != "",
		"nodeCount":      len(nodes),
		"reachableNodes": reachable,
		"nodes":          nodes,
		"totals": map[string]any{
			"disk":        totalDisk,
			"objectBytes": objectBytes,
			"objectCount": objectCount,
		},
	})
}

// fetchPeerSystemInfo logs in to a peer with the shared admin credentials and
// reads its /api/v1/system. An unreachable peer is returned with Reachable=false
// rather than failing the whole rollup.
func (h *APIHandler) fetchPeerSystemInfo(id, addr string) NodeSystemInfo {
	ni := NodeSystemInfo{NodeID: id, Address: addr}
	scheme := "http"
	if h.cfg != nil && h.cfg.Server.TLS.Enabled {
		scheme = "https"
	}
	base := scheme + "://" + addr

	token, err := h.peerLogin(base)
	if err != nil {
		ni.Error = "login: " + err.Error()
		return ni
	}
	req, err := http.NewRequest(http.MethodGet, base+"/api/v1/system", nil)
	if err != nil {
		ni.Error = err.Error()
		return ni
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := clusterInfoClient.Do(req)
	if err != nil {
		ni.Error = err.Error()
		return ni
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		ni.Error = fmt.Sprintf("system returned HTTP %d", resp.StatusCode)
		return ni
	}
	if err := json.NewDecoder(resp.Body).Decode(&ni); err != nil {
		ni.Error = err.Error()
		return ni
	}
	ni.NodeID, ni.Address, ni.Reachable, ni.Error = id, addr, true, ""
	return ni
}

func (h *APIHandler) peerLogin(base string) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"accessKey": h.cfg.Auth.AdminAccessKey,
		"secretKey": h.cfg.Auth.AdminSecretKey,
	})
	resp, err := clusterInfoClient.Post(base+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// A 403 usually means this address is not serving the dashboard API (e.g.
		// it points at the S3 port, or a split console_port); 401 means the admin
		// credentials differ between nodes.
		return "", fmt.Errorf("HTTP %d (peer may not serve /api/v1 at this address, or admin credentials differ)", resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", fmt.Errorf("login returned no token")
	}
	return out.Token, nil
}

func uniqueNonEmpty(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

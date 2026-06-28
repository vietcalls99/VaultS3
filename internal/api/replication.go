package api

import (
	"net/http"
	"strconv"
	"time"
)

type replicationPeerResponse struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	QueueDepth  int    `json:"queueDepth"`
	LastSync    string `json:"lastSync"`
	TotalSynced int64  `json:"totalSynced"`
	TotalFailed int64  `json:"totalFailed"`
	LastError   string `json:"lastError,omitempty"`
}

type replicationStatusResponse struct {
	Enabled bool                      `json:"enabled"`
	Peers   []replicationPeerResponse `json:"peers"`
}

type replicationEventResponse struct {
	ID         uint64 `json:"id"`
	Type       string `json:"type"`
	Bucket     string `json:"bucket"`
	Key        string `json:"key"`
	Peer       string `json:"peer"`
	Size       int64  `json:"size"`
	RetryCount int    `json:"retryCount"`
	NextRetry  string `json:"nextRetry"`
	CreatedAt  string `json:"createdAt"`
}

func (h *APIHandler) handleReplicationStatus(w http.ResponseWriter, r *http.Request) {
	statuses, err := h.store.GetReplicationStatuses()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// List the configured peers (the ones the worker actually replicates to) and
	// enrich each with its live status. Deriving the list from status records
	// instead hid freshly-configured peers that hadn't synced anything yet, so the
	// dashboard showed "No replication peers configured" despite peers=N (issue #10).
	peers := make([]replicationPeerResponse, 0, len(h.cfg.Replication.Peers))
	for _, p := range h.cfg.Replication.Peers {
		resp := replicationPeerResponse{Name: p.Name, URL: p.URL}
		for _, s := range statuses {
			if s.Peer != p.Name {
				continue
			}
			if s.LastSyncTime > 0 {
				resp.LastSync = time.Unix(s.LastSyncTime, 0).UTC().Format(time.RFC3339)
			}
			resp.QueueDepth = s.QueueDepth
			resp.TotalSynced = s.TotalSynced
			resp.TotalFailed = s.TotalFailed
			resp.LastError = s.LastError
			break
		}
		peers = append(peers, resp)
	}

	writeJSON(w, http.StatusOK, replicationStatusResponse{
		Enabled: h.cfg.Replication.Enabled,
		Peers:   peers,
	})
}

func (h *APIHandler) handleReplicationQueue(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > 1000 {
		limit = 1000
	}

	events, err := h.store.ListReplicationQueue(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	items := make([]replicationEventResponse, 0, len(events))
	for _, e := range events {
		nextRetry := ""
		if e.NextRetryAt > 0 {
			nextRetry = time.Unix(e.NextRetryAt, 0).UTC().Format(time.RFC3339)
		}
		createdAt := ""
		if e.CreatedAt > 0 {
			createdAt = time.Unix(e.CreatedAt, 0).UTC().Format(time.RFC3339)
		}
		items = append(items, replicationEventResponse{
			ID:         e.ID,
			Type:       e.Type,
			Bucket:     e.Bucket,
			Key:        e.Key,
			Peer:       e.Peer,
			Size:       e.Size,
			RetryCount: e.RetryCount,
			NextRetry:  nextRetry,
			CreatedAt:  createdAt,
		})
	}

	writeJSON(w, http.StatusOK, items)
}

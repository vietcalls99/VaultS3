package api

import (
	"context"
	"net/http"
	"time"

	"github.com/Kodiqa-Solutions/VaultS3/internal/vector"
)

// handleVectorQuery handles POST /api/v1/vectors/query — semantic / RAG search.
//
// Request body: {"query": "...", "topK": 10, "bucket": ""}
// Response:     {"results": [{"bucket","key","score"}, ...]}
func (h *APIHandler) handleVectorQuery(w http.ResponseWriter, r *http.Request) {
	if h.vectorMgr == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "vector search is not enabled",
		})
		return
	}

	var req struct {
		Query  string `json:"query"`
		TopK   int    `json:"topK"`
		Bucket string `json:"bucket"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}
	if req.TopK <= 0 {
		req.TopK = 10
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	results, err := h.vectorMgr.Query(ctx, req.Query, req.TopK, req.Bucket)
	if err != nil {
		writeError(w, http.StatusBadGateway, "vector query failed: "+err.Error())
		return
	}
	if results == nil {
		results = []vector.Match{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"results": results})
}

// handleVectorStatus handles GET /api/v1/vectors/status — reports whether vector
// search is enabled and how many vectors are indexed.
func (h *APIHandler) handleVectorStatus(w http.ResponseWriter, r *http.Request) {
	if h.vectorMgr == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"enabled": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"enabled": true,
		"vectors": h.vectorMgr.Count(),
	})
}

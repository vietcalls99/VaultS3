package api

import "net/http"

// handleVersion handles GET /api/v1/version — returns the running version and,
// if the update checker is enabled, whether a newer release is available.
func (h *APIHandler) handleVersion(w http.ResponseWriter, _ *http.Request) {
	if h.updater == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"current": "", "updateAvailable": false})
		return
	}
	writeJSON(w, http.StatusOK, h.updater.LastStatus())
}

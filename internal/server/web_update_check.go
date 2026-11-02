package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path/filepath"

	"404-probe/internal/server/updatecheck"
)

func (a *App) initializeUpdateCheck() {
	path := ""
	if dir := a.store.StateDirectory(); dir != "" {
		path = filepath.Join(dir, "update-check.json")
	}
	a.updateCheck = updatecheck.New(path, a.buildInfo, a.logger)
}

func (a *App) handlePutUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if r.URL.EscapedPath() != "/api/v1/web/settings/update-check" || r.URL.RawQuery != "" {
		writeJobError(w, 400, "invalid_request", "invalid settings request")
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, 415, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	body, err := readBoundedBody(w, r, maxWebAgentRenameBytes)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	var request struct {
		Enabled *bool `json:"enabled"`
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if d.Decode(&request) != nil || request.Enabled == nil || ensureJSONEOF(d) != nil {
		writeJobError(w, 400, "invalid_request", "enabled must be a boolean")
		return
	}
	state, err := a.updateCheck.SetEnabled(*request.Enabled)
	if err != nil {
		a.logger.Warn("save update check settings failed")
		writeJobError(w, 500, "internal_error", "could not save update check settings")
		return
	}
	writeJSON(w, 200, state)
}

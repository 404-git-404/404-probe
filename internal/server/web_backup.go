package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"404-probe/internal/backup"
)

const backupSettingsPath = "/api/v1/web/settings/backup"

func WithOffsiteBackup(manager *backup.Manager) Option {
	return func(app *App) error {
		app.offsiteBackup = manager
		return nil
	}
}

func (a *App) BackupLoop() {
	if a.offsiteBackup != nil {
		a.offsiteBackup.Run(a.shutdown)
	}
}

func (a *App) backupRoutes(mux *http.ServeMux) {
	mux.Handle("GET "+backupSettingsPath, a.requireWebSession(http.HandlerFunc(a.handleBackup), true))
	mux.Handle("PUT "+backupSettingsPath, a.requireWebMutation(http.HandlerFunc(a.handleBackup)))
	mux.Handle("POST "+backupSettingsPath+"/test", a.requireWebMutation(http.HandlerFunc(a.handleBackup)))
	mux.Handle("POST "+backupSettingsPath+"/run", a.requireWebMutation(http.HandlerFunc(a.handleBackup)))
}

func (a *App) handleBackup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if a.offsiteBackup == nil {
		writeJobError(w, 503, "backup_unavailable", "backup requires an on-disk Server database")
		return
	}
	if r.URL.RawQuery != "" || r.URL.EscapedPath() != r.URL.Path {
		writeJobError(w, 400, "invalid_request", "invalid backup request")
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, 200, a.offsiteBackup.Status())
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, 415, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	body, err := readBoundedBody(w, r, 8<<10)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var result any
	status := http.StatusOK
	switch r.URL.Path {
	case backupSettingsPath:
		var edit *backup.Edit
		if decoder.Decode(&edit) != nil || edit == nil || ensureJSONEOF(decoder) != nil {
			writeJobError(w, 400, "invalid_request", "invalid backup settings")
			return
		}
		result, err = a.offsiteBackup.Save(r.Context(), *edit)
	case backupSettingsPath + "/test":
		var config *backup.R2Config
		if decoder.Decode(&config) != nil || config == nil || ensureJSONEOF(decoder) != nil {
			writeJobError(w, 400, "invalid_request", "invalid R2 settings")
			return
		}
		err = a.offsiteBackup.Test(r.Context(), *config)
		result = struct{}{}
	case backupSettingsPath + "/run":
		var request *struct{}
		if decoder.Decode(&request) != nil || request == nil || ensureJSONEOF(decoder) != nil {
			writeJobError(w, 400, "invalid_request", "invalid backup request")
			return
		}
		result, err = a.offsiteBackup.Start()
		status = http.StatusAccepted
	}
	if err != nil {
		status = http.StatusBadRequest
		if errors.Is(err, backup.ErrBusy) {
			status = http.StatusConflict
		}
		writeJobError(w, status, "backup_error", err.Error())
		return
	}
	writeJSON(w, status, result)
}

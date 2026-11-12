package server

import (
	"errors"
	"net/http"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

var youtubeAdFreeRegions = [...]string{"AL", "MD", "AD", "SM", "MO", "KH", "LA", "MV", "MN", "CI", "MG", "ET", "RU", "MM", "CN"}

type webYouTubeRegion struct {
	Region string `json:"region"`
}

func (a *App) handleWebYouTubeRegion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agent_id")
	if !validWebAgentID(id) || r.URL.EscapedPath() != webAgentPathPrefix+id+"/youtube-region" || r.URL.RawQuery != "" {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid YouTube region request")
		return
	}

	var value webYouTubeRegion
	var err error
	if r.Method == http.MethodPut {
		if !hasJSONContentType(r) {
			writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
			return
		}
		body, bodyErr := readBoundedBody(w, r, 256)
		if bodyErr != nil {
			writeBodyError(w, bodyErr)
			return
		}
		if decodeStrictJSON(body, &value) != nil || !protocol.ValidCountryCode(value.Region) {
			writeJobError(w, http.StatusBadRequest, "invalid_region", "region must be a supported uppercase ISO alpha-2 code")
			return
		}
		err = a.store.SetYouTubeManualRegion(r.Context(), id, value.Region, a.now())
	} else {
		body, bodyErr := readBoundedBody(w, r, 256)
		if bodyErr != nil {
			writeBodyError(w, bodyErr)
			return
		}
		if len(body) != 0 {
			writeJobError(w, http.StatusBadRequest, "invalid_request", "request body must be empty")
			return
		}
		err = a.store.ClearYouTubeManualRegion(r.Context(), id)
	}
	if errors.Is(err, storage.ErrAgentNotFound) {
		writeJobError(w, http.StatusNotFound, "agent_not_found", "agent not found")
		return
	}
	if err != nil {
		a.logger.Error("save Web YouTube region", "agent_id", id, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not save YouTube region")
		return
	}
	a.publishDashboardChange(id, "youtube-region", false)
	writeJSON(w, http.StatusOK, value)
}

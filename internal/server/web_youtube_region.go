package server

import (
	"errors"
	"net/http"
	"slices"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

type webYouTubeRegion struct {
	Region string `json:"region"`
}

type webYouTubeAdFreeRegions struct {
	Regions  []string `json:"regions"`
	Defaults []string `json:"defaults"`
}

func (a *App) handleYouTubeAdFreeRegions(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid YouTube regions request")
		return
	}
	if r.Method == http.MethodGet {
		regions, err := a.store.YouTubeAdFreeRegions(r.Context())
		if err != nil {
			writeJobError(w, http.StatusInternalServerError, "internal_error", "could not read YouTube regions")
			return
		}
		writeJSON(w, http.StatusOK, webYouTubeAdFreeRegions{regions, storage.DefaultYouTubeAdFreeRegions()})
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	body, err := readBoundedBody(w, r, 16<<10)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	var request struct {
		Regions []string `json:"regions"`
	}
	if decodeStrictJSON(body, &request) != nil || request.Regions == nil || len(request.Regions) > 249 {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "regions must be an array of at most 249 country codes")
		return
	}
	for _, region := range request.Regions {
		if !protocol.ValidCountryCode(region) {
			writeJobError(w, http.StatusBadRequest, "invalid_region", "region must be a supported uppercase ISO alpha-2 code")
			return
		}
	}
	slices.Sort(request.Regions)
	regions := slices.Compact(request.Regions)
	changed, err := a.store.SetYouTubeAdFreeRegions(r.Context(), regions, a.now())
	if err != nil {
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not save YouTube regions")
		return
	}
	if changed {
		a.publishDashboardChange("", "youtube-ad-free-regions", false)
	}
	writeJSON(w, http.StatusOK, webYouTubeAdFreeRegions{regions, storage.DefaultYouTubeAdFreeRegions()})
}

func (a *App) handleWebYouTubeRegion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agent_id")
	if !validWebAgentID(id) || r.URL.EscapedPath() != webAgentPathPrefix+id+"/youtube-region" || r.URL.RawQuery != "" {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid YouTube region request")
		return
	}

	var value webYouTubeRegion
	var err error
	switch r.Method {
	case http.MethodPut:
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
	case http.MethodDelete:
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
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		writeJobError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be PUT or DELETE")
		return
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

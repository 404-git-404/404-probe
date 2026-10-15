package server

import (
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
	"encoding/json"
	"net/http"
)

func resolveQualityTargetV2(c protocol.QualityChoice, family string) (protocol.QualityTarget, error) {
	found := false
	for _, region := range qualityDirectory.Regions {
		if region.ID == c.Region {
			found = true
			break
		}
	}
	if !found {
		return protocol.QualityTarget{}, storage.ErrQualityInvalid
	}
	region := c.Region
	if c.Source == "manual" {
		c.Region = ""
	}
	target, err := resolveQualityTarget(c, family)
	target.Region = region
	return target, err
}
func (a *App) handleQualityConfigV2(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !qualityWebPath(r, "v2/config") || r.URL.RawQuery != "" {
		writeJobError(w, 400, "invalid_request", "invalid quality v2 path")
		return
	}
	var config protocol.QualityConfig
	var err error
	if r.Method == http.MethodGet {
		config, err = a.store.GetQualityConfigV2(r.Context(), r.PathValue("agent_id"))
	} else {
		if !hasJSONContentType(r) {
			writeJobError(w, 415, "invalid_content_type", "JSON required")
			return
		}
		body, readErr := readBoundedBody(w, r, 16384)
		if readErr != nil {
			writeBodyError(w, readErr)
			return
		}
		var request protocol.QualityV2ConfigUpdate
		if decodeAgentRemovalJSON(body, &request) != nil || request.Validate() != nil {
			writeJobError(w, 400, "invalid_request", "invalid quality v2 config JSON")
			return
		}
		config, err = a.store.UpdateQualityConfigV2(r.Context(), r.PathValue("agent_id"), request, resolveQualityTargetV2, a.now())
	}
	if err != nil {
		qualityHTTPError(w, err)
		return
	}
	wire, err := json.Marshal(config)
	if err != nil || len(wire)+1 > protocol.QualityConfigResponseLimit {
		writeJobError(w, 503, "retryable", "quality config response unavailable")
		return
	}
	writeJSON(w, 200, config)
}

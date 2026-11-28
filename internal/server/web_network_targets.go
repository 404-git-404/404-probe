package server

import (
	"errors"
	"net/http"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

const networkTargetsPath = "/api/v1/web/network-targets"

func (a *App) networkTargetRoutes(mux *http.ServeMux) {
	mux.Handle("GET "+networkTargetsPath, a.requireWebSession(http.HandlerFunc(a.handleNetworkTargets), true))
	mux.Handle("POST "+networkTargetsPath, a.requireWebMutation(http.HandlerFunc(a.handleNetworkTargets)))
	for _, route := range []struct{ method, suffix string }{
		{"PUT", "/{id}"}, {"DELETE", "/{id}"}, {"POST", "/{id}/apply"},
	} {
		mux.Handle(route.method+" "+networkTargetsPath+route.suffix, a.requireWebMutation(http.HandlerFunc(a.handleNetworkTargets)))
	}
}

func readNetworkTargetRequest(w http.ResponseWriter, r *http.Request, dst any) bool {
	if !hasJSONContentType(r) {
		writeJobError(w, 415, "invalid_content_type", "JSON required")
		return false
	}
	body, err := readBoundedBody(w, r, 64<<10)
	if err != nil {
		writeBodyError(w, err)
		return false
	}
	if protocol.CheckQualityRequestEncoding(body) != nil || decodeAgentRemovalJSON(body, dst) != nil {
		writeQualityFieldErrors(w, nil)
		return false
	}
	return true
}

func (a *App) validateNetworkTarget(w http.ResponseWriter, r *http.Request, fields *storage.NetworkTargetFields) bool {
	var fieldErrors []protocol.QualityFieldError
	add := func(field, code string) {
		fieldErrors = append(fieldErrors, protocol.QualityFieldError{Field: field, Code: code})
	}
	for _, label := range []struct{ field, value string }{{"region", fields.Region}, {"isp", fields.ISP}} {
		if label.value == "" {
			add(label.field, protocol.QualityErrLabelRequired)
		} else if !protocol.ValidQualityLabel(label.value) {
			add(label.field, protocol.QualityErrInvalidLabel)
		}
	}
	host, literal, code := protocol.NormalizeQualityHost(fields.Host)
	if code != "" {
		add("host", code)
	} else if host == "" {
		add("host", protocol.QualityErrInvalidRequest)
	}
	if fields.Port < 1 || fields.Port > 65535 {
		add("port", protocol.QualityErrInvalidPort)
	}
	if len(fieldErrors) == 0 && literal == "" {
		unsafe, err := a.checkQualityNames(r.Context(), []string{host})
		if err != nil {
			writeJobError(w, 503, "retryable", "address check is busy, try again")
			return false
		}
		if len(unsafe) != 0 {
			add("host", protocol.QualityErrUnsafeAddress)
		}
	}
	if len(fieldErrors) != 0 {
		writeQualityFieldErrors(w, fieldErrors)
		return false
	}
	fields.Host = host
	return true
}

func networkTargetHTTPError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrNetworkTargetNotFound):
		writeJobError(w, 404, "not_found", "network target not found")
	case errors.Is(err, storage.ErrNetworkTargetDuplicate):
		writeJobError(w, 409, "duplicate_target", "network target already exists")
	case errors.Is(err, storage.ErrNetworkTargetLimit):
		writeJobError(w, 409, "target_limit", "network target limit reached")
	case errors.Is(err, storage.ErrNetworkTargetConflict):
		writeJobError(w, 409, "revision_conflict", "network target revision changed")
	default:
		qualityHTTPError(w, err)
	}
}

func (a *App) handleNetworkTargets(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("id")
	if id != "" && !validLowerHexID(id, 32) {
		writeJobError(w, 404, "not_found", "network target not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		targets, err := a.store.ListNetworkTargets(r.Context())
		if err != nil {
			networkTargetHTTPError(w, err)
			return
		}
		writeJSON(w, 200, struct {
			Targets []storage.NetworkTarget `json:"targets"`
		}{targets})
	case http.MethodPost:
		if id != "" {
			a.handleApplyNetworkTarget(w, r, id)
			return
		}
		var fields storage.NetworkTargetFields
		if !readNetworkTargetRequest(w, r, &fields) || !a.validateNetworkTarget(w, r, &fields) {
			return
		}
		target, err := a.store.CreateNetworkTarget(r.Context(), fields)
		if err != nil {
			networkTargetHTTPError(w, err)
			return
		}
		writeJSON(w, 201, struct {
			Target storage.NetworkTarget `json:"target"`
		}{target})
	case http.MethodPut:
		var input struct {
			ExpectedRevision int64 `json:"expected_revision"`
			storage.NetworkTargetFields
		}
		if !readNetworkTargetRequest(w, r, &input) || !validNetworkTargetRevision(w, input.ExpectedRevision) ||
			!a.validateNetworkTarget(w, r, &input.NetworkTargetFields) {
			return
		}
		out, err := a.store.UpdateNetworkTarget(r.Context(), id, input.ExpectedRevision, input.NetworkTargetFields, a.now())
		if err != nil {
			networkTargetHTTPError(w, err)
			return
		}
		if out.UpdatedAgents > 0 {
			for _, agent := range out.Target.UsedBy {
				a.publishDashboardChange(agent.ID, "quality", false)
			}
		}
		writeJSON(w, 200, out)
	case http.MethodDelete:
		var input struct {
			ExpectedRevision int64 `json:"expected_revision"`
		}
		if !readNetworkTargetRequest(w, r, &input) || !validNetworkTargetRevision(w, input.ExpectedRevision) {
			return
		}
		agents, err := a.store.DeleteNetworkTarget(r.Context(), id, input.ExpectedRevision, a.now())
		if err != nil {
			networkTargetHTTPError(w, err)
			return
		}
		for _, agent := range agents {
			a.publishDashboardChange(agent.ID, "quality", false)
		}
		writeJSON(w, 200, struct {
			RemovedFrom int `json:"removed_from"`
		}{len(agents)})
	}
}

func validNetworkTargetRevision(w http.ResponseWriter, revision int64) bool {
	if revision < 1 {
		writeQualityFieldErrors(w, []protocol.QualityFieldError{{Field: "expected_revision", Code: protocol.QualityErrInvalidRequest}})
		return false
	}
	return true
}

func (a *App) handleApplyNetworkTarget(w http.ResponseWriter, r *http.Request, id string) {
	var input struct {
		AgentIDs []string `json:"agent_ids"`
	}
	if !readNetworkTargetRequest(w, r, &input) {
		return
	}
	if input.AgentIDs == nil {
		writeQualityFieldErrors(w, []protocol.QualityFieldError{{Field: "agent_ids", Code: protocol.QualityErrInvalidRequest}})
		return
	}
	out, err := a.store.ApplyNetworkTarget(r.Context(), id, input.AgentIDs, a.now())
	if err != nil {
		networkTargetHTTPError(w, err)
		return
	}
	for _, agentID := range out.Added {
		a.publishDashboardChange(agentID, "quality", false)
	}
	writeJSON(w, 200, out)
}

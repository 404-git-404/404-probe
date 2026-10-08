package server

import (
	"404-probe/internal/protocol"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

var errQualityResponseBudget = errors.New("quality config exceeds complete report response budget")

func (a *App) handleAgentQualityConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r, cancel := qualityHTTPBudget(w, r)
	defer cancel()
	id, ok := a.authenticateJobRequest(w, r, "quality config")
	if !ok {
		return
	}
	if r.URL.EscapedPath() != "/api/v1/agent/network-quality/config" || r.URL.RawQuery != "" {
		writeJobError(w, 400, "invalid_request", "invalid quality config path")
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, 415, "invalid_content_type", "JSON required")
		return
	}
	body, err := readBoundedBody(w, r, protocol.QualityConfigRequestLimit)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	var request protocol.QualityConfigRequest
	if err := decodeAgentRemovalJSON(body, &request); err != nil || request.Validate() != nil {
		writeJobError(w, 400, "invalid_request", "invalid quality config request")
		return
	}
	epoch, _ := protocol.QualityInteger(request.Epoch)
	config, err := a.store.GetQualitySessionConfig(r.Context(), id, uint64(epoch), request.SessionID)
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

func (a *App) cleanupQuality() {
	ctx, cancel := context.WithTimeout(a.shutdown, 10*time.Second)
	defer cancel()
	for i := 0; i < 32; i++ {
		_, more, err := a.store.CleanupQualityBatch(ctx, a.now())
		if err != nil {
			if a.shutdown.Err() == nil {
				a.logger.Error("quality cleanup", "error", err)
			}
			return
		}
		if !more {
			return
		}
	}
}

func (a *App) handleAgentQuality(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r, cancel := qualityHTTPBudget(w, r)
	defer cancel()
	ctx := r.Context()
	id, ok := a.authenticateJobRequest(w, r, "quality samples")
	if !ok {
		return
	}
	if r.URL.EscapedPath() != "/api/v1/agent/network-quality" || r.URL.RawQuery != "" {
		writeJobError(w, 400, "invalid_request", "invalid quality path")
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, 415, "invalid_content_type", "JSON required")
		return
	}
	body, err := readBoundedBody(w, r, protocol.QualityBodyLimit)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	var batch protocol.QualityBatch
	if err := decodeAgentRemovalJSON(body, &batch); err != nil || batch.Validate() != nil {
		writeJobError(w, 400, "invalid_request", "invalid quality batch")
		return
	}
	result, err := a.store.SaveQualityBatch(ctx, id, batch, a.now())
	if err != nil {
		qualityHTTPError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

// Standard transport deadlines interrupt body reads; context alone cannot stop io.ReadAll.
// In-memory ResponseRecorders have no transport, so only their context is bounded.
func qualityHTTPBudget(w http.ResponseWriter, r *http.Request) (*http.Request, func()) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	deadline, _ := ctx.Deadline()
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(deadline)
	_ = controller.SetWriteDeadline(deadline)
	return r.WithContext(ctx), func() {
		expired := ctx.Err() != nil
		cancel()
		if !expired {
			_ = controller.SetReadDeadline(time.Time{})
			_ = controller.SetWriteDeadline(time.Time{})
		}
	}
}

func qualityBudgetHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r, cancel := qualityHTTPBudget(w, r)
		defer cancel()
		next.ServeHTTP(w, r)
	})
}

// Resource report has already committed: quality failure must not undo its ACK.
func (a *App) qualityReportResponse(r *http.Request, response protocol.ReportResponse, id string, report protocol.Report) protocol.ReportResponse {
	if !response.Accepted || !report.NetworkQuality {
		return response
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	config, err := a.store.GetQualitySessionConfig(ctx, id, report.Epoch, report.SessionID)
	if err == nil && config.Supported {
		response.NetworkQuality = &config
		wire, encodeErr := json.Marshal(response)
		if encodeErr != nil || len(wire)+1 > protocol.QualityResponseLimit {
			err = errQualityResponseBudget
			response.NetworkQuality = nil
		}
	}
	if err != nil {
		a.logger.Error("omit quality config after accepted report", "agent_id", id, "error", err)
	}
	return response
}

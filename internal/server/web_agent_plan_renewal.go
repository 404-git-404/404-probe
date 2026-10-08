package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"404-probe/internal/storage"
)

const maxWebPlanRenewalBytes = 2 << 10

type webPlanRenewalPreviewRequest struct {
	Field *string `json:"field"`
}
type webPlanRenewalApplyRequest struct {
	RequestID        string `json:"request_id"`
	ExpectedRevision string `json:"expected_revision"`
	Field            string `json:"field"`
	FromDate         string `json:"from_date"`
	ToDate           string `json:"to_date"`
	NewOverdue       *bool  `json:"new_overdue"`
	DueToday         *bool  `json:"due_today"`
}

// Reject duplicate keys as well as unknown fields, null, arrays and trailing data.
// The body was bounded before parsing; only these small flat DTOs are accepted.
func decodePlanRenewalJSON(body []byte, target any) error {
	allowed := map[string]bool{}
	var fields []string
	switch target.(type) {
	case *webPlanRenewalPreviewRequest:
		fields = []string{"field"}
	case *webPlanRenewalApplyRequest:
		fields = []string{"request_id", "expected_revision", "field", "from_date", "to_date", "new_overdue", "due_today"}
	case *storage.PlanRenewalUndo:
		fields = []string{"request_id", "operation_id", "expected_revision"}
	default:
		return errors.New("unsupported renewal DTO")
	}
	for _, field := range fields {
		allowed[field] = true
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("request must be an object")
	}
	seen := map[string]bool{}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := key.(string)
		if !ok || seen[name] || !allowed[name] {
			return errors.New("unknown or duplicate request field")
		}
		seen[name] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("null request field")
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return err
	}
	decoder = json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}
func validPlanRenewalPath(r *http.Request, action string) bool {
	id := r.PathValue("agent_id")
	return validWebAgentID(id) && r.URL.EscapedPath() == webAgentPathPrefix+id+"/plan/renewal/"+action && r.URL.RawQuery == "" && !r.URL.ForceQuery
}
func (a *App) handleWebPlanRenewal(w http.ResponseWriter, r *http.Request, action string) {
	if !validPlanRenewalPath(r, action) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid renewal path or query")
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	body, err := readBoundedBody(w, r, maxWebPlanRenewalBytes)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	id := r.PathValue("agent_id")
	if action == "preview" {
		var q webPlanRenewalPreviewRequest
		if err := decodePlanRenewalJSON(body, &q); err != nil {
			writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid renewal preview")
			return
		}
		field := ""
		if q.Field != nil {
			field = *q.Field
		}
		preview, err := a.store.PreviewPlanRenewal(r.Context(), id, field, a.now())
		if err != nil {
			a.writePlanRenewalError(w, id, err)
			return
		}
		writeJSON(w, http.StatusOK, preview)
		return
	}
	var result storage.PlanRenewalResult
	if action == "apply" {
		var q webPlanRenewalApplyRequest
		if err := decodePlanRenewalJSON(body, &q); err != nil || q.NewOverdue == nil || q.DueToday == nil || !validLowerHexID(q.RequestID, 32) || !validLowerHexID(q.ExpectedRevision, 32) {
			writeJobError(w, http.StatusBadRequest, "invalid_request", "renewal confirmation requires all fields and both warning flags")
			return
		}
		result, err = a.store.ApplyPlanRenewal(r.Context(), id, storage.PlanRenewalApply{RequestID: q.RequestID, ExpectedRevision: q.ExpectedRevision, Field: q.Field, FromDate: q.FromDate, ToDate: q.ToDate, NewOverdue: *q.NewOverdue, DueToday: *q.DueToday}, a.now())
	} else {
		var q storage.PlanRenewalUndo
		if err := decodePlanRenewalJSON(body, &q); err != nil || !validLowerHexID(q.RequestID, 32) || !validLowerHexID(q.ExpectedRevision, 32) || !validLowerHexID(q.OperationID, 32) {
			writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid renewal undo")
			return
		}
		result, err = a.store.UndoPlanRenewal(r.Context(), id, q, a.now())
	}
	if err != nil {
		a.writePlanRenewalError(w, id, err)
		return
	}
	// The receipt/date transaction has committed. A failed best-effort notification
	// must not turn an applied operation into an apparent failed write.
	if !result.Replayed {
		if err := a.publishAgentDetail(r.Context(), id); err != nil {
			a.logger.Error("publish committed plan renewal", "agent_id", id, "error", err)
		}
	}
	writeJSON(w, http.StatusOK, result)
}
func (a *App) handleWebPlanRenewalMethod(w http.ResponseWriter, r *http.Request, action string) {
	if !validPlanRenewalPath(r, action) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid renewal path or query")
		return
	}
	w.Header().Set("Allow", http.MethodPost)
	writeJobError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be POST")
}
func (a *App) writePlanRenewalError(w http.ResponseWriter, id string, err error) {
	if errors.Is(err, storage.ErrAgentNotFound) {
		writeJobError(w, http.StatusNotFound, "agent_not_found", "agent not found")
		return
	}
	if errors.Is(err, storage.ErrAgentRemovalPending) {
		writeJobError(w, http.StatusConflict, "removal_pending", "agent removal is pending")
		return
	}
	var renewal *storage.PlanRenewalError
	if errors.As(err, &renewal) {
		status := http.StatusConflict
		switch renewal.Code {
		case "invalid_request", "invalid_field", "invalid_date", "invalid_anchor", "invalid_timezone", "invalid_period", "date_out_of_range":
			status = http.StatusBadRequest
		case "receipt_capacity":
			status = http.StatusTooManyRequests
			w.Header().Set("Retry-After", strconv.FormatInt(renewal.RetryAfter, 10))
		}
		message := map[string]string{
			"revision_conflict": "套餐已改变，请重新预览后确认。", "confirmation_changed": "确认的日期与服务端预览不一致，请重新预览。",
			"preview_warning_changed": "到期提示已改变，请重新预览后确认。", "request_id_conflict": "请求 ID 已用于其他操作；请核对原操作。",
			"undo_not_latest": "只有最后一次且未被其他编辑改变的续费可以撤销。", "undo_expired": "撤销窗口已结束。",
			"receipt_capacity": "操作收据已满，请稍后再试。", "plan_missing": "套餐已清除，请先编辑套餐。",
			"date_missing": "未设置选中日期，请编辑套餐。", "period_missing": "未设置续费价格周期，请编辑套餐。", "one_time": "一次性套餐不能按周期续费，请编辑套餐。",
		}[renewal.Code]
		if message == "" {
			message = "invalid renewal request"
		}
		writeJobError(w, status, renewal.Code, message)
		return
	}
	a.logger.Error("plan renewal transaction", "agent_id", id, "error", err)
	writeJobError(w, http.StatusInternalServerError, "internal_error", "无法确认操作结果；请保留原请求 ID，使用同一确认内容复核。")
}

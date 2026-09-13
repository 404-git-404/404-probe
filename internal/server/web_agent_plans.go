package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"time"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

const maxWebAgentPlanBytes = 16 << 10

var (
	decimalPlanValue = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})(\.[0-9]{1,3})?$`)
	currencyCode     = regexp.MustCompile(`^[A-Z]{3}$`)
)

type webAgentPlanRequest struct {
	TrafficMode         string `json:"traffic_mode"`
	QuotaValue          string `json:"quota_value"`
	QuotaUnit           string `json:"quota_unit"`
	CycleKind           string `json:"cycle_kind"`
	CycleCount          int    `json:"cycle_count"`
	CycleAnchor         string `json:"cycle_anchor"`
	Timezone            string `json:"timezone"`
	CalibrationValue    string `json:"calibration_value"`
	CalibrationUnit     string `json:"calibration_unit"`
	BandwidthValue      string `json:"bandwidth_value"`
	BandwidthUnit       string `json:"bandwidth_unit"`
	Currency            string `json:"currency"`
	PurchasePrice       string `json:"purchase_price"`
	PurchasePricePeriod string `json:"purchase_price_period"`
	RenewalPrice        string `json:"renewal_price"`
	RenewalPricePeriod  string `json:"renewal_price_period"`
	PurchaseDate        string `json:"purchase_date"`
	RenewalDate         string `json:"renewal_date"`
	ExpiryDate          string `json:"expiry_date"`
	CountryCodeOverride string `json:"country_code_override"`
}

func (a *App) handleGetWebAgentPlan(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	if !validWebAgentPlanRequestPath(r, agentID) {
		http.NotFound(w, r)
		return
	}
	plan, exists, err := a.store.GetAgentPlan(r.Context(), agentID)
	if err != nil {
		a.logger.Error("read Web agent plan", "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not read plan")
		return
	}
	if !exists {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	plan, err = storage.AgentPlanAt(plan, a.now())
	if err != nil {
		a.logger.Error("calculate Web agent plan", "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not read plan")
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (a *App) handlePutWebAgentPlan(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	if !validWebAgentPlanRequestPath(r, agentID) {
		http.NotFound(w, r)
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	body, err := readBoundedBody(w, r, maxWebAgentPlanBytes)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var request *webAgentPlanRequest
	if err := decoder.Decode(&request); err != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid plan")
		return
	}
	if request == nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "plan must be a JSON object")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid plan")
		return
	}
	if request.empty() {
		if _, err := a.store.DeleteAgentPlan(r.Context(), agentID); err != nil {
			a.logger.Error("delete Web agent plan", "agent_id", agentID, "error", err)
			writeJobError(w, http.StatusInternalServerError, "internal_error", "could not clear plan")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	plan, calibration, err := request.plan(agentID)
	if err != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	saved, err := a.store.PutAgentPlan(r.Context(), plan, calibration, a.now())
	if err != nil {
		if errors.Is(err, storage.ErrAgentNotFound) {
			writeJobError(w, http.StatusNotFound, "agent_not_found", "active agent not found")
			return
		}
		a.logger.Error("save Web agent plan", "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not save plan")
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (a *App) handleWebAgentPlanMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	if !validWebAgentPlanRequestPath(r, r.PathValue("agent_id")) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Allow", "GET, PUT")
	writeJobError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func validWebAgentPlanRequestPath(r *http.Request, agentID string) bool {
	return validWebAgentID(agentID) && r.URL.EscapedPath() == webAgentPathPrefix+agentID+"/plan"
}

func (request webAgentPlanRequest) empty() bool {
	return request == (webAgentPlanRequest{})
}

func (request webAgentPlanRequest) plan(agentID string) (storage.AgentPlan, *uint64, error) {
	plan := storage.AgentPlan{
		AgentID: agentID, TrafficMode: request.TrafficMode, QuotaValue: request.QuotaValue, QuotaUnit: request.QuotaUnit,
		CycleKind: request.CycleKind, CycleCount: request.CycleCount, CycleAnchor: request.CycleAnchor, Timezone: request.Timezone,
		BandwidthValue: request.BandwidthValue, BandwidthUnit: request.BandwidthUnit, Currency: request.Currency,
		PurchasePrice: request.PurchasePrice, PurchasePricePeriod: request.PurchasePricePeriod,
		RenewalPrice: request.RenewalPrice, RenewalPricePeriod: request.RenewalPricePeriod,
		PurchaseDate: request.PurchaseDate, RenewalDate: request.RenewalDate, ExpiryDate: request.ExpiryDate, CountryCodeOverride: request.CountryCodeOverride,
	}
	if request.CountryCodeOverride != "" && !protocol.ValidCountryCode(request.CountryCodeOverride) {
		return storage.AgentPlan{}, nil, errors.New("country override must be a supported uppercase ISO alpha-2 code")
	}
	if request.TrafficMode != "" && request.TrafficMode != "rx" && request.TrafficMode != "tx" && request.TrafficMode != "sum" {
		return storage.AgentPlan{}, nil, errors.New("traffic mode must be RX, TX, or RX+TX")
	}
	if (request.QuotaValue == "") != (request.QuotaUnit == "") {
		return storage.AgentPlan{}, nil, errors.New("traffic quota value and unit must be provided together")
	}
	if request.QuotaValue != "" {
		value, err := planQuantity(request.QuotaValue, request.QuotaUnit, map[string]uint64{"GB": 1_000_000_000, "TB": 1_000_000_000_000, "GiB": 1 << 30, "TiB": 1 << 40}, false)
		if err != nil || request.TrafficMode == "" {
			return storage.AgentPlan{}, nil, errors.New("traffic quota requires a positive value, explicit unit, and billing direction")
		}
		plan.QuotaBytes = &value
	}
	if request.TrafficMode != "" {
		if request.CycleKind != "monthly" && request.CycleKind != "yearly" && request.CycleKind != "days" {
			return storage.AgentPlan{}, nil, errors.New("traffic tracking requires a monthly, yearly, or day billing cycle")
		}
		if request.CycleCount < 1 || request.CycleCount > 120 {
			return storage.AgentPlan{}, nil, errors.New("billing cycle count is outside the supported range")
		}
		if _, err := time.Parse("2006-01-02", request.CycleAnchor); err != nil {
			return storage.AgentPlan{}, nil, errors.New("billing cycle start must be a calendar date")
		}
		if _, err := time.LoadLocation(request.Timezone); err != nil {
			return storage.AgentPlan{}, nil, errors.New("billing timezone must be an IANA timezone")
		}
	} else if request.CycleKind != "" || request.CycleCount != 0 || request.CycleAnchor != "" || request.CalibrationValue != "" || request.CalibrationUnit != "" {
		return storage.AgentPlan{}, nil, errors.New("billing cycle and calibration require a traffic direction")
	}
	if request.Timezone != "" {
		if _, err := time.LoadLocation(request.Timezone); err != nil {
			return storage.AgentPlan{}, nil, errors.New("plan timezone must be an IANA timezone")
		}
	} else if request.TrafficMode != "" {
		return storage.AgentPlan{}, nil, errors.New("traffic tracking requires a billing timezone")
	}
	var calibration *uint64
	if (request.CalibrationValue == "") != (request.CalibrationUnit == "") {
		return storage.AgentPlan{}, nil, errors.New("traffic calibration value and unit must be provided together")
	}
	if request.CalibrationValue != "" {
		value, err := planQuantity(request.CalibrationValue, request.CalibrationUnit, map[string]uint64{"GB": 1_000_000_000, "TB": 1_000_000_000_000, "GiB": 1 << 30, "TiB": 1 << 40}, true)
		if err != nil {
			return storage.AgentPlan{}, nil, errors.New("traffic calibration is invalid")
		}
		calibration = &value
	}
	if (request.BandwidthValue == "") != (request.BandwidthUnit == "") {
		return storage.AgentPlan{}, nil, errors.New("bandwidth value and unit must be provided together")
	}
	if request.BandwidthValue != "" {
		value, err := planQuantity(request.BandwidthValue, request.BandwidthUnit, map[string]uint64{"Mbps": 1_000_000, "Gbps": 1_000_000_000}, false)
		if err != nil {
			return storage.AgentPlan{}, nil, errors.New("bandwidth is invalid")
		}
		plan.BandwidthBPS = &value
	}
	if request.Currency != "" && !currencyCode.MatchString(request.Currency) {
		return storage.AgentPlan{}, nil, errors.New("currency must be a three-letter uppercase code")
	}
	for _, price := range []string{request.PurchasePrice, request.RenewalPrice} {
		if price != "" && !decimalPlanValue.MatchString(price) {
			return storage.AgentPlan{}, nil, errors.New("price must be a non-negative decimal with at most three fractional digits")
		}
	}
	if (request.PurchasePrice != "" || request.RenewalPrice != "") && request.Currency == "" {
		return storage.AgentPlan{}, nil, errors.New("prices require a currency")
	}
	if (request.PurchasePrice == "") != (request.PurchasePricePeriod == "") || (request.RenewalPrice == "") != (request.RenewalPricePeriod == "") {
		return storage.AgentPlan{}, nil, errors.New("each price requires its original price period")
	}
	for _, period := range []string{request.PurchasePricePeriod, request.RenewalPricePeriod} {
		if period != "" && period != "once" && period != "monthly" && period != "yearly" {
			return storage.AgentPlan{}, nil, errors.New("price period must be one-time, monthly, or yearly")
		}
	}
	for _, date := range []string{request.PurchaseDate, request.RenewalDate, request.ExpiryDate} {
		if date != "" {
			if _, err := time.Parse("2006-01-02", date); err != nil {
				return storage.AgentPlan{}, nil, errors.New("plan dates must use YYYY-MM-DD")
			}
		}
	}
	return plan, calibration, nil
}

func planQuantity(value, unit string, factors map[string]uint64, allowZero bool) (uint64, error) {
	factor, ok := factors[unit]
	if !ok || !decimalPlanValue.MatchString(value) {
		return 0, errors.New("invalid quantity")
	}
	parts := strings.SplitN(value, ".", 2)
	digits := parts[0]
	scale := int64(1)
	if len(parts) == 2 {
		digits += parts[1]
		for range parts[1] {
			scale *= 10
		}
	}
	numerator := new(big.Int)
	if _, ok := numerator.SetString(digits, 10); !ok {
		return 0, errors.New("invalid quantity")
	}
	numerator.Mul(numerator, new(big.Int).SetUint64(factor))
	denominator := big.NewInt(scale)
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, denominator, remainder)
	if new(big.Int).Lsh(remainder, 1).Cmp(denominator) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsUint64() || quotient.Uint64() > math.MaxInt64 || (!allowZero && quotient.Sign() == 0) {
		return 0, errors.New("quantity is outside the supported range")
	}
	return quotient.Uint64(), nil
}

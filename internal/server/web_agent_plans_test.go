package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func webPlanPut(t *testing.T, app *App, agentID, body string, csrf bool) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPut, "/api/v1/web/agents/"+agentID+"/plan", strings.NewReader(body))
	session := addTestWebSession(t, app, request)
	request.Header.Set("Origin", "https://probe.test")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("Content-Type", "application/json")
	if csrf {
		request.Header.Set("X-CSRF-Token", session.csrfToken)
	}
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	return response
}

func TestWebAgentPlanRequiresMutationProofAndPersistsExactUnits(t *testing.T) {
	app, store, agentID := newWebAuthenticationTestApp(t)
	defer store.Close()
	body := `{"traffic_mode":"sum","quota_value":"5","quota_unit":"TB","cycle_kind":"monthly","cycle_count":1,"cycle_anchor":"2026-01-31","timezone":"America/Los_Angeles","calibration_value":"1.25","calibration_unit":"TB","bandwidth_value":"10","bandwidth_unit":"Gbps","currency":"USD","purchase_price":"15","purchase_price_period":"once","renewal_price":"49","renewal_price_period":"yearly","purchase_date":"2026-01-31","renewal_date":"2027-01-31","expiry_date":"2027-02-01"}`
	if response := webPlanPut(t, app, agentID, body, false); response.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d body=%s", response.Code, response.Body.String())
	}
	response := webPlanPut(t, app, agentID, body, true)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"quota_value":"5"`) || !strings.Contains(response.Body.String(), `"quota_bytes":5000000000000`) || !strings.Contains(response.Body.String(), `"usage_bytes":1250000000000`) {
		t.Fatalf("save status=%d body=%s", response.Code, response.Body.String())
	}
	detail := webAgentResponse(t, app, http.MethodGet, "/api/v1/web/agents/"+agentID)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"renewal_price":"49"`) {
		t.Fatalf("detail status=%d body=%s", detail.Code, detail.Body.String())
	}
}

func TestWebAgentPlanRejectsAmbiguousQuantitiesAndSupportsClear(t *testing.T) {
	app, store, agentID := newWebAuthenticationTestApp(t)
	defer store.Close()
	invalid := `{"traffic_mode":"sum","quota_value":"5","quota_unit":"T","cycle_kind":"monthly","cycle_count":1,"cycle_anchor":"2026-01-01","timezone":"UTC"}`
	if response := webPlanPut(t, app, agentID, invalid, true); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid status=%d body=%s", response.Code, response.Body.String())
	}
	if response := webPlanPut(t, app, agentID, `{}`, true); response.Code != http.StatusNoContent {
		t.Fatalf("clear status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWebAgentPlanSupportsPriceOnlyWithoutTrafficOrBandwidthUnits(t *testing.T) {
	app, store, agentID := newWebAuthenticationTestApp(t)
	defer store.Close()
	body := `{"timezone":"America/Los_Angeles","currency":"USD","purchase_price":"12.50","purchase_price_period":"once","renewal_date":"2027-01-31"}`
	response := webPlanPut(t, app, agentID, body, true)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"purchase_price":"12.50"`) || !strings.Contains(response.Body.String(), `"timezone":"America/Los_Angeles"`) || strings.Contains(response.Body.String(), `"traffic_mode"`) || strings.Contains(response.Body.String(), `"bandwidth_unit"`) {
		t.Fatalf("save status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWebAgentPlanSupportsCountryOnlyAndRejectsInvalidCountry(t *testing.T) {
	app, store, agentID := newWebAuthenticationTestApp(t)
	defer store.Close()
	response := webPlanPut(t, app, agentID, `{"country_code_override":"JP"}`, true)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"country_code_override":"JP"`) {
		t.Fatalf("country-only status=%d body=%s", response.Code, response.Body.String())
	}
	for _, invalid := range []string{"jp", "J1", "JPN", "AA", "ZZ"} {
		response = webPlanPut(t, app, agentID, `{"country_code_override":"`+invalid+`"}`, true)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid country %q status=%d body=%s", invalid, response.Code, response.Body.String())
		}
	}
}

func TestWebAgentPlanRejectsNullWithoutClearingExistingPlan(t *testing.T) {
	app, store, agentID := newWebAuthenticationTestApp(t)
	defer store.Close()
	if response := webPlanPut(t, app, agentID, `{"currency":"USD","renewal_price":"9","renewal_price_period":"monthly"}`, true); response.Code != http.StatusOK {
		t.Fatalf("initial save status=%d body=%s", response.Code, response.Body.String())
	}
	if response := webPlanPut(t, app, agentID, `null`, true); response.Code != http.StatusBadRequest {
		t.Fatalf("null status=%d body=%s", response.Code, response.Body.String())
	}
	plan, exists, err := store.GetAgentPlan(context.Background(), agentID)
	if err != nil || !exists || plan.RenewalPrice != "9" {
		t.Fatalf("plan=%+v exists=%t err=%v", plan, exists, err)
	}
}

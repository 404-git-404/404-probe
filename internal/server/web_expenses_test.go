package server

import (
	"404-probe/internal/storage"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func expenseFixture(prices ...[3]string) storage.DashboardData {
	d := storage.DashboardData{Catalog: storage.OrganizationCatalog{Revision: 7, Rates: []storage.ExchangeRate{{Currency: "CNY", UnitsPerUSD: "7"}}}}
	for i, p := range prices {
		d.Records = append(d.Records, storage.DashboardRecord{Snapshot: storage.AgentSnapshot{Agent: storage.Agent{ID: fmt.Sprintf("%032x", i+1), Name: fmt.Sprintf("device-%d", i)}}, Plan: &storage.AgentPlan{RenewalPrice: p[0], RenewalPricePeriod: p[1], Currency: p[2]}})
	}
	return d
}
func TestExpenseExactPeriodsPrecisionAndDirection(t *testing.T) {
	for _, v := range []struct{ period, price, month, year string }{{"monthly", "1", "1.00", "12.00"}, {"quarterly", "100", "4.76", "57.14"}, {"semiannual", "6", "6.00", "72.00"}, {"yearly", "12", "12.00", "144.00"}, {"monthly", "0", "0.00", "0.00"}, {"quarterly", "0.001", "0.00", "0.00"}, {"yearly", "999999999999.999", "83333333333.33", "999999999999.999"}} {
		d := expenseFixture([3]string{v.price, v.period, "USD"})
		if v.period == "quarterly" && v.price == "100" {
			d.Records[0].Plan.Currency = "CNY"
		}
		if v.period == "semiannual" {
			d.Records[0].Plan.RenewalPrice = "36"
		}
		if v.period == "yearly" && v.price == "12" {
			d.Records[0].Plan.RenewalPrice = "144"
		}
		out := estimateExpenses(d, nil)
		wantYear := v.year
		if v.price == "999999999999.999" {
			wantYear = "1000000000000.00"
		}
		if out.MonthlyUSD != v.month || out.YearlyUSD != wantYear || out.IncludedCount != 1 {
			t.Fatal(v, out)
		}
	}
	d := expenseFixture([3]string{"1", "monthly", "USD"}, [3]string{"7", "monthly", "CNY"})
	if out := estimateExpenses(d, nil); out.MonthlyUSD != "2.00" || out.YearlyUSD != "24.00" || len(out.Currencies) != 2 {
		t.Fatal(out)
	}
}
func TestExpenseExclusionsPurchaseFallbackAndEmptySelection(t *testing.T) {
	d := expenseFixture([3]string{"", "", "USD"}, [3]string{"9", "once", "USD"}, [3]string{"2", "monthly", "EUR"}, [3]string{"1", "", "USD"}, [3]string{"1", "monthly", ""}, [3]string{"0", "monthly", "USD"}, [3]string{"", "", "USD"})
	d.Records[6].Plan.PurchasePrice = "12"
	d.Records[6].Plan.PurchasePricePeriod = "yearly"
	out := estimateExpenses(d, nil)
	if out.ScopeCount != 7 || out.IncludedCount != 2 || out.OnceCount != 1 || out.MissingPriceCount != 1 || out.ExcludedCount != 5 || out.MonthlyUSD != "1.00" || len(out.MissingRates) != 1 || out.MissingRates[0].Currency != "EUR" || out.MissingPriceDevices[0].Name != "device-0" || !out.Devices[6].EstimatedFromPurchase {
		t.Fatal(out)
	}
	if out.Devices[2].MonthlyUSD != nil || out.Devices[3].Exclusion != "missing_period" || out.Devices[4].Exclusion != "missing_currency" {
		t.Fatal(out.Devices)
	}
	if out := estimateExpenses(d, map[string]bool{}); out.ScopeCount != 0 || out.MonthlyUSD != "0.00" {
		t.Fatal(out)
	}
	if out := estimateExpenses(d, map[string]bool{d.Records[6].Snapshot.Agent.ID: true}); out.ScopeCount != 1 || out.MonthlyUSD != "1.00" {
		t.Fatal(out)
	}
	d.Records[6].Snapshot.Agent.Revoked = true
	if out := estimateExpenses(d, nil); out.ScopeCount != 6 || out.MonthlyUSD != "0.00" {
		t.Fatal(out)
	}
}
func TestExpenseAPIAuthScopeAndOverviewIndependentOfFilteredIDs(t *testing.T) {
	a, s, id, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	_, err := s.PutAgentPlan(t.Context(), storage.AgentPlan{AgentID: id, Currency: "USD", RenewalPrice: "12", RenewalPricePeriod: "quarterly"}, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/web/expenses/estimate"
	for _, v := range []struct {
		auth, csrf bool
		want       int
	}{{false, false, 401}, {true, false, 403}, {true, true, 200}} {
		w := organizationHTTP(t, a, "POST", path, `{"scope":"all"}`, v.auth, v.csrf)
		if w.Code != v.want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, body := range []string{`{}`, `{"scope":"filtered"}`, `{"scope":"filtered","agent_ids":null}`, `{"scope":"all","agent_ids":[]}`, `{"scope":"filtered","agent_ids":["bad"]}`, `{"scope":"all","unknown":1}`} {
		if w := organizationHTTP(t, a, "POST", path, body, true, true); w.Code != 400 {
			t.Fatal(body, w.Code)
		}
	}
	if w := organizationHTTP(t, a, "POST", path, `{"scope":"filtered","agent_ids":["`+strings.Repeat("f", 32)+`"]}`, true, true); w.Code != 404 {
		t.Fatal(w.Code)
	}
	w := organizationHTTP(t, a, "POST", path, `{"scope":"filtered","agent_ids":[]}`, true, true)
	var out expenseEstimate
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || out.ScopeCount != 0 || out.MonthlyUSD != "0.00" {
		t.Fatal(w.Body.String())
	}
	w = organizationHTTP(t, a, "GET", "/api/v1/web/dashboard?ids="+id, "", true, false)
	var dash dashboardResponse
	if json.Unmarshal(w.Body.Bytes(), &dash) != nil || !dash.Partial || dash.Expenses.MonthlyUSD != "4.00" {
		t.Fatal(w.Body.String())
	}
}

func TestWebPlanNewPaymentPeriodsAndUnknownOldUIValue(t *testing.T) {
	a, s, id, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	for _, period := range []string{"quarterly", "semiannual"} {
		body := `{"currency":"USD","renewal_price":"9","renewal_price_period":"` + period + `"}`
		if w := webPlanPut(t, a, id, body, true); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := webPlanPut(t, a, id, `{"currency":"USD","renewal_price":"9","renewal_price_period":""}`, true); w.Code != 400 {
		t.Fatal("old UI empty period coerced", w.Code)
	}
	p, _, err := s.GetAgentPlan(t.Context(), id)
	if err != nil || p.RenewalPricePeriod != "semiannual" {
		t.Fatal(p, err)
	}
}

package server

import (
	"encoding/json"
	"math/big"
	"net/http"
	"sort"

	"404-probe/internal/storage"
)

type expenseDevice struct {
	ID                    string  `json:"id"`
	Name                  string  `json:"name"`
	Currency              string  `json:"currency"`
	Price                 string  `json:"price"`
	Period                string  `json:"period"`
	EstimatedFromPurchase bool    `json:"estimated_from_purchase"`
	MonthlyOriginal       string  `json:"monthly_original"`
	MonthlyUSD            *string `json:"monthly_usd"`
	Exclusion             string  `json:"exclusion,omitempty"`
}
type expenseCurrency struct {
	Currency        string  `json:"currency"`
	DeviceCount     int     `json:"device_count"`
	MonthlyOriginal string  `json:"monthly_original"`
	MonthlyUSD      *string `json:"monthly_usd"`
	RateMissing     bool    `json:"rate_missing"`
}
type expenseMissingRate struct {
	Currency    string `json:"currency"`
	DeviceCount int    `json:"device_count"`
}
type expenseTotals struct {
	MonthlyUSD        string `json:"monthly_usd"`
	YearlyUSD         string `json:"yearly_usd"`
	ScopeCount        int    `json:"scope_count"`
	IncludedCount     int    `json:"included_count"`
	OnceCount         int    `json:"once_count"`
	MissingPriceCount int    `json:"missing_price_count"`
	ExcludedCount     int    `json:"excluded_count"`
}
type expenseEstimate struct {
	expenseTotals
	Revision            int64                `json:"revision"`
	Currencies          []expenseCurrency    `json:"currencies"`
	Devices             []expenseDevice      `json:"devices"`
	Excluded            []expenseDevice      `json:"excluded"`
	MissingPriceDevices []expenseDevice      `json:"missing_price_devices"`
	MissingRates        []expenseMissingRate `json:"missing_rates"`
}

// Amounts remain exact rational decimals throughout arithmetic. Two-place totals
// and twelve-place detail amounts are display strings, never inputs to totals.
func estimateExpenses(data storage.DashboardData, selected map[string]bool) expenseEstimate {
	out := expenseEstimate{Revision: data.Catalog.Revision, Currencies: []expenseCurrency{}, Devices: []expenseDevice{}, Excluded: []expenseDevice{}, MissingPriceDevices: []expenseDevice{}, MissingRates: []expenseMissingRate{}}
	rates := map[string]*big.Rat{"USD": big.NewRat(1, 1)}
	for _, r := range data.Catalog.Rates {
		if r.Currency == "USD" {
			continue
		}
		if value, err := storage.NormalizeExchangeRate(r.Currency, r.UnitsPerUSD); err == nil {
			rates[r.Currency], _ = new(big.Rat).SetString(value)
		}
	}
	total := new(big.Rat)
	originals := map[string]*big.Rat{}
	counts := map[string]int{}
	missing := map[string]int{}
	for _, r := range data.Records {
		agent := r.Snapshot.Agent
		if agent.Revoked || (selected != nil && !selected[agent.ID]) {
			continue
		}
		out.ScopeCount++
		d := expenseDevice{ID: agent.ID, Name: agent.Name}
		if p := r.Plan; p != nil {
			d.Currency = p.Currency
			d.Price = p.RenewalPrice
			d.Period = p.RenewalPricePeriod
			if d.Price == "" {
				d.Price = p.PurchasePrice
				d.Period = p.PurchasePricePeriod
				d.EstimatedFromPurchase = d.Price != ""
			}
		}
		months := map[string]int64{"monthly": 1, "quarterly": 3, "semiannual": 6, "yearly": 12}[d.Period]
		switch {
		case d.Period == "once":
			d.Exclusion = "once"
			out.OnceCount++
		case d.Price == "":
			d.Exclusion = "missing_price"
			out.MissingPriceCount++
		case !decimalPlanValue.MatchString(d.Price):
			d.Exclusion = "invalid_price"
		case d.Currency == "":
			d.Exclusion = "missing_currency"
		case !storage.ValidISOCurrency(d.Currency):
			d.Exclusion = "invalid_currency"
		case d.Period == "":
			d.Exclusion = "missing_period"
		case months == 0:
			d.Exclusion = "invalid_period"
		default:
			price, _ := new(big.Rat).SetString(d.Price)
			month := new(big.Rat).Quo(price, big.NewRat(months, 1))
			d.MonthlyOriginal = month.FloatString(12)
			if originals[d.Currency] == nil {
				originals[d.Currency] = new(big.Rat)
			}
			originals[d.Currency].Add(originals[d.Currency], month)
			counts[d.Currency]++
			if rate := rates[d.Currency]; rate == nil {
				d.Exclusion = "missing_rate"
				missing[d.Currency]++
			} else {
				usd := new(big.Rat).Quo(month, rate)
				display := usd.FloatString(12)
				d.MonthlyUSD = &display
				total.Add(total, usd)
				out.IncludedCount++
			}
		}
		out.Devices = append(out.Devices, d)
		if d.Exclusion != "" {
			out.Excluded = append(out.Excluded, d)
		}
		if d.Exclusion == "missing_price" {
			out.MissingPriceDevices = append(out.MissingPriceDevices, d)
		}
	}
	keys := []string{}
	for code := range originals {
		keys = append(keys, code)
	}
	sort.Strings(keys)
	for _, code := range keys {
		c := expenseCurrency{Currency: code, DeviceCount: counts[code], MonthlyOriginal: originals[code].FloatString(12), RateMissing: rates[code] == nil}
		if !c.RateMissing {
			v := new(big.Rat).Quo(originals[code], rates[code]).FloatString(12)
			c.MonthlyUSD = &v
		}
		out.Currencies = append(out.Currencies, c)
		if missing[code] > 0 {
			out.MissingRates = append(out.MissingRates, expenseMissingRate{code, missing[code]})
		}
	}
	out.MonthlyUSD = total.FloatString(2)
	out.YearlyUSD = new(big.Rat).Mul(total, big.NewRat(12, 1)).FloatString(2)
	out.ExcludedCount = len(out.Excluded)
	return out
}

func (a *App) handleExpenseEstimate(w http.ResponseWriter, r *http.Request) {
	if _, err := parseControlQuery(r); err != nil || r.URL.EscapedPath() != "/api/v1/web/expenses/estimate" {
		writeJobError(w, 400, "invalid_request", "invalid expense query")
		return
	}
	var in struct {
		Scope    string          `json:"scope"`
		AgentIDs json.RawMessage `json:"agent_ids"`
	}
	if err := readOrganizationJSON(w, r, &in); err != nil {
		writeJobError(w, 400, "invalid_request", "invalid expense input")
		return
	}
	var selected map[string]bool
	switch in.Scope {
	case "all":
		if in.AgentIDs != nil {
			writeJobError(w, 400, "invalid_request", "all scope does not accept ids")
			return
		}
	case "filtered":
		var ids []string
		if len(in.AgentIDs) == 0 || string(in.AgentIDs) == "null" || json.Unmarshal(in.AgentIDs, &ids) != nil || len(ids) > 200 {
			writeJobError(w, 400, "invalid_request", "filtered scope requires at most 200 ids")
			return
		}
		selected = map[string]bool{}
		for _, id := range ids {
			if !validWebAgentID(id) || selected[id] {
				writeJobError(w, 400, "invalid_request", "invalid expense ids")
				return
			}
			selected[id] = true
		}
	default:
		writeJobError(w, 400, "invalid_request", "scope must be all or filtered")
		return
	}
	data, err := a.store.ReadDashboardData(r.Context(), a.now(), a.offlineTimeout, false)
	if err != nil {
		a.organizationError(w, err)
		return
	}
	if selected != nil {
		found := 0
		for _, record := range data.Records {
			if selected[record.Snapshot.Agent.ID] {
				found++
			}
		}
		if found != len(selected) {
			writeJobError(w, 404, "not_found", "one or more expense devices are unavailable")
			return
		}
	}
	writeJSON(w, 200, estimateExpenses(data, selected))
}

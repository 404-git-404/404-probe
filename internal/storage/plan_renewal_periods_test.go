package storage

import (
	"testing"
	"time"
)

func TestPlanRenewalAllPaymentPeriodsApplyUndoIndependentTraffic(t *testing.T) {
	for _, v := range []struct{ period, date, next string }{{"monthly", "2024-01-31", "2024-02-29"}, {"quarterly", "2024-01-31", "2024-04-30"}, {"semiannual", "2024-08-31", "2025-02-28"}, {"yearly", "2024-02-29", "2025-02-28"}} {
		t.Run(v.period, func(t *testing.T) {
			s, id := renewalSetup(t, ":memory:")
			p := renewalPlan(id)
			p.PurchaseDate = v.date
			p.RenewalDate = v.date
			p.RenewalPricePeriod = v.period
			p.TrafficMode = "sum"
			p.CycleKind = "days"
			p.CycleCount = 17
			p.CycleAnchor = "2024-01-01"
			p.QuotaValue = "100"
			p.QuotaUnit = "GB"
			if _, err := s.PutAgentPlan(t.Context(), p, nil, renewalNow()); err != nil {
				t.Fatal(err)
			}
			q := renewalRequest(t, s, id, "11111111111111111111111111111111", renewalNow())
			if q.ToDate != v.next {
				t.Fatal(q)
			}
			a := renewalApply(t, s, id, q, renewalNow())
			if a.CurrentPlan.CycleKind != "days" || a.CurrentPlan.CycleCount != 17 {
				t.Fatal("payment changed traffic", a)
			}
			u, err := s.UndoPlanRenewal(t.Context(), id, PlanRenewalUndo{"33333333333333333333333333333333", q.RequestID, a.CurrentRevision}, renewalNow().Add(time.Second))
			if err != nil || u.CurrentPlan.RenewalDate != v.date || u.CurrentPlan.RenewalPricePeriod != v.period {
				t.Fatal(u, err)
			}
		})
	}
	for _, v := range []struct{ purchase, date, period, want string }{{"2024-01-31", "2024-04-30", "quarterly", "2024-07-31"}, {"2023-08-31", "2024-02-29", "semiannual", "2024-08-31"}, {"2024-02-29", "2027-11-29", "quarterly", "2028-02-29"}, {"2024-01-31", "2024-02-15", "quarterly", "2024-05-15"}} {
		p := AgentPlan{PurchaseDate: v.purchase, RenewalDate: v.date, RenewalPricePeriod: v.period}
		out, err := renewalPreview(&p, planRenewalConfig{renewalAnchor: planRenewalAnchor(p, v.date)}, "", renewalNow())
		if err != nil || out.ToDate != v.want {
			t.Fatal(v, out, err)
		}
	}
}

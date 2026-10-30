package server

import (
	"fmt"
	"testing"
	"time"

	"404-probe/internal/auth"
)

func TestDashboardSoakClockBridgesConstructionGap(t *testing.T) {
	a, s, _, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	seededAt := time.Now().Add(-4 * time.Minute)
	a.now = func() time.Time { return seededAt }
	ids, tokens := []string{}, map[string]string{}
	for i := range 6 {
		id := fmt.Sprintf("%032x", i+100)
		token, hash, err := auth.NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.AddAgent(t.Context(), id, id, hash, seededAt); err != nil {
			t.Fatal(err)
		}
		ids, tokens[id] = append(ids, id), token
		r := userReport(id, 1)
		r.CollectedAt = seededAt.UnixMilli()
		if w := postWithHeaders(t, a, token, "/api/v1/report", r, userHeaders); w.Code != 200 {
			t.Fatal(w.Code)
		}
		configureUserTargets(t, s, id, seededAt.Add(-2*time.Hour), [3]string{"8.8.8.8", "1.1.1.1", "9.9.9.9"}, 443)
	}
	cfgs := seedDashboardSoak(t, a, s, ids, tokens, seededAt)
	alert := func(i int, now time.Time) bool {
		v, err := s.QualityAlertActive(t.Context(), ids[i], now)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if !alert(3, seededAt) || alert(3, time.Now()) {
		t.Fatal("fixture did not reproduce its construction-time gap")
	}
	prepareDashboardSoakClock(t, a, ids, tokens, cfgs, seededAt)
	if !alert(3, time.Now()) || alert(1, time.Now()) || alert(0, time.Now()) || alert(5, time.Now()) {
		t.Fatal("bridge lost red/healthy/missing fixture distinctions")
	}
}

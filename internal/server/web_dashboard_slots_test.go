package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestDashboardNetworkSlotsWireFullAndPartial(t *testing.T) {
	a, s, id, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	for _, path := range []string{"/api/v1/web/dashboard", "/api/v1/web/dashboard?ids=" + id} {
		r := httptest.NewRequest("GET", path, nil)
		addTestWebSession(t, a, r)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		var data struct {
			Items []struct {
				Slots []struct {
					Slot    int      `json:"slot"`
					Latest  *float64 `json:"latest_ms"`
					Sampled int64    `json:"sampled_at"`
					State   string   `json:"state"`
					Name    string   `json:"target_name"`
				} `json:"network_slots"`
			} `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil || w.Code != 200 || len(data.Items) != 1 || len(data.Items[0].Slots) != 3 {
			t.Fatal(w.Code, w.Body.String(), err)
		}
		for i, slot := range data.Items[0].Slots {
			if slot.Slot != i+1 || slot.State != "unsupported" || slot.Latest != nil || slot.Sampled != 0 || slot.Name == "" {
				t.Fatal(slot)
			}
		}
	}
}

package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

func TestNetworkStatusWebRoutes(t *testing.T) {
	app, store, id, _ := testApp(t)
	defer store.Close()
	defer app.Shutdown()
	now := time.Date(2020, 11, 22, 12, 0, 15, 0, time.UTC)
	app.now = func() time.Time { return now }
	base := webAgentPathPrefix + id
	line := "slot=telecom&family=ipv4&protocol=tcp"
	rng := func(d time.Duration) string {
		return fmt.Sprintf("start=%d&end=%d", now.Add(-d).UnixMilli(), now.UnixMilli())
	}
	cases := []struct {
		path     string
		validate func(*testing.T, []byte)
	}{
		{base + "/network-quality/range?" + line + "&" + rng(7*24*time.Hour), func(t *testing.T, data []byte) {
			var out storage.NetworkHistory
			if err := json.Unmarshal(data, &out); err != nil {
				t.Fatal(err)
			}
			if out.ResolutionMS != 300000 || len(out.Points) != storage.NetworkStatusPointLimit || out.Points[0].P50 != nil || out.Points[0].Completeness != "no_data" {
				t.Fatalf("bad empty history: resolution=%d, points=%d, first=%+v", out.ResolutionMS, len(out.Points), out.Points[0])
			}
		}},
		{networkFleetPath + "?window=30d", func(t *testing.T, data []byte) {
			var out storage.NetworkFleet
			if err := json.Unmarshal(data, &out); err != nil {
				t.Fatal(err)
			}
			if len(out.Machines) != 1 || len(out.Machines[0].Lines) != 12 || out.Machines[0].Lines[0].Status != "no_data" || out.Machines[0].Lines[0].Stats.FailurePercent != nil {
				t.Fatalf("bad empty fleet: %s", data)
			}
		}},
		{base + "/network-quality/heatmap?" + line + "&days=7", func(t *testing.T, data []byte) {
			var out storage.NetworkHeatmap
			if err := json.Unmarshal(data, &out); err != nil {
				t.Fatal(err)
			}
			if len(out.Days) != 7 || out.Days[6].Date != "2020-11-22" || out.Days[6].Hours[20].P50 != nil {
				t.Fatalf("bad empty heatmap: %s", data)
			}
		}},
		{base + "/network-quality/alerts", func(t *testing.T, data []byte) {
			var out []storage.NetworkAlert
			if err := json.Unmarshal(data, &out); err != nil || out == nil || len(out) != 0 {
				t.Fatalf("bad empty alerts: %s (%v)", data, err)
			}
		}},
		{base + "/presence?" + rng(time.Hour), func(t *testing.T, data []byte) {
			var out storage.PresenceHistory
			if err := json.Unmarshal(data, &out); err != nil || out.OnlinePercent != nil || out.UnknownMS != time.Hour.Milliseconds() {
				t.Fatalf("bad empty presence: %s (%v)", data, err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, tc.path, nil)
			request.Host = "probe.test"
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated status=%d", response.Code)
			}
			response = webAgentResponse(t, app, http.MethodGet, tc.path)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			tc.validate(t, response.Body.Bytes())
		})
	}
	for _, path := range []string{
		base + "/network-quality/range?" + line + "&" + rng(31*24*time.Hour),
		base + "/network-quality/heatmap?" + line + "&days=8",
		base + "/network-quality/alerts?extra=1",
		base + "/presence?" + rng(time.Hour) + "&start=1",
		networkFleetPath + "?" + rng(time.Hour) + "&protocol=https",
		networkFleetPath + "?" + rng(time.Hour) + "&window=30d",
	} {
		if resp := webAgentResponse(t, app, http.MethodGet, path); resp.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s", path, resp.Code, resp.Body.String())
		}
	}
}

func TestNetworkQualityClockEstimateOnDeviceAndFleet(t *testing.T) {
	app, store, id, token := testApp(t)
	defer store.Close()
	defer app.Shutdown()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	app.now = func() time.Time { return now }
	cfg := qualityServerConfig(t, app, store, id, token)
	probe := func(seq int64, skew time.Duration) {
		t.Helper()
		sample := userTCPSample(t, cfg, "telecom", seq, now.Add(-skew-30*time.Millisecond), 20, "success")
		sample.DurationMS += 10
		sample.FinishedAt += 10
		batch := protocol.QualityBatch{Version: 2, Epoch: "1", SessionID: "session", Samples: []protocol.QualitySample{sample}}
		w := postAgentUpgrade(t, app, token, "/api/v1/agent/network-quality", batch)
		var ack protocol.QualityBatchResponse
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &ack) != nil || len(ack.Results) != 1 || ack.Results[0][2] != "committed" {
			t.Fatalf("quality report %d: %d %s", seq, w.Code, w.Body)
		}
	}
	now = now.Add(5 * time.Minute)
	probe(2, 75*time.Second)
	verify := func(offset int64, skewed bool) {
		t.Helper()
		w := webAgentResponse(t, app, http.MethodGet, webAgentPathPrefix+id)
		var detail webAgentDetailView
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &detail) != nil || detail.State == nil ||
			detail.State.QualityClockOffsetMS == nil || *detail.State.QualityClockOffsetMS != offset || detail.State.QualityClockSkewed != skewed {
			t.Fatalf("device clock offset %d: %d %s", offset, w.Code, w.Body)
		}
		path := fmt.Sprintf("%s?start=%d&end=%d&family=ipv4&protocol=tcp", networkFleetPath, now.Add(-time.Hour).UnixMilli(), now.UnixMilli())
		w = webAgentResponse(t, app, http.MethodGet, path)
		var fleet storage.NetworkFleet
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &fleet) != nil || len(fleet.Machines) != 1 ||
			fleet.Machines[0].QualityClockOffsetMS == nil || *fleet.Machines[0].QualityClockOffsetMS != offset || fleet.Machines[0].QualityClockSkewed != skewed ||
			fleet.Machines[0].Lines[0].Stats.P50 == nil || *fleet.Machines[0].Lines[0].Stats.P50 != 20 {
			t.Fatalf("fleet clock offset %d: %d %s", offset, w.Code, w.Body)
		}
	}
	verify(75000, true)
	probe(1, 90*time.Second)
	verify(75000, true) // A late retry cannot replace the latest estimate.
	now = now.Add(time.Minute)
	probe(3, 60*time.Second)
	verify(60000, false)
}

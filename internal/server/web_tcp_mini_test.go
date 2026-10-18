package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

func TestTCPMiniHTTPReadGuards(t *testing.T) {
	a, s, _ := newWebAgentTestApp(t)
	defer s.Close()
	defer a.Shutdown()
	path := webAgentPathPrefix + controlAgentA + "/network-quality/v3/tcp-mini"
	for _, q := range []string{"", "?timezone=", "?timezone=Local", "?timezone=bad/zone", "?timezone=UTC&extra=1", "?timezone=UTC&timezone=UTC", "?timezone=UTC;ignored=1", "?timezone=%ZZ"} {
		if w := webAgentResponse(t, a, "GET", path+q); w.Code != 400 {
			t.Fatal(q, w.Code, w.Body.String())
		}
	}
	w := webAgentResponse(t, a, "GET", path+"?timezone=America%2FLos_Angeles")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	var o storage.TCPMiniStatus
	if err := json.Unmarshal(w.Body.Bytes(), &o); err != nil || o.State != "unknown" || o.Level != 2000 {
		t.Fatal(o, err)
	}
	w = httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("GET", path+"?timezone=UTC", nil))
	if w.Code == 200 {
		t.Fatal("unauthenticated")
	}
	if w = webAgentResponse(t, a, "POST", path+"?timezone=UTC"); w.Code != 405 && w.Code != 404 {
		t.Fatal(w.Code)
	}
}

// Optional local Chromium proof uses this real App/SQLite fixture. The control
// endpoint exists ONLY inside this test's loopback httptest wrapper. It advances
// the fake clock and commits valid protocol samples, never supplies fake API DTOs.
func TestTCPMiniChromiumRealAPI(t *testing.T) {
	script := os.Getenv("M05_BROWSER_SCRIPT")
	if script == "" {
		t.Skip("set M05_BROWSER_SCRIPT for local browser proof")
	}
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "browser.db")
	s, err := storage.Open(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	start := time.Date(2026, 10, 17, 6, 50, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(start.UnixMilli())
	ids := []string{controlAgentA, controlAgentB, webAgentC, "dddddddddddddddddddddddddddddddd"}
	configs := map[string]protocol.QualityConfig{}
	var sequence int64
	report := func(id string, seq uint64) {
		r := reportFor(id, seq)
		r.NetworkQuality = true
		r.NetworkQualityV2 = a1ServerCapabilities()
		r.NetworkQualityV2.Semantics = protocol.QualityStagedSemantics
		r.CollectedAt = clock.Load()
		r.Hostname = "m05-local"
		r.OS = "Debian 13"
		if _, ok, _, e := s.ProcessReport(ctx, id, r, time.UnixMilli(clock.Load())); e != nil || !ok {
			t.Fatal("report", e, ok)
		}
	}
	for i, id := range ids {
		addControlAgent(t, s, id, fmt.Sprintf("M05 VPS %d", i+1), start.Add(-48*time.Hour))
		report(id, 1)
		u := protocol.QualityV2ConfigUpdate{ExpectedRevision: "0", Enabled: true, RegionID: qualityDirectory.Regions[0].ID}
		for _, slot := range []string{"telecom", "unicom", "mobile"} {
			for _, p := range []string{"tcp", "icmp"} {
				port := 80
				if p == "icmp" {
					port = 0
				}
				u.Lanes = append(u.Lanes, protocol.QualityLaneChoice{QualityChoice: protocol.QualityChoice{Slot: slot, Source: "catalog", Protocol: p, Host: "127.0.0.1", Port: port}, Family: "ipv4", Enabled: true})
			}
		}
		c, e := s.UpdateQualityConfigV2(ctx, id, u, qualityResolverV2, start.Add(-6*time.Hour))
		if e != nil {
			t.Fatal(e)
		}
		configs[id] = c
		quota := uint64(1000000000000)
		if _, e = s.PutAgentPlan(ctx, storage.AgentPlan{AgentID: id, TrafficMode: "sum", QuotaValue: "1", QuotaUnit: "TB", QuotaBytes: &quota, CycleKind: "monthly", CycleCount: 1, CycleAnchor: "2026-10-01", Timezone: "America/Los_Angeles", RenewalPrice: "9", RenewalPricePeriod: "monthly", Currency: "USD", RenewalDate: "2026-11-01"}, nil, start); e != nil {
			t.Fatal(e)
		}
		if e = s.SaveOutboundSnapshot(ctx, id, protocol.OutboundSnapshot{Available: true, Status: protocol.OutboundStatusConnected, OrderSource: "name", Selectors: []protocol.OutboundSelector{{Name: "YouTube", Current: "direct", Choices: []string{"direct", "test"}}}}, start); e != nil {
			t.Fatal(e)
		}
	}
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`INSERT OR REPLACE INTO tcp_mini_state(agent_id,evidence_start,scanned_at,initialized) SELECT id,?,?,1 FROM agents`, start.Add(-48*time.Hour).UnixMilli(), start.Add(-time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	save := func(id, slot string, at time.Time, value float64, outcome string) {
		c := configs[id]
		var target protocol.QualityTarget
		for _, v := range c.Targets {
			if v.Slot == slot && v.Protocol == "tcp" && v.Family == "ipv4" {
				target = v
			}
		}
		sequence++
		duration := value
		if duration < 20 {
			duration = 20
		}
		sample := protocol.QualitySample{Sequence: strconv.FormatInt(sequence, 10), TargetID: target.ID, ConfigRevision: c.Revision, SlotRevision: target.SlotRevision, ScheduledAt: at.UnixMilli(), StartedAt: at.UnixMilli(), FinishedAt: at.UnixMilli() + int64(duration), DurationMS: duration, Outcome: outcome, ResolvedIP: "127.0.0.1"}
		d := protocol.QualityDetails{Semantics: protocol.QualityStagedSemantics, DNSOutcome: "literal", ProbeComplete: true, ConnectAttempted: true, ConnectComplete: true, ConnectMS: &value, IntervalMS: 30000}
		if outcome == "success" {
			sample.LatencyMS = &value
			d.ConnectMS = &value
		} else {
			d.ErrorStage = "connect"
			d.ErrorCode = outcome
		}
		sample.Details = d.Encode()
		ack, e := s.SaveQualityBatch(ctx, id, protocol.QualityBatch{Version: 2, Epoch: "1", SessionID: reportFor(id, 1).SessionID, Samples: []protocol.QualitySample{sample}}, time.UnixMilli(clock.Load()))
		if e != nil || len(ack.Results) != 1 || ack.Results[0][2] != "committed" {
			t.Fatal("sample", ack, e)
		}
	}
	for _, id := range ids {
		for j, slot := range []string{"telecom", "unicom", "mobile"} {
			for i := 0; i < 20; i++ {
				v := float64(30 + i + j*20)
				if i == 0 {
					v = 0
				}
				if i == 1 {
					v = .25
				}
				outcome := "success"
				if i == 8 {
					outcome = "timeout"
				}
				save(id, slot, start.Add(time.Duration(-40+i)*time.Minute), v, outcome)
			}
		}
	}
	// Known gap preserves the genuine P95 as the original hollow marker.
	_, err = s.SaveQualityBatch(ctx, ids[0], protocol.QualityBatch{Version: 2, Epoch: "1", SessionID: reportFor(ids[0], 1).SessionID, Gaps: []protocol.QualityGap{{Start: start.Add(-35 * time.Minute).UnixMilli(), End: start.Add(-34 * time.Minute).UnixMilli(), Reason: "unknown_gap"}}}, start)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.MaintainTCPMini(ctx, start); err != nil {
		t.Fatal(err)
	}
	a, err := NewApp(s, 30*time.Second, nil, WithWebAuthentication(WebAuthenticationConfig{PasswordHash: webTestPasswordHash(t), PublicOrigin: "https://probe.test"}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Shutdown()
	a.now = func() time.Time { return time.UnixMilli(clock.Load()) }
	seedTraffic := func() {
		states, e := s.ListStates(ctx)
		if e != nil {
			t.Fatal(e)
		}
		for _, st := range states {
			a.traffic.retire(st.AgentID, a.now(), false)
			owner := a.traffic.capture(st.AgentID, a.now())
			for j := 0; j < 3; j++ {
				v := st
				v.Sequence = uint64(j + 1)
				v.LastSeen = clock.Load() - int64(20-j*10)*1000
				v.CollectedAt = v.LastSeen
				v.RXRate = 1024 * float64(j+1)
				v.TXRate = 512 * float64(j+1)
				a.traffic.append(owner, v, time.UnixMilli(v.LastSeen), a.now())
			}
		}
	}
	seedTraffic()
	sessionRequest := httptest.NewRequest("GET", "/", nil)
	addTestWebSession(t, a, sessionRequest)
	cookie := sessionRequest.Header.Get("Cookie")
	var reportSeq uint64 = 1
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__m05/phase" {
			if r.Method != "POST" {
				w.WriteHeader(405)
				return
			}
			phase := r.URL.Query().Get("name")
			switch phase {
			case "high":
				save(ids[0], "mobile", start.Add(-5*time.Minute), 4700, "success")
				save(ids[0], "telecom", start.Add(-3*time.Minute), 100, "success")
				save(ids[1], "unicom", start.Add(-5*time.Minute), 2001, "success")
			case "midnight":
				clock.Store(start.Add(20 * time.Minute).UnixMilli())
			case "equal":
				save(ids[0], "telecom", time.UnixMilli(clock.Load()).Add(-time.Minute), 2000, "success")
			case "restore":
				save(ids[0], "telecom", time.UnixMilli(clock.Load()).Add(-2*time.Minute), 100, "success")
			case "again":
				save(ids[0], "mobile", time.UnixMilli(clock.Load()).Add(-3*time.Minute), 2001, "success")
			default:
				w.WriteHeader(400)
				return
			}
			if e := s.MaintainTCPMini(ctx, time.UnixMilli(clock.Load())); e != nil {
				t.Error(e)
				w.WriteHeader(500)
				return
			}
			reportSeq++
			for _, id := range ids {
				report(id, reportSeq)
			}
			seedTraffic()
			states, e := s.ListStates(ctx)
			if e != nil {
				t.Error(e)
			}
			for _, state := range states {
				a.publishState(state)
			}
			w.WriteHeader(204)
			return
		}
		r.Header.Set("Cookie", cookie)
		r.Host = "probe.test"
		a.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	cmd := exec.Command("node", script)
	cmd.Env = append(os.Environ(), "M05_ORIGIN="+server.URL, "M05_AGENT="+ids[0])
	wire, err := cmd.CombinedOutput()
	t.Log(string(wire))
	if err != nil {
		t.Fatal(err)
	}
}

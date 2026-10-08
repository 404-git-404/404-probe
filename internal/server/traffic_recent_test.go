package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

func recentTraffic(t *testing.T, a *App, id string) trafficRecentView {
	t.Helper()
	r := webAgentResponse(t, a, "GET", webAgentPathPrefix+id+"/traffic/recent")
	if r.Code != 200 || r.Header().Get("Cache-Control") != "no-store" || r.Body.Len() > trafficResponseLimit {
		t.Fatalf("status/body %d %s", r.Code, r.Body.String())
	}
	var v trafficRecentView
	if err := json.Unmarshal(r.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func trafficState(id string, seq uint64, at time.Time) storage.State {
	return storage.State{AgentID: id, Epoch: 1, SessionID: "session", BootID: "boot", Sequence: seq, LastSeen: at.UnixMilli(), CollectedAt: at.Add(123 * time.Second).UnixMilli(), RXRate: 12, TXRate: 34}
}
func TestTrafficRecentAcceptedReportRatesIdentityAndReset(t *testing.T) {
	a, s, id, token := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	now := time.Unix(4000, 0)
	a.now = func() time.Time { return now }
	r := reportFor(id, 1)
	r.CollectedAt = now.Add(time.Hour).UnixMilli()
	if out := postReport(t, a, token, r); out.Code != 200 {
		t.Fatal(out.Body.String())
	}
	v := recentTraffic(t, a, id)
	if len(v.Points) != 1 || v.Points[0].RXRate != nil || v.Points[0].Reason != "first_observation" || v.Points[0].CollectedAt != r.CollectedAt {
		t.Fatalf("first %+v", v)
	}
	now = now.Add(10 * time.Second)
	r.Sequence = 2
	r.RXBytes += 100
	r.TXBytes += 200
	r.CollectedAt -= 10000
	postReport(t, a, token, r)
	v = recentTraffic(t, a, id)
	if len(v.Points) != 2 || v.Points[1].RXRate == nil || *v.Points[1].RXRate != 10 || *v.Points[1].TXRate != 20 || v.Points[1].ReceivedAt != now.UnixMilli() {
		t.Fatalf("actual rate %+v", v)
	}
	postReport(t, a, token, r)
	postReport(t, a, token, reportFor(id, 1))
	if len(recentTraffic(t, a, id).Points) != 2 {
		t.Fatal("duplicate appended")
	}
	now = now.Add(10 * time.Second)
	r.Sequence++
	postReport(t, a, token, r)
	v = recentTraffic(t, a, id)
	if v.Points[2].RXRate == nil || *v.Points[2].RXRate != 0 {
		t.Fatal("actual idle zero lost")
	}
	oldgen := v.Generation
	now = now.Add(10 * time.Second)
	r.Sequence++
	r.BootID = "new-boot"
	postReport(t, a, token, r)
	v = recentTraffic(t, a, id)
	if v.Generation == oldgen || v.Points[3].RXRate != nil || v.Points[3].Reason != "continuity_reset" {
		t.Fatal("boot reset is fake idle")
	}
	now = now.Add(31 * time.Second)
	r.Sequence++
	postReport(t, a, token, r)
	v = recentTraffic(t, a, id)
	if v.Points[4].RXRate != nil || v.Points[4].Reason != "gap" {
		t.Fatal("gap joined")
	}
	data, _ := json.Marshal(v)
	for _, private := range []string{"boot_id", "session_id", "epoch", "csrf_token"} {
		if strings.Contains(string(data), private) {
			t.Fatal("private identity leaked")
		}
	}
	b, err := NewApp(s, 30*time.Second, nil, WithWebAuthentication(WebAuthenticationConfig{PasswordHash: webTestPasswordHash(t), PublicOrigin: "https://probe.test"}))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Shutdown()
	b.now = a.now
	restarted := recentTraffic(t, b, id)
	if len(restarted.Points) != 0 || restarted.Generation == v.Generation {
		t.Fatal("startup state treated as history")
	}
}
func TestTrafficRecentDelayedAcceptedRetirementAndResume(t *testing.T) {
	for _, action := range []string{"disable", "revoke", "remove"} {
		t.Run(action, func(t *testing.T) {
			a, s, id, token := testApp(t)
			defer s.Close()
			defer a.Shutdown()
			now := time.Unix(5000, 0)
			a.now = func() time.Time { return now }
			r := reportFor(id, 1)
			r.Management = &protocol.AgentManagementCapabilities{RemoteRemoval: true}
			postReport(t, a, token, r)
			now = now.Add(10 * time.Second)
			owner := a.traffic.capture(id, now)
			r.Sequence = 2
			accepted, ok, _, err := s.ProcessReport(context.Background(), id, r, now)
			if err != nil || !ok {
				t.Fatal(err)
			} // deliberately pause between Store commit and App publication
			var response *httptest.ResponseRecorder
			if action == "remove" {
				response = webAgentRemovalResponse(t, a, id, `{"operation_id":"abcdefabcdefabcdefabcdefabcdefab"}`)
			} else {
				response = webAgentStateResponse(t, a, id, action, nil)
			}
			if response.Code != 200 && response.Code != 202 {
				t.Fatal(response.Body.String())
			}
			if !a.publishReportedState(accepted, owner, now) {
				t.Fatal("optional chart changed core publish")
			}
			v := recentTraffic(t, a, id)
			if len(v.Points) != 0 {
				t.Fatal("late accepted report resurrected history")
			}
			want := map[string]string{"disable": "paused", "revoke": "revoked", "remove": "removing"}[action]
			if v.Status != want {
				t.Fatalf("status %s", v.Status)
			}
			if action == "disable" {
				webAgentStateResponse(t, a, id, "enable", nil)
				now = now.Add(10 * time.Second)
				r.Sequence = 3
				postReport(t, a, token, r)
				v = recentTraffic(t, a, id)
				if len(v.Points) != 1 || v.Points[0].RXRate != nil {
					t.Fatal("resume inherited pre-pause line")
				}
			}
		})
	}
}
func TestTrafficRecentPublishedOrderEvictionAndClock(t *testing.T) {
	a, s, id, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	now := time.Unix(6000, 0)
	a.now = func() time.Time { return now }
	old := a.traffic.capture(id, now)
	first := trafficState(id, 1, now)
	now = now.Add(10 * time.Second)
	newer := a.traffic.capture(id, now)
	second := trafficState(id, 2, now)
	if !a.publishReportedState(second, newer, now) || a.publishReportedState(first, old, now.Add(-10*time.Second)) {
		t.Fatal("publication ordering changed")
	}
	if len(a.traffic.snapshot(id, now).Points) != 1 {
		t.Fatal("reordered delivery appended")
	}
	for i := 0; i < trafficSeriesLimit; i++ {
		other := fmt.Sprintf("%032x", i+123)
		o := a.traffic.capture(other, now)
		a.traffic.append(o, trafficState(other, 1, now), now, now)
	}
	a.traffic.append(old, trafficState(id, 3, now.Add(time.Second)), now.Add(time.Second), now.Add(time.Second))
	if len(a.traffic.snapshot(id, now.Add(time.Second)).Points) != 0 {
		t.Fatal("evicted owner revived")
	}
	c, _ := newTrafficCache()
	at := time.Unix(9000, 0)
	o := c.capture("clock", at)
	c.append(o, trafficState("clock", 1, at), at, at)
	back := at.Add(-time.Second)
	o = c.capture("clock", back)
	c.append(o, trafficState("clock", 2, back), back, back)
	v := c.snapshot("clock", back)
	if len(v.Points) != 1 || v.Points[0].RXRate != nil {
		t.Fatal("rollback retained future")
	}
	// Monotonic forward jump: Round strips monotonic, preserving wall comparison.
	mono := time.Now()
	o = c.capture("jump", mono)
	c.append(o, trafficState("jump", 1, mono), mono, mono)
	c.mu.Lock()
	c.clock = c.clock.Add(-time.Hour).Round(0)
	c.mu.Unlock()
	future := mono.Add(time.Hour)
	if len(c.snapshot("jump", future).Points) != 0 {
		t.Fatal("forward stale window survived")
	}
}
func TestTrafficRecentBoundsPrivateCopyAndNullable(t *testing.T) {
	c, _ := newTrafficCache()
	at := time.Unix(10000, 0)
	for j := 0; j < 181; j++ {
		now := at.Add(time.Duration(j) * 10 * time.Second)
		for i := 0; i < 100; i++ {
			id := fmt.Sprintf("%032x", i+1)
			o := c.capture(id, now)
			c.append(o, trafficState(id, uint64(j+1), now), now, now)
		}
	}
	id := fmt.Sprintf("%032x", 1)
	now := at.Add(1800 * time.Second)
	v := c.snapshot(id, now)
	if len(v.Points) != 181 || v.Truncated {
		t.Fatalf("normal %d", len(v.Points))
	}
	v.Points[1].Order = "tampered"
	*v.Points[1].RXRate = 999
	if c.snapshot(id, now).Points[1].Order == "tampered" || *c.snapshot(id, now).Points[1].RXRate == 999 {
		t.Fatal("snapshot alias")
	}
	for j := 182; j <= 400; j++ {
		now = now.Add(time.Millisecond)
		o := c.capture(id, now)
		c.append(o, trafficState(id, uint64(j), now), now, now)
	}
	v = c.snapshot(id, now)
	if len(v.Points) != 181 || !v.Truncated {
		t.Fatal("rapid count unbounded")
	}
	for i := 1; i < len(v.Points); i++ {
		if v.Points[i].ReceivedAt <= v.Points[i-1].ReceivedAt {
			t.Fatal("not strictly increasing")
		}
	}
	bad := trafficState(id, 401, now.Add(time.Second))
	bad.RXRate = math.Inf(1)
	o := c.capture(id, now.Add(time.Second))
	c.append(o, bad, now.Add(time.Second), now.Add(time.Second))
	v = c.snapshot(id, now.Add(time.Second))
	if v.Points[180].RXRate != nil {
		t.Fatal("invalid float")
	}
	if len(c.snapshot(id, now.Add(trafficWindow+2*time.Second)).Points) != 0 {
		t.Fatal("expired read")
	}
	if trafficLogicalBytes() > 16<<20 {
		t.Fatal("logical budget")
	}
	t.Logf("sample=%d series=%d cache=%d logical_reserved=%d slots=%d", unsafe.Sizeof(trafficSample{}), unsafe.Sizeof(trafficSeries{}), unsafe.Sizeof(trafficCache{}), trafficLogicalBytes(), 512*181)
}
func TestTrafficRecentAPIGatesOfflineDirectLifecycleAndCeiling(t *testing.T) {
	a, s, id, token := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	now := time.Unix(12000, 0)
	a.now = func() time.Time { return now }
	postReport(t, a, token, reportFor(id, 1))
	path := webAgentPathPrefix + id + "/traffic/recent"
	for _, bad := range []string{path + "?hours=1", path + "?", strings.Replace(path, id, strings.ToUpper(id), 1)} {
		if bad == path {
			continue
		}
		if r := webAgentResponse(t, a, "GET", bad); r.Code == 200 {
			t.Fatalf("bad path %s", bad)
		}
	}
	if r := webAgentResponse(t, a, "POST", path); r.Code != 405 || r.Header().Get("Allow") != "GET" {
		t.Fatal("method gate")
	}
	if r := webAgentResponse(t, a, "HEAD", path); r.Code != 405 || r.Header().Get("Allow") != "GET" {
		t.Fatal("HEAD gate")
	}
	req := httptest.NewRequest("GET", path, nil)
	req.Host = "probe.test"
	out := httptest.NewRecorder()
	a.Handler().ServeHTTP(out, req)
	if out.Code == 200 {
		t.Fatal("session gate")
	}
	req = httptest.NewRequest("GET", path, strings.NewReader("body"))
	addTestWebSession(t, a, req)
	out = httptest.NewRecorder()
	a.Handler().ServeHTTP(out, req)
	if out.Code != 400 {
		t.Fatal("body accepted")
	}
	now = now.Add(time.Minute)
	v := recentTraffic(t, a, id)
	if v.Status != "offline" || len(v.Points) != 1 {
		t.Fatal("offline filled/dropped")
	}
	s.DisableAgent(context.Background(), id, now)
	v = recentTraffic(t, a, id)
	if v.Status != "paused" || len(v.Points) != 0 {
		t.Fatal("direct lifecycle gate")
	}
	missing := webAgentResponse(t, a, "GET", webAgentPathPrefix+strings.Repeat("f", 32)+"/traffic/recent")
	if missing.Code != 404 {
		t.Fatal("missing gate")
	}
	req = httptest.NewRequest("GET", path, nil)
	addTestWebSession(t, a, req)
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	out = httptest.NewRecorder()
	req.SetPathValue("agent_id", id)
	a.handleTrafficRecent(out, req.WithContext(ctx))
	if out.Body.Len() != 0 {
		t.Fatal("cancel wrote response")
	}
	v.Points = make([]trafficPointView, 181)
	for i := range v.Points {
		n := math.MaxFloat64
		v.Points[i] = trafficPointView{ReceivedAt: math.MaxInt64, CollectedAt: math.MaxInt64, Order: "18446744073709551615", RXRate: &n, TXRate: &n, Reason: "continuity_reset", BreakBefore: true}
	}
	data, _ := json.Marshal(v)
	if len(data) > trafficResponseLimit {
		t.Fatal("valid worst-case DTO exceeds ceiling")
	}
	v.Reason = strings.Repeat("x", trafficResponseLimit)
	out = httptest.NewRecorder()
	writeTrafficRecentJSON(out, httptest.NewRequest("GET", path, nil), v)
	if out.Code != 500 || strings.Contains(out.Body.String(), `"points"`) {
		t.Fatal("partial oversized success")
	}
}

func TestTrafficRecentDoesNotReadMinuteOrSecurityTables(t *testing.T) {
	file := filepath.Join(t.TempDir(), "no-history.sqlite")
	s, err := storage.Open(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	addControlAgent(t, s, controlAgentA, "no-history", time.Now())
	a, err := NewApp(s, 30*time.Second, nil, WithWebAuthentication(WebAuthenticationConfig{PasswordHash: webTestPasswordHash(t), PublicOrigin: "https://probe.test"}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Shutdown()
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"minute_metrics", "agent_security_batches"} {
		if _, err := db.Exec("ALTER TABLE " + table + " RENAME TO unavailable_" + table); err != nil {
			t.Fatal(err)
		}
	}
	v := recentTraffic(t, a, controlAgentA)
	if len(v.Points) != 0 {
		t.Fatal("invented history")
	}
	old := webAgentResponse(t, a, "GET", webAgentPathPrefix+controlAgentA+"/history?hours=1")
	if old.Code != 500 {
		t.Fatal("control did not prove unavailable tables")
	}
}

func TestTrafficRecentActualRemovalReceiptPurgesAndGates(t *testing.T) {
	a, s, id, token := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	r := reportFor(id, 1)
	r.Management = &protocol.AgentManagementCapabilities{RemoteRemoval: true}
	postReport(t, a, token, r)
	owner := a.traffic.capture(id, a.now())
	r.Sequence = 2
	received := a.now()
	state, ok, _, err := s.ProcessReport(context.Background(), id, r, received)
	if err != nil || !ok {
		t.Fatal(err)
	}
	op := "abcdefabcdefabcdefabcdefabcdefab"
	created := webAgentRemovalResponse(t, a, id, `{"operation_id":"`+op+`"}`)
	if created.Code != 202 {
		t.Fatal(created.Body.String())
	}
	claim := postAgentUpgrade(t, a, token, "/api/v1/agent/removals/claim", protocol.AgentRemovalClaimRequest{ProtocolVersion: protocol.AgentRemovalProtocolVersion, AgentEpoch: 1, SessionID: "session"})
	var delivery protocol.AgentRemovalDelivery
	if json.Unmarshal(claim.Body.Bytes(), &delivery) != nil || delivery.ReceiptToken == "" {
		t.Fatal(claim.Body.String())
	}
	progress := postAgentUpgrade(t, a, token, "/api/v1/agent/removals/"+op+"/status", protocol.AgentRemovalStatusRequest{ProtocolVersion: protocol.AgentRemovalProtocolVersion, AgentEpoch: 1, SessionID: "session", Status: protocol.AgentRemovalStatusUninstalling})
	if progress.Code != 200 {
		t.Fatal(progress.Body.String())
	}
	receipt := postAgentUpgrade(t, a, delivery.ReceiptToken, "/api/v1/agent/removals/"+op+"/receipt", protocol.AgentRemovalReceiptRequest{Receipt: protocol.AgentRemovalReceiptKind})
	if receipt.Code != 200 {
		t.Fatal(receipt.Body.String())
	}
	a.publishReportedState(state, owner, received)
	out := webAgentResponse(t, a, "GET", webAgentPathPrefix+id+"/traffic/recent")
	if out.Code != 404 || len(a.traffic.snapshot(id, a.now()).Points) != 0 {
		t.Fatal("deleted late acceptance recreated points")
	}
}
func TestTrafficRecentConcurrentOwners(t *testing.T) {
	c, _ := newTrafficCache()
	var wg sync.WaitGroup
	for j := 0; j < 12; j++ {
		wg.Add(1)
		go func(j int) {
			defer wg.Done()
			id := fmt.Sprint(j)
			for i := 0; i < 100; i++ {
				at := time.Now()
				o := c.capture(id, at)
				c.append(o, trafficState(id, uint64(i+1), at), at, time.Now())
				v := c.snapshot(id, time.Now())
				if len(v.Points) > 181 {
					t.Error("unbounded")
				}
				if i%20 == 0 {
					c.retire(id, time.Now(), false)
				}
			}
		}(j)
	}
	wg.Wait()
}

func TestTrafficRecentCanceledLookupAndResponse(t *testing.T) {
	a, s, id, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	req := httptest.NewRequest("GET", webAgentPathPrefix+id+"/traffic/recent", nil)
	req.SetPathValue("agent_id", id)
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(ctx)
	// Real context becomes cancelled after the handler's entrance check but
	// before the actual Store snapshot transaction, without timing sleeps.
	a.now = func() time.Time { cancel(); return time.Now() }
	out := httptest.NewRecorder()
	a.handleTrafficRecent(out, req)
	if out.Body.Len() != 0 {
		t.Fatal("cancelled DB lookup wrote response")
	}
	out = httptest.NewRecorder()
	writeTrafficRecentJSON(out, req, trafficRecentView{Points: []trafficPointView{}})
	if out.Body.Len() != 0 {
		t.Fatal("cancelled serialization wrote response")
	}
}

func TestTrafficRecentDirectRemovalSurvivesSlotEviction(t *testing.T) {
	a, s, id, token := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	r := reportFor(id, 1)
	r.Management = &protocol.AgentManagementCapabilities{RemoteRemoval: true}
	postReport(t, a, token, r)
	if _, _, err := s.CreateAgentRemovalOperation(context.Background(), id, "abcdefabcdefabcdefabcdefabcdefab", a.now()); err != nil {
		t.Fatal(err)
	}
	// An absent/evicted retired chart slot must not bypass authoritative removal.
	a.traffic, _ = newTrafficCache()
	r.Sequence = 2
	out := postReport(t, a, token, r)
	if out.Code != 200 {
		t.Fatal("optional chart altered ACK", out.Body.String())
	}
	if len(a.traffic.snapshot(id, a.now()).Points) != 0 || recentTraffic(t, a, id).Status != "removing" {
		t.Fatal("direct removal retained samples")
	}
}

// Actual handler+Store regression adapted from the supervisor's deterministic
// late-pause reproduction, not a standalone model of the cache.
func trafficStateHandler(t *testing.T, a *App, id, action string) {
	t.Helper()
	req := httptest.NewRequest("POST", webAgentPathPrefix+id+"/"+action, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("agent_id", id)
	out := httptest.NewRecorder()
	a.handleSetWebAgentDisabled(out, req, action == "disable")
	if out.Code != 200 {
		t.Fatal(out.Body.String())
	}
}
func TestTrafficRecentLatePauseCannotBlockResumedAgent(t *testing.T) {
	a, s, id, token := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	now := time.Unix(16000, 0)
	a.now = func() time.Time { return now }
	postReport(t, a, token, reportFor(id, 1))
	calls := 0
	a.now = func() time.Time {
		calls++
		if calls == 2 {
			record, err := s.GetAgentSnapshot(context.Background(), id, now, 30*time.Second)
			if err != nil || record.Agent.DisabledAt == nil {
				t.Fatal("pause not committed", err)
			}
			a.now = func() time.Time { return now }
			trafficStateHandler(t, a, id, "enable")
		}
		return now
	}
	trafficStateHandler(t, a, id, "disable")
	record, err := s.GetAgentSnapshot(context.Background(), id, now, 30*time.Second)
	if err != nil || record.Agent.DisabledAt != nil {
		t.Fatal("resume not authoritative", err)
	}
	now = now.Add(10 * time.Second)
	out := postReport(t, a, token, reportFor(id, 2))
	if out.Code != 200 {
		t.Fatal(out.Body.String())
	}
	v := recentTraffic(t, a, id)
	if len(v.Points) != 1 || v.Points[0].RXRate != nil {
		t.Fatal("late pause blocked accepted sample")
	}
}
func TestTrafficRecentOldPausedReadCannotPurgeResumedWindow(t *testing.T) {
	a, s, id, token := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	now := time.Unix(17000, 0)
	a.now = func() time.Time { return now }
	postReport(t, a, token, reportFor(id, 1))
	trafficStateHandler(t, a, id, "disable")
	req := httptest.NewRequest("GET", webAgentPathPrefix+id+"/traffic/recent", nil)
	req.SetPathValue("agent_id", id)
	calls := 0
	a.now = func() time.Time {
		calls++
		if calls == 2 {
			a.now = func() time.Time { return now }
			trafficStateHandler(t, a, id, "enable")
			now = now.Add(10 * time.Second)
			out := postReport(t, a, token, reportFor(id, 2))
			if out.Code != 200 {
				t.Fatal(out.Body.String())
			}
		}
		return now
	}
	out := httptest.NewRecorder()
	a.handleTrafficRecent(out, req)
	var old trafficRecentView
	if json.Unmarshal(out.Body.Bytes(), &old) != nil || old.Status != "paused" || len(old.Points) != 0 {
		t.Fatal("old read leaked new points", out.Body.String())
	}
	v := recentTraffic(t, a, id)
	if len(v.Points) != 1 || v.Status != "online" {
		t.Fatal("old read purged newer resume", v)
	}
}

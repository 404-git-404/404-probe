package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"404-probe/internal/storage"
)

func resourceTestApp(t *testing.T) (*App, *storage.Store, time.Time) {
	t.Helper()
	a, s, _ := newWebAgentTestApp(t)
	now := time.Unix(1700000000, 0).Add(12345 * time.Millisecond)
	a.now = func() time.Time { return now }
	t.Cleanup(func() { a.Shutdown(); s.Close() })
	return a, s, now
}
func resourcePath(id string) string { return webAgentPathPrefix + id + "/resources/history" }
func readResourceView(t *testing.T, a *App, path string) webResourceHistoryView {
	t.Helper()
	out := webAgentResponse(t, a, "GET", path)
	if out.Code != 200 || out.Header().Get("Cache-Control") != "no-store" || out.Body.Len() > resourceHistoryResponseLimit {
		t.Fatalf("%s %d %s", path, out.Code, out.Body.String())
	}
	assertNoWebAgentSecrets(t, out.Body.String())
	var v webResourceHistoryView
	if err := json.Unmarshal(out.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestResourceHistoryAuthenticatedHoursAndEmpty(t *testing.T) {
	a, s, now := resourceTestApp(t)
	processControlAgentReport(t, s, controlAgentA, now.Add(-10*time.Second))
	for _, hours := range []int{1, 6, 24} {
		v := readResourceView(t, a, resourcePath(controlAgentA)+fmt.Sprintf("?hours=%d", hours))
		from := now.Add(-time.Duration(hours) * time.Hour).UnixMilli()
		if v.AgentID != controlAgentA || v.ServerNow != now.UnixMilli() || v.From != from || v.To != now.UnixMilli() || v.BucketFrom != from/60000*60000 || v.BucketTo != now.UnixMilli()/60000*60000 || v.IntervalMS != 60000 || v.PointLimit != 1441 || v.Source != "minute_metrics" || v.Aggregation != "arithmetic_mean" || v.RetentionDays != 30 || v.Coverage != "partial" || len(v.Points) != 1 {
			t.Fatalf("%+v", v)
		}
		original, err := s.History(context.Background(), controlAgentA, time.UnixMilli(from))
		if err != nil || !reflect.DeepEqual(v.Points[0], newWebAgentHistoryPointView(original[0])) {
			t.Fatal("original means differ", err)
		}
		if v.OldestTimestamp == nil || v.LastTimestamp == nil || *v.OldestTimestamp != v.Points[0].Timestamp || *v.LastTimestamp != v.Points[0].Timestamp {
			t.Fatal("actual timestamps")
		}
	}
	defaultView := readResourceView(t, a, resourcePath(controlAgentA))
	if defaultView.From != now.Add(-time.Hour).UnixMilli() {
		t.Fatal("default")
	}
	empty := readResourceView(t, a, resourcePath(controlAgentB))
	if empty.Points == nil || len(empty.Points) != 0 || empty.OldestTimestamp != nil || empty.LastTimestamp != nil {
		t.Fatal("empty", empty)
	}
	// Paused/offline and revoked records retain truthful historical data, no status invented.
	if _, err := s.DisableAgent(context.Background(), controlAgentA, now); err != nil {
		t.Fatal(err)
	}
	if v := readResourceView(t, a, resourcePath(controlAgentA)); len(v.Points) != 1 {
		t.Fatal("paused history lost")
	}
	if _, err := s.RevokeAgent(context.Background(), controlAgentA, now); err != nil {
		t.Fatal(err)
	}
	if v := readResourceView(t, a, resourcePath(controlAgentA)); len(v.Points) != 1 {
		t.Fatal("revoked history lost")
	}
}

func TestResourceHistoryAuthenticatedQueryAndRequestGuards(t *testing.T) {
	a, _, now := resourceTestApp(t)
	base := resourcePath(controlAgentA)
	n := now.UnixMilli()
	from := n - 3600000
	custom := fmt.Sprintf("from=%d&to=%d", from, n)
	invalid := []string{"?", "?hours=0", "?hours=25", "?hours=-1", "?hours=1.5", "?hours=1e1", "?hours=", "?hours=1&hours=1", "?unknown=1", "?from=1", "?to=1", "?hours=1&" + custom, "?" + custom + "&from=1", "?from=-1&to=2", "?from=01&to=2", "?from=%2B1&to=2", "?from=9223372036854775808&to=2", "?from=１&to=2", "?from=1.0&to=2", "?from=&to=2", "?from=%ZZ&to=2", fmt.Sprintf("?from=%d&to=%d", n, n), fmt.Sprintf("?from=%d&to=%d", n, n-1), fmt.Sprintf("?from=%d&to=%d", n-1, n+1), fmt.Sprintf("?from=%d&to=%d", n-86400001, n), fmt.Sprintf("?from=%d&to=%d", n-30*86400000-1, n-30*86400000+1)}
	for _, q := range invalid {
		t.Run(q, func(t *testing.T) {
			out := webAgentResponse(t, a, "GET", base+q)
			if out.Code != 400 || out.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("%d %s", out.Code, out.Body.String())
			}
		})
	}
	for _, q := range []string{"?hours=01", "?hours=%2B1", "?" + custom, fmt.Sprintf("?from=%d&to=%d", n-30*86400000, n-30*86400000+1)} {
		readResourceView(t, a, base+q)
	}
	for _, method := range []string{"HEAD", "POST", "PUT", "DELETE"} {
		out := webAgentResponse(t, a, method, base)
		if out.Code != 405 || out.Header().Get("Allow") != "GET" {
			t.Fatal(method, out.Code)
		}
	}
	for _, path := range []string{resourcePath("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"), strings.Replace(base, "/aaaa", "/%61aaa", 1)} {
		if out := webAgentResponse(t, a, "GET", path); out.Code != 400 {
			t.Fatal(path, out.Code)
		}
	}
	if out := webAgentResponse(t, a, "GET", resourcePath("dddddddddddddddddddddddddddddddd")); out.Code != 404 {
		t.Fatal("unknown", out.Code)
	}
	unauth := httptest.NewRequest("GET", base, nil)
	unauth.Host = "probe.test"
	out := httptest.NewRecorder()
	a.Handler().ServeHTTP(out, unauth)
	if out.Code != 401 {
		t.Fatal("session guard", out.Code)
	}
	for _, transfer := range []bool{false, true} {
		r := httptest.NewRequest("GET", base, strings.NewReader("x"))
		if transfer {
			r.ContentLength = 0
			r.TransferEncoding = []string{"chunked"}
		}
		addTestWebSession(t, a, r)
		out := httptest.NewRecorder()
		a.Handler().ServeHTTP(out, r)
		if out.Code != 400 {
			t.Fatal("body", out.Code)
		}
	}
}

func TestResourceHistoryActualSQLRouteEdgesLimitAndNoSecurity(t *testing.T) {
	file := filepath.Join(t.TempDir(), "resources.sqlite")
	s, err := storage.Open(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Unix(1800000000, 0).Add(12345 * time.Millisecond)
	addControlAgent(t, s, controlAgentA, "resources", now.Add(-time.Hour))
	a, err := NewApp(s, 30*time.Second, nil, WithWebAuthentication(WebAuthenticationConfig{PasswordHash: webTestPasswordHash(t), PublicOrigin: "https://probe.test"}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Shutdown()
	a.now = func() time.Time { return now }
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO minute_metrics(agent_id,bucket,samples,cpu_sum,ram_sum,swap_sum,disk_sum,load1_sum,load5_sum,load15_sum,rx_rate_sum,tx_rate_sum,rx_total,tx_total) VALUES(?,?,2,24,100,0,40,2,4,6,2000,4000,9007199254740993,42)`)
	if err != nil {
		t.Fatal(err)
	}
	base := now.UnixMilli()/60000*60000 - 86400000
	for i := 6440; i >= 0; i-- {
		if _, err := stmt.Exec(controlAgentA, base+int64(i)*60000); err != nil {
			t.Fatal(err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	v := readResourceView(t, a, resourcePath(controlAgentA)+"?hours=24")
	if len(v.Points) != 1441 {
		t.Fatal(len(v.Points))
	}
	for i, p := range v.Points {
		if p.Timestamp != base+int64(i)*60000 || p.CPU != 12 || p.RXRate != 1000 {
			t.Fatal(i, p)
		}
	}
	if old := webAgentResponse(t, a, "GET", webAgentPathPrefix+controlAgentA+"/history?hours=1"); old.Code != 200 {
		t.Fatal("legacy before", old.Code)
	}
	if _, err := db.Exec("ALTER TABLE agent_security_batches RENAME TO unavailable_security"); err != nil {
		t.Fatal(err)
	}
	readResourceView(t, a, resourcePath(controlAgentA))
	if old := webAgentResponse(t, a, "GET", webAgentPathPrefix+controlAgentA+"/history?hours=1"); old.Code != 500 {
		t.Fatal("legacy security behavior changed", old.Code)
	}
	if _, err := db.Exec("DELETE FROM minute_metrics WHERE bucket=?", base+60000); err != nil {
		t.Fatal(err)
	}
	v = readResourceView(t, a, resourcePath(controlAgentA)+fmt.Sprintf("?from=%d&to=%d", base+59999, base+120001))
	if len(v.Points) != 2 || v.Points[0].Timestamp != base || v.Points[1].Timestamp != base+120000 {
		t.Fatal("edge gaps", v)
	}
	if _, err := db.Exec("UPDATE minute_metrics SET samples=0 WHERE bucket=?", base); err != nil {
		t.Fatal(err)
	}
	out := webAgentResponse(t, a, "GET", resourcePath(controlAgentA)+"?hours=24")
	if out.Code != 500 || strings.Contains(out.Body.String(), `"points"`) {
		t.Fatal("scan failure success", out.Code)
	}
	if _, err := db.Exec("UPDATE minute_metrics SET samples=2,cpu_sum=1e999 WHERE bucket=?", base); err != nil {
		t.Fatal(err)
	}
	out = webAgentResponse(t, a, "GET", resourcePath(controlAgentA)+"?hours=24")
	if out.Code != 500 || jobErrorCode(t, out) != "response_too_large" || strings.Contains(out.Body.String(), `"points"`) {
		t.Fatal("real SQL nonfinite marshal failure", out.Code, out.Body.String())
	}
	if _, err := db.Exec("ALTER TABLE minute_metrics RENAME TO unavailable_minutes"); err != nil {
		t.Fatal(err)
	}
	if out := webAgentResponse(t, a, "GET", resourcePath(controlAgentA)); out.Code != 500 {
		t.Fatal("query failure", out.Code)
	}
}

type resourceWriteTracker struct {
	header http.Header
	writes int
}

func (w *resourceWriteTracker) Header() http.Header         { return w.header }
func (w *resourceWriteTracker) WriteHeader(int)             { w.writes++ }
func (w *resourceWriteTracker) Write(p []byte) (int, error) { w.writes++; return len(p), nil }

func TestResourceHistoryAuthenticatedCancellationAndOneHandlerNow(t *testing.T) {
	a, _, now := resourceTestApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest("GET", resourcePath(controlAgentA), nil).WithContext(ctx)
	addTestWebSession(t, a, r)
	calls := 0
	a.now = func() time.Time {
		calls++
		if calls == 2 {
			cancel()
		}
		return now
	}
	out := &resourceWriteTracker{header: make(http.Header)}
	a.Handler().ServeHTTP(out, r)
	if calls != 2 || out.writes != 0 {
		t.Fatalf("now=%d writes=%d", calls, out.writes)
	}
	a.now = func() time.Time { return now }
	readResourceView(t, a, resourcePath(controlAgentA))
	r = httptest.NewRequest("GET", resourcePath(controlAgentA), nil)
	addTestWebSession(t, a, r)
	calls = 0
	a.now = func() time.Time { calls++; return now.Add(time.Duration(calls-1) * time.Millisecond) }
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, r)
	var v webResourceHistoryView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || rec.Code != 200 || calls != 2 || v.ServerNow != now.Add(time.Millisecond).UnixMilli() {
		t.Fatal("single handler now", calls, v, err)
	}
}

func TestResourceHistorySerializationBoundAndCancellation(t *testing.T) {
	r := httptest.NewRequest("GET", resourcePath(controlAgentA), nil)
	v := webResourceHistoryView{AgentID: controlAgentA, Points: make([]webAgentHistoryPointView, 1441)}
	for i := range v.Points {
		v.Points[i] = newWebAgentHistoryPointView(storage.HistoryPoint{Timestamp: math.MaxInt64, CPU: math.MaxFloat64, RAMPercent: math.MaxFloat64, SwapPercent: math.MaxFloat64, DiskPercent: math.MaxFloat64, Load1: math.MaxFloat64, Load5: math.MaxFloat64, Load15: math.MaxFloat64, RXRate: math.MaxFloat64, TXRate: math.MaxFloat64, RXTotal: math.MaxUint64, TXTotal: math.MaxUint64})
	}
	out := httptest.NewRecorder()
	writeResourceHistoryJSON(out, r, v)
	if out.Code != 200 || out.Body.Len() > resourceHistoryResponseLimit {
		t.Fatal("bounded maximum", out.Code, out.Body.Len())
	}
	for _, bad := range []webResourceHistoryView{{AgentID: strings.Repeat("x", resourceHistoryResponseLimit)}, {Points: []webAgentHistoryPointView{newWebAgentHistoryPointView(storage.HistoryPoint{CPU: math.NaN()})}}} {
		out := httptest.NewRecorder()
		writeResourceHistoryJSON(out, r, bad)
		if out.Code != 500 || strings.Contains(out.Body.String(), `"points"`) {
			t.Fatal("partial serialization", out.Code, out.Body.String())
		}
	}
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	tracker := &resourceWriteTracker{header: make(http.Header)}
	writeResourceHistoryJSON(tracker, r.WithContext(ctx), v)
	if tracker.writes != 0 {
		t.Fatal("late write")
	}
}

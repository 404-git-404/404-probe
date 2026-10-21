package server

import (
	"404-probe/internal/auth"
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// While the device-level authorization could not be recorded, a session that finished both
// negotiation steps still gets no user address at all, in the inline configuration or in the pulled
// one, not even in paused targets. The resource report is acknowledged and its sequence continues.
func TestR07FailedGrantDeliversNoUserAddress(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "grant-failure.db")
	store, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id, _ := auth.NewID()
	token, hash, _ := auth.NewToken()
	if err := store.AddAgent(ctx, id, "grant-failure", hash, time.Now()); err != nil {
		t.Fatal(err)
	}
	a, err := NewApp(store, 30*time.Second, nil, WithWebAuthentication(WebAuthenticationConfig{PasswordHash: webTestPasswordHash(t), PublicOrigin: "https://probe.test"}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Shutdown()
	admin, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	oldHeaders := map[string]string{protocol.QualityVersionHeader: "2", protocol.QualitySemanticsHeader: protocol.QualityV2Semantics}
	var epoch, seq uint64 = 1, 0
	session := "session-1"
	send := func(headers map[string]string, caps *protocol.QualityCapabilities) (protocol.ReportResponse, string) {
		t.Helper()
		seq++
		r := reportFor(id, seq)
		r.Epoch, r.SessionID, r.NetworkQuality, r.NetworkQualityV2 = epoch, session, true, caps
		w := postWithHeaders(t, a, token, "/api/v1/report", r, headers)
		response := decodeReportResponse(t, w)
		if w.Code != 200 || !response.Accepted {
			t.Fatalf("resource report not acknowledged: %d %s", w.Code, w.Body.String())
		}
		var stored uint64
		if err := admin.QueryRow(`SELECT sequence FROM agent_state WHERE agent_id=?`, id).Scan(&stored); err != nil || stored != seq {
			t.Fatalf("resource sequence %d, want %d (%v)", stored, seq, err)
		}
		return response, w.Body.String()
	}
	pull := func() (int, string) {
		t.Helper()
		w := postWithHeaders(t, a, token, "/api/v1/agent/network-quality/config", protocol.QualityConfigRequest{Version: 2, Epoch: "2", SessionID: session}, userHeaders)
		return w.Code, w.Body.String()
	}
	noAddress := func(what, body string) {
		t.Helper()
		if containsAny(body, "example.com", "example.org", "example.net", `"host"`, `"port":443`) {
			t.Fatalf("%s: a user address was delivered: %s", what, body)
		}
	}

	// An old Agent saved targets before it was upgraded.
	send(oldHeaders, a1ServerCapabilities())
	configureUserTargets(t, store, id, a.now(), defaultUserHosts, 443)
	epoch, session, seq = 2, "session-2", 0
	send(userHeaders, nil) // step one

	if _, err := admin.Exec(`CREATE TRIGGER inject_grant_failure BEFORE UPDATE ON quality_config BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	response, body := send(userHeaders, userCapabilities()) // step two; the recalculation fails
	if response.NetworkQuality != nil {
		wire := string(mustJSON(t, response.NetworkQuality))
		noAddress("inline", wire)
		if response.NetworkQuality.Enabled || len(response.NetworkQuality.Targets) != 0 {
			t.Fatalf("inline configuration is not the stop configuration: %s", wire)
		}
	}
	noAddress("inline response", body)
	code, pulled := pull()
	if code != 200 {
		t.Fatal(code, pulled)
	}
	noAddress("pull", pulled)
	if openLanes(t, a, id) != 0 {
		cfg, _ := a.store.GetQualityConfigV4(ctx, id, a.now())
		t.Fatalf("lanes opened without a recorded authorization: %+v", cfg)
	}

	// The failure goes away; the next report finishes the job and the addresses flow.
	if _, err := admin.Exec(`DROP TRIGGER inject_grant_failure`); err != nil {
		t.Fatal(err)
	}
	response, _ = send(userHeaders, userCapabilities())
	if response.NetworkQuality == nil || len(response.NetworkQuality.Targets) == 0 || openLanes(t, a, id) != 6 {
		t.Fatalf("authorization not restored: lanes=%d", openLanes(t, a, id))
	}
	if code, pulled = pull(); code != 200 || !strings.Contains(pulled, "example.com") {
		t.Fatalf("authorized pull: %d %s", code, pulled)
	}
}

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"404-probe/internal/buildinfo"
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

type rejectUpgradeTransport struct{ t *testing.T }

func (r rejectUpgradeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.t.Errorf("unexpected outbound upgrade HTTP request: %s", req.URL)
	return nil, errors.New("upgrade HTTP forbidden in same-build tests")
}

func rejectUpgradeHTTP(t *testing.T) {
	t.Helper()
	previous := http.DefaultTransport
	http.DefaultTransport = rejectUpgradeTransport{t}
	t.Cleanup(func() { http.DefaultTransport = previous })
}

func TestSameBuildUpgradeVersionRelationsWithoutHTTP(t *testing.T) {
	rejectUpgradeHTTP(t)
	for _, release := range []struct{ name, version, lower, higher, channel string }{
		{"stable", "v1.0.8", "v1.0.7", "v1.0.9", "stable"},
		{"rc", "v1.0.8-rc.2", "v1.0.8-rc.1", "v1.0.8-rc.3", "beta"},
		{"beta", "v1.0.8-beta.2", "v1.0.8-beta.1", "v1.0.8-beta.3", "beta"},
	} {
		for _, relation := range []struct {
			name, version, reason string
			supported             bool
		}{
			{"lower", release.lower, "", true},
			{"equal", release.version, "已是最新版本", false},
			{"higher", release.higher, "Agent 版本高于 Server，不能回退；如需回退请重新安装", false},
		} {
			t.Run(release.name+"/"+relation.name, func(t *testing.T) {
				app, store, id, token := testApp(t)
				defer store.Close()
				defer app.Shutdown()
				app.buildInfo = buildinfo.Info{Version: release.version, Commit: strings.Repeat("a", 40)}
				r := reportFor(id, 1)
				r.AgentVersion, r.AgentUpgradeCapable, r.AgentUpgradeV2 = relation.version, true, true
				if _, accepted, _, err := store.ProcessReport(context.Background(), id, r, app.now()); err != nil || !accepted {
					t.Fatal(accepted, err)
				}
				w := webAgentResponse(t, app, "GET", webAgentPathPrefix+id+"/upgrade-targets")
				var catalog struct {
					Targets []upgradeTarget `json:"targets"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil || w.Code != 200 || len(catalog.Targets) != 1 {
					t.Fatal(w.Code, w.Body.String(), err)
				}
				target := catalog.Targets[0]
				if target.Version != release.version || target.Channel != release.channel || target.RequiredProtocol != 2 || target.Supported != relation.supported || target.Reason != relation.reason {
					t.Fatalf("target=%+v", target)
				}
				t.Logf("GET targets: %s", w.Body.String())
				body := `{}`
				if release.channel == "beta" {
					body = fmt.Sprintf(`{"channel":"beta","target_version":%q,"confirm_beta":true}`, release.version)
				}
				w = webUpgradeResponse(t, app, id, body)
				if !relation.supported {
					if w.Code != 409 || jobErrorCode(t, w) != "upgrade_not_available" {
						t.Fatal(w.Code, w.Body.String())
					}
					if op, err := store.GetLatestUpgrade(context.Background(), id); err != nil || op != nil {
						t.Fatal(op, err)
					}
					return
				}
				if w.Code != 201 {
					t.Fatal(w.Code, w.Body.String())
				}
				w = postAgentUpgrade(t, app, token, "/api/v1/agent/upgrades/claim", protocol.UpgradeClaimRequest{ProtocolVersion: 2})
				var op protocol.UpgradeOperation
				if err := protocol.DecodeUpgradeJSON(w.Body.Bytes(), &op); err != nil || w.Code != 200 || op.Validate() != nil || op.TargetVersion != app.buildInfo.Version || op.TargetCommit != app.buildInfo.Commit || op.ServerVersion != app.buildInfo.Version || op.ProtocolVersion != 2 || op.Channel != release.channel {
					t.Fatal(w.Code, w.Body.String(), err)
				}

			})
		}
	}
}

func TestSameBuildUpgradeRejectsTamperedStableTargetWithoutHTTP(t *testing.T) {
	rejectUpgradeHTTP(t)
	app, store, id := newWebAuthenticationTestApp(t)
	defer store.Close()
	defer app.Shutdown()
	app.buildInfo = buildinfo.Info{Version: "v1.0.8", Commit: strings.Repeat("a", 40)}
	r := reportFor(id, 1)
	r.AgentVersion, r.AgentUpgradeCapable, r.AgentUpgradeV2 = "v1.0.7", true, true
	if _, _, _, err := store.ProcessReport(context.Background(), id, r, app.now()); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"channel":"stable","target_version":"v1.0.9"}`,
		`{"channel":"stable","target_version":"v1.0.7"}`,
		`{"channel":"beta","target_version":"v1.0.8-beta.1","confirm_beta":true}`,
		`{"channel":"stable","target_version":"v1.0.8","target_commit":"` + strings.Repeat("b", 40) + `"}`,
		`{"target_commit":"` + strings.Repeat("b", 40) + `"}`,
	} {
		w := webUpgradeResponse(t, app, id, body)
		if w.Code != 400 || jobErrorCode(t, w) != "invalid_request" {
			t.Fatalf("accepted %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	if op, err := store.GetLatestUpgrade(context.Background(), id); err != nil || op != nil {
		t.Fatal(op, err)
	}
	if w := webUpgradeResponse(t, app, id, `{"channel":"stable","target_version":"v1.0.8"}`); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	op, err := store.ClaimUpgrade(context.Background(), id, app.now(), 2)
	if err != nil || op == nil || op.TargetVersion != app.buildInfo.Version || op.TargetCommit != app.buildInfo.Commit || op.ServerVersion != app.buildInfo.Version {
		t.Fatal(op, err)
	}
}

func TestSameBuildIneligibleServerWithoutHTTP(t *testing.T) {
	rejectUpgradeHTTP(t)
	for _, info := range []buildinfo.Info{
		{Version: "dev", Commit: strings.Repeat("a", 40)},
		{Version: "v1.0.8", Commit: strings.Repeat("a", 40), Dirty: true},
		{Version: "v1.0.8"},
		{Version: "v1.0.8", Commit: "abc"},
		{Version: "v1.0.8", Commit: strings.Repeat("A", 40)},
		{Version: "v1.0.8-beta.2", Commit: "invalid"},
	} {
		app, store, id := newWebAuthenticationTestApp(t)
		app.buildInfo = info
		makeUpgradeableAgent(t, app, store, id)
		w := webAgentResponse(t, app, "GET", webAgentPathPrefix+id+"/upgrade-targets")
		var catalog struct {
			Targets []upgradeTarget `json:"targets"`
			Message string          `json:"message"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil || w.Code != 200 || len(catalog.Targets) != 0 || catalog.Message != "当前 Server 构建不支持远程升级" {
			t.Fatal(w.Code, w.Body.String(), err)
		}
		if w := webUpgradeResponse(t, app, id, `{}`); w.Code != 409 || jobErrorCode(t, w) != "server_build_ineligible" {
			t.Fatal(w.Code, w.Body.String())
		}
		if info.Version == "v1.0.8-beta.2" {
			if w := webUpgradeResponse(t, app, id, `{"channel":"beta","target_version":"v1.0.8-beta.2","confirm_beta":true}`); w.Code != 409 || jobErrorCode(t, w) != "server_build_ineligible" {
				t.Fatal(w.Code, w.Body.String())
			}
		}
		if op, err := store.GetLatestUpgrade(context.Background(), id); err != nil || op != nil {
			t.Fatal(op, err)
		}
		app.Shutdown()
		store.Close()
	}
}

func TestSameBuildPreservesAgentGatesWithoutHTTP(t *testing.T) {
	rejectUpgradeHTTP(t)
	for _, gate := range []struct {
		name    string
		prepare func(*App, *storage.Store, string, *protocol.Report)
	}{
		{"updater unavailable", func(_ *App, _ *storage.Store, _ string, r *protocol.Report) { r.AgentUpgradeCapable = false }},
		{"old protocol", func(_ *App, _ *storage.Store, _ string, r *protocol.Report) { r.AgentUpgradeV2 = false }},
		{"old Beta", func(_ *App, _ *storage.Store, _ string, r *protocol.Report) {
			r.AgentVersion = "v1.0.1-beta.1"
			r.AgentUpgradeV2 = false
		}},
		{"RC without helper", func(_ *App, _ *storage.Store, _ string, r *protocol.Report) {
			r.AgentVersion = "v1.0.3-rc.1"
			r.AgentUpgradeV2 = false
		}},
		{"offline", func(app *App, _ *storage.Store, _ string, _ *protocol.Report) {
			later := app.now().Add(time.Minute)
			app.now = func() time.Time { return later }
		}},
		{"paused", func(app *App, s *storage.Store, id string, _ *protocol.Report) {
			if _, err := s.DisableAgent(context.Background(), id, app.now()); err != nil {
				t.Fatal(err)
			}
		}},
		{"revoked", func(app *App, s *storage.Store, id string, _ *protocol.Report) {
			if _, err := s.RevokeAgent(context.Background(), id, app.now()); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(gate.name, func(t *testing.T) {
			app, store, id := newWebAuthenticationTestApp(t)
			defer store.Close()
			defer app.Shutdown()
			app.buildInfo = buildinfo.Info{Version: "v1.0.8", Commit: strings.Repeat("a", 40)}
			r := reportFor(id, 1)
			r.AgentVersion, r.AgentUpgradeCapable, r.AgentUpgradeV2 = "v1.0.7", true, true
			// Apply state gates before reporting; lifecycle gates after the report.
			if gate.name != "paused" && gate.name != "revoked" && gate.name != "offline" {
				gate.prepare(app, store, id, &r)
			}
			if _, accepted, _, err := store.ProcessReport(context.Background(), id, r, app.now()); err != nil || !accepted {
				t.Fatal(accepted, err)
			}
			if gate.name == "paused" || gate.name == "revoked" || gate.name == "offline" {
				gate.prepare(app, store, id, &r)
			}
			w := webAgentResponse(t, app, "GET", webAgentPathPrefix+id+"/upgrade-targets")
			var catalog struct {
				Targets []upgradeTarget `json:"targets"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil || w.Code != 200 || len(catalog.Targets) != 1 || catalog.Targets[0].Supported || catalog.Targets[0].Reason == "" {
				t.Fatal(w.Code, w.Body.String(), err)
			}
			if w := webUpgradeResponse(t, app, id, `{}`); w.Code != 409 {
				t.Fatal(w.Code, w.Body.String())
			}
			if op, err := store.GetLatestUpgrade(context.Background(), id); err != nil || op != nil {
				t.Fatal(op, err)
			}
		})
	}
}

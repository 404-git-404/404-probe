package server

import (
	"404-probe/internal/buildinfo"
	"404-probe/internal/storage"
	"context"
	"strings"
	"testing"
)

func TestUpgradeExplicitInputFailsClosed(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `{"channel":"beta","target_version":"v1.0.2-beta.1"}`, `{"channel":"beta","target_version":"v1.0.2","confirm_beta":true}`, `{"channel":"stable","target_version":"v1.0.2","confirm_beta":false}`, `{"channel":"stable","target_version":"v1.0.2","channel":"beta","confirm_beta":true}`, `{"url":"https://evil.test/"}`, `{"channel":null}`} {
		if _, err := decodeUpgradeInput([]byte(body)); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	for _, body := range []string{`{}`, `{"channel":"beta","target_version":"v1.0.2-beta.1","confirm_beta":true}`, `{"channel":"stable","target_version":"v1.0.2"}`} {
		if _, err := decodeUpgradeInput([]byte(body)); err != nil {
			t.Fatal(body, err)
		}
	}
}

func TestWebExplicitBetaAuthorizationAndLegacyClaimIsolation(t *testing.T) {
	rejectUpgradeHTTP(t)
	app, store, id := newWebAuthenticationTestApp(t)
	defer store.Close()
	defer app.Shutdown()
	app.buildInfo = buildinfo.Info{Version: "v1.0.8-beta.2", Commit: strings.Repeat("a", 40)}
	report := reportFor(id, 1)
	report.AgentVersion = "v1.0.8-beta.1"
	report.AgentUpgradeCapable = true
	if _, _, _, err := store.ProcessReport(context.Background(), id, report, app.now()); err != nil {
		t.Fatal(err)
	}
	body := `{"channel":"beta","target_version":"v1.0.8-beta.2","confirm_beta":true}`
	if w := webUpgradeResponse(t, app, id, body); w.Code != 409 {
		t.Fatalf("legacy accepted: %d %s", w.Code, w.Body.String())
	}
	if operation, err := store.GetLatestUpgrade(context.Background(), id); err != nil || operation != nil {
		t.Fatalf("legacy created task %+v %v", operation, err)
	}
	report.Sequence++
	report.AgentUpgradeV2 = true
	if _, _, _, err := store.ProcessReport(context.Background(), id, report, app.now()); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []struct {
		body   string
		status int
		code   string
	}{
		{`{}`, 409, "server_build_ineligible"},
		{`{"channel":"beta","target_version":"v1.0.8-beta.2"}`, 400, "invalid_request"},
		{`{"channel":"beta","target_version":"v1.0.8-beta.3","confirm_beta":true}`, 400, "invalid_request"},
		{`{"channel":"beta","target_version":"v1.0.8-beta.2","target_commit":"` + strings.Repeat("b", 40) + `","confirm_beta":true}`, 400, "invalid_request"},
	} {
		if w := webUpgradeResponse(t, app, id, invalid.body); w.Code != invalid.status || jobErrorCode(t, w) != invalid.code {
			t.Fatalf("different/unconfirmed Beta build accepted: %d %s", w.Code, w.Body.String())
		}
	}
	if operation, err := store.GetLatestUpgrade(context.Background(), id); err != nil || operation != nil {
		t.Fatal(operation, err)
	}
	if w := webUpgradeResponse(t, app, id, body); w.Code != 201 {
		t.Fatalf("V2 rejected: %d %s", w.Code, w.Body.String())
	}
	if operation, err := store.ClaimUpgrade(context.Background(), id, app.now(), 1); err != nil || operation != nil {
		t.Fatalf("legacy claimed V2 %+v %v", operation, err)
	}
	operation, err := store.ClaimUpgrade(context.Background(), id, app.now(), 2)
	if err != nil || operation == nil || operation.Status != storage.UpgradeClaimed || !operation.BetaConfirmed || operation.TargetVersion != app.buildInfo.Version || operation.TargetCommit != app.buildInfo.Commit || operation.ServerVersion != app.buildInfo.Version {
		t.Fatalf("authorization %+v %v", operation, err)
	}
}

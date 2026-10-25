package server

import (
	"404-probe/internal/buildinfo"
	"404-probe/internal/storage"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRCServerExplicitConfirmationAndAgentGate(t *testing.T) {
	for _, body := range []string{`{"channel":"beta","target_version":"v1.0.3-rc.1"}`, `{"channel":"stable","target_version":"v1.0.3-rc.1"}`} {
		if _, e := decodeUpgradeInput([]byte(body)); e == nil {
			t.Fatal(body)
		}
	}
	if _, e := decodeUpgradeInput([]byte(`{"channel":"beta","target_version":"v1.0.3-rc.1","confirm_beta":true}`)); e != nil {
		t.Fatal(e)
	}
	target := upgradeTarget{Version: "v1.0.3-rc.2", Channel: "beta", RequiredProtocol: 2, Supported: true}
	record := storage.AgentSnapshot{Online: true, State: &storage.State{AgentVersion: "v1.0.2", AgentUpgradeCapable: true, AgentUpgradeV2: true}}
	if targetForAgent(target, record).Supported {
		t.Fatal("old helper offered RC")
	}
	record.State.AgentVersion = "v1.0.3-rc.1"
	if !targetForAgent(target, record).Supported {
		t.Fatal("RC-aware agent blocked")
	}
	record.State.AgentUpgradeV2 = false
	if targetForAgent(target, record).Supported {
		t.Fatal("RC agent without helper proof accepted")
	}
	a, s, id := newWebAuthenticationTestApp(t)
	defer s.Close()
	defer a.Shutdown()
	a.buildInfo = buildinfo.Info{Version: "v1.0.3-rc.1", Commit: strings.Repeat("a", 40)}
	r := reportFor(id, 1)
	r.AgentVersion = "v1.0.2"
	r.AgentUpgradeCapable = true
	r.AgentUpgradeV2 = true
	if _, ok, _, e := s.ProcessReport(context.Background(), id, r, a.now()); e != nil || !ok {
		t.Fatal("old Agent communication", ok, e)
	}
	if w := webUpgradeResponse(t, a, id, `{}`); w.Code != 409 || jobErrorCode(t, w) != "server_build_ineligible" {
		t.Fatal("RC server default created upgrade")
	}
}

func TestRCOfficialCatalogAndExplicitCreation(t *testing.T) {
	rejectUpgradeHTTP(t)
	a, s, id := newWebAuthenticationTestApp(t)
	defer s.Close()
	defer a.Shutdown()
	a.buildInfo = buildinfo.Info{Version: "v1.0.3-rc.2", Commit: strings.Repeat("a", 40)}
	r := reportFor(id, 1)
	r.AgentVersion = "v1.0.2"
	r.AgentUpgradeCapable = true
	r.AgentUpgradeV2 = true
	if _, _, _, err := s.ProcessReport(context.Background(), id, r, a.now()); err != nil {
		t.Fatal(err)
	}
	body := `{"channel":"beta","target_version":"v1.0.3-rc.2","confirm_beta":true}`
	if w := webUpgradeResponse(t, a, id, body); w.Code != 409 {
		t.Fatal("old agent got RC", w.Code, w.Body.String())
	}
	r.Sequence++
	r.AgentVersion = buildinfo.FirstRCSupportVersion
	if _, _, _, err := s.ProcessReport(context.Background(), id, r, a.now()); err != nil {
		t.Fatal(err)
	}
	w := webAgentResponse(t, a, "GET", webAgentPathPrefix+id+"/upgrade-targets")
	var catalog struct {
		Targets []upgradeTarget `json:"targets"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); w.Code != 200 || err != nil || len(catalog.Targets) != 1 || catalog.Targets[0].Version != a.buildInfo.Version || catalog.Targets[0].Channel != "beta" || !catalog.Targets[0].Supported {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	for _, invalid := range []string{
		`{"channel":"beta","target_version":"v1.0.3-rc.3","confirm_beta":true}`,
		`{"channel":"beta","target_version":"v1.0.3-rc.2","target_commit":"` + strings.Repeat("b", 40) + `","confirm_beta":true}`,
	} {
		if w := webUpgradeResponse(t, a, id, invalid); w.Code != 400 {
			t.Fatal("different RC build accepted", w.Code, w.Body.String())
		}
	}
	if op, err := s.GetLatestUpgrade(context.Background(), id); err != nil || op != nil {
		t.Fatal(op, err)
	}
	if w = webUpgradeResponse(t, a, id, body); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	op, err := s.ClaimUpgrade(context.Background(), id, a.now(), 2)
	if err != nil || op == nil || op.TargetVersion != a.buildInfo.Version || op.TargetCommit != a.buildInfo.Commit || !op.BetaConfirmed || op.ServerVersion != a.buildInfo.Version {
		t.Fatal(op, err)
	}
}

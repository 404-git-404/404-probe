package server

import (
	"404-probe/internal/buildinfo"
	"404-probe/internal/releasemetadata"
	"404-probe/internal/storage"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	a, s, id := newWebAuthenticationTestApp(t)
	defer s.Close()
	defer a.Shutdown()
	a.buildInfo = buildinfo.Info{Version: buildinfo.FirstRCSupportVersion, Commit: strings.Repeat("a", 40)}
	version := "v1.0.3-rc.2"
	d := releasemetadata.Document{SchemaVersion: 2, Version: version, Commit: strings.Repeat("b", 40), Compatibility: &releasemetadata.Compatibility{MinServerVersion: buildinfo.FirstRCSupportVersion, UpgradeProtocol: 2}}
	for _, arch := range []string{"amd64", "arm64"} {
		for _, role := range []string{"agent", "server"} {
			d.Assets = append(d.Assets, releasemetadata.Asset{Name: "404-probe-" + role + "-linux-" + arch, GOOS: "linux", GOARCH: arch, SHA256: strings.Repeat("c", 64)})
		}
	}
	meta, e := releasemetadata.Encode(d)
	if e != nil {
		t.Fatal(e)
	}
	hash := sha256.Sum256(meta)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api":
			fmt.Fprintf(w, `[{"tag_name":%q,"prerelease":true}]`, version)
		case "/api/tags/" + version:
			fmt.Fprintf(w, `{"tag_name":%q,"prerelease":true,"assets":[{"name":"RELEASE-METADATA.json"},{"name":"SHA256SUMS"},{"name":"install.sh"},{"name":"404-probe-agent-linux-amd64"},{"name":"404-probe-agent-linux-arm64"},{"name":"404-probe-server-linux-amd64"},{"name":"404-probe-server-linux-arm64"}]}`, version)
		case "/downloads/" + version + "/RELEASE-METADATA.json":
			w.Write(meta)
		case "/downloads/" + version + "/SHA256SUMS":
			fmt.Fprintf(w, "%x  RELEASE-METADATA.json\n%s  install.sh\n", hash, strings.Repeat("c", 64))
			for _, v := range d.Assets {
				fmt.Fprintf(w, "%s  %s\n", v.SHA256, v.Name)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer fixture.Close()
	oldAPI, oldDownloads := upgradeReleaseAPI, upgradeReleaseDownloads
	defer func() { upgradeReleaseAPI, upgradeReleaseDownloads = oldAPI, oldDownloads }()
	upgradeReleaseAPI, upgradeReleaseDownloads = fixture.URL+"/api", fixture.URL+"/downloads"
	r := reportFor(id, 1)
	r.AgentVersion = "v1.0.2"
	r.AgentUpgradeCapable = true
	r.AgentUpgradeV2 = true
	if _, _, _, e = s.ProcessReport(context.Background(), id, r, a.now()); e != nil {
		t.Fatal(e)
	}
	body := `{"channel":"beta","target_version":"v1.0.3-rc.2","confirm_beta":true}`
	if w := webUpgradeResponse(t, a, id, body); w.Code != 409 {
		t.Fatal("old agent got RC", w.Code, w.Body.String())
	}
	r.Sequence++
	r.AgentVersion = buildinfo.FirstRCSupportVersion
	if _, _, _, e = s.ProcessReport(context.Background(), id, r, a.now()); e != nil {
		t.Fatal(e)
	}
	w := webAgentResponse(t, a, "GET", webAgentPathPrefix+id+"/upgrade-targets")
	var catalog struct {
		Targets []upgradeTarget `json:"targets"`
	}
	if e = json.Unmarshal(w.Body.Bytes(), &catalog); w.Code != 200 || e != nil || len(catalog.Targets) != 1 || catalog.Targets[0].Channel != "beta" || !catalog.Targets[0].Supported {
		t.Fatal(w.Code, w.Body.String(), e)
	}
	if w = webUpgradeResponse(t, a, id, body); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	op, e := s.ClaimUpgrade(context.Background(), id, a.now(), 2)
	if e != nil || op == nil || op.TargetVersion != version || op.TargetCommit != d.Commit || !op.BetaConfirmed || op.ServerVersion != a.buildInfo.Version {
		t.Fatal(op, e)
	}
}

package server

import (
	"404-probe/internal/buildinfo"
	"404-probe/internal/releasemetadata"
	"404-probe/internal/storage"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyStableOfficialPayloadIsRejectedBeforeCreatingTaskAndNeverExecuted(t *testing.T) {
	app, store, id := newWebAuthenticationTestApp(t)
	defer store.Close()
	app.buildInfo = buildinfo.Info{Version: "v1.0.1", Commit: strings.Repeat("a", 40)}
	marker := filepath.Join(t.TempDir(), "executed")
	payload := []byte("#!/bin/sh\nprintf executed > '" + marker + "'\n")
	hash := sha256.Sum256(payload)
	doc := releasemetadata.Document{SchemaVersion: 1, Version: "v1.0.1", Commit: app.buildInfo.Commit}
	for _, arch := range []string{"amd64", "arm64"} {
		for _, role := range []string{"agent", "server"} {
			doc.Assets = append(doc.Assets, releasemetadata.Asset{Name: "404-probe-" + role + "-linux-" + arch, GOOS: "linux", GOARCH: arch, SHA256: fmt.Sprintf("%x", hash)})
		}
	}
	metadata, err := releasemetadata.Encode(doc)
	if err != nil {
		t.Fatal(err)
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case "v1.0.1":
			fmt.Fprint(w, `{"tag_name":"v1.0.1","assets":[{"name":"RELEASE-METADATA.json"},{"name":"SHA256SUMS"},{"name":"install.sh"},{"name":"404-probe-agent-linux-amd64"},{"name":"404-probe-agent-linux-arm64"},{"name":"404-probe-server-linux-amd64"},{"name":"404-probe-server-linux-arm64"}]}`)
		case "RELEASE-METADATA.json":
			w.Write(metadata)
		case "SHA256SUMS":
			for _, asset := range doc.Assets {
				fmt.Fprintf(w, "%s  %s\n", asset.SHA256, asset.Name)
			}
			fmt.Fprintf(w, "%s  install.sh\n", strings.Repeat("b", 64))
		case "404-probe-agent-linux-amd64":
			w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer fixture.Close()
	oldAPI, oldDownloads := upgradeReleaseAPI, upgradeReleaseDownloads
	defer func() { upgradeReleaseAPI, upgradeReleaseDownloads = oldAPI, oldDownloads }()
	upgradeReleaseAPI, upgradeReleaseDownloads = fixture.URL+"/api", fixture.URL+"/downloads"
	report := reportFor(id, 1)
	report.AgentVersion = "v0.9.3"
	report.AgentUpgradeCapable = true
	report.Arch = "amd64"
	if _, _, _, err := store.ProcessReport(context.Background(), id, report, app.now()); err != nil {
		t.Fatal(err)
	}
	w := webUpgradeResponse(t, app, id, `{}`)
	if w.Code != 409 || jobErrorCode(t, w) != "release_unavailable" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if op, err := store.GetLatestUpgrade(context.Background(), id); err != nil || op != nil {
		t.Fatal("unsafe task created", op, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("official payload executed", err)
	}
}

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
	app, store, id := newWebAuthenticationTestApp(t)
	defer store.Close()
	app.buildInfo = buildinfo.Info{Version: "v1.0.1", Commit: strings.Repeat("a", 40)}
	version := "v1.0.2-beta.1"
	document := releasemetadata.Document{SchemaVersion: 2, Version: version, Commit: strings.Repeat("b", 40), Compatibility: &releasemetadata.Compatibility{MinServerVersion: "v1.0.1", UpgradeProtocol: 2}}
	for _, arch := range []string{"amd64", "arm64"} {
		for _, role := range []string{"agent", "server"} {
			document.Assets = append(document.Assets, releasemetadata.Asset{Name: "404-probe-" + role + "-linux-" + arch, GOOS: "linux", GOARCH: arch, SHA256: strings.Repeat("c", 64)})
		}
	}
	metadata, err := releasemetadata.Encode(document)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(metadata)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags/" + version:
			fmt.Fprintf(w, `{"tag_name":%q,"prerelease":true,"assets":[{"name":"RELEASE-METADATA.json"},{"name":"SHA256SUMS"},{"name":"install.sh"},{"name":"404-probe-agent-linux-amd64"},{"name":"404-probe-agent-linux-arm64"},{"name":"404-probe-server-linux-amd64"},{"name":"404-probe-server-linux-arm64"}]}`, version)
		case "/downloads/" + version + "/RELEASE-METADATA.json":
			w.Write(metadata)
		case "/downloads/" + version + "/SHA256SUMS":
			fmt.Fprintf(w, "%s  RELEASE-METADATA.json\n", hex.EncodeToString(digest[:]))
			fmt.Fprintf(w, "%s  install.sh\n", strings.Repeat("c", 64))
			for _, asset := range document.Assets {
				fmt.Fprintf(w, "%s  %s\n", asset.SHA256, asset.Name)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer fixture.Close()
	oldAPI, oldDownload := upgradeReleaseAPI, upgradeReleaseDownloads
	defer func() { upgradeReleaseAPI, upgradeReleaseDownloads = oldAPI, oldDownload }()
	upgradeReleaseAPI = fixture.URL + "/api"
	upgradeReleaseDownloads = fixture.URL + "/downloads"
	report := reportFor(id, 1)
	report.AgentVersion = "v1.0.1"
	report.AgentUpgradeCapable = true
	if _, _, _, err := store.ProcessReport(context.Background(), id, report, app.now()); err != nil {
		t.Fatal(err)
	}
	body := `{"channel":"beta","target_version":"v1.0.2-beta.1","confirm_beta":true}`
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
	if w := webUpgradeResponse(t, app, id, body); w.Code != 201 {
		t.Fatalf("V2 rejected: %d %s", w.Code, w.Body.String())
	}
	if operation, err := store.ClaimUpgrade(context.Background(), id, app.now(), 1); err != nil || operation != nil {
		t.Fatalf("legacy claimed V2 %+v %v", operation, err)
	}
	operation, err := store.ClaimUpgrade(context.Background(), id, app.now(), 2)
	if err != nil || operation == nil || operation.Status != storage.UpgradeClaimed || !operation.BetaConfirmed || operation.TargetCommit != document.Commit || operation.ServerVersion != "v1.0.1" {
		t.Fatalf("authorization %+v %v", operation, err)
	}
}

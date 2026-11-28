package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

func TestSecurityBaselineChangeRules(t *testing.T) {
	ssh := func(user, fp, comment string) protocol.SecuritySSHKey {
		return protocol.SecuritySSHKey{User: user, Type: "ssh-ed25519", Fingerprint: fp, Comment: comment}
	}
	port := func(p uint16, process string) protocol.SecurityPort {
		return protocol.SecurityPort{Protocol: "tcp", Port: p, Processes: []string{process}}
	}
	member := func(group, user string) protocol.SecuritySudoMember {
		return protocol.SecuritySudoMember{Group: group, User: user}
	}
	file := func(path, hash string) protocol.SecuritySudoersFile {
		return protocol.SecuritySudoersFile{Path: path, SHA256: hash}
	}
	base := &protocol.SecuritySnapshot{
		SSHKeys: []protocol.SecuritySSHKey{ssh("root", "a", "old"), ssh("root", "b", "keep off")},
		Ports:   []protocol.SecurityPort{port(80, "old-p"), port(22, "ssh")}, UID0: []string{"root", "remove"},
		SudoMembers:  []protocol.SecuritySudoMember{member("sudo", "keep"), member("admin", "remove")},
		SudoersFiles: []protocol.SecuritySudoersFile{file("/a", "old"), file("/b", "remove"), file("/d", "keep")},
	}
	current := &protocol.SecuritySnapshot{
		SSHKeys: []protocol.SecuritySSHKey{ssh("root", "a", "new comment"), ssh("root", "c", "new key")},
		Ports:   []protocol.SecurityPort{port(80, "new-p"), port(53, "dns")}, UID0: []string{"root", "add"},
		SudoMembers:  []protocol.SecuritySudoMember{member("sudo", "keep"), member("wheel", "add")},
		SudoersFiles: []protocol.SecuritySudoersFile{file("/a", "new"), file("/c", "add"), file("/d", "keep")},
	}
	changes := securityChanges(base, current)
	var got []string
	for _, ch := range changes {
		got = append(got, ch.Kind+" "+ch.Change)
	}
	const want = "ssh_key added,ssh_key removed,port added,uid0 added,sudo_member added,sudoers_file modified,sudoers_file added,sudoers_file removed"
	if strings.Join(got, ",") != want || changes[5].Item != current.SudoersFiles[0] {
		t.Fatalf("changes = %#v", changes)
	}
	now := time.Now()
	for i := range 30 {
		current.UID0 = append(current.UID0, fmt.Sprintf("extra-%02d", i))
	}
	b := storage.SecurityBaseline{Status: "ok", Current: current, Baseline: base, SnapshotAt: now.UnixMilli()}
	if summary := summarizeSecurityBaseline(b, now, false); summary.Status != "changed" || len(summary.Changes) != 20 || summary.ChangeCount != 38 {
		t.Fatal(summary)
	}
	if detail := summarizeSecurityBaseline(b, now.Add(15*time.Minute+time.Second), true); detail.Status != "stale" || len(detail.Changes) != 38 {
		t.Fatal(detail)
	}
}

func TestSecurityBaselineReportAndWebLifecycle(t *testing.T) {
	a, store, id, token := testApp(t)
	t.Cleanup(func() { a.Shutdown(); store.Close() })
	now := a.now()
	a.debianStable.next = now.Add(24 * time.Hour)
	basePath := "/api/v1/web/agents/" + id
	seq := uint64(0)
	send := func(checks *protocol.SystemChecks) {
		t.Helper()
		seq++
		report := reportFor(id, seq)
		report.SystemChecks = checks
		raw, _ := json.Marshal(report)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/report", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set(protocol.SystemCheckHeader, "1")
		req.Header.Set(protocol.RootInventoryHeader, "1")
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, req)
		if response := decodeReportResponse(t, w); w.Code != 200 || !response.Accepted || !response.Capabilities.RootInventory {
			t.Fatalf("report status=%d body=%s", w.Code, w.Body.String())
		}
	}
	get := func(path string) securityBaselineView {
		t.Helper()
		w := organizationHTTP(t, a, "GET", path, "", true, false)
		var out securityBaselineView
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		return out
	}
	status := func(want string, count int) securityBaselineView {
		t.Helper()
		view := get(basePath + "/security-baseline")
		if view.Status != want || view.ChangeCount != count {
			t.Fatal(view)
		}
		return view
	}
	status("unavailable", 0)
	checks := &protocol.SystemChecks{Supported: true, RootStatus: protocol.CheckOK}
	send(checks)
	status("pending", 0)
	snapshot := protocol.SecuritySnapshot{SSHKeys: []protocol.SecuritySSHKey{}, Ports: []protocol.SecurityPort{}, UID0: []string{"root"},
		SudoMembers: []protocol.SecuritySudoMember{}, SudoersFiles: []protocol.SecuritySudoersFile{}}
	report := func(doc *protocol.SecuritySnapshot, at time.Time) protocol.SecurityBaselineReport {
		digest, _, err := protocol.InventoryDigest(doc)
		if err != nil {
			t.Fatal(err)
		}
		return protocol.SecurityBaselineReport{Status: "ok", CollectedAt: at.UnixMilli(), Digest: digest, Snapshot: doc}
	}
	first := report(&snapshot, now)
	checks.SecurityBaseline = &first
	structure := &protocol.SingboxStructure{Outbounds: []protocol.SingboxOutbound{{Tag: "main", Type: "selector"}}}
	digest, _, _ := protocol.InventoryDigest(structure)
	checks.SingboxStructure = &protocol.SingboxStructureReport{Status: "ok", CollectedAt: now.UnixMilli(), Digest: digest, Structure: structure}
	send(checks)
	if got := status("ok", 0); got.BaselineAt == nil || got.SnapshotDigest != first.Digest {
		t.Fatal(got)
	}
	w := organizationHTTP(t, a, "GET", basePath+"/singbox-structure", "", true, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"tag":"main"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	changed := snapshot
	changed.SSHKeys = []protocol.SecuritySSHKey{{User: "fixture", Type: "ssh-ed25519", Fingerprint: "SHA256:" + strings.Repeat("A", 43)}}
	second := report(&changed, now)
	beat := second
	beat.Snapshot = nil
	checks.SecurityBaseline = &beat
	checks.SingboxStructure = nil
	send(checks)
	if status("ok", 0).SnapshotDigest != first.Digest {
		t.Fatal("different heartbeat digest replaced latest document")
	}
	checks.SecurityBaseline = &second
	send(checks)
	status("changed", 1)
	for _, path := range []string{basePath, "/api/v1/web/dashboard", "/api/v1/web/agents"} {
		w := organizationHTTP(t, a, "GET", path, "", true, false)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"security_baseline":{"status":"changed"`) {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	ack := func(digest string, csrf bool, want int) {
		t.Helper()
		w := organizationHTTP(t, a, "PUT", basePath+"/security-baseline-acknowledgement", `{"snapshot_digest":"`+digest+`"}`, true, csrf)
		if w.Code != want || want == 409 && !strings.Contains(w.Body.String(), `"code":"snapshot_changed"`) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	ack(second.Digest, false, 403)
	ack(first.Digest, true, 409)
	ack(second.Digest, true, 204)
	status("ok", 0)
	checks.SecurityBaseline = &first // Deleting the key warns again.
	send(checks)
	status("changed", 1)
	first.CollectedAt = now.Add(-16 * time.Minute).UnixMilli()
	first.Snapshot = nil
	send(checks)
	status("stale", 1)
	checks.SecurityBaseline = &protocol.SecurityBaselineReport{Status: "error", Reason: "passwd:read_failed"}
	send(checks)
	if status("unavailable", 0).Reason != "passwd:read_failed" {
		t.Fatal("collection failure reason not returned")
	}
}

package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"404-probe/internal/auth"
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

func webTargetBody(host string) string {
	return fmt.Sprintf(`{"region":"华东","isp":"电信","host":%q,"port":443}`, host)
}

func createWebTestTarget(t *testing.T, a *App, host string) storage.NetworkTarget {
	t.Helper()
	var response struct{ Target storage.NetworkTarget }
	checkS1WebJSON(t, a, http.MethodPost, networkTargetsPath, webTargetBody(host), 201, &response)
	return response.Target
}

func checkS1WebJSON(t *testing.T, a *App, method, path, body string, status int, result any) {
	t.Helper()
	w := qualityWebRequest(t, a, method, path, body)
	if w.Code != status {
		t.Fatalf("%s %s: %d, wanted %d: %s", method, path, w.Code, status, w.Body)
	}
	if result != nil {
		if err := json.Unmarshal(w.Body.Bytes(), result); err != nil {
			t.Fatal(err, w.Body.String())
		}
	}
}

func TestNetworkTargetLibraryLifecycle(t *testing.T) {
	a, first, resolver, done := v4App(t)
	defer done()
	ctx := t.Context()
	second := strings.Repeat("b", 32)
	third := strings.Repeat("c", 32)
	missing := strings.Repeat("d", 32)
	for id, name := range map[string]string{second: "second", third: "third"} {
		if err := a.store.AddAgent(ctx, id, name, auth.Hash(id), a.now()); err != nil {
			t.Fatal(err)
		}
	}
	resolver.answers["ip4/example.net"] = addr("93.184.216.34")
	target := createWebTestTarget(t, a, "Example.NET.")
	if target.Host != "example.net" || target.Revision != 1 || target.UsedBy == nil || len(resolver.questions()) != 2 {
		t.Fatal(target, resolver.questions())
	}
	var webErr v4ErrorBody
	checkS1WebJSON(t, a, "POST", networkTargetsPath, webTargetBody("EXAMPLE.net"), 409, &webErr)
	if webErr.Error.Code != "duplicate_target" {
		t.Fatal(webErr)
	}
	path := networkTargetsPath + "/" + target.ID
	config := func(agentID string) protocol.QualityV4Config {
		t.Helper()
		var cfg protocol.QualityV4Config
		checkS1WebJSON(t, a, "GET", v4Path(agentID, "config"), "", 200, &cfg)
		return cfg
	}
	apply := func(ids ...string) storage.NetworkTargetApply {
		t.Helper()
		body, _ := json.Marshal(map[string][]string{"agent_ids": ids})
		var response storage.NetworkTargetApply
		checkS1WebJSON(t, a, "POST", path+"/apply", string(body), 200, &response)
		return response
	}
	var before = map[string]protocol.QualityV4Config{}
	if out := apply(first, second, missing); !slices.Equal(out.Added, []string{first, second}) ||
		!slices.Equal(out.Skipped, []storage.NetworkTargetSkip{{AgentID: missing, Reason: "not_found"}}) {
		t.Fatal(out)
	}
	for _, id := range []string{first, second} {
		before[id] = config(id)
		if len(before[id].Slots) != 1 {
			t.Fatal(before[id].Slots)
		}
		s := before[id].Slots[0]
		if s.LibraryID != target.ID || s.Kind != "custom" || !s.SlotEnabled || !s.ShowOnCard ||
			!s.IPv4TCP || !s.IPv4ICMP || !s.IPv6TCP || !s.IPv6ICMP || before[id].Enabled {
			t.Fatal(before[id])
		}
	}
	if got := apply(first); !slices.Equal(got.Skipped, []storage.NetworkTargetSkip{{AgentID: first, Reason: "already"}}) {
		t.Fatal(got)
	}
	duplicate := createWebTestTarget(t, a, "8.8.8.8")
	checkS1WebJSON(t, a, "PUT", path, `{"expected_revision":1,"region":"华东","isp":"电信","host":"8.8.8.8","port":443}`, 409, &webErr)
	if webErr.Error.Code != "duplicate_target" {
		t.Fatal(webErr)
	}
	var updated storage.NetworkTargetUpdate
	checkS1WebJSON(t, a, "PUT", path, `{"expected_revision":1,"region":"华东","isp":"电信","host":"1.1.1.1","port":8443}`, 200, &updated)
	if updated.UpdatedAgents != 2 || len(updated.FamilyAdjusted) != 2 || updated.Target.Revision != 2 || len(updated.Target.UsedBy) != 2 {
		t.Fatal(updated)
	}
	for _, id := range []string{first, second} {
		current := config(id)
		oldConfig, _ := strconv.Atoi(before[id].ConfigRevision)
		oldSettings, _ := strconv.Atoi(before[id].SettingsRevision)
		s := current.Slots[0]
		if s.Host != "1.1.1.1" || s.Port != 8443 || s.IPv6TCP || s.IPv6ICMP || s.Slot != before[id].Slots[0].Slot ||
			current.ConfigRevision != strconv.Itoa(oldConfig+1) || current.SettingsRevision != strconv.Itoa(oldSettings+1) {
			t.Fatal(current)
		}
		before[id] = current
	}
	checkS1WebJSON(t, a, "PUT", path, `{"expected_revision":1,"region":"华东","isp":"电信","host":"1.1.1.1","port":443}`, 409, &webErr)
	if webErr.Error.Code != "revision_conflict" {
		t.Fatal(webErr)
	}
	checkS1WebJSON(t, a, "DELETE", path, `{"expected_revision":1}`, 409, &webErr)
	if webErr.Error.Code != "revision_conflict" {
		t.Fatal(webErr)
	}
	var removed map[string]int
	checkS1WebJSON(t, a, "DELETE", path, `{"expected_revision":2}`, 200, &removed)
	if removed["removed_from"] != 2 {
		t.Fatal(removed)
	}
	for _, id := range []string{first, second} {
		old, _ := strconv.Atoi(before[id].ConfigRevision)
		if got := config(id); len(got.Slots) != 0 || got.ConfigRevision != strconv.Itoa(old+1) {
			t.Fatal(got)
		}
	}
	duplicatePath := networkTargetsPath + "/" + duplicate.ID + "/apply"
	checkS1WebJSON(t, a, "POST", duplicatePath, fmt.Sprintf(`{"agent_ids":[%q]}`, third), 200, nil)
	v4 := config(third).Slots[0]
	if !v4.IPv4TCP || !v4.IPv4ICMP || v4.IPv6TCP || v4.IPv6ICMP || !v4.ShowOnCard {
		t.Fatal(v4)
	}
	for i := range 7 {
		more := createWebTestTarget(t, a, fmt.Sprintf("8.8.4.%d", i+1))
		checkS1WebJSON(t, a, "POST", networkTargetsPath+"/"+more.ID+"/apply", fmt.Sprintf(`{"agent_ids":[%q]}`, third), 200, nil)
	}
	for i, slot := range config(third).Slots {
		if slot.ShowOnCard != (i < 3) {
			t.Fatal("append/card order", i, slot)
		}
	}
	extra := createWebTestTarget(t, a, "9.9.9.9")
	var full storage.NetworkTargetApply
	checkS1WebJSON(t, a, "POST", networkTargetsPath+"/"+extra.ID+"/apply", fmt.Sprintf(`{"agent_ids":[%q]}`, third), 200, &full)
	if len(full.Added) != 0 || !slices.Equal(full.Skipped, []storage.NetworkTargetSkip{{AgentID: third, Reason: "full"}}) {
		t.Fatal(full)
	}
	if err := a.store.DeleteOfflineAgent(ctx, third, "third", a.now(), a.offlineTimeout); err != nil {
		t.Fatal(err)
	}
	var list struct{ Targets []storage.NetworkTarget }
	checkS1WebJSON(t, a, "GET", networkTargetsPath, "", 200, &list)
	for _, row := range list.Targets {
		if len(row.UsedBy) != 0 {
			t.Fatal("deleted Agent still uses", row)
		}
	}
}

func TestNetworkTargetLibraryLimitAndDeviceReferences(t *testing.T) {
	a, id, resolver, done := v4App(t)
	defer done()
	target := createWebTestTarget(t, a, "1.1.1.1")
	for i := range 63 {
		createWebTestTarget(t, a, fmt.Sprintf("8.8.8.%d", i+1))
	}
	var failure v4ErrorBody
	checkS1WebJSON(t, a, "POST", networkTargetsPath, webTargetBody("9.9.9.9"), 409, &failure)
	if failure.Error.Code != "target_limit" {
		t.Fatal(failure)
	}
	path := v4Path(id, "config")
	checkS1WebJSON(t, a, "PUT", path, v4Body("0", 30000, "9.9.9.9"), 400, &failure)
	if failure.Error.Code != "library_required" {
		t.Fatal(failure)
	}
	checkS1WebJSON(t, a, "PUT", path, v4ReferencedBody("0", 30000, "", strings.Repeat("e", 32)), 400, &failure)
	if failure.Error.Code != "library_not_found" {
		t.Fatal(failure)
	}
	body := v4ReferencedBody("0", 30000, "127.0.0.1", target.ID)
	body = strings.Replace(body, `"port":443`, `"port":-1`, 1)
	var cfg protocol.QualityV4Config
	checkS1WebJSON(t, a, "PUT", path, body, 200, &cfg)
	s := cfg.Slots[0]
	if s.LibraryID != target.ID || s.Region != "华东" || s.ISP != "电信" || s.Host != "1.1.1.1" || s.Port != 443 ||
		len(resolver.questions()) != 0 {
		t.Fatal(s, resolver.questions())
	}
}

func TestNetworkTargetLibraryFieldValidation(t *testing.T) {
	a, _, resolver, done := v4App(t)
	defer done()
	resolver.answers["ip6/private.example.com"] = addr("fd00::1")
	var failure v4ErrorBody
	checkS1WebJSON(t, a, "POST", networkTargetsPath, webTargetBody("private.example.com"), 400, &failure)
	if len(failure.Error.Details) != 1 || failure.Error.Details[0].Code != protocol.QualityErrUnsafeAddress {
		t.Fatal(failure)
	}
	checkS1WebJSON(t, a, "POST", networkTargetsPath, `{"region":"","isp":"网络","host":"127.0.0.1","port":0}`, 400, &failure)
	if len(failure.Error.Details) != 3 {
		t.Fatal(failure)
	}
}

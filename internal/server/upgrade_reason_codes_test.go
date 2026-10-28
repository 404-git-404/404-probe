package server

import (
	"404-probe/internal/buildinfo"
	"404-probe/internal/storage"
	"encoding/json"
	"strings"
	"testing"
)

func TestIneligibleUpgradeCatalogAddsCodeAndRetainsMessage(t *testing.T) {
	app, store, id := newWebAuthenticationTestApp(t)
	defer store.Close()
	defer app.Shutdown()
	app.buildInfo = buildinfo.Info{Version: "dev", Commit: strings.Repeat("a", 40)}
	makeUpgradeableAgent(t, app, store, id)
	w := webAgentResponse(t, app, "GET", webAgentPathPrefix+id+"/upgrade-targets")
	var response struct {
		Message     string          `json:"message"`
		MessageCode string          `json:"message_code"`
		Targets     []upgradeTarget `json:"targets"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 200 || response.MessageCode != "server_build_ineligible" || response.Message != "当前 Server 构建不支持远程升级" || len(response.Targets) != 0 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
}

func TestUpgradeReasonCodesKeepOriginalDiagnosticsAndDecisions(t *testing.T) {
	for _, row := range []struct {
		version, target     string
		v2, capable, online bool
		protocol            int
		code, reason        string
	}{
		{"v1.0.2", "v1.0.10-rc.1", true, true, true, 2, "rc_agent_required", "RC需要RC-aware Agent及Updater；旧Stable/Beta不能通过Web直接进入RC"},
		{"v1.0.9", "v1.0.10", true, false, true, 2, "updater_unavailable", "本地 Updater 不可用；需要保留身份配置的原地迁移"},
		{"v1.0.9-rc.1", "v1.0.10", false, true, true, 2, "rc_updater_required", "当前RC Agent未证明RC-aware Updater能力；不能使用旧v1.0.1桥接替代"},
		{"v1.0.9", "v1.0.10", false, true, true, 2, "legacy_migration_required", "需要先迁移至 v1.0.1 Stable 并启动新版 Updater"},
		{"v1.0.9", "v1.0.10", true, true, false, 2, "agent_unavailable", "设备离线、暂停或已撤销"},
		{"unknown", "v1.0.10", true, true, true, 2, "target_not_newer", "目标不是严格更新版本；重复升级与降级不可用"},
		{"v1.0.10", "v1.0.10", true, true, true, 2, "already_latest", "已是最新版本"},
		{"v1.0.10", "v1.0.9", true, true, true, 2, "agent_newer_than_server", "Agent 版本高于 Server，不能回退；如需回退请重新安装"},
		{"v1.0.1-beta.1", "v1.0.1", false, true, true, 1, "old_beta_migration_required", "旧 Beta 需要保留身份配置的原地 Stable 迁移入口"},
	} {
		record := storage.AgentSnapshot{Online: row.online, State: &storage.State{AgentVersion: row.version, AgentUpgradeV2: row.v2, AgentUpgradeCapable: row.capable}}
		target := targetForAgent(upgradeTarget{Version: row.target, Supported: true, RequiredProtocol: row.protocol}, record)
		if target.Supported || target.ReasonCode != row.code || target.Reason != row.reason {
			t.Fatalf("%s: %+v", row.code, target)
		}
	}
	a := &App{buildInfo: buildinfo.Info{Version: "v1.0.10-rc.1"}}
	target := a.compatibleAgentTarget(upgradeTarget{Version: "v1.0.10-rc.1", Supported: true}, storage.AgentSnapshot{Online: true, State: &storage.State{AgentVersion: "v1.0.2", AgentUpgradeV2: true, AgentUpgradeCapable: true}})
	if target.Supported || target.ReasonCode != "rc_server_agent_required" || target.Reason != "当前RC Server仅对RC-aware Agent/Updater开放显式升级；旧Agent监控通信不受影响" {
		t.Fatal(target)
	}
}

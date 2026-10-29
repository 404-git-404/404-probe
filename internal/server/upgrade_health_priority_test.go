package server

import (
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
	"bytes"
	"encoding/json"
	"os/exec"
	"testing"
)

func TestUpdaterHealthVersionPriorityMatchesJavaScript(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable; run scripts/test-upgrade-copy.cjs on the Windows test host")
	}
	rows := []map[string]any{}
	for _, version := range []string{"v1.0.9", "v1.0.10", "v1.0.11"} {
		for _, status := range []string{"healthy", "missing", "unreachable", "identity_mismatch", "unsupported", "unknown"} {
			h := &protocol.UpdaterHealth{Status: status, Reason: "runtime_directory_missing"}
			target := targetForAgent(upgradeTarget{Version: "v1.0.10", Supported: true, RequiredProtocol: 2}, storage.AgentSnapshot{Online: true, State: &storage.State{AgentVersion: version, AgentUpgradeCapable: true, AgentUpgradeV2: true, AgentUpdaterHealth: h}})
			rows = append(rows, map[string]any{"agent": map[string]any{"version": version, "online": true, "upgrade_capable": true, "upgrade_v2": true, "updater_health": h}, "code": target.ReasonCode})
		}
	}
	body, _ := json.Marshal(rows)
	command := exec.Command(node, "-e", `const fs=require('fs'),assert=require('assert/strict'),s=require('./web/static/agent-state.js');for(const row of JSON.parse(fs.readFileSync(0,'utf8'))){assert.equal(s.upgradeButtonHint(row.agent,{version:'v1.0.10',upgrade_eligible:true}),s.upgradeReasonDisplay({reason_code:row.code})||'可从面板升级到与面板相同的版本',JSON.stringify(row));}`)
	command.Dir = "../.."
	command.Stdin = bytes.NewReader(body)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Go/JS priority mismatch: %v %s", err, out)
	}
}

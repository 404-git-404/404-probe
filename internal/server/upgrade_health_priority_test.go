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

	for _, targetVersion := range []string{"v1.0.10", "v1.0.10-rc.2"} {
		for _, version := range []string{"v1.0.9", "v1.0.10", "v1.0.11", "v0.9.3", "v1.0.0", "v1.0.1-beta.1", "v1.0.3-rc.1", "v1.0.10-rc.1", "dev"} {
			for _, status := range []string{"healthy", "missing", "unreachable", "identity_mismatch", "unsupported", "unknown"} {
				for _, online := range []bool{false, true} {
					for _, capable := range []bool{false, true} {
						for _, v2 := range []bool{false, true} {
							for _, busy := range []bool{false, true} {
								h := &protocol.UpdaterHealth{Status: status, Reason: "runtime_directory_missing"}
								record := storage.AgentSnapshot{Online: online, State: &storage.State{AgentVersion: version, AgentUpgradeCapable: capable, AgentUpgradeV2: v2, AgentUpdaterHealth: h}}
								input := upgradeTarget{Version: targetVersion, Supported: true, RequiredProtocol: 2}
								target := targetForAgent(input, record, busy)
								if target.Supported != targetForAgentGate(input, record).Supported {
									t.Fatal("support decision changed", record)
								}
								rows = append(rows, map[string]any{"agent": map[string]any{"version": version, "online": online, "upgrade_capable": capable, "upgrade_v2": v2, "updater_health": h}, "code": target.ReasonCode, "busy": busy, "target_version": targetVersion})
							}
						}
					}
				}
			}
		}
	}
	body, _ := json.Marshal(rows)
	command := exec.Command(node, "-e", `const fs=require('fs'),assert=require('assert/strict'),s=require('./web/static/agent-state.js');for(const row of JSON.parse(fs.readFileSync(0,'utf8'))){assert.equal(s.upgradeButtonHint(row.agent,{version:row.target_version,upgrade_eligible:true},row.busy),row.busy?'正在升级，请等待完成':s.upgradeReasonDisplay({reason_code:row.code})||'可从面板升级到与面板相同的版本',JSON.stringify(row));}`)
	command.Dir = "../.."
	command.Stdin = bytes.NewReader(body)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Go/JS priority mismatch: %v %s", err, out)
	}
}

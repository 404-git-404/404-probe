package server

import (
	"404-probe/internal/buildinfo"
	"404-probe/internal/releasemetadata"
	"404-probe/internal/storage"
	"net/http"
)

type upgradeTarget struct {
	Version          string `json:"version"`
	Channel          string `json:"channel"`
	Supported        bool   `json:"supported"`
	Reason           string `json:"reason,omitempty"`
	ReasonCode       string `json:"reason_code,omitempty"`
	RequiredProtocol int    `json:"required_protocol"`
	Commit           string `json:"-"`
}

func targetForAgent(target upgradeTarget, record storage.AgentSnapshot, busy ...bool) upgradeTarget {
	// Preserve every existing support decision; only select the displayed reason.
	gated := targetForAgentGate(target, record)
	code := ""
	comparison, comparable := 0, false
	if record.State != nil {
		comparison, comparable = buildinfo.CompareReleaseVersions(record.State.AgentVersion, target.Version)
	}
	switch {
	case len(busy) > 0 && busy[0]:
		code = "upgrade_in_progress"
	case !record.Online || record.Agent.DisabledAt != nil || record.Agent.Revoked:
		code = "agent_unavailable"
	case !comparable:
		code = "version_unrecognized"
	case comparison == 0:
		code = "already_latest"
	case comparison > 0:
		code = "agent_newer_than_server"
	case record.State.AgentUpdaterHealth.ReasonCode() != "":
		code = record.State.AgentUpdaterHealth.ReasonCode()
	case !record.State.AgentUpgradeCapable:
		code = "updater_unavailable"
	case buildinfo.IsRCVersion(target.Version) && !rcAwareAgent(record):
		code = "rc_agent_required"
	case buildinfo.IsRCVersion(record.State.AgentVersion) && !record.State.AgentUpgradeV2:
		code = "rc_updater_required"
	case !buildinfo.IsCanonicalVersion(record.State.AgentVersion) && !record.State.AgentUpgradeV2:
		code = "updater_unavailable"
	case target.RequiredProtocol == 2 && !record.State.AgentUpgradeV2:
		code = "updater_unavailable"
	default:
		code = gated.ReasonCode
	}
	reasons := map[string]string{
		"upgrade_in_progress":     "正在升级，请等待完成",
		"agent_unavailable":       "设备离线、暂停或已撤销",
		"version_unrecognized":    "目标不是严格更新版本；重复升级与降级不可用",
		"already_latest":          "已是最新版本",
		"agent_newer_than_server": "Agent 版本高于 Server，不能回退；如需回退请重新安装",
		"updater_unavailable":     "本地 Updater 不可用；请检查本地升级服务",
		"rc_agent_required":       "RC需要RC-aware Agent及Updater；旧Stable/Beta不能通过Web直接进入RC",
		"rc_updater_required":     "当前RC Agent未证明RC-aware Updater能力；请更新配套升级服务",
	}
	if code != gated.ReasonCode {
		gated.ReasonCode = code
		gated.Reason = reasons[code]
	}
	return gated
}

func targetForAgentGate(target upgradeTarget, record storage.AgentSnapshot) upgradeTarget {
	if record.State != nil {
		comparison, ok := buildinfo.CompareReleaseVersions(record.State.AgentVersion, target.Version)
		if ok && comparison >= 0 {
			target.Supported = false
			switch {
			case comparison == 0:
				target.Reason = "已是最新版本"
				target.ReasonCode = "already_latest"
			default:
				target.Reason = "Agent 版本高于 Server，不能回退；如需回退请重新安装"
				target.ReasonCode = "agent_newer_than_server"
			}
			return target
		}
	}

	comparison, comparable := 0, false
	if record.State != nil {
		comparison, comparable = buildinfo.CompareReleaseVersions(record.State.AgentVersion, target.Version)
	}
	if comparable && comparison < 0 && record.State.AgentUpdaterHealth.ReasonCode() != "" {
		target.Supported = false
		target.ReasonCode = record.State.AgentUpdaterHealth.ReasonCode()
		target.Reason = "Agent updater health: " + record.State.AgentUpdaterHealth.Status
		return target
	}
	if buildinfo.IsRCVersion(target.Version) && !rcAwareAgent(record) {
		target.Supported = false
		target.Reason = "RC需要RC-aware Agent及Updater；旧Stable/Beta不能通过Web直接进入RC"
		target.ReasonCode = "rc_agent_required"
		return target
	}
	if record.State == nil || !record.State.AgentUpgradeCapable {
		target.Supported = false
		target.Reason = "本地 Updater 不可用；请检查本地升级服务"
		target.ReasonCode = "updater_unavailable"
		return target
	}
	if buildinfo.IsRCVersion(record.State.AgentVersion) && !record.State.AgentUpgradeV2 {
		target.Supported = false
		target.Reason = "当前RC Agent未证明RC-aware Updater能力；请更新配套升级服务"
		target.ReasonCode = "rc_updater_required"
		return target
	}
	if target.RequiredProtocol == 2 && !record.State.AgentUpgradeV2 {
		target.Supported = false
		target.Reason = "本地 Updater 协议不兼容，暂不支持从面板升级"
		target.ReasonCode = "updater_unavailable"
		return target
	}
	if !record.Online || record.Agent.DisabledAt != nil || record.Agent.Revoked {
		target.Supported = false
		target.Reason = "设备离线、暂停或已撤销"
		target.ReasonCode = "agent_unavailable"
		return target
	}
	if !comparable {
		target.Supported = false
		target.Reason = "目标不是严格更新版本；重复升级与降级不可用"
		target.ReasonCode = "version_unrecognized"
	}
	if !buildinfo.IsCanonicalVersion(record.State.AgentVersion) && !record.State.AgentUpgradeV2 {
		target.Supported = false
		target.Reason = "本地 Updater 协议不兼容，暂不支持从面板升级"
		target.ReasonCode = "updater_unavailable"
	}
	return target
}

func rcAwareAgent(record storage.AgentSnapshot) bool {
	if record.State == nil || !record.State.AgentUpgradeV2 {
		return false
	}
	cmp, ok := buildinfo.CompareReleaseVersions(record.State.AgentVersion, buildinfo.FirstRCSupportVersion)
	return ok && cmp >= 0
}

func (a *App) compatibleAgentTarget(target upgradeTarget, record storage.AgentSnapshot) upgradeTarget {
	record = a.withUpdaterHealth(record)
	target = targetForAgent(target, record)
	if buildinfo.IsRCVersion(a.buildInfo.Version) && !rcAwareAgent(record) && (record.State == nil || record.State.AgentUpdaterHealth.ReasonCode() == "") {
		target.Supported = false
		target.Reason = "当前RC Server仅对RC-aware Agent/Updater开放显式升级；旧Agent监控通信不受影响"
		target.ReasonCode = "rc_server_agent_required"
	}
	return target
}
func (a *App) handleWebUpgradeTargets(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agent_id")
	if !validWebAgentID(id) || r.URL.EscapedPath() != webAgentPathPrefix+id+"/upgrade-targets" || r.URL.RawQuery != "" {
		writeJobError(w, 400, "invalid_request", "invalid target request")
		return
	}
	record, err := a.store.GetAgentSnapshot(r.Context(), id, a.now(), a.offlineTimeout)
	if err != nil {
		writeJobError(w, 404, "agent_not_found", "Agent not found")
		return
	}
	targets := []upgradeTarget{}
	message := ""
	messageCode := ""
	if releasemetadata.IsCommit(a.buildInfo.Commit) && (a.buildInfo.UpgradeEligible() || a.buildInfo.ExplicitUpgradeEligible()) {
		target := a.compatibleAgentTarget(a.serverUpgradeTarget(), record)
		active, err := a.store.GetActiveUpgrade(r.Context(), id)
		if err != nil {
			writeJobError(w, 500, "internal_error", "could not inspect upgrade progress")
			return
		}
		if active != nil {
			// Display priority only; the existing support result and POST gates
			// remain unchanged (the active-operation constraint still gates POST).
			target.ReasonCode, target.Reason = "upgrade_in_progress", "正在升级，请等待完成"
		}
		targets = append(targets, target)
	} else {
		message = "当前 Server 构建不支持远程升级"
		messageCode = "server_build_ineligible"
	}
	writeJSON(w, 200, struct {
		Targets     []upgradeTarget `json:"targets"`
		Message     string          `json:"message,omitempty"`
		MessageCode string          `json:"message_code,omitempty"`
	}{targets, message, messageCode})
}

// New-protocol upgrades are authorized by this Server's own build identity.
// Release metadata and payload validation remain the Agent Updater's responsibility.
func (a *App) serverUpgradeTarget() upgradeTarget {
	target := upgradeTarget{Version: a.buildInfo.Version, Commit: a.buildInfo.Commit, Channel: "stable", RequiredProtocol: 1, Supported: true}
	if !buildinfo.IsCanonicalVersion(target.Version) {
		target.Channel = "beta"
		target.RequiredProtocol = 2
	} else if requiresNewUpgradeContract(target.Version) {
		target.RequiredProtocol = 2
	}
	return target
}

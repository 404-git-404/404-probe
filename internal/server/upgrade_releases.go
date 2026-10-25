package server

import (
	"404-probe/internal/buildinfo"
	"404-probe/internal/releasemetadata"
	"404-probe/internal/storage"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Fixed production sources. Only controlled test builds may replace with -X.
var upgradeReleaseAPI = "https://api.github.com/repos/404-git-404/404-probe/releases"
var upgradeReleaseDownloads = "https://github.com/404-git-404/404-probe/releases/download/"

type upgradeTarget struct {
	Version          string                   `json:"version"`
	Channel          string                   `json:"channel"`
	Supported        bool                     `json:"supported"`
	Reason           string                   `json:"reason,omitempty"`
	RequiredProtocol int                      `json:"required_protocol"`
	Commit           string                   `json:"-"`
	Metadata         releasemetadata.Document `json:"-"`
}
type githubUpgradeRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
	} `json:"assets"`
}

func upgradeFetch(ctx context.Context, address string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	response, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		return nil, errors.New("release response exceeds limit")
	}
	return body, nil
}
func metadataChecksum(body, metadata []byte) bool {
	digest := sha256.Sum256(metadata)
	matches := 0
	for _, line := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") {
		fields := strings.Split(line, "  ")
		if len(fields) == 2 && fields[1] == "RELEASE-METADATA.json" {
			matches++
			if fields[0] != hex.EncodeToString(digest[:]) {
				return false
			}
		}
	}
	return matches == 1
}

func releaseChecksums(body, metadata []byte, doc releasemetadata.Document) bool {
	expected := map[string]string{"install.sh": ""}
	for _, asset := range doc.Assets {
		expected[asset.Name] = asset.SHA256
	}
	if doc.SchemaVersion == 2 {
		digest := sha256.Sum256(metadata)
		expected["RELEASE-METADATA.json"] = hex.EncodeToString(digest[:])
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n"), "\n") {
		fields := strings.Split(line, "  ")
		if len(fields) != 2 || len(fields[0]) != 64 || seen[fields[1]] {
			return false
		}
		digest, err := hex.DecodeString(fields[0])
		if err != nil || len(digest) != 32 || strings.ToLower(fields[0]) != fields[0] {
			return false
		}
		wanted, ok := expected[fields[1]]
		if !ok || wanted != "" && wanted != fields[0] {
			return false
		}
		seen[fields[1]] = true
	}
	return len(seen) == len(expected)
}
func (a *App) releaseTarget(ctx context.Context, version string) (upgradeTarget, error) {
	target := upgradeTarget{Version: version, Channel: "stable", RequiredProtocol: 1}
	if !buildinfo.IsReleaseVersion(version) {
		return target, errors.New("invalid fixed release version")
	}
	if !buildinfo.IsCanonicalVersion(version) {
		target.Channel = "beta"
	}
	body, err := upgradeFetch(ctx, strings.TrimRight(upgradeReleaseAPI, "/")+"/tags/"+url.PathEscape(version), 1<<20)
	if err != nil {
		return target, err
	}
	var release githubUpgradeRelease
	if json.Unmarshal(body, &release) != nil || release.TagName != version || release.Draft || release.Prerelease != (target.Channel == "beta") {
		return target, errors.New("release tag/channel identity mismatch")
	}
	names := map[string]int{}
	for _, x := range release.Assets {
		names[x.Name]++
	}
	if len(release.Assets) != 7 {
		return target, errors.New("release must contain exactly seven approved assets")
	}
	for _, name := range []string{"RELEASE-METADATA.json", "SHA256SUMS", "install.sh", "404-probe-agent-linux-amd64", "404-probe-agent-linux-arm64", "404-probe-server-linux-amd64", "404-probe-server-linux-arm64"} {
		if names[name] != 1 {
			return target, errors.New("release assets missing or ambiguous")
		}
	}
	base := strings.TrimRight(upgradeReleaseDownloads, "/") + "/" + version + "/"
	metadata, err := upgradeFetch(ctx, base+"RELEASE-METADATA.json", 64<<10)
	if err != nil {
		return target, err
	}
	document, err := releasemetadata.Decode(metadata)
	if err != nil || document.Version != version {
		return target, errors.New("release metadata identity invalid")
	}
	if version == "v1.0.1" && document.SchemaVersion != 1 {
		return target, errors.New("v1.0.1 Stable requires legacy schema1 metadata")
	}
	if (target.Channel == "beta" || requiresNewUpgradeContract(version)) && document.SchemaVersion != 2 {
		return target, errors.New("pre-release requires explicit compatibility metadata")
	}
	if document.SchemaVersion == 2 {
		sums, err := upgradeFetch(ctx, base+"SHA256SUMS", 256<<10)
		if err != nil || !releaseChecksums(sums, metadata, document) {
			return target, errors.New("release compatibility checksum invalid")
		}
		target.RequiredProtocol = 2
	}
	if !releasemetadata.Compatible(document, a.buildInfo.Version) {
		return target, errors.New("请先升级 Server：目标升级协议或最低 Server 版本不兼容")
	}
	if len(document.Assets) != 4 {
		return target, errors.New("release metadata must cover four approved binaries")
	}
	for _, arch := range []string{"amd64", "arm64"} {
		for _, role := range []string{"agent", "server"} {
			if _, err := releasemetadata.Select(document, version, "404-probe-"+role+"-linux-"+arch, "linux", arch); err != nil {
				return target, err
			}
		}
	}
	target.Commit = document.Commit
	target.Metadata = document
	target.Supported = true
	return target, nil
}

// Old Stable Updaters cannot inspect the new linked Version field. Prove the
// fixed official target before creating their legacy task, without executing it.
func (a *App) verifyLegacyRelease(ctx context.Context, target upgradeTarget, arch string) error {
	if arch != "amd64" && arch != "arm64" {
		return errors.New("unsupported Agent platform")
	}
	if target.Version == a.buildInfo.Version && target.Commit != a.buildInfo.Commit {
		return errors.New("release commit does not match the current Stable Server")
	}
	name := "404-probe-agent-linux-" + arch
	asset, err := releasemetadata.Select(target.Metadata, target.Version, name, "linux", arch)
	if err != nil {
		return err
	}
	base := strings.TrimRight(upgradeReleaseDownloads, "/") + "/" + target.Version + "/"
	sums, err := upgradeFetch(ctx, base+"SHA256SUMS", 256<<10)
	if err != nil {
		return err
	}
	if !releaseChecksums(sums, nil, target.Metadata) {
		return errors.New("legacy release checksum manifest mismatch")
	}
	body, err := upgradeFetch(ctx, base+name, 128<<20)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(body)
	if hex.EncodeToString(hash[:]) != asset.SHA256 {
		return errors.New("legacy release binary checksum mismatch")
	}
	file, err := os.CreateTemp("", "404-agent-release-proof-*")
	if err != nil {
		return err
	}
	path := file.Name()
	defer os.Remove(path)
	if _, err := file.Write(body); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	info, err := buildinfo.InspectLinkedRelease(path)
	if err != nil || info.Version != target.Version || info.Commit != target.Commit || info.Dirty || info.Path != "404-probe/cmd/agent" || info.GOOS != "linux" || info.GOARCH != arch {
		return errors.New("legacy release static version/commit/platform proof failed")
	}
	return nil
}
func targetForAgent(target upgradeTarget, record storage.AgentSnapshot) upgradeTarget {
	if buildinfo.IsRCVersion(target.Version) && !rcAwareAgent(record) {
		target.Supported = false
		target.Reason = "RC需要RC-aware Agent及Updater；旧Stable/Beta不能通过Web直接进入RC"
		return target
	}
	if record.State == nil || !record.State.AgentUpgradeCapable {
		target.Supported = false
		target.Reason = "本地 Updater 不可用；需要保留身份配置的原地迁移"
		return target
	}
	if buildinfo.IsRCVersion(record.State.AgentVersion) && !record.State.AgentUpgradeV2 {
		target.Supported = false
		target.Reason = "当前RC Agent未证明RC-aware Updater能力；不能使用旧v1.0.1桥接替代"
		return target
	}
	if target.RequiredProtocol == 2 && !record.State.AgentUpgradeV2 {
		target.Supported = false
		target.Reason = "需要先迁移至 v1.0.1 Stable 并启动新版 Updater"
		return target
	}
	if !record.Online || record.Agent.DisabledAt != nil || record.Agent.Revoked {
		target.Supported = false
		target.Reason = "设备离线、暂停或已撤销"
		return target
	}
	comparison, ok := buildinfo.CompareReleaseVersions(record.State.AgentVersion, target.Version)
	if !ok || comparison >= 0 {
		target.Supported = false
		switch {
		case !ok:
			target.Reason = "目标不是严格更新版本；重复升级与降级不可用"
		case comparison == 0:
			target.Reason = "已是最新版本"
		default:
			target.Reason = "Agent 版本高于 Server，不能回退；如需回退请重新安装"
		}
	}
	if !buildinfo.IsCanonicalVersion(record.State.AgentVersion) && !record.State.AgentUpgradeV2 {
		target.Supported = false
		target.Reason = "旧 Beta 需要保留身份配置的原地 Stable 迁移入口"
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
	target = targetForAgent(target, record)
	if buildinfo.IsRCVersion(a.buildInfo.Version) && !rcAwareAgent(record) {
		target.Supported = false
		target.Reason = "当前RC Server仅对RC-aware Agent/Updater开放显式升级；旧Agent监控通信不受影响"
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
	if a.buildInfo.UpgradeEligible() || a.buildInfo.ExplicitUpgradeEligible() {
		targets = append(targets, a.compatibleAgentTarget(a.serverUpgradeTarget(), record))
	} else {
		message = "当前 Server 构建不支持远程升级"
	}
	writeJSON(w, 200, struct {
		Targets []upgradeTarget `json:"targets"`
		Message string          `json:"message,omitempty"`
	}{targets, message})
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

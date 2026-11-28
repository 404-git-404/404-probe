package updater

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"404-probe/internal/buildinfo"
	"404-probe/internal/protocol"
	"404-probe/internal/releasemetadata"
)

// The first installer exporting the local management helper is v1.4.1,
// including its prerelease builds.
func SupportsInstallHelperUpdate(version string) bool {
	if !buildinfo.IsReleaseVersion(version) {
		return false
	}
	core, _, _ := strings.Cut(version, "-")
	comparison, ok := buildinfo.CompareReleaseVersions(core, "v1.4.1")
	return ok && comparison >= 0
}

func (e *Engine) finishLegacyHelperUpdate() {
	if err := e.finishSuccessfulUpgrade(nil); err != nil {
		slog.Warn("persist management helper update result", "error", err)
	}
	e.mu.Lock()
	e.running = false
	e.mu.Unlock()
}

func (e *Engine) finishSuccessfulUpgrade(checksums []byte) error {
	e.mu.Lock()
	state := e.state
	e.mu.Unlock()
	checkHelper := e.config.InstallHelper != "" && SupportsInstallHelperUpdate(state.TargetVersion)
	var warning string
	if checkHelper {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
		warning = e.updateInstallHelper(ctx, state, checksums)
		cancel()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.state.Status, e.state.FailureCode, e.state.FailureMessage = "succeeded", "", warning
	e.state.HelperUpdateComplete = checkHelper
	e.state.UpdatedAt = e.config.Now().UnixMilli()
	return e.persistLocked()
}

func (e *Engine) updateInstallHelper(ctx context.Context, state State, checksums []byte) string {
	warning := func(reason string) string { return protocol.UpgradeHelperWarningPrefix + reason }
	path := e.config.InstallHelper
	original, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil || !original.Mode().IsRegular() {
		return warning("无法读取现有管理脚本")
	}
	base := strings.TrimRight(e.config.ReleaseBase, "/") + "/" + state.TargetVersion + "/"
	if checksums == nil {
		// A restarted updater has no in-memory manifest. Revalidate the exact
		// release and its committed Agent digest before using the manifest.
		checksums, err = e.recoverHelperChecksums(ctx, state, base)
		if err != nil {
			return warning("无法重新验证发布校验清单")
		}
	}
	want, err := parseChecksum(checksums, "install.sh")
	if err != nil {
		return warning("校验清单缺少有效的 install.sh 摘要")
	}
	installer, err := e.fetch(ctx, base+"install.sh", 2<<20)
	if err != nil {
		return warning("下载 install.sh 失败")
	}
	digest := sha256.Sum256(installer)
	if subtle.ConstantTimeCompare(want, digest[:]) != 1 {
		return warning("install.sh 校验失败")
	}
	helper, err := renderInstallHelper(ctx, installer)
	if err != nil {
		return warning("生成或语法检查失败")
	}
	if e.config.ApplyInstallHelper != nil {
		err = e.config.ApplyInstallHelper(ctx, helper)
	} else {
		err = replaceInstallHelper(path, helper, original)
	}
	if err != nil {
		slog.Warn("replace management helper", "error", err)
		return warning("写入管理脚本失败")
	}
	return ""
}

func (e *Engine) recoverHelperChecksums(ctx context.Context, state State, base string) ([]byte, error) {
	metadataBody, err := e.fetch(ctx, base+"RELEASE-METADATA.json", 64<<10)
	if err != nil {
		return nil, err
	}
	metadata, err := releasemetadata.Decode(metadataBody)
	if err != nil || state.ReleaseCommit == "" || metadata.Commit != state.ReleaseCommit {
		return nil, errors.New("release metadata does not match committed upgrade")
	}
	asset, err := assetName(e.config.GOOS, e.config.GOARCH)
	if err != nil {
		return nil, err
	}
	selected, err := releasemetadata.Select(metadata, state.TargetVersion, asset, e.config.GOOS, e.config.GOARCH)
	if err != nil {
		return nil, err
	}
	checksums, err := e.fetch(ctx, base+"SHA256SUMS", 256<<10)
	if err != nil {
		return nil, err
	}
	if metadata.SchemaVersion == 2 {
		want, err := parseChecksum(checksums, "RELEASE-METADATA.json")
		digest := sha256.Sum256(metadataBody)
		if err != nil || subtle.ConstantTimeCompare(want, digest[:]) != 1 {
			return nil, errors.New("metadata checksum mismatch")
		}
	}
	want, err := parseChecksum(checksums, asset)
	if err != nil || selected.SHA256 != hex.EncodeToString(want) {
		return nil, errors.New("Agent checksum mismatch")
	}
	live, err := os.Open(e.config.LiveBinary)
	if err != nil {
		return nil, err
	}
	defer live.Close()
	hash := sha256.New()
	const maxAgentBytes = 128 << 20
	n, err := io.Copy(hash, io.LimitReader(live, maxAgentBytes+1))
	if err != nil || n > maxAgentBytes || subtle.ConstantTimeCompare(want, hash.Sum(nil)) != 1 {
		return nil, errors.New("installed Agent does not match recovered manifest")
	}
	return checksums, nil
}

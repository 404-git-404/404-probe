package updater

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"404-probe/internal/buildinfo"
	"404-probe/internal/releasemetadata"
)

// officialReleaseBase is fixed in production builds. A controlled E2E build may
// replace it with -ldflags -X; the running updater exposes no URL override.
var officialReleaseBase = "https://github.com/404-git-404/404-probe/releases/download/"

type CandidateBuildInfo struct {
	Path   string
	Commit string
	Dirty  bool
	GOOS   string
	GOARCH string
}

type Inspector func(string) (CandidateBuildInfo, error)
type ServiceCommand func(context.Context, string) error

type EngineConfig struct {
	CurrentVersion string
	GOOS           string
	GOARCH         string
	StateDirectory string
	LiveBinary     string
	StagedBinary   string
	PreviousBinary string
	HTTPClient     *http.Client
	ReleaseBase    string
	Inspect        Inspector
	ServiceCommand ServiceCommand
	HealthTimeout  time.Duration
	Now            func() time.Time
	OnCommitted    func()
}

type Engine struct {
	config  EngineConfig
	mu      sync.Mutex
	state   State
	running bool
	health  chan struct{}
}

func NewEngine(config EngineConfig) (*Engine, error) {
	if config.GOOS == "" {
		config.GOOS = runtime.GOOS
	}
	if config.GOARCH == "" {
		config.GOARCH = runtime.GOARCH
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: 2 * time.Minute}
	}
	if config.ReleaseBase == "" {
		config.ReleaseBase = officialReleaseBase
	}
	if config.HealthTimeout <= 0 {
		config.HealthTimeout = 2 * time.Minute
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.StateDirectory == "" || config.LiveBinary == "" || config.StagedBinary == "" || config.PreviousBinary == "" || config.Inspect == nil || config.ServiceCommand == nil {
		return nil, errors.New("updater paths, inspector, and service controller are required")
	}
	engine := &Engine{config: config}
	if state, err := readStateFile(filepath.Join(config.StateDirectory, "operation.json")); err == nil {
		engine.state = state
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return engine, nil
}

func (e *Engine) Recover() {
	e.mu.Lock()
	status := e.state.Status
	if !isActiveLocalStatus(status) || e.running {
		e.mu.Unlock()
		return
	}
	if status == "claimed" || status == "downloading" || status == "verifying" || status == "staging" {
		e.running = true
		e.mu.Unlock()
		e.finishFailure(false, "updater_restarted")
		return
	}
	e.running = true
	e.health = make(chan struct{})
	e.mu.Unlock()
	go e.recoverInstalled()
}

func (e *Engine) Start(request Request) (State, error) {
	if request.Action != ActionStart {
		return State{}, errors.New("start action required")
	}
	if err := request.Validate(); err != nil {
		return State{}, err
	}
	if comparison, ok := buildinfo.CompareVersions(e.config.CurrentVersion, request.TargetVersion); !ok || comparison >= 0 {
		return State{}, errors.New("target version must be newer than the installed version")
	}
	if e.config.GOOS != "linux" || (e.config.GOARCH != "amd64" && e.config.GOARCH != "arm64") {
		return State{}, errors.New("unsupported platform")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running || isActiveLocalStatus(e.state.Status) {
		if e.state.OperationID == request.OperationID && e.state.TargetVersion == request.TargetVersion {
			return e.state, nil
		}
		return State{}, errors.New("an updater operation is already active")
	}
	e.state = State{OperationID: request.OperationID, TargetVersion: request.TargetVersion, Status: "claimed", UpdatedAt: e.config.Now().UnixMilli()}
	e.health = make(chan struct{})
	e.running = true
	if err := e.persistLocked(); err != nil {
		e.running = false
		return State{}, err
	}
	state := e.state
	go e.run(request)
	return state, nil
}

func (e *Engine) Status(request Request) (State, error) {
	if request.Action != ActionStatus || request.Validate() != nil {
		return State{}, errors.New("invalid status request")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state.OperationID != request.OperationID || e.state.TargetVersion != request.TargetVersion {
		return State{}, errors.New("operation not found")
	}
	return e.state, nil
}

func (e *Engine) Healthy(request Request) (State, error) {
	if request.Action != ActionHealthy || request.Validate() != nil {
		return State{}, errors.New("invalid health request")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state.OperationID != request.OperationID || e.state.TargetVersion != request.TargetVersion || e.state.Status != "health_check" || e.health == nil {
		return State{}, errors.New("operation is not awaiting health confirmation")
	}
	select {
	case <-e.health:
	default:
		close(e.health)
	}
	return e.state, nil
}

func (e *Engine) run(request Request) {
	installed := false
	failureCode := ""
	if err := e.setStatus("downloading", "", ""); err != nil {
		failureCode = "stage_failed"
	} else if err := e.downloadAndVerify(context.Background(), request.TargetVersion); err != nil {
		failureCode = classifyDownloadFailure(err)
	} else if err := e.setStatus("staging", "", ""); err != nil {
		failureCode = "stage_failed"
	} else if err := e.stageCandidate(); err != nil {
		failureCode = "stage_failed"
	} else if err := e.setStatus("installing", "", ""); err != nil {
		failureCode = "install_failed"
	} else if err := e.installCandidate(); err != nil {
		failureCode = "install_failed"
	} else {
		installed = true
	}
	if failureCode != "" {
		e.finishFailure(installed, failureCode)
		return
	}
	if err := e.setStatus("restarting", "", ""); err != nil {
		e.finishFailure(true, "restart_failed")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	err := e.config.ServiceCommand(ctx, "restart")
	cancel()
	if err != nil {
		e.finishFailure(true, "restart_failed")
		return
	}
	if err := e.setStatus("health_check", "", ""); err != nil {
		e.finishFailure(true, "health_timeout")
		return
	}
	timer := time.NewTimer(e.config.HealthTimeout)
	defer timer.Stop()
	e.mu.Lock()
	health := e.health
	e.mu.Unlock()
	select {
	case <-health:
		if err := e.setStatus("succeeded", "", ""); err != nil {
			e.finishFailure(true, "stage_failed")
			return
		}
		e.cleanupCommittedFiles()
		e.mu.Lock()
		e.running = false
		e.mu.Unlock()
		if e.config.OnCommitted != nil {
			e.config.OnCommitted()
		}
	case <-timer.C:
		e.finishFailure(true, "health_timeout")
	}
}

func (e *Engine) downloadAndVerify(ctx context.Context, target string) error {
	asset, err := assetName(e.config.GOOS, e.config.GOARCH)
	if err != nil {
		return err
	}
	base := strings.TrimRight(e.config.ReleaseBase, "/") + "/" + target + "/"
	metadataBody, err := e.fetch(ctx, base+"RELEASE-METADATA.json", 64<<10)
	if err != nil {
		return fmt.Errorf("release_metadata_download_failed: %w", err)
	}
	metadata, err := releasemetadata.Decode(metadataBody)
	if err != nil {
		return fmt.Errorf("release_metadata_invalid: %w", err)
	}
	selected, err := releasemetadata.Select(metadata, target, asset, e.config.GOOS, e.config.GOARCH)
	if err != nil {
		if strings.Contains(err.Error(), "version") {
			return fmt.Errorf("release_metadata_version_mismatch: %w", err)
		}
		return fmt.Errorf("release_metadata_asset_invalid: %w", err)
	}
	want, err := hex.DecodeString(selected.SHA256)
	if err != nil {
		return fmt.Errorf("release_metadata_invalid: %w", err)
	}
	if err := e.setReleaseCommit(metadata.Commit); err != nil {
		return fmt.Errorf("stage_failed: %w", err)
	}
	checksumBody, err := e.fetch(ctx, base+"SHA256SUMS", 256<<10)
	if err != nil {
		return fmt.Errorf("checksum_manifest_failed: %w", err)
	}
	checksumWant, err := parseChecksum(checksumBody, asset)
	if err != nil {
		return fmt.Errorf("checksum_manifest_failed: %w", err)
	}
	if subtle.ConstantTimeCompare(checksumWant, want) != 1 {
		return errors.New("checksum_manifest_mismatch")
	}
	candidate, err := e.fetch(ctx, base+asset, 128<<20)
	if err != nil {
		return fmt.Errorf("download_failed: %w", err)
	}
	digest := sha256.Sum256(candidate)
	if subtle.ConstantTimeCompare(digest[:], want) != 1 {
		return errors.New("checksum_mismatch")
	}
	path := filepath.Join(e.config.StateDirectory, "candidate")
	if err := writeRootFile(path, candidate, 0600); err != nil {
		return fmt.Errorf("stage_failed: %w", err)
	}
	if err := e.setStatus("verifying", "", ""); err != nil {
		return fmt.Errorf("stage_failed: %w", err)
	}
	info, err := e.config.Inspect(path)
	if err != nil || info.Path != "404-probe/cmd/agent" {
		return errors.New("candidate_buildinfo_invalid")
	}
	if info.Commit == "" || info.Commit != metadata.Commit {
		return errors.New("candidate_revision_mismatch")
	}
	if info.Dirty {
		return errors.New("candidate_dirty")
	}
	if info.GOOS != selected.GOOS || info.GOARCH != selected.GOARCH {
		return errors.New("candidate_platform_mismatch")
	}
	return nil
}

func (e *Engine) setReleaseCommit(commit string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.state.ReleaseCommit = commit
	e.state.UpdatedAt = e.config.Now().UnixMilli()
	return e.persistLocked()
}

func (e *Engine) fetch(ctx context.Context, url string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := e.config.HTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("response is too large")
	}
	return data, nil
}

func (e *Engine) stageCandidate() error {
	data, err := os.ReadFile(filepath.Join(e.config.StateDirectory, "candidate"))
	if err != nil {
		return err
	}
	if err := removeFixedRegular(e.config.StagedBinary); err != nil {
		return err
	}
	return writeRootFile(e.config.StagedBinary, data, 0755)
}

func (e *Engine) installCandidate() error {
	if err := requireRegular(e.config.LiveBinary); err != nil {
		return err
	}
	if err := removeFixedRegular(e.config.PreviousBinary); err != nil {
		return err
	}
	if err := os.Link(e.config.LiveBinary, e.config.PreviousBinary); err != nil {
		return err
	}
	if err := os.Rename(e.config.StagedBinary, e.config.LiveBinary); err != nil {
		_ = os.Remove(e.config.PreviousBinary)
		return err
	}
	return syncDirectory(filepath.Dir(e.config.LiveBinary))
}

func (e *Engine) finishFailure(installed bool, code string) {
	if installed {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_ = e.config.ServiceCommand(ctx, "stop")
		cancel()
		if requireRegular(e.config.PreviousBinary) == nil && os.Rename(e.config.PreviousBinary, e.config.LiveBinary) == nil && syncDirectory(filepath.Dir(e.config.LiveBinary)) == nil {
			ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
			err := e.config.ServiceCommand(ctx, "restart")
			cancel()
			if err == nil {
				_ = e.setStatus("rolled_back", code, "upgrade failed; previous version restored")
				e.mu.Lock()
				e.running = false
				e.mu.Unlock()
				return
			}
		}
		code = "rollback_failed"
	}
	_ = e.setStatus("failed", code, safeFailureMessage(code))
	e.mu.Lock()
	e.running = false
	e.mu.Unlock()
}

func (e *Engine) setStatus(status, code, message string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.state.Status, e.state.FailureCode, e.state.FailureMessage = status, code, message
	e.state.UpdatedAt = e.config.Now().UnixMilli()
	return e.persistLocked()
}

func (e *Engine) persistLocked() error {
	data, err := json.Marshal(e.state)
	if err != nil {
		return err
	}
	return writeRootFile(filepath.Join(e.config.StateDirectory, "operation.json"), append(data, '\n'), 0600)
}

func readStateFile(path string) (State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, err
	}
	var state State
	if json.Unmarshal(data, &state) != nil || !validOperationID(state.OperationID) || !buildinfo.IsCanonicalVersion(state.TargetVersion) || (state.ReleaseCommit != "" && !releasemetadata.IsCommit(state.ReleaseCommit)) {
		return State{}, errors.New("stored updater state is invalid")
	}
	return state, nil
}

func assetName(goos, goarch string) (string, error) {
	if goos != "linux" || (goarch != "amd64" && goarch != "arm64") {
		return "", errors.New("unsupported_platform")
	}
	return "404-probe-agent-linux-" + goarch, nil
}

func parseChecksum(manifest []byte, asset string) ([]byte, error) {
	var match string
	for _, line := range strings.Split(strings.ReplaceAll(string(manifest), "\r\n", "\n"), "\n") {
		parts := strings.Split(line, "  ")
		if len(parts) == 2 && parts[1] == asset {
			if match != "" {
				return nil, errors.New("duplicate checksum")
			}
			match = parts[0]
		}
	}
	if len(match) != sha256.Size*2 {
		return nil, errors.New("checksum not found")
	}
	digest, err := hex.DecodeString(match)
	if err != nil {
		return nil, errors.New("invalid checksum")
	}
	return digest, nil
}

func writeRootFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temporary := path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if err = file.Chmod(mode); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return os.Rename(temporary, path)
}

func requireRegular(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("fixed updater path is not a regular file")
	}
	return nil
}

func removeFixedRegular(path string) error {
	if err := requireRegular(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return os.Remove(path)
}

func syncDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (e *Engine) recoverInstalled() {
	info, err := e.config.Inspect(e.config.LiveBinary)
	if err != nil || e.state.ReleaseCommit == "" || info.Path != "404-probe/cmd/agent" || info.Commit != e.state.ReleaseCommit || info.Dirty || info.GOOS != e.config.GOOS || info.GOARCH != e.config.GOARCH {
		e.finishFailure(true, "updater_restarted")
		return
	}
	if err := e.setStatus("restarting", "", ""); err != nil {
		e.finishFailure(true, "updater_restarted")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = e.config.ServiceCommand(ctx, "restart")
	cancel()
	if err != nil {
		e.finishFailure(true, "restart_failed")
		return
	}
	if err := e.setStatus("health_check", "", ""); err != nil {
		e.finishFailure(true, "health_timeout")
		return
	}
	e.mu.Lock()
	health := e.health
	e.mu.Unlock()
	select {
	case <-health:
		if err := e.setStatus("succeeded", "", ""); err != nil {
			e.finishFailure(true, "stage_failed")
			return
		}
		e.cleanupCommittedFiles()
		e.mu.Lock()
		e.running = false
		e.mu.Unlock()
		if e.config.OnCommitted != nil {
			e.config.OnCommitted()
		}
	case <-time.After(e.config.HealthTimeout):
		e.finishFailure(true, "health_timeout")
	}
}

func (e *Engine) cleanupCommittedFiles() {
	_ = os.Remove(e.config.PreviousBinary)
	_ = os.Remove(filepath.Join(e.config.StateDirectory, "candidate"))
}

func classifyDownloadFailure(err error) string {
	for _, code := range []string{"release_metadata_download_failed", "release_metadata_version_mismatch", "release_metadata_asset_invalid", "release_metadata_invalid", "checksum_manifest_failed", "checksum_manifest_mismatch", "checksum_mismatch", "download_failed", "candidate_buildinfo_invalid", "candidate_revision_mismatch", "candidate_dirty", "candidate_platform_mismatch", "unsupported_platform", "stage_failed"} {
		if strings.Contains(err.Error(), code) {
			return code
		}
	}
	return "download_failed"
}

func safeFailureMessage(code string) string {
	switch code {
	case "release_metadata_download_failed", "release_metadata_invalid", "release_metadata_version_mismatch", "release_metadata_asset_invalid":
		return "upgrade release metadata could not be verified"
	case "checksum_manifest_failed":
		return "upgrade checksum manifest could not be verified"
	case "checksum_manifest_mismatch":
		return "upgrade release metadata and checksum manifest did not match"
	case "checksum_mismatch":
		return "upgrade candidate checksum did not match"
	case "candidate_buildinfo_invalid", "candidate_revision_mismatch", "candidate_dirty", "candidate_platform_mismatch":
		return "upgrade candidate build metadata is invalid"
	case "restart_failed":
		return "Agent restart failed"
	case "health_timeout":
		return "new Agent did not become healthy before timeout"
	default:
		return "upgrade failed before the previous binary was replaced"
	}
}

func isActiveLocalStatus(status string) bool {
	switch status {
	case "claimed", "downloading", "verifying", "staging", "installing", "restarting", "health_check":
		return true
	default:
		return false
	}
}

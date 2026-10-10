package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"404-probe/internal/buildinfo"
	"404-probe/internal/protocol"
	"404-probe/internal/releasemetadata"
)

// officialReleaseBase is fixed in production builds. A controlled E2E build may
// replace it with -ldflags -X; the running updater exposes no URL override.
var officialReleaseBase = "https://github.com/404-git-404/404-probe/releases/download/"
var officialReleaseAPIBase = "https://api.github.com/repos/404-git-404/404-probe/releases/"

type CandidateBuildInfo struct {
	Path    string
	Version string
	Commit  string
	Dirty   bool
	GOOS    string
	GOARCH  string
}

type Inspector func(string) (CandidateBuildInfo, error)
type ServiceCommand func(context.Context, string) error

type EngineConfig struct {
	// LocalMigration is set only by the root-only, fixed Beta-to-v1.0.1 CLI.
	LocalMigration   bool
	CurrentVersion   string
	GOOS             string
	GOARCH           string
	StateDirectory   string
	LiveBinary       string
	StagedBinary     string
	PreviousBinary   string
	HTTPClient       *http.Client
	ReleaseBase      string
	Inspect          Inspector
	ServiceCommand   ServiceCommand
	HealthTimeout    time.Duration
	DownloadTimeout  time.Duration
	RetryBaseDelay   time.Duration
	DownloadAttempts int
	Now              func() time.Time
	OnCommitted      func()
	OnRolledBack     func()
	AgentRemoval     AgentRemovalLauncher
}

type Engine struct {
	config        EngineConfig
	mu            sync.Mutex
	state         State
	running       bool
	health        chan struct{}
	startStateOps rootFileOps
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
	if config.DownloadTimeout <= 0 {
		config.DownloadTimeout = 3 * time.Minute
	}
	if config.RetryBaseDelay <= 0 {
		config.RetryBaseDelay = time.Second
	}
	if config.DownloadAttempts <= 0 {
		config.DownloadAttempts = 5
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.StateDirectory == "" || config.LiveBinary == "" || config.StagedBinary == "" || config.PreviousBinary == "" || config.Inspect == nil || config.ServiceCommand == nil {
		return nil, errors.New("updater paths, inspector, and service controller are required")
	}
	engine := &Engine{config: config, startStateOps: defaultRootFileOps()}
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
	if e.config.LocalMigration && (request.ProtocolVersion != 1 || request.TargetVersion != "v1.0.1" || !supportedLocalMigrationSource(e.config.CurrentVersion)) {
		return State{}, errors.New("local migration is restricted to old Beta -> v1.0.1 Stable")
	}
	if e.config.GOOS != "linux" || (e.config.GOARCH != "amd64" && e.config.GOARCH != "arm64") {
		return State{}, errors.New("unsupported platform")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	// An old Agent may retry Start before observing the last terminal result.
	// Never rerun an already completed operation; a user retry needs a new ID.
	if e.state.Status != "" && e.state.OperationID == request.OperationID && e.state.TargetVersion == request.TargetVersion && stateMatches(e.state, request) {
		return e.state, nil
	}
	if e.running || isActiveLocalStatus(e.state.Status) {
		if e.state.OperationID == request.OperationID && e.state.TargetVersion == request.TargetVersion && stateMatches(e.state, request) {
			return e.state, nil
		}
		return State{}, errors.New("an updater operation is already active")
	}
	if comparison, ok := buildinfo.CompareReleaseVersions(e.config.CurrentVersion, request.TargetVersion); !ok || comparison >= 0 {
		return State{}, errors.New("target version must be newer than the installed version")
	}
	candidate := State{OperationID: request.OperationID, TargetVersion: request.TargetVersion, Status: "claimed", UpdatedAt: e.config.Now().UnixMilli()}
	candidate.SourceVersion = e.config.CurrentVersion
	candidate.LocalMigration = e.config.LocalMigration
	if request.ProtocolVersion == 2 {
		candidate.ProtocolVersion = 2
		candidate.Channel = request.Channel
		candidate.TargetCommit = request.TargetCommit
		candidate.ServerVersion = request.ServerVersion
	}
	encoded, err := json.Marshal(candidate)
	if err != nil {
		return State{}, err
	}
	encoded = append(encoded, '\n')
	committed, persistErr := writeRootFileCommitted(filepath.Join(e.config.StateDirectory, "operation.json"), encoded, 0600, e.startStateOps)
	if persistErr != nil && !committed {
		return State{}, fmt.Errorf("persist updater start state before commit: %w", persistErr)
	}
	if persistErr != nil {
		persisted, readErr := os.ReadFile(filepath.Join(e.config.StateDirectory, "operation.json"))
		if readErr != nil {
			return State{}, errors.Join(
				fmt.Errorf("updater start state was renamed but parent sync failed: %w", persistErr),
				fmt.Errorf("confirm renamed updater start state: %w", readErr),
			)
		}
		if !bytes.Equal(persisted, encoded) {
			return State{}, errors.Join(
				fmt.Errorf("updater start state was renamed but parent sync failed: %w", persistErr),
				errors.New("renamed updater operation.json does not exactly match the start candidate"),
			)
		}
	}
	e.state = candidate
	e.health = make(chan struct{})
	e.running = true
	go e.run(request)
	if persistErr != nil {
		return candidate, fmt.Errorf("updater start state committed but parent directory sync failed: %w", persistErr)
	}
	return candidate, nil
}

func supportedLocalMigrationSource(version string) bool {
	return version == "v0.9.3" || version == "v1.0.0" || version == "v1.0.1-beta.1" || version == "v1.0.1-beta.2"
}

func (e *Engine) Status(request Request) (State, error) {
	if request.Action != ActionStatus || request.Validate() != nil {
		return State{}, errors.New("invalid status request")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state.OperationID != request.OperationID || e.state.TargetVersion != request.TargetVersion || !stateMatches(e.state, request) {
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
	if e.state.OperationID != request.OperationID || e.state.TargetVersion != request.TargetVersion || !stateMatches(e.state, request) || e.state.Status != "health_check" || e.health == nil {
		return State{}, errors.New("operation is not awaiting health confirmation")
	}
	select {
	case <-e.health:
	default:
		close(e.health)
	}
	return e.state, nil
}

func (e *Engine) SupportsRemoteRemoval() bool {
	return e.config.AgentRemoval != nil && e.config.AgentRemoval.SupportsRemoteRemoval()
}

func (e *Engine) StartAgentRemoval(request Request) error {
	if request.Action != ActionRemove || request.Validate() != nil || e.config.AgentRemoval == nil || !e.config.AgentRemoval.SupportsRemoteRemoval() {
		return errors.New("Agent removal is not supported by the local updater")
	}
	return e.config.AgentRemoval.Start(context.Background(), request.OperationID, request.ReceiptToken)
}

func (e *Engine) run(request Request) {
	installed := false
	failureCode := ""
	if err := e.setStatus("downloading", "", ""); err != nil {
		failureCode = "stage_failed"
	} else if err := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), e.config.DownloadTimeout)
		defer cancel()
		return e.downloadAndVerify(ctx, request.TargetVersion)
	}(); err != nil {
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
	e.mu.Lock()
	authorized := e.state
	e.mu.Unlock()
	if authorized.ProtocolVersion == 2 && (metadata.SchemaVersion != 2 || metadata.Commit != authorized.TargetCommit || !releasemetadata.Compatible(metadata, authorized.ServerVersion)) || authorized.ProtocolVersion != 2 && (metadata.SchemaVersion != 1 || !buildinfo.IsCanonicalVersion(target)) {
		return errors.New("release_metadata_invalid: incompatible authorized release")
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
	if metadata.SchemaVersion == 2 {
		sum, err := parseChecksum(checksumBody, "RELEASE-METADATA.json")
		actual := sha256.Sum256(metadataBody)
		if err != nil || subtle.ConstantTimeCompare(sum, actual[:]) != 1 {
			return errors.New("checksum_manifest_mismatch")
		}
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
	if requiresLinkedVersion(target) && info.Version != target {
		return errors.New("candidate_version_mismatch")
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
	type endpoint struct {
		url    string
		accept string
	}
	endpoints := []endpoint{{url: url}}
	official := strings.HasPrefix(url, officialReleaseBase) && !strings.Contains(url, "?")
	apiResolved := false
	endpointIndex := 0
	var data []byte
	var lastErr error
	var expectedTotal int64 = -1
	var entityTag string
	for attempt := 0; attempt < e.config.DownloadAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		selected := endpoints[endpointIndex]
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, selected.url, nil)
		if err != nil {
			return nil, err
		}
		if selected.accept != "" {
			request.Header.Set("Accept", selected.accept)
		}
		if len(data) > 0 {
			request.Header.Set("Range", fmt.Sprintf("bytes=%d-", len(data)))
			if entityTag != "" {
				request.Header.Set("If-Range", entityTag)
			}
		}
		response, err := e.config.HTTPClient.Do(request)
		if err != nil {
			lastErr = err
		} else {
			status := response.StatusCode
			if status == http.StatusOK || status == http.StatusPartialContent {
				expectedChunk := int64(-1)
				if status == http.StatusOK && len(data) > 0 {
					// The endpoint ignored Range. Restart this attempt rather than
					// appending a second complete object to the partial bytes.
					data = data[:0]
					expectedTotal = -1
					entityTag = ""
				}
				if status == http.StatusPartialContent {
					start, end, total, rangeErr := parseContentRange(response.Header.Get("Content-Range"))
					if rangeErr != nil || start != int64(len(data)) || (expectedTotal >= 0 && total != expectedTotal) {
						_ = response.Body.Close()
						data = nil
						expectedTotal = -1
						entityTag = ""
						lastErr = errors.New("invalid resume response")
						goto retry
					}
					expectedTotal = total
					expectedChunk = end - start + 1
				}
				if tag := response.Header.Get("ETag"); tag != "" {
					entityTag = tag
				}
				chunk, readErr := io.ReadAll(io.LimitReader(response.Body, limit-int64(len(data))+1))
				closeErr := response.Body.Close()
				data = append(data, chunk...)
				if int64(len(data)) > limit {
					return nil, errors.New("response is too large")
				}
				if readErr == nil {
					readErr = closeErr
				}
				if readErr == nil && expectedChunk >= 0 && int64(len(chunk)) != expectedChunk {
					readErr = io.ErrUnexpectedEOF
				}
				if readErr == nil && expectedTotal >= 0 && int64(len(data)) != expectedTotal {
					readErr = io.ErrUnexpectedEOF
				}
				if readErr == nil {
					return data, nil
				}
				lastErr = readErr
			} else if status == http.StatusRequestedRangeNotSatisfiable && len(data) > 0 {
				_ = response.Body.Close()
				data = nil
				expectedTotal = -1
				entityTag = ""
				lastErr = errors.New("server rejected resume range")
			} else {
				_ = response.Body.Close()
				lastErr = fmt.Errorf("HTTP %d", status)
				if !retryableHTTPStatus(status) && !official {
					return nil, lastErr
				}
			}
		}
	retry:
		if official && !apiResolved {
			apiResolved = true
			if apiURL, resolveErr := e.resolveOfficialAPIAsset(ctx, url); resolveErr == nil {
				endpoints = append(endpoints, endpoint{url: apiURL, accept: "application/octet-stream"})
				endpointIndex = len(endpoints) - 1
			} else if lastErr == nil {
				lastErr = resolveErr
			}
		}
		if attempt+1 == e.config.DownloadAttempts {
			break
		}
		delay := e.config.RetryBaseDelay << attempt
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if lastErr == nil {
		lastErr = errors.New("download failed")
	}
	return nil, lastErr
}

func parseContentRange(value string) (start, end, total int64, err error) {
	if !strings.HasPrefix(value, "bytes ") {
		return 0, 0, 0, errors.New("missing byte content range")
	}
	parts := strings.Split(strings.TrimPrefix(value, "bytes "), "/")
	if len(parts) != 2 || parts[1] == "*" {
		return 0, 0, 0, errors.New("invalid content range")
	}
	bounds := strings.Split(parts[0], "-")
	if len(bounds) != 2 {
		return 0, 0, 0, errors.New("invalid content range")
	}
	start, err = strconv.ParseInt(bounds[0], 10, 64)
	if err != nil {
		return 0, 0, 0, errors.New("invalid content range")
	}
	end, err = strconv.ParseInt(bounds[1], 10, 64)
	if err != nil {
		return 0, 0, 0, errors.New("invalid content range")
	}
	total, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil || start < 0 || end < start || total <= end {
		return 0, 0, 0, errors.New("invalid content range")
	}
	return start, end, total, nil
}

func (e *Engine) resolveOfficialAPIAsset(ctx context.Context, directURL string) (string, error) {
	remainder := strings.TrimPrefix(directURL, officialReleaseBase)
	parts := strings.Split(remainder, "/")
	if len(parts) != 2 || !buildinfo.IsReleaseVersion(parts[0]) || parts[1] == "" || strings.ContainsAny(parts[1], "?#") {
		return "", errors.New("official release asset URL is invalid")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, officialReleaseAPIBase+"tags/"+url.PathEscape(parts[0]), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := e.config.HTTPClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("release API returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil || len(body) > 1<<20 {
		return "", errors.New("release API response is invalid")
	}
	var release struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"assets"`
	}
	if json.Unmarshal(body, &release) != nil || release.TagName != parts[0] {
		return "", errors.New("release API identity mismatch")
	}
	prefix := strings.TrimRight(officialReleaseAPIBase, "/") + "/assets/"
	match := ""
	for _, asset := range release.Assets {
		if asset.Name != parts[1] {
			continue
		}
		id := strings.TrimPrefix(asset.URL, prefix)
		if !strings.HasPrefix(asset.URL, prefix) || id == "" {
			return "", errors.New("release API asset URL is invalid")
		}
		if _, err := strconv.ParseUint(id, 10, 64); err != nil || match != "" {
			return "", errors.New("release API asset identity is ambiguous")
		}
		match = asset.URL
	}
	if match == "" {
		return "", errors.New("release API asset was not found")
	}
	return match, nil
}

func retryableHTTPStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
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
				if err := e.setStatus("rolled_back", code, "upgrade failed; previous version restored"); err == nil {
					e.cleanupCommittedFiles()
					e.mu.Lock()
					e.running = false
					e.mu.Unlock()
					if e.config.OnRolledBack != nil {
						e.config.OnRolledBack()
					}
					return
				}
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
	if protocol.DecodeUpgradeJSON(data, &state) != nil || !validOperationID(state.OperationID) || (state.ReleaseCommit != "" && !releasemetadata.IsCommit(state.ReleaseCommit)) || (Request{ProtocolVersion: stateProtocol(state), Action: ActionStatus, OperationID: state.OperationID, TargetVersion: state.TargetVersion, Channel: state.Channel, TargetCommit: state.TargetCommit, ServerVersion: state.ServerVersion}).Validate() != nil {
		return State{}, errors.New("stored updater state is invalid")
	}
	if state.LocalMigration && (stateProtocol(state) != 1 || state.TargetVersion != "v1.0.1") {
		return State{}, errors.New("invalid local migration authorization")
	}
	if state.SourceVersion != "" {
		comparison, ok := buildinfo.CompareReleaseVersions(state.SourceVersion, state.TargetVersion)
		if !ok || comparison >= 0 {
			return State{}, errors.New("invalid stored source version")
		}
	}
	if !isActiveLocalStatus(state.Status) && state.Status != "succeeded" && state.Status != "failed" && state.Status != "rolled_back" {
		return State{}, errors.New("invalid stored updater status")
	}
	return state, nil
}

func assetName(goos, goarch string) (string, error) {
	if goos != "linux" || (goarch != "amd64" && goarch != "arm64") {
		return "", errors.New("unsupported_platform")
	}
	return "404-probe-agent-linux-" + goarch, nil
}

func requiresLinkedVersion(target string) bool {
	comparison, ok := buildinfo.CompareReleaseVersions(target, "v1.0.1")
	return ok && comparison >= 0
}
func stateProtocol(s State) int {
	if s.ProtocolVersion == 0 {
		return 1
	}
	return s.ProtocolVersion
}
func stateMatches(s State, r Request) bool {
	return stateProtocol(s) == r.ProtocolVersion && s.Channel == r.Channel && s.TargetCommit == r.TargetCommit && s.ServerVersion == r.ServerVersion
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

type rootStateFile interface {
	Chmod(os.FileMode) error
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

type rootFileOps struct {
	mkdirAll      func(string, os.FileMode) error
	openFile      func(string, int, os.FileMode) (rootStateFile, error)
	rename        func(string, string) error
	remove        func(string) error
	syncDirectory func(string) error
}

func defaultRootFileOps() rootFileOps {
	return rootFileOps{
		mkdirAll: os.MkdirAll,
		openFile: func(path string, flag int, mode os.FileMode) (rootStateFile, error) {
			return os.OpenFile(path, flag, mode)
		},
		rename:        os.Rename,
		remove:        os.Remove,
		syncDirectory: syncDirectory,
	}
}

// writeRootFileCommitted reports whether rename completed. A directory-sync
// error is post-commit: callers must inspect the destination before deciding
// whether to publish the candidate in memory or start work.
func writeRootFileCommitted(path string, data []byte, mode os.FileMode, ops rootFileOps) (bool, error) {
	if err := ops.mkdirAll(filepath.Dir(path), 0700); err != nil {
		return false, fmt.Errorf("create updater state directory: %w", err)
	}
	temporary := path + ".tmp"
	file, err := ops.openFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return false, fmt.Errorf("open updater state temporary file: %w", err)
	}
	var writeErr error
	if err := file.Chmod(mode); err != nil {
		writeErr = fmt.Errorf("set updater state temporary file mode: %w", err)
	}
	if writeErr == nil {
		written, err := file.Write(data)
		if err != nil {
			writeErr = fmt.Errorf("write updater state temporary file: %w", err)
		} else if written != len(data) {
			writeErr = fmt.Errorf("write updater state temporary file: %w", io.ErrShortWrite)
		}
	}
	if writeErr == nil {
		if err := file.Sync(); err != nil {
			writeErr = fmt.Errorf("sync updater state temporary file: %w", err)
		}
	}
	if closeErr := file.Close(); closeErr != nil {
		wrappedCloseErr := fmt.Errorf("close updater state temporary file: %w", closeErr)
		if writeErr == nil {
			writeErr = wrappedCloseErr
		} else {
			writeErr = errors.Join(writeErr, wrappedCloseErr)
		}
	}
	if writeErr != nil {
		return false, cleanupUpdaterStateTemporary(ops, temporary, writeErr)
	}
	if err := ops.rename(temporary, path); err != nil {
		return false, cleanupUpdaterStateTemporary(ops, temporary, fmt.Errorf("rename updater state temporary file: %w", err))
	}
	if err := ops.syncDirectory(filepath.Dir(path)); err != nil {
		return true, fmt.Errorf("sync updater state parent directory after rename: %w", err)
	}
	return true, nil
}

func cleanupUpdaterStateTemporary(ops rootFileOps, path string, cause error) error {
	if err := ops.remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.Join(cause, fmt.Errorf("remove updater state temporary file: %w", err))
	}
	return cause
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
	if err != nil || e.state.ReleaseCommit == "" || info.Path != "404-probe/cmd/agent" || info.Commit != e.state.ReleaseCommit || info.Dirty || info.GOOS != e.config.GOOS || info.GOARCH != e.config.GOARCH || requiresLinkedVersion(e.state.TargetVersion) && info.Version != e.state.TargetVersion {
		// The installing journal precedes the atomic swap. If there is no
		// backup and this process runs the actual recorded source version,
		// restart that original Agent instead of attempting a missing rollback.
		if _, previousErr := os.Lstat(e.config.PreviousBinary); errors.Is(previousErr, os.ErrNotExist) && e.state.SourceVersion != "" && e.config.CurrentVersion == e.state.SourceVersion && requireRegular(e.config.LiveBinary) == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			restartErr := e.config.ServiceCommand(ctx, "restart")
			cancel()
			if restartErr == nil {
				e.finishFailure(false, "updater_restarted")
				return
			}
		}
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
	if strings.Contains(err.Error(), "candidate_version_mismatch") {
		return "candidate_version_mismatch"
	}
	for _, code := range []string{"release_metadata_download_failed", "release_metadata_version_mismatch", "release_metadata_asset_invalid", "release_metadata_invalid", "checksum_manifest_failed", "checksum_manifest_mismatch", "checksum_mismatch", "download_failed", "candidate_buildinfo_invalid", "candidate_revision_mismatch", "candidate_dirty", "candidate_platform_mismatch", "unsupported_platform", "stage_failed"} {
		if strings.Contains(err.Error(), code) {
			return code
		}
	}
	return "download_failed"
}

func safeFailureMessage(code string) string {
	switch code {
	case "rollback_failed":
		return "previous Agent restoration or actual process startup could not be confirmed"
	case "release_metadata_download_failed", "release_metadata_invalid", "release_metadata_version_mismatch", "release_metadata_asset_invalid":
		return "upgrade release metadata could not be verified"
	case "checksum_manifest_failed":
		return "upgrade checksum manifest could not be verified"
	case "checksum_manifest_mismatch":
		return "upgrade release metadata and checksum manifest did not match"
	case "checksum_mismatch":
		return "upgrade candidate checksum did not match"
	case "candidate_buildinfo_invalid", "candidate_revision_mismatch", "candidate_dirty", "candidate_platform_mismatch", "candidate_version_mismatch":
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

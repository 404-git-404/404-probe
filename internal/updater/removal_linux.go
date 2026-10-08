//go:build linux

package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type fixedAgentRemovalLauncher struct {
	agentEnvPath      string
	stateDirectory    string
	requestPath       string
	unitPath          string
	unitName          string
	finalizerPath     string
	finalizerName     string
	workerBinary      string
	sourceAgentBinary string
	helperPath        string
	serviceAccountIDs func() (uint32, uint32, error)
	startCommand      func(context.Context, ...string) error
	runHelper         func(context.Context, ...string) error
}

func newFixedAgentRemovalLauncher() *fixedAgentRemovalLauncher {
	return &fixedAgentRemovalLauncher{
		agentEnvPath: agentEnvironmentPath, stateDirectory: removalStateDirectory,
		requestPath: removalRequestPath, unitPath: removalWorkerUnitPath,
		unitName: removalWorkerUnitName, finalizerPath: removalFinalizerUnitPath,
		finalizerName: removalFinalizerUnitName, workerBinary: removalWorkerBinary,
		sourceAgentBinary: liveAgentBinary, serviceAccountIDs: localServiceAccountIDs,
		helperPath: installHelperPath, startCommand: runSystemctl,
		runHelper: func(ctx context.Context, args ...string) error {
			command := exec.CommandContext(ctx, installHelperPath, args...)
			command.Stdout, command.Stderr = os.Stdout, os.Stderr
			return command.Run()
		},
	}
}

func (l *fixedAgentRemovalLauncher) Prepare() error {
	if os.Geteuid() != 0 {
		return errors.New("Agent removal preparation requires root")
	}
	unit, err := readFixedRootRegular(l.unitPath, 64<<10)
	if err != nil || !bytes.Equal(unit, []byte(removalWorkerUnitContents)) {
		return errors.New("fixed Agent removal worker unit is unavailable")
	}
	finalizer, err := readFixedRootRegular(l.finalizerPath, 64<<10)
	if err != nil || !bytes.Equal(finalizer, []byte(removalFinalizerUnitContents)) {
		return errors.New("fixed Agent removal finalizer unit is unavailable")
	}
	if _, err := readFixedRootRegular(l.helperPath, 1<<20); err != nil {
		return errors.New("fixed Agent uninstall helper is unavailable")
	}
	helper, err := os.ReadFile(l.helperPath)
	if err != nil || !bytes.Contains(helper, []byte(removalHelperContract)) ||
		!bytes.Contains(helper, []byte(l.unitName)) || !bytes.Contains(helper, []byte(removalAccountHelperContract)) {
		return errors.New("Agent uninstall helper does not support receipt recovery")
	}
	if _, err := l.agentServerURL(); err != nil {
		return err
	}
	return ensureCopiedWorkerBinary(l.sourceAgentBinary, l.workerBinary)
}

func (l *fixedAgentRemovalLauncher) SupportsRemoteRemoval() bool {
	unit, err := readFixedRootRegular(l.unitPath, 64<<10)
	if err != nil || !bytes.Equal(unit, []byte(removalWorkerUnitContents)) {
		return false
	}
	finalizer, err := readFixedRootRegular(l.finalizerPath, 64<<10)
	if err != nil || !bytes.Equal(finalizer, []byte(removalFinalizerUnitContents)) {
		return false
	}
	if _, err := readFixedRootRegular(l.workerBinary, 128<<20); err != nil {
		return false
	}
	if _, err := readFixedRootRegular(l.helperPath, 1<<20); err != nil {
		return false
	}
	data, err := os.ReadFile(l.helperPath)
	if err != nil || !bytes.Contains(data, []byte(removalHelperContract)) ||
		!bytes.Contains(data, []byte(l.unitName)) || !bytes.Contains(data, []byte(removalAccountHelperContract)) {
		return false
	}
	_, err = l.agentServerURL()
	return err == nil
}

func (l *fixedAgentRemovalLauncher) agentServerURL() (string, error) {
	lookup := l.serviceAccountIDs
	if lookup == nil {
		lookup = localServiceAccountIDs
	}
	serviceUID, serviceGID, err := lookup()
	if err != nil {
		return "", errors.New("managed Agent service account is unavailable for fixed removal")
	}
	return readAgentServerURLForAccount(l.agentEnvPath, serviceUID, serviceGID)
}

func (l *fixedAgentRemovalLauncher) Start(ctx context.Context, operationID, receiptToken string) error {
	if !validOperationID(operationID) || !validReceiptToken(receiptToken) || !l.SupportsRemoteRemoval() {
		return errors.New("fixed Agent removal worker is unavailable")
	}
	serverURL, err := l.agentServerURL()
	if err != nil {
		return errors.New("Agent Server endpoint is unavailable for removal receipt")
	}
	if err := ensureFixedRootDirectory(l.stateDirectory); err != nil {
		return err
	}
	serviceUID, serviceGID, err := localServiceAccountIDs()
	if err != nil {
		return errors.New("managed Agent service account is unavailable for fixed removal")
	}
	state := removalRequestState{OperationID: operationID, ReceiptToken: receiptToken, ServerURL: serverURL,
		Phase: "requested", ServiceUID: serviceUID, ServiceGID: serviceGID}
	markerExists, err := fixedRemovalMarkerExists()
	if err != nil {
		return err
	}
	if markerExists {
		if _, err := os.Lstat(l.requestPath); errors.Is(err, os.ErrNotExist) {
			return errors.New("stale Agent removal completion marker requires local recovery")
		}
	}
	if existing, err := readRemovalRequest(l.requestPath); err == nil {
		if existing.OperationID != operationID {
			return errors.New("another Agent removal worker request is pending")
		}
		if existing.ServiceUID != serviceUID || existing.ServiceGID != serviceGID {
			return errors.New("Agent removal account identity changed; local recovery is required")
		}
		state.Phase = existing.Phase
		if state.Phase == "receipt_succeeded" || state.Phase == "finalizing" {
			return errors.New("Agent removal receipt already succeeded; local cleanup is pending")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := writeRemovalRequest(l.requestPath, state); err != nil {
		return err
	}
	if err := l.startCommand(ctx, "enable", l.unitName); err != nil {
		return errors.New("could not enable fixed Agent removal worker")
	}
	if err := l.startCommand(ctx, "start", "--no-block", l.unitName); err != nil {
		return errors.New("could not start fixed Agent removal worker")
	}
	return nil
}

func localServiceAccountIDs() (uint32, uint32, error) {
	account, err := user.Lookup(serviceUserName)
	if err != nil {
		return 0, 0, err
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil || uid == 0 {
		return 0, 0, errors.New("managed Agent service UID is invalid")
	}
	gid, err := strconv.ParseUint(account.Gid, 10, 32)
	if err != nil || gid == 0 {
		return 0, 0, errors.New("managed Agent service GID is invalid")
	}
	group, err := user.LookupGroupId(strconv.FormatUint(gid, 10))
	if err != nil || group.Name != serviceUserName {
		return 0, 0, errors.New("managed Agent primary group is invalid")
	}
	return uint32(uid), uint32(gid), nil
}

func ensureCopiedWorkerBinary(sourcePath, destination string) error {
	if err := requireRootOwnedPathParents(filepath.Dir(destination)); err != nil {
		return err
	}
	source, err := readFixedRootRegular(sourcePath, 128<<20)
	if err != nil {
		return fmt.Errorf("read installed Agent binary for removal worker: %w", err)
	}
	if current, err := readFixedRootRegular(destination, 128<<20); err == nil && bytes.Equal(source, current) {
		return nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeFixedRootFile(destination, source, 0755)
}

func readAgentServerURL(path string) (string, error) {
	serviceUID, serviceGID, err := localServiceAccountIDs()
	if err != nil {
		return "", errors.New("managed Agent service account is unavailable for fixed removal")
	}
	return readAgentServerURLForAccount(path, serviceUID, serviceGID)
}

func readAgentServerURLForAccount(path string, serviceUID, serviceGID uint32) (string, error) {
	data, err := readManagedAgentEnvironment(path, serviceUID, serviceGID)
	if err != nil {
		return "", err
	}
	serverURL := ""
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "PROBE_404_SERVER=") {
			if serverURL != "" {
				return "", errors.New("Agent Server endpoint is ambiguous")
			}
			serverURL = strings.TrimPrefix(line, "PROBE_404_SERVER=")
		}
	}
	parsed, err := url.Parse(serverURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || strings.TrimSpace(serverURL) != serverURL {
		return "", errors.New("Agent removal requires a configured HTTPS Server origin")
	}
	return strings.TrimRight(serverURL, "/"), nil
}

const maxAgentEnvironmentSize int64 = 16 << 10

func readManagedAgentEnvironment(path string, serviceUID, serviceGID uint32) ([]byte, error) {
	unsafe := errors.New("managed Agent environment file is unsafe")
	if serviceUID == 0 || serviceGID == 0 {
		return nil, unsafe
	}
	parent := filepath.Dir(path)
	if err := requireRootOwnedPathParents(parent); err != nil {
		return nil, unsafe
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 || parentInfo.Mode().Perm() != 0750 ||
		parentInfo.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return nil, unsafe
	}
	parentStat, ok := parentInfo.Sys().(*syscall.Stat_t)
	if !ok || parentStat.Uid != 0 || parentStat.Gid != serviceGID {
		return nil, unsafe
	}

	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, unsafe
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0400 ||
		info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || info.Size() > maxAgentEnvironmentSize {
		return nil, unsafe
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != serviceUID || stat.Gid != serviceGID {
		return nil, unsafe
	}

	data, err := io.ReadAll(io.LimitReader(file, maxAgentEnvironmentSize+1))
	if err != nil || int64(len(data)) > maxAgentEnvironmentSize {
		return nil, unsafe
	}
	return data, nil
}

func ServeAgentRemovalWorker(ctx context.Context) error {
	if os.Geteuid() != 0 {
		return errors.New("Agent removal worker must run as root")
	}
	return runFixedAgentRemovalWorker(ctx, newFixedAgentRemovalLauncher())
}

func ServeAgentRemovalFinalizer(ctx context.Context) error {
	if os.Geteuid() != 0 {
		return errors.New("Agent removal finalizer must run as root")
	}
	return runFixedAgentRemovalFinalizer(ctx, newFixedAgentRemovalLauncher())
}

func runFixedAgentRemovalWorker(ctx context.Context, paths *fixedAgentRemovalLauncher) error {
	state, err := readRemovalRequest(paths.requestPath)
	if err != nil {
		return fmt.Errorf("read Agent removal work state: %w", err)
	}
	if state.Phase == "requested" {
		state.Phase = "uninstalling"
		if err := writeRemovalRequest(paths.requestPath, state); err != nil {
			return fmt.Errorf("persist Agent removal phase: %w", err)
		}
	}
	if state.Phase == "uninstalling" {
		if err := paths.startCommand(ctx, "start", paths.finalizerName); err != nil {
			return errors.New("fixed local Agent removal finalizer failed; recovery state was preserved")
		}
		state, err = readRemovalRequest(paths.requestPath)
		if err != nil || state.Phase != "agent_uninstalled" {
			return errors.New("fixed local Agent removal finalizer did not persist account-cleaned completion")
		}
	}
	if state.Phase == "agent_uninstalled" {
		if err := submitAgentRemovalReceipt(ctx, state); err != nil {
			return err
		}
		state.Phase = "receipt_succeeded"
		state.ReceiptToken = ""
		if err := writeRemovalRequest(paths.requestPath, state); err != nil {
			return fmt.Errorf("persist Agent removal receipt result: %w", err)
		}
	}
	if state.Phase == "receipt_succeeded" {
		state.Phase = "finalizing"
		if err := writeRemovalRequest(paths.requestPath, state); err != nil {
			return fmt.Errorf("persist Agent removal finalizing phase: %w", err)
		}
	}
	if state.Phase != "finalizing" {
		return errors.New("Agent removal state is not ready for receipt or finalization")
	}
	if err := paths.startCommand(ctx, "enable", paths.finalizerName); err != nil {
		return errors.New("could not enable fixed Agent removal recovery service")
	}
	if err := paths.startCommand(ctx, "start", "--no-block", paths.finalizerName); err != nil {
		return errors.New("could not start fixed Agent removal finalizer; recovery state was preserved")
	}
	return nil
}

func runFixedAgentRemovalFinalizer(ctx context.Context, paths *fixedAgentRemovalLauncher) error {
	state, err := readRemovalRequest(paths.requestPath)
	if errors.Is(err, os.ErrNotExist) {
		return recoverFinalizationTail(paths)
	}
	if err != nil {
		return fmt.Errorf("read Agent removal finalizer state: %w", err)
	}
	switch state.Phase {
	case "uninstalling":
		if _, err := readFixedRootRegular(paths.helperPath, 1<<20); err != nil {
			return errors.New("fixed Agent uninstall helper is unavailable")
		}
		if err := paths.runHelper(ctx, "uninstall", "agent", "--confirm-delete-data"); err != nil {
			return errors.New("fixed local Agent uninstall failed; recovery state was preserved")
		}
		if err := paths.runHelper(ctx, removalAccountHelperContract,
			strconv.FormatUint(uint64(state.ServiceUID), 10), strconv.FormatUint(uint64(state.ServiceGID), 10)); err != nil {
			return errors.New("fixed Agent account cleanup failed; success receipt was withheld")
		}
		if err := verifyRemovalAccountOutcome(state); err != nil {
			return fmt.Errorf("Agent account cleanup verification failed; success receipt was withheld: %w", err)
		}
		if err := writeFixedRootFile(removalFinishedMarker, nil, 0600); err != nil {
			return fmt.Errorf("persist Agent uninstall completion marker: %w", err)
		}
		state.Phase = "agent_uninstalled"
		if err := writeRemovalRequest(paths.requestPath, state); err != nil {
			return fmt.Errorf("persist Agent account-cleaned completion: %w", err)
		}
		return nil
	case "agent_uninstalled":
		return nil
	case "receipt_succeeded":
		state.Phase = "finalizing"
		if err := writeRemovalRequest(paths.requestPath, state); err != nil {
			return fmt.Errorf("persist Agent removal finalizing phase: %w", err)
		}
		fallthrough
	case "finalizing":
		return finalizeFixedAgentRemoval(paths, state)
	default:
		return errors.New("Agent removal finalizer received an unsupported local phase")
	}
}

func fixedRemovalMarkerExists() (bool, error) {
	_, err := readFixedRootRegular(removalFinishedMarker, 1)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func finalizeFixedAgentRemoval(paths *fixedAgentRemovalLauncher, state removalRequestState) error {
	if state.Phase != "finalizing" {
		return errors.New("Agent removal finalization is not durably staged")
	}
	if err := verifyRemovalAccountOutcome(state); err != nil {
		return fmt.Errorf("refusing to finalize with an unexpected service account: %w", err)
	}
	if err := assertAgentBusinessResourcesRemoved(true); err != nil {
		return err
	}
	if err := preflightRemovalStateDirectory(removalStateDirectory); err != nil {
		return err
	}
	if data, err := readFixedRootRegular(paths.unitPath, 64<<10); err != nil || !bytes.Equal(data, []byte(removalWorkerUnitContents)) {
		return errors.New("refusing to remove an unrecognized Agent removal worker unit")
	}
	if data, err := readFixedRootRegular(paths.finalizerPath, 64<<10); err != nil || !bytes.Equal(data, []byte(removalFinalizerUnitContents)) {
		return errors.New("refusing to remove an unrecognized Agent removal finalizer unit")
	}
	if err := removeAgentUpdaterState(); err != nil {
		return fmt.Errorf("Agent removal finalizing state remains recoverable: %w", err)
	}
	if err := runSystemctl(context.Background(), "disable", paths.unitName); err != nil {
		return errors.New("could not disable completed Agent removal worker; finalizer recovery service was preserved")
	}
	if err := removeFixedRegularIfPresent(paths.unitPath); err != nil {
		return err
	}
	if err := runSystemctl(context.Background(), "daemon-reload"); err != nil {
		return errors.New("could not reload systemd after removing Agent removal worker")
	}
	return recoverFinalizationTail(paths)
}

func recoverFinalizationTail(paths *fixedAgentRemovalLauncher) error {
	if err := removeEmptyUpdaterStateRemainder(updaterStateDirectory); err != nil {
		return fmt.Errorf("Agent removal finalizing recovery found ambiguous updater state: %w", err)
	}
	if err := assertAgentBusinessResourcesRemoved(false); err != nil {
		return fmt.Errorf("Agent removal finalizing recovery refused: %w", err)
	}
	serverExists, err := serverProtectedResidueExists()
	if err != nil {
		return err
	}
	_, accountLookupErr := user.Lookup(serviceUserName)
	_, groupLookupErr := user.LookupGroup(serviceUserName)
	if err := verifyFinalizationTailAccountOutcome(serverExists, accountLookupErr, groupLookupErr); err != nil {
		return err
	}
	if data, err := readFixedRootRegular(paths.unitPath, 64<<10); err == nil {
		if !bytes.Equal(data, []byte(removalWorkerUnitContents)) {
			return errors.New("refusing to remove an unrecognized Agent removal worker unit")
		}
		if err := runSystemctl(context.Background(), "disable", paths.unitName); err != nil {
			return errors.New("could not disable residual Agent removal worker")
		}
		if err := os.Remove(paths.unitPath); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if data, err := readFixedRootRegular(paths.finalizerPath, 64<<10); err == nil {
		if !bytes.Equal(data, []byte(removalFinalizerUnitContents)) {
			return errors.New("refusing to remove an unrecognized Agent removal finalizer unit")
		}
		if err := runSystemctl(context.Background(), "disable", paths.finalizerName); err != nil {
			return errors.New("could not disable residual Agent removal finalizer")
		}
		if err := os.Remove(paths.finalizerPath); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := runSystemctl(context.Background(), "daemon-reload"); err != nil {
		return errors.New("could not reload systemd after Agent removal recovery")
	}
	if err := removeFixedRegularIfPresent(paths.workerBinary); err != nil {
		return err
	}
	if !serverExists {
		if err := removeFixedRegularIfPresent(paths.helperPath); err != nil {
			return err
		}
	}
	return nil
}

func assertAgentBusinessResourcesRemoved(allowFinalizingState bool) error {
	paths := []string{
		agentUnitPath, agentUpdaterUnitPath, liveAgentBinary, stagedAgentBinary, previousAgentBinary,
		bootstrapAgentBinary, agentEnvironmentPath, agentEpochPath, agentEpochLockPath,
		agentSecurityAcksPath, selectorOrderPath, DefaultSocket, securityServiceUnitPath,
		securityTimerUnitPath, securityStateDirectory,
	}
	if !allowFinalizingState {
		paths = append(paths, updaterStateDirectory)
	}
	for _, path := range paths {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("Agent business resource remains before removal receipt finalization: %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func verifyRemovalAccountOutcome(state removalRequestState) error {
	serverProtected, err := serverProtectedResidueExists()
	if err != nil {
		return err
	}
	account, accountErr := user.Lookup(serviceUserName)
	group, groupErr := user.LookupGroup(serviceUserName)
	if serverProtected {
		if accountErr != nil || groupErr != nil || account.Uid != strconv.FormatUint(uint64(state.ServiceUID), 10) ||
			account.Gid != strconv.FormatUint(uint64(state.ServiceGID), 10) || group.Gid != strconv.FormatUint(uint64(state.ServiceGID), 10) {
			return errors.New("shared Server service account no longer matches the recorded UID/GID")
		}
		return nil
	}
	accountAbsent, err := isUnknownUser(accountErr)
	if err != nil {
		return err
	}
	groupAbsent, err := isUnknownGroup(groupErr)
	if err != nil {
		return err
	}
	if !accountAbsent || !groupAbsent {
		return errors.New("service username or group remains after Agent removal")
	}
	_, lookupErr := user.LookupId(strconv.FormatUint(uint64(state.ServiceUID), 10))
	uidAbsent, err := isUnknownUserID(lookupErr)
	if err != nil {
		return err
	}
	if !uidAbsent {
		return errors.New("recorded service UID belongs to another account")
	}
	_, lookupErr = user.LookupGroupId(strconv.FormatUint(uint64(state.ServiceGID), 10))
	gidAbsent, err := isUnknownGroupID(lookupErr)
	if err != nil {
		return err
	}
	if !gidAbsent {
		return errors.New("recorded service GID belongs to another group")
	}
	return nil
}

func serverProtectedResidueExists() (bool, error) {
	return serverProtectedResidueExistsAt([]string{
		serverUnitPath,
		serverBinaryPath,
		serverDatabasePath,
		serverDatabasePath + "-wal",
		serverDatabasePath + "-shm",
		serverEnvironmentPath,
		serverControlTokenPath,
		serverWebPasswordHashPath,
		serverUpgradeDirectory,
		serverUpgradeLockDirectory,
		serverUpgradeLockPath,
		serverUpgradeCandidatePath,
		serverUpgradePreviousPath,
		serverUpgradeHelperCandidate,
	})
}

func serverProtectedResidueExistsAt(paths []string) (bool, error) {
	for _, path := range paths {
		if _, err := os.Lstat(path); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

func verifyFinalizationTailAccountOutcome(serverProtected bool, accountErr, groupErr error) error {
	accountAbsent, err := isUnknownUser(accountErr)
	if err != nil {
		return err
	}
	groupAbsent, err := isUnknownGroup(groupErr)
	if err != nil {
		return err
	}
	if serverProtected {
		if accountAbsent || groupAbsent {
			return errors.New("Agent removal recovery found protected Server residue without its shared service account/group")
		}
		return nil
	}
	if !accountAbsent || !groupAbsent {
		return errors.New("Agent removal recovery found the service account/group after all Server and Agent data was removed")
	}
	return nil
}

func isUnknownUser(err error) (bool, error) {
	if err == nil {
		return false, nil
	}
	var unknown user.UnknownUserError
	if errors.As(err, &unknown) {
		return true, nil
	}
	return false, err
}

func isUnknownGroup(err error) (bool, error) {
	if err == nil {
		return false, nil
	}
	var unknown user.UnknownGroupError
	if errors.As(err, &unknown) {
		return true, nil
	}
	return false, err
}

func isUnknownUserID(err error) (bool, error) {
	if err == nil {
		return false, nil
	}
	var unknown user.UnknownUserIdError
	if errors.As(err, &unknown) {
		return true, nil
	}
	return false, err
}

func isUnknownGroupID(err error) (bool, error) {
	if err == nil {
		return false, nil
	}
	var unknown user.UnknownGroupIdError
	if errors.As(err, &unknown) {
		return true, nil
	}
	return false, err
}

func runSystemctl(ctx context.Context, args ...string) error {
	command := exec.CommandContext(ctx, "/usr/bin/systemctl", args...)
	if err := command.Run(); err != nil {
		return fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func ensureFixedRootDirectory(path string) error {
	if err := requireRootOwnedPathParents(filepath.Dir(path)); err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByRoot(info) {
		return errors.New("Agent removal state directory is unsafe")
	}
	return os.Chmod(path, 0700)
}

func requireRootOwnedPathParents(path string) error {
	current := string(filepath.Separator)
	for _, component := range strings.Split(strings.TrimPrefix(filepath.Clean(path), string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("fixed Agent removal path traverses an unsafe parent")
		}
	}
	return nil
}

func readFixedRootRegular(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !ownedByRoot(info) || info.Mode().Perm()&0022 != 0 || info.Size() > limit {
		return nil, errors.New("fixed Agent removal file is unsafe")
	}
	return os.ReadFile(path)
}

func writeFixedRootFile(path string, data []byte, mode os.FileMode) error {
	if err := requireRootOwnedPathParents(filepath.Dir(path)); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !ownedByRoot(info) {
			return errors.New("fixed Agent removal destination is unsafe")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if info, err := os.Lstat(path + ".tmp"); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !ownedByRoot(info) {
			return errors.New("fixed Agent removal temporary file is unsafe")
		}
		if err := os.Remove(path + ".tmp"); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeRootFile(path, data, mode)
}

func writeRemovalRequest(path string, state removalRequestState) error {
	if !validOperationID(state.OperationID) || state.ServiceUID == 0 || state.ServiceGID == 0 || !validRemovalPhase(state.Phase) {
		return errors.New("invalid Agent removal state")
	}
	if state.Phase == "receipt_succeeded" || state.Phase == "finalizing" {
		if state.ReceiptToken != "" {
			return errors.New("invalid Agent removal state")
		}
	} else if !validReceiptToken(state.ReceiptToken) {
		return errors.New("invalid Agent removal state")
	}
	if _, err := validateReceiptServerURL(state.ServerURL); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeFixedRootFile(path, append(data, '\n'), 0600)
}

func readRemovalRequest(path string) (removalRequestState, error) {
	data, err := readFixedRootRegular(path, removalWorkerMaxConfig)
	if err != nil {
		return removalRequestState{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state removalRequestState
	if err := decoder.Decode(&state); err != nil {
		return removalRequestState{}, errors.New("stored Agent removal state is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return removalRequestState{}, errors.New("stored Agent removal state is invalid")
	}
	if !validOperationID(state.OperationID) || state.ServiceUID == 0 || state.ServiceGID == 0 || !validRemovalPhase(state.Phase) ||
		((state.Phase == "receipt_succeeded" || state.Phase == "finalizing") && state.ReceiptToken != "") ||
		(state.Phase != "receipt_succeeded" && state.Phase != "finalizing" && !validReceiptToken(state.ReceiptToken)) {
		return removalRequestState{}, errors.New("stored Agent removal state is invalid")
	}
	if _, err := validateReceiptServerURL(state.ServerURL); err != nil {
		return removalRequestState{}, errors.New("stored Agent removal state is invalid")
	}
	return state, nil
}

func validRemovalPhase(phase string) bool {
	switch phase {
	case "requested", "uninstalling", "agent_uninstalled", "receipt_succeeded", "finalizing":
		return true
	default:
		return false
	}
}

func ownedByRoot(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0
}

func fixedPathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func preflightFixedRegularOrAbsent(path string) error {
	if _, err := readFixedRootRegular(path, 128<<20); err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	} else {
		return err
	}
}

func removeFixedRegularIfPresent(path string) error {
	if err := preflightFixedRegularOrAbsent(path); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func preflightRemovalStateDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByRoot(info) {
		return errors.New("Agent removal state directory is unsafe")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		switch entry.Name() {
		case filepath.Base(removalRequestPath), filepath.Base(removalRequestPath) + ".tmp", filepath.Base(removalFinishedMarker):
			if _, err := readFixedRootRegular(filepath.Join(path, entry.Name()), removalWorkerMaxConfig); err != nil {
				return err
			}
		default:
			return errors.New("Agent removal state directory contains an unknown file")
		}
	}
	return nil
}

func removeRemovalStateDirectory(path string) error {
	if err := preflightRemovalStateDirectory(path); err != nil {
		return err
	}
	_, err := os.ReadDir(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, name := range []string{
		filepath.Base(removalRequestPath) + ".tmp",
		filepath.Base(removalFinishedMarker),
		filepath.Base(removalRequestPath),
	} {
		if err := os.Remove(filepath.Join(path, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.Remove(path)
}

func removeAgentUpdaterState() error {
	return removeAgentUpdaterStateAt(updaterStateDirectory)
}

func removeAgentUpdaterStateAt(directory string) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByRoot(info) {
		return errors.New("Agent updater state directory is unsafe")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "operation.json", "operation.json.tmp", "candidate", "candidate.tmp":
			if _, err := readFixedRootRegular(filepath.Join(directory, entry.Name()), 128<<20); err != nil {
				return err
			}
		case "removal":
			if err := preflightRemovalStateDirectory(filepath.Join(directory, entry.Name())); err != nil {
				return err
			}
		default:
			return errors.New("Agent updater state contains an unknown file")
		}
	}
	// Keep the durable finalizing request until every other updater file is gone.
	for _, name := range []string{"operation.json", "operation.json.tmp", "candidate", "candidate.tmp"} {
		if err := removeFixedRegularIfPresent(filepath.Join(directory, name)); err != nil {
			return err
		}
	}
	if err := removeRemovalStateDirectory(filepath.Join(directory, "removal")); err != nil {
		return err
	}
	return os.Remove(directory)
}

func removeEmptyUpdaterStateRemainder(directory string) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByRoot(info) {
		return errors.New("Agent updater state directory is unsafe during finalization recovery")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != "removal" {
			return errors.New("Agent updater state contains data without its durable removal request")
		}
		removalPath := filepath.Join(directory, entry.Name())
		removalInfo, err := os.Lstat(removalPath)
		if err != nil || !removalInfo.IsDir() || removalInfo.Mode()&os.ModeSymlink != 0 || !ownedByRoot(removalInfo) {
			return errors.New("Agent removal state directory is unsafe during finalization recovery")
		}
		removalEntries, err := os.ReadDir(removalPath)
		if err != nil {
			return err
		}
		if len(removalEntries) != 0 {
			return errors.New("Agent removal state contains files without its durable request")
		}
		if err := os.Remove(removalPath); err != nil {
			return err
		}
	}
	return os.Remove(directory)
}

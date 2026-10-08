//go:build linux

package updater

import (
	"bytes"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestRemovalRequestPersistsBoundAccountIdentityAndFinalizingPhase(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("fixed removal-state ownership contract requires root")
	}
	const token = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcde_-"
	for _, phase := range []string{"requested", "uninstalling", "agent_uninstalled", "receipt_succeeded", "finalizing"} {
		t.Run(phase, func(t *testing.T) {
			state := removalRequestState{
				OperationID: "0123456789abcdef0123456789abcdef",
				ServerURL:   "https://probe.example",
				Phase:       phase,
				ServiceUID:  404,
				ServiceGID:  405,
			}
			if phase != "receipt_succeeded" && phase != "finalizing" {
				state.ReceiptToken = token
			}
			path := filepath.Join(t.TempDir(), "request.json")
			if err := writeRemovalRequest(path, state); err != nil {
				t.Fatal(err)
			}
			got, err := readRemovalRequest(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != state {
				t.Fatalf("round trip state=%+v, want %+v", got, state)
			}
		})
	}
}

func TestReadManagedAgentEnvironmentMatchesInstallerOwnershipContract(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("managed Agent environment ownership contract requires root")
	}

	const serviceUID, serviceGID = uint32(43210), uint32(43211)
	for name, mutate := range map[string]func(*testing.T, string, string){
		"symlink": func(t *testing.T, directory, path string) {
			target := filepath.Join(directory, "agent.env.target")
			if err := os.Rename(path, target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		},
		"wrong_owner": func(t *testing.T, _, path string) {
			if err := os.Chown(path, 0, int(serviceGID)); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0400); err != nil {
				t.Fatal(err)
			}
		},
		"wrong_group": func(t *testing.T, _, path string) {
			if err := os.Chown(path, int(serviceUID), int(serviceGID+1)); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0400); err != nil {
				t.Fatal(err)
			}
		},
		"wrong_mode": func(t *testing.T, _, path string) {
			if err := os.Chmod(path, 0600); err != nil {
				t.Fatal(err)
			}
		},
		"oversized": func(t *testing.T, _, path string) {
			if err := os.Chmod(path, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, bytes.Repeat([]byte("x"), int(maxAgentEnvironmentSize+1)), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chown(path, int(serviceUID), int(serviceGID)); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0400); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "config")
			path := writeTestManagedAgentEnvironment(t, directory, serviceUID, serviceGID)
			mutate(t, directory, path)
			if _, err := readAgentServerURLForAccount(path, serviceUID, serviceGID); err == nil {
				t.Fatal("unsafe Agent environment was accepted")
			}
		})
	}

	for name, mutate := range map[string]func(*testing.T, string){
		"parent_wrong_owner": func(t *testing.T, directory string) {
			if err := os.Chown(directory, int(serviceUID), int(serviceGID)); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(directory, 0750); err != nil {
				t.Fatal(err)
			}
		},
		"parent_wrong_group": func(t *testing.T, directory string) {
			if err := os.Chown(directory, 0, int(serviceGID+1)); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(directory, 0750); err != nil {
				t.Fatal(err)
			}
		},
		"parent_wrong_mode": func(t *testing.T, directory string) {
			if err := os.Chmod(directory, 0770); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "config")
			path := writeTestManagedAgentEnvironment(t, directory, serviceUID, serviceGID)
			mutate(t, directory)
			if _, err := readAgentServerURLForAccount(path, serviceUID, serviceGID); err == nil {
				t.Fatal("unsafe Agent configuration directory was accepted")
			}
		})
	}
}

func TestFixedAgentRemovalPrepareCopiesRootOwnedWorkerForInstalledAgent(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("fixed removal ownership contract requires root")
	}

	const serviceUID, serviceGID = uint32(43210), uint32(43211)
	root := t.TempDir()
	configDirectory := filepath.Join(root, "config")
	agentEnvPath := writeTestManagedAgentEnvironment(t, configDirectory, serviceUID, serviceGID)
	unitPath := filepath.Join(root, "agent-removal.service")
	finalizerPath := filepath.Join(root, "agent-removal-finalize.service")
	helperPath := filepath.Join(root, "404-probe-install")
	sourceAgentBinary := filepath.Join(root, "404-probe-agent")
	workerBinary := filepath.Join(root, ".404-probe-agent-removal-worker")
	helperContents := []byte(strings.Join([]string{
		removalHelperContract,
		removalWorkerUnitName,
		removalAccountHelperContract,
	}, "\n"))
	for path, contents := range map[string][]byte{
		unitPath:          []byte(removalWorkerUnitContents),
		finalizerPath:     []byte(removalFinalizerUnitContents),
		helperPath:        helperContents,
		sourceAgentBinary: []byte("test Agent binary"),
	} {
		if err := os.WriteFile(path, contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(helperPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sourceAgentBinary, 0755); err != nil {
		t.Fatal(err)
	}

	launcher := &fixedAgentRemovalLauncher{
		agentEnvPath:      agentEnvPath,
		unitPath:          unitPath,
		unitName:          removalWorkerUnitName,
		finalizerPath:     finalizerPath,
		workerBinary:      workerBinary,
		sourceAgentBinary: sourceAgentBinary,
		helperPath:        helperPath,
		serviceAccountIDs: func() (uint32, uint32, error) { return serviceUID, serviceGID, nil },
	}
	if err := launcher.Prepare(); err != nil {
		t.Fatalf("Prepare rejected the installer's managed Agent environment: %v", err)
	}
	if !launcher.SupportsRemoteRemoval() {
		t.Fatal("launcher does not advertise remote removal after successful Prepare")
	}

	workerInfo, err := os.Lstat(workerBinary)
	if err != nil {
		t.Fatalf("copied removal worker is missing: %v", err)
	}
	workerStat, ok := workerInfo.Sys().(*syscall.Stat_t)
	if !ok || workerStat.Uid != 0 || workerStat.Gid != 0 || workerInfo.Mode().Perm() != 0755 ||
		!workerInfo.Mode().IsRegular() || workerInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("copied worker metadata=%v, want root:root regular 0755", workerInfo)
	}
	workerContents, err := os.ReadFile(workerBinary)
	if err != nil || !bytes.Equal(workerContents, []byte("test Agent binary")) {
		t.Fatalf("copied worker contents=%q err=%v", workerContents, err)
	}
}

func writeTestManagedAgentEnvironment(t *testing.T, directory string, serviceUID, serviceGID uint32) string {
	t.Helper()
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(directory, 0, int(serviceGID)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "agent.env")
	if err := os.WriteFile(path, []byte("PROBE_404_SERVER=https://probe.example\nPROBE_404_TOKEN=test-placeholder\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, int(serviceUID), int(serviceGID)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRemovalRequestRejectsMissingOrLateReceiptCredentials(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("fixed removal-state ownership contract requires root")
	}
	base := removalRequestState{
		OperationID:  "0123456789abcdef0123456789abcdef",
		ServerURL:    "https://probe.example",
		Phase:        "requested",
		ReceiptToken: "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcde_-",
		ServiceUID:   404,
		ServiceGID:   405,
	}
	for name, mutate := range map[string]func(*removalRequestState){
		"missing_uid":                  func(state *removalRequestState) { state.ServiceUID = 0 },
		"missing_gid":                  func(state *removalRequestState) { state.ServiceGID = 0 },
		"missing_token_before_receipt": func(state *removalRequestState) { state.ReceiptToken = "" },
		"token_after_receipt":          func(state *removalRequestState) { state.Phase, state.ReceiptToken = "finalizing", base.ReceiptToken },
		"unsupported_phase":            func(state *removalRequestState) { state.Phase = "cleanup_done" },
	} {
		t.Run(name, func(t *testing.T) {
			state := base
			mutate(&state)
			path := filepath.Join(t.TempDir(), "request.json")
			if err := writeRemovalRequest(path, state); err == nil {
				t.Fatalf("invalid state was written: %+v", state)
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid state left a file behind: %v", err)
			}
		})
	}
}

func TestRemovalUnitAuthorityIsSplitBetweenReceiptAndLocalFinalizer(t *testing.T) {
	for _, required := range []string{
		"StartLimitIntervalSec=30min",
		"StartLimitBurst=10",
		"RestartSec=30s",
		"TimeoutStartSec=5min",
		"RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6",
		"ReadWritePaths=" + removalWorkerWritablePaths,
	} {
		if !strings.Contains(removalWorkerUnitContents, required) {
			t.Fatalf("receipt worker missing bounded/network contract %q", required)
		}
	}
	if strings.Contains(removalWorkerUnitContents, "PrivateNetwork=true") || strings.Contains(removalWorkerUnitContents, "ReadWritePaths=/etc ") {
		t.Fatal("network receipt worker has local account-cleanup authority")
	}
	for _, required := range []string{
		"StartLimitIntervalSec=10min",
		"StartLimitBurst=5",
		"RestartSec=30s",
		"TimeoutStartSec=10min",
		"PrivateNetwork=true",
		"RestrictAddressFamilies=AF_UNIX",
		"ReadWritePaths=" + removalFinalizerWritablePaths,
	} {
		if !strings.Contains(removalFinalizerUnitContents, required) {
			t.Fatalf("local finalizer missing bounded/network contract %q", required)
		}
	}
	if strings.Contains(removalFinalizerUnitContents, "AF_INET") {
		t.Fatal("local account-cleanup finalizer permits network address families")
	}
	for _, stableParent := range []string{"/etc", "/var/lib", "/run"} {
		if !strings.Contains(removalFinalizerUnitContents, "ReadWritePaths="+removalFinalizerWritablePaths) ||
			!strings.Contains(removalFinalizerWritablePaths, stableParent) {
			t.Fatalf("fixed finalizer does not use stable writable parent %s", stableParent)
		}
	}
	for _, removableSubtree := range []string{
		"/etc/404-probe",
		"/var/lib/404-probe-updater",
		"/var/lib/404-probe",
		"/var/lib/404-probe-security",
		"/run/404-probe",
	} {
		if strings.Contains(removalFinalizerUnitContents, removableSubtree) {
			t.Fatalf("fixed finalizer makes removable subtree a bind-mount boundary: %s", removableSubtree)
		}
	}
}

func TestFinalizationRecoveryRemovesOnlyEmptyKnownUpdaterDirectories(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("fixed removal-state ownership contract requires root")
	}
	parent := filepath.Join(t.TempDir(), "updater")
	if err := os.MkdirAll(filepath.Join(parent, "removal"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := removeEmptyUpdaterStateRemainder(parent); err != nil {
		t.Fatalf("empty post-removal directories were not recoverable: %v", err)
	}
	if _, err := os.Lstat(parent); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty post-removal parent remains or could not be removed: %v", err)
	}

	for name, setup := range map[string]func(string) error{
		"unknown_file": func(directory string) error {
			return os.WriteFile(filepath.Join(directory, "unknown"), []byte("preserve"), 0600)
		},
		"request_temp": func(directory string) error {
			return os.WriteFile(filepath.Join(directory, "removal", "request.json.tmp"), []byte("{}"), 0600)
		},
	} {
		t.Run(name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "updater")
			if err := os.MkdirAll(filepath.Join(directory, "removal"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := setup(directory); err != nil {
				t.Fatal(err)
			}
			if err := removeEmptyUpdaterStateRemainder(directory); err == nil {
				t.Fatal("ambiguous updater state was removed")
			}
			if _, err := os.Lstat(directory); err != nil {
				t.Fatalf("ambiguous updater state was not preserved: %v", err)
			}
		})
	}
}

func TestRemoveAgentUpdaterStateRemovesValidatedState(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("fixed removal-state ownership contract requires root")
	}
	directory := filepath.Join(t.TempDir(), "updater")
	removalDirectory := filepath.Join(directory, "removal")
	if err := os.MkdirAll(removalDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"operation.json", "operation.json.tmp", "candidate", "candidate.tmp"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	state := removalRequestState{
		OperationID: "0123456789abcdef0123456789abcdef",
		ServerURL:   "https://probe.example",
		Phase:       "finalizing",
		ServiceUID:  404,
		ServiceGID:  405,
	}
	if err := writeRemovalRequest(filepath.Join(removalDirectory, "request.json"), state); err != nil {
		t.Fatal(err)
	}
	if err := writeFixedRootFile(filepath.Join(removalDirectory, "agent-uninstalled"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := removeAgentUpdaterStateAt(directory); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("updater removal state remains: %v", err)
	}
}

func TestFinalizingRequestDeletionCanRecoverTailWithProtectedServerResidue(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("fixed removal-state ownership contract requires root")
	}
	updaterDirectory := filepath.Join(t.TempDir(), "updater")
	removalDirectory := filepath.Join(updaterDirectory, "removal")
	if err := os.MkdirAll(removalDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	state := removalRequestState{
		OperationID: "0123456789abcdef0123456789abcdef",
		ServerURL:   "https://probe.example",
		Phase:       "finalizing",
		ServiceUID:  404,
		ServiceGID:  405,
	}
	requestPath := filepath.Join(removalDirectory, filepath.Base(removalRequestPath))
	if err := writeRemovalRequest(requestPath, state); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(removalDirectory, filepath.Base(removalFinishedMarker))
	if err := writeFixedRootFile(markerPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	serverResidue := filepath.Join(t.TempDir(), "server.db-wal")
	if err := os.WriteFile(serverResidue, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}

	// Model the fixed finalizer having durably entered finalizing and removed
	// its request state, then being restarted into the no-request recovery tail.
	if err := removeAgentUpdaterStateAt(updaterDirectory); err != nil {
		t.Fatalf("remove validated finalizing request state: %v", err)
	}
	if _, err := os.Lstat(requestPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("finalizing request was not removed before tail recovery: %v", err)
	}
	if err := removeEmptyUpdaterStateRemainder(updaterDirectory); err != nil {
		t.Fatalf("recover no-request finalization tail: %v", err)
	}
	protected, err := serverProtectedResidueExistsAt([]string{serverResidue})
	if err != nil || !protected {
		t.Fatalf("Server WAL residue was not recognized as protected: protected=%t err=%v", protected, err)
	}
	if err := verifyFinalizationTailAccountOutcome(protected, nil, nil); err != nil {
		t.Fatalf("recovery tail rejected the shared account with Server residue: %v", err)
	}
	if err := verifyFinalizationTailAccountOutcome(protected, user.UnknownUserError(serviceUserName), nil); err == nil {
		t.Fatal("recovery tail accepted protected Server residue without its shared account")
	}
	if err := verifyFinalizationTailAccountOutcome(false,
		user.UnknownUserError(serviceUserName), user.UnknownGroupError(serviceUserName)); err != nil {
		t.Fatalf("recovery tail rejected absent account/group without Server residue: %v", err)
	}
	if err := verifyFinalizationTailAccountOutcome(false, nil, nil); err == nil {
		t.Fatal("recovery tail accepted a service account after all Server and Agent data was removed")
	}
	if err := verifyFinalizationTailAccountOutcome(false, errors.New("NSS unavailable"),
		user.UnknownGroupError(serviceUserName)); err == nil {
		t.Fatal("recovery tail treated an unknown NSS failure as an absent service account")
	}
}

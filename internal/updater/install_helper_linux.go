//go:build linux

package updater

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

const installHelperCandidateFile = "management-helper.candidate"

// Existing Agents run with /usr/local/sbin read-only. Keep that restriction
// and let systemd run one fixed, short-lived writer with only that directory writable.
func applyInstallHelperWithSystemd(ctx context.Context, helper []byte) error {
	staged := filepath.Join(updaterStateDirectory, installHelperCandidateFile)
	if err := writeRootFile(staged, helper, 0600); err != nil {
		return err
	}
	defer os.Remove(staged)
	cmd := exec.CommandContext(ctx, "/usr/bin/systemd-run", "--quiet", "--wait", "--collect",
		"--unit=404-probe-install-helper-update", "--service-type=exec",
		"--property=RuntimeMaxSec=15s", "--property=UMask=0077", "--property=NoNewPrivileges=yes",
		"--property=PrivateTmp=yes", "--property=PrivateNetwork=yes", "--property=ProtectHome=yes",
		"--property=ProtectSystem=strict", "--property=ReadWritePaths="+filepath.Dir(installHelperPath),
		liveAgentBinary, "updater", "apply-install-helper")
	cmd.WaitDelay = 2 * time.Second
	return cmd.Run()
}

// ApplyStagedInstallHelper is the fixed entry point for the transient writer.
// Its input and destination are compiled paths, with no user-specified inputs.
func ApplyStagedInstallHelper() error {
	if os.Geteuid() != 0 {
		return errors.New("management helper replacement requires root")
	}
	original, err := os.Lstat(installHelperPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !original.Mode().IsRegular() {
		return errors.New("existing management helper is not a regular file")
	}
	staged := filepath.Join(updaterStateDirectory, installHelperCandidateFile)
	if err := requireRootOwnedPathParents(filepath.Dir(updaterStateDirectory)); err != nil {
		return err
	}
	stateDir, err := os.Lstat(updaterStateDirectory)
	if err != nil || !stateDir.IsDir() || stateDir.Mode().Perm() != 0700 || !ownedByRoot(stateDir) {
		return errors.New("unsafe management helper staging directory")
	}
	info, err := os.Lstat(staged)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Sys().(*syscall.Stat_t).Uid != 0 {
		return errors.New("unsafe management helper candidate")
	}
	file, err := os.Open(staged)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (2<<20)+1))
	if err != nil || len(data) > 2<<20 || !bytes.HasPrefix(data, []byte("#!/usr/bin/env bash\n")) {
		return errors.New("invalid management helper candidate")
	}
	return replaceInstallHelper(installHelperPath, data, original)
}

func renderInstallHelper(ctx context.Context, installer []byte) ([]byte, error) {
	run := func(input []byte, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "/bin/bash", append([]string{"--noprofile", "--norc"}, args...)...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
		cmd.Stdin = bytes.NewReader(input)
		return cmd.Output()
	}
	helper, err := run(installer, "-s", "--", "render-local-helper")
	if err != nil || !bytes.HasPrefix(helper, []byte("#!/usr/bin/env bash\n")) || len(helper) > 2<<20 {
		return nil, errors.New("installer did not render a valid management helper")
	}
	if _, err := run(helper, "-n"); err != nil {
		return nil, err
	}
	return helper, nil
}

func replaceInstallHelper(path string, data []byte, original os.FileInfo) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".404-probe-install.*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	mode, uid, gid := os.FileMode(0755), 0, 0
	if stat, ok := original.Sys().(*syscall.Stat_t); ok {
		mode, uid, gid = original.Mode(), int(stat.Uid), int(stat.Gid)
	}
	if err := file.Chown(uid, gid); err != nil {
		return err
	}
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(original, current) {
		return errors.New("management helper changed during update")
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	return syncDirectory(dir)
}

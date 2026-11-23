package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"404-probe/internal/backup"
	"404-probe/internal/storage"
	"404-probe/internal/webdomain"
	"golang.org/x/sys/unix"
)

const restoreRecovery = "/var/lib/404-probe-backup-recovery"

func privateRestoreFile(info os.FileInfo) bool { return info.Mode().Perm()&0077 == 0 }

func restoreWorkDirectory() (string, error) {
	if os.Geteuid() != 0 {
		return "", errors.New("restoring an installed Server requires root")
	}
	if err := os.Mkdir(restoreRecovery, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(restoreRecovery)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || info.Sys().(*syscall.Stat_t).Uid != 0 {
		return "", errors.New("unsafe Server restore recovery directory")
	}
	return os.MkdirTemp(restoreRecovery, ".verify-")
}

type restoreFile struct {
	name, target             string
	mode                     os.FileMode
	uid, gid                 int
	replacement              bool
	existed                  bool
	previousMode             os.FileMode
	previousUID, previousGID int
}

func restoreInstalledBackup(ctx context.Context, dir, init string, version int, input *bufio.Reader, output io.Writer) error {
	if os.Geteuid() != 0 || (init != "systemd" && init != "openrc") {
		return errors.New("installed Server restore requires root and the installer service manager")
	}
	account, err := user.Lookup("404-probe")
	if err != nil {
		return errors.New("the installed 404-probe service user was not found")
	}
	uid, uidErr := strconv.Atoi(account.Uid)
	gid, gidErr := strconv.Atoi(account.Gid)
	if uidErr != nil || gidErr != nil || uid <= 0 || gid <= 0 {
		return errors.New("invalid installed 404-probe service user")
	}
	unit := "/etc/systemd/system/404-probe-server.service"
	envMode, envOwner := os.FileMode(0400), uid
	if init == "openrc" {
		unit, envMode, envOwner = "/etc/init.d/404-probe-server", 0640, 0
	}
	info, err := os.Lstat(unit)
	if err != nil || !info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Uid != 0 {
		return errors.New("an installed 404-probe Server service is required")
	}
	lock, err := lockRestore()
	if err != nil {
		return err
	}
	defer lock.Close()
	if _, err := os.Lstat("/var/lib/404-probe-upgrade/pending"); !errors.Is(err, os.ErrNotExist) {
		return errors.New("resolve the pending Server upgrade before restoring a backup")
	}
	for _, parent := range []string{filepath.Dir(installedDatabase), installedConfig} {
		info, err := os.Lstat(parent)
		if err != nil || !info.IsDir() {
			return errors.New("the installed Server directories must be real directories")
		}
	}
	if err := validateRestoreConfig(dir); err != nil {
		return err
	}
	paths := []restoreFile{
		{name: backup.EnvironmentName, target: filepath.Join(installedConfig, backup.EnvironmentName), mode: envMode, uid: envOwner, gid: gid, replacement: true},
		{name: backup.ControlTokenName, target: filepath.Join(installedConfig, backup.ControlTokenName), mode: 0400, uid: uid, gid: gid, replacement: true},
		{name: backup.PasswordHashName, target: filepath.Join(installedConfig, backup.PasswordHashName), mode: 0400, uid: uid, gid: gid, replacement: true},
		{name: backup.UpdateCheckName, target: filepath.Join(filepath.Dir(installedDatabase), backup.UpdateCheckName), mode: 0600, uid: uid, gid: gid},
		{name: backup.DatabaseName + "-wal", target: installedDatabase + "-wal"},
		{name: backup.DatabaseName + "-shm", target: installedDatabase + "-shm"},
		{name: backup.DatabaseName, target: installedDatabase, mode: 0600, uid: uid, gid: gid, replacement: true},
	}
	if _, err := os.Stat(filepath.Join(dir, backup.UpdateCheckName)); err == nil {
		paths[3].replacement = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := storage.PauseBackupForRestore(ctx, filepath.Join(dir, backup.DatabaseName), version); err != nil {
		return fmt.Errorf("prepare offline recovery database: %w", err)
	}
	if _, err := fmt.Fprintln(output, "The Server will stop. Its current database, SQLite sidecars and configuration will be saved before replacement."); err != nil {
		return err
	}
	for _, entry := range paths {
		if entry.replacement {
			fmt.Fprintln(output, "  Replace", entry.target)
		} else {
			fmt.Fprintln(output, "  Clear if present", entry.target)
		}
	}
	if _, err := fmt.Fprint(output, "Scheduled backups will be paused after restore. Type RESTORE to continue: "); err != nil {
		return err
	}
	answer, err := input.ReadString('\n')
	if (err != nil && !errors.Is(err, io.EOF)) || strings.TrimSpace(answer) != "RESTORE" {
		return errors.New("restore cancelled before stopping the Server")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return applyInstalledBackup(ctx, dir, init, paths, output)
}

func lockRestore() (*os.File, error) {
	const dir = "/run/404-probe-upgrade"
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || info.Sys().(*syscall.Stat_t).Uid != 0 {
		return nil, errors.New("unsafe Server operation lock directory")
	}
	path := filepath.Join(dir, "server.lock")
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Sys().(*syscall.Stat_t).Uid != 0 {
		file.Close()
		return nil, errors.New("unsafe Server operation lock file")
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		return nil, errors.New("another Server operation is already running")
	}
	return file, nil
}

func validateRestoreConfig(dir string) error {
	env, err := readRestoreInput(filepath.Join(dir, backup.EnvironmentName), 4096, true)
	if err != nil {
		return errors.New("backup is missing a valid installed Server environment file")
	}
	origin, ok := strings.CutPrefix(strings.TrimSuffix(string(env), "\n"), "PROBE_404_WEB_PUBLIC_ORIGIN=")
	if !ok || strings.ContainsAny(origin, "\r\n\x00") {
		return errors.New("backup Server environment must contain only the public origin")
	}
	if _, err := webdomain.ParsePublicOrigin(origin, true); err != nil {
		return errors.New("backup Server environment has an invalid public origin")
	}
	if _, err := loadControlTokenHash(filepath.Join(dir, backup.ControlTokenName)); err != nil {
		return errors.New("backup is missing a valid Server control credential")
	}
	if _, err := readWebPasswordFile(filepath.Join(dir, backup.PasswordHashName)); err != nil {
		return errors.New("backup is missing a valid Web password file")
	}
	return nil
}

func applyInstalledBackup(ctx context.Context, dir, init string, paths []restoreFile, output io.Writer) (err error) {
	previous, err := os.MkdirTemp(restoreRecovery, "previous-")
	if err != nil {
		return err
	}
	saved, changed, finished := false, false, false
	wasActive := restoreService(ctx, init, "is-active") == nil
	if err := ensureRestoreStopped(ctx, init); err != nil {
		_ = os.Remove(previous)
		return fmt.Errorf("could not stop the installed Server: %w", err)
	}
	defer func() {
		if finished {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Minute)
		defer cancel()
		if changed {
			if rollbackErr := ensureRestoreStopped(rollbackCtx, init); rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("rollback could not stop the Server; originals are in %s", previous))
				return
			}
			if rollbackErr := rollbackRestore(rollbackCtx, previous, paths); rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("rollback failed; originals are in %s: %w", previous, rollbackErr))
				return
			}
		}
		if wasActive {
			if startErr := startRestoredServer(rollbackCtx, init); startErr != nil {
				err = errors.Join(err, fmt.Errorf("could not restart the original Server: %w", startErr))
			}
		}
		if !saved {
			err = errors.Join(err, os.RemoveAll(previous))
		}
	}()
	if err := captureRestore(ctx, previous, paths); err != nil {
		return fmt.Errorf("could not save the stopped Server files: %w", err)
	}
	saved = true
	fmt.Fprintln(output, "Previous Server files saved in", previous)
	for _, entry := range paths {
		changed = true
		if entry.replacement {
			err = installRestoreFile(ctx, filepath.Join(dir, entry.name), entry.target, entry.mode, entry.uid, entry.gid)
		} else {
			err = removeRestoreFile(entry.target)
		}
		if err != nil {
			return fmt.Errorf("replace %s: %w", entry.target, err)
		}
	}
	if err := startRestoredServer(ctx, init); err != nil {
		return fmt.Errorf("restored Server did not become ready: %w", err)
	}
	finished = true
	_, err = fmt.Fprintln(output, "Restore complete. The Server is running.")
	return err
}

func ensureRestoreStopped(ctx context.Context, init string) error {
	if err := restoreService(ctx, init, "stop"); err != nil {
		return err
	}
	if restoreService(ctx, init, "is-active") == nil {
		return errors.New("Server is still active")
	}
	return ctx.Err()
}

func restoreService(ctx context.Context, init, action string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if init == "openrc" {
		if action == "is-active" {
			action = "status"
		}
		return exec.CommandContext(ctx, "rc-service", "404-probe-server", action).Run()
	}
	return exec.CommandContext(ctx, "systemctl", action, "404-probe-server.service").Run()
}

func startRestoredServer(ctx context.Context, init string) error {
	if err := restoreService(ctx, init, "start"); err != nil {
		return err
	}
	deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	probe := &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	stable := 0
	for deadline.Err() == nil {
		r, _ := http.NewRequestWithContext(deadline, http.MethodPost, "http://127.0.0.1:8080/api/v1/agent/jobs/claim", nil)
		response, err := probe.Do(r)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusUnauthorized && restoreService(deadline, init, "is-active") == nil {
				stable++
				if stable == 5 {
					return nil
				}
			} else {
				stable = 0
			}
		} else {
			stable = 0
		}
		time.Sleep(time.Second)
	}
	return errors.New("Server HTTP readiness check timed out")
}

func captureRestore(ctx context.Context, previous string, paths []restoreFile) error {
	for i := range paths {
		entry := &paths[i]
		info, err := os.Lstat(entry.target)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("unsafe existing Server file %s", entry.target)
		}
		entry.existed = true
		stat := info.Sys().(*syscall.Stat_t)
		entry.previousMode, entry.previousUID, entry.previousGID = info.Mode().Perm(), int(stat.Uid), int(stat.Gid)
		if err := installRestoreFile(ctx, entry.target, filepath.Join(previous, entry.name), 0600, 0, 0); err != nil {
			return err
		}
	}
	return syncRestoreDir(filepath.Dir(previous))
}

func rollbackRestore(ctx context.Context, previous string, paths []restoreFile) (err error) {
	for _, entry := range paths {
		if entry.existed {
			copyErr := installRestoreFile(ctx, filepath.Join(previous, entry.name), entry.target, entry.previousMode, entry.previousUID, entry.previousGID)
			err = errors.Join(err, copyErr)
		} else {
			err = errors.Join(err, removeRestoreFile(entry.target))
		}
	}
	return err
}

func installRestoreFile(ctx context.Context, source, target string, mode os.FileMode, uid, gid int) error {
	fd, err := unix.Open(source, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	in := os.NewFile(uintptr(fd), source)
	defer in.Close()
	info, err := in.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("restore input is not a regular file")
	}
	dir := filepath.Dir(target)
	if err := storage.CheckBackupSpace(dir, info.Size()); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".restore-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := io.Copy(tmp, restoreReader{ctx, in}); err != nil {
		return err
	}
	if err := tmp.Chown(uid, gid); err != nil {
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return err
	}
	return syncRestoreDir(dir)
}

type restoreReader struct {
	ctx context.Context
	r   io.Reader
}

func (r restoreReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func removeRestoreFile(path string) error {
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return syncRestoreDir(filepath.Dir(path))
}

func syncRestoreDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

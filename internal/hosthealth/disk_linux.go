package hosthealth

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"404-probe/internal/protocol"
	"golang.org/x/sys/unix"
)

type probeFile interface {
	WriteAt([]byte, int64) (int, error)
	Sync() error
	ReadBack([]byte) (bool, error)
}

func platformProbe(dir string) (func() probeResult, func() kernelSample) {
	return func() probeResult { return probeDisk(dir) }, func() kernelSample {
		return inspectKernel(dir, "/proc/self/mountinfo", "/sys")
	}
}

func safeProbeFile(st *unix.Stat_t) bool {
	return st.Mode&unix.S_IFMT == unix.S_IFREG && st.Nlink == 1 && st.Uid == uint32(os.Geteuid())
}

func removeProbe(dir string) {
	path := filepath.Join(dir, ProbeFilename)
	var st unix.Stat_t
	if unix.Lstat(path, &st) == nil && safeProbeFile(&st) {
		_ = unix.Unlink(path)
	}
}

func probeDisk(dir string) probeResult {
	path := filepath.Join(dir, ProbeFilename)
	var previous unix.Stat_t
	err := unix.Lstat(path, &previous)
	existing := err == nil && safeProbeFile(&previous)
	if err == nil && !existing {
		if previous.Mode&unix.S_IFMT == unix.S_IFDIR {
			return probeResult{reason: protocol.DiskUnwritable}
		}
		err = unix.Unlink(path)
	}
	if err != nil && !errors.Is(err, unix.ENOENT) {
		return probeFailure(err, protocol.DiskWriteEIO)
	}
	flags := unix.O_WRONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if !existing {
		flags |= unix.O_CREAT | unix.O_EXCL
	}
	fd, err := unix.Open(path, flags, 0600)
	if err != nil {
		return probeFailure(err, protocol.DiskWriteEIO)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return probeFailure(err, protocol.DiskWriteEIO)
	}
	if !safeProbeFile(&st) || existing && (st.Dev != previous.Dev || st.Ino != previous.Ino) {
		return probeResult{reason: protocol.DiskUnwritable}
	}
	if st.Mode&07777 != 0600 {
		if err := f.Chmod(0600); err != nil {
			return probeFailure(err, protocol.DiskWriteEIO)
		}
	}
	if st.Size > probeSize {
		if err := f.Truncate(probeSize); err != nil {
			return probeFailure(err, protocol.DiskWriteEIO)
		}
	}
	payload := make([]byte, probeSize)
	rand.Read(payload)
	return checkFile(directProbeFile{f, st}, payload)
}

func checkFile(file probeFile, payload []byte) probeResult {
	if n, err := file.WriteAt(payload, 0); err != nil || n != len(payload) {
		if err == nil {
			err = io.ErrShortWrite
		}
		return probeFailure(err, protocol.DiskWriteEIO)
	}
	if err := file.Sync(); err != nil {
		return probeFailure(err, protocol.DiskWriteEIO)
	}
	verified, err := file.ReadBack(payload)
	if err != nil {
		return probeFailure(err, protocol.DiskReadEIO)
	}
	return probeResult{verified: &verified}
}

func probeFailure(err error, ioReason protocol.DiskHealthReason) probeResult {
	switch {
	case errors.Is(err, unix.EIO), errors.Is(err, io.ErrShortWrite):
		return probeResult{reason: ioReason}
	case errors.Is(err, unix.ENOSPC), errors.Is(err, unix.EDQUOT):
		return probeResult{reason: protocol.DiskFull}
	default:
		return probeResult{reason: protocol.DiskUnwritable, readOnly: errors.Is(err, unix.EROFS)}
	}
}

type directProbeFile struct {
	*os.File
	identity unix.Stat_t
}

func (f directProbeFile) ReadBack(expected []byte) (bool, error) {
	fd, err := unix.Open(f.Name(), unix.O_RDONLY|unix.O_DIRECT|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if directUnsupported(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		return false, err
	}
	if st.Dev != f.identity.Dev || st.Ino != f.identity.Ino {
		return false, errors.New("probe file was replaced")
	}
	// An anonymous mapping supplies the page alignment required by O_DIRECT.
	buf, err := unix.Mmap(-1, 0, probeSize, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANONYMOUS)
	if err != nil {
		return false, err
	}
	defer unix.Munmap(buf)
	var n int
	for {
		n, err = unix.Pread(fd, buf, 0)
		if err != unix.EINTR {
			break
		}
	}
	if directUnsupported(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if n != len(expected) || !bytes.Equal(buf, expected) {
		return false, unix.EIO
	}
	return true, nil
}

func directUnsupported(err error) bool {
	return errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOSYS)
}

type mount struct {
	device, path, fsType string
	readOnly             bool
}

var mountEscapes = strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)

func parseMounts(r io.Reader, dataDir string) (root, data mount, err error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		left, right, ok := strings.Cut(scanner.Text(), " - ")
		if !ok {
			continue
		}
		fields, super := strings.Fields(left), strings.Fields(right)
		if len(fields) < 6 || len(super) < 3 || !deviceNumber(fields[2]) {
			continue
		}
		path := mountEscapes.Replace(fields[4])
		m := mount{device: fields[2], path: path, fsType: super[0]}
		for option := range strings.SplitSeq(super[2], ",") {
			m.readOnly = m.readOnly || option == "ro"
		}
		if path == "/" {
			root = m
		}
		if len(path) >= len(data.path) && (path == "/" || dataDir == path || strings.HasPrefix(dataDir, path+"/")) {
			data = m
		}
	}
	return root, data, scanner.Err()
}

func deviceNumber(s string) bool {
	major, minor, ok := strings.Cut(s, ":")
	_, majorErr := strconv.ParseUint(major, 10, 32)
	_, minorErr := strconv.ParseUint(minor, 10, 32)
	return ok && majorErr == nil && minorErr == nil
}

func inContainer(root mount) bool {
	if root.fsType == "overlay" || os.Getenv("container") != "" {
		return true
	}
	for _, p := range []string{"/.dockerenv", "/run/.containerenv", "/run/systemd/container"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	cgroups, _ := os.ReadFile("/proc/1/cgroup")
	for _, marker := range []string{"/docker/", "docker-", "kubepods", "libpod-", "/lxc/"} {
		if bytes.Contains(cgroups, []byte(marker)) {
			return true
		}
	}
	return false
}

func inspectKernel(dir, mountinfo, sysfs string) kernelSample {
	f, err := os.Open(mountinfo)
	if err != nil {
		return kernelSample{container: inContainer(mount{})}
	}
	defer f.Close()
	dir, err = filepath.Abs(dir)
	if err != nil {
		return kernelSample{}
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	root, data, err := parseMounts(f, dir)
	if inContainer(root) {
		return kernelSample{container: true}
	}
	if err != nil || root.path == "" || data.path == "" {
		return kernelSample{}
	}
	ro := root.readOnly || data.readOnly
	s := kernelSample{readOnly: &ro, filesystems: []filesystem{readFilesystem(sysfs, "root", root)}}
	if root.device != data.device || root.fsType != data.fsType {
		s.filesystems = append(s.filesystems, readFilesystem(sysfs, "data", data))
	}
	return s
}

func readFilesystem(sysfs, scope string, m mount) filesystem {
	f := filesystem{key: m.device, DiskFilesystem: protocol.DiskFilesystem{Scope: scope}}
	block := filepath.Join(sysfs, "dev/block", m.device)
	dev, err := filepath.EvalSymlinks(block)
	if err != nil {
		return f
	}
	f.key = dev
	if m.fsType == "ext4" {
		base := filepath.Join(sysfs, "fs/ext4", filepath.Base(dev))
		f.Ext4Errors = readCount(filepath.Join(base, "errors_count"), 10, 32)
		if last := readCount(filepath.Join(base, "last_error_time"), 10, 38); last != nil && *last <= 253402300799 {
			f.Ext4LastErrorAt = int64(*last)
		}
	}
	if _, err := os.Stat(filepath.Join(dev, "partition")); err == nil {
		dev = filepath.Dir(dev)
	}
	device := filepath.Join(dev, "device")
	f.SCSIErrors = readCount(filepath.Join(device, "ioerr_cnt"), 0, 63)
	if raw, err := os.ReadFile(filepath.Join(device, "state")); err == nil {
		state := strings.TrimSpace(string(raw))
		if len(state) <= 32 && !strings.ContainsAny(state, "\x00\r\n") {
			f.SCSIState = state
		}
	}
	return f
}

func readCount(path string, base, bits int) *uint64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(data)), base, bits)
	if err != nil {
		return nil
	}
	return &n
}

package systemchecks

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"404-probe/internal/protocol"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

const securityBaselineFile = "security-baseline.json"

var errInventoryLimit = errors.New("root inventory limit exceeded")

type baselineUser struct {
	name, home string
	uid, gid   uint64
}

func collectSecurityBaseline(ctx context.Context, etc, proc string, now time.Time, run commandRunner) protocol.SecurityBaselineReport {
	report := protocol.SecurityBaselineReport{Status: protocol.CheckError, CollectedAt: now.UnixMilli()}
	s := protocol.SecuritySnapshot{
		SSHKeys: []protocol.SecuritySSHKey{}, Ports: []protocol.SecurityPort{}, UID0: []string{},
		SudoMembers: []protocol.SecuritySudoMember{}, SudoersFiles: []protocol.SecuritySudoersFile{},
	}
	var reasons []string
	users, err := boundedRootInventory(ctx, 5*time.Second, func(ctx context.Context) ([]baselineUser, error) {
		return readBaselineUsers(ctx, filepath.Join(etc, "passwd"))
	})
	if err != nil {
		reasons = append(reasons, "passwd:"+inventoryErrorReason(err))
	} else {
		for _, user := range users {
			if user.uid == 0 {
				s.UID0 = append(s.UID0, user.name)
			}
		}
		if len(s.UID0) > 256 {
			reasons = append(reasons, "passwd:too_large")
		}
		slices.Sort(s.UID0)
	}
	steps := []struct {
		name string
		run  func(context.Context, *protocol.SecuritySnapshot) error
	}{
		{"ssh_keys", func(ctx context.Context, s *protocol.SecuritySnapshot) error {
			return collectAuthorizedKeys(ctx, users, run, s)
		}},
		{"ports", func(ctx context.Context, s *protocol.SecuritySnapshot) error {
			return collectBaselinePorts(ctx, proc, s)
		}},
		{"sudo_members", func(ctx context.Context, s *protocol.SecuritySnapshot) error {
			return collectSudoMembers(ctx, filepath.Join(etc, "group"), users, s)
		}},
		{"sudoers_files", func(ctx context.Context, s *protocol.SecuritySnapshot) error { return collectSudoersFiles(ctx, etc, s) }},
	}
	for _, step := range steps {
		part, err := boundedRootInventory(ctx, 5*time.Second, func(ctx context.Context) (protocol.SecuritySnapshot, error) {
			var part protocol.SecuritySnapshot
			err := step.run(ctx, &part)
			return part, err
		})
		if err != nil {
			// Paths and contents from privileged input stay on the device.
			reasons = append(reasons, step.name+":"+inventoryErrorReason(err))
			continue
		}
		s.SSHKeys = append(s.SSHKeys, part.SSHKeys...)
		s.Ports = append(s.Ports, part.Ports...)
		s.SudoMembers = append(s.SudoMembers, part.SudoMembers...)
		s.SudoersFiles = append(s.SudoersFiles, part.SudoersFiles...)
	}
	if len(reasons) != 0 {
		report.Reason = inventoryText(strings.Join(reasons, ","), protocol.MaxInventoryTextBytes)
		return report
	}
	digest, size, err := protocol.InventoryDigest(&s)
	if err != nil || size > protocol.MaxSecuritySnapshotBytes {
		report.Reason = "too_large"
		return report
	}
	report.Status, report.Digest, report.Snapshot = protocol.CheckOK, digest, &s
	return report
}

// A regular file on a slow mount can block inside open or read. Each worker
// owns its result, so the short-lived root command can continue after a timeout.
func boundedRootInventory[T any](ctx context.Context, timeout time.Duration, run func(context.Context) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	type observation struct {
		value T
		err   error
	}
	done := make(chan observation, 1)
	go func() {
		value, err := run(ctx)
		done <- observation{value, err}
	}()
	select {
	case result := <-done:
		if result.err != nil {
			return result.value, result.err
		}
		return result.value, ctx.Err()
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

func inventoryErrorReason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "timeout"
	case errors.Is(err, errInventoryLimit):
		return "too_large"
	case errors.Is(err, os.ErrNotExist):
		return "not_found"
	default:
		return "read_failed"
	}
}

func inventoryText(value string, limit int) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	if len(value) <= limit {
		return value
	}
	for !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}

func openInventoryFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("inventory input is not a regular file")
	}
	return f, nil
}

func readInventoryBytes(ctx context.Context, path string, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := openInventoryFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(data)) > limit {
		return nil, errInventoryLimit
	}
	return data, err
}

func inventoryLines(ctx context.Context, path string, limit int64, each func(string) error) error {
	data, err := readInventoryBytes(ctx, path, limit)
	if err != nil {
		return err
	}
	for line := range bytes.Lines(data) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := each(strings.TrimRight(string(line), "\r\n")); err != nil {
			return err
		}
	}
	return nil
}

func readBaselineUsers(ctx context.Context, path string) ([]baselineUser, error) {
	var users []baselineUser
	err := inventoryLines(ctx, path, 1<<20, func(line string) error {
		if line == "" || strings.HasPrefix(line, "#") {
			return nil
		}
		fields := strings.Split(line, ":")
		if len(fields) != 7 || fields[0] == "" || len(fields[0]) > 128 || len(fields[5]) > 4096 {
			return errors.New("invalid passwd entry")
		}
		uid, uidErr := strconv.ParseUint(fields[2], 10, 32)
		gid, gidErr := strconv.ParseUint(fields[3], 10, 32)
		if uidErr != nil || gidErr != nil {
			return errors.New("invalid passwd UID or GID")
		}
		if len(users) == 4096 {
			return errInventoryLimit
		}
		users = append(users, baselineUser{fields[0], fields[5], uid, gid})
		return nil
	})
	if err != nil || len(users) == 0 {
		if err == nil {
			err = errors.New("empty passwd file")
		}
		return nil, err
	}
	return users, nil
}

func authorizedKeyPaths(ctx context.Context, run commandRunner) []string {
	output, err := run(ctx, "/usr/sbin/sshd", "-T")
	if err == nil {
		for line := range strings.Lines(output) {
			fields := strings.Fields(line)
			if len(fields) > 1 && fields[0] == "authorizedkeysfile" {
				return fields[1:]
			}
		}
	}
	return []string{".ssh/authorized_keys", ".ssh/authorized_keys2"}
}

func expandAuthorizedKeyPath(pattern string, user baselineUser) (string, error) {
	var out strings.Builder
	for i := 0; i < len(pattern); i++ {
		if pattern[i] != '%' {
			out.WriteByte(pattern[i])
			continue
		}
		i++
		if i == len(pattern) {
			return "", errors.New("invalid authorized keys path token")
		}
		switch pattern[i] {
		case '%':
			out.WriteByte('%')
		case 'h':
			out.WriteString(user.home)
		case 'u':
			out.WriteString(user.name)
		case 'U':
			out.WriteString(strconv.FormatUint(user.uid, 10))
		default:
			return "", errors.New("invalid authorized keys path token")
		}
	}
	path := out.String()
	if !filepath.IsAbs(path) {
		path = filepath.Join(user.home, path)
	}
	return path, nil
}

func collectAuthorizedKeys(ctx context.Context, users []baselineUser, run commandRunner, s *protocol.SecuritySnapshot) error {
	patterns := authorizedKeyPaths(ctx, run)
	if len(patterns) > 32 {
		return errInventoryLimit
	}
	for _, user := range users {
		if !filepath.IsAbs(user.home) {
			continue
		}
		keys := make(map[string]protocol.SecuritySSHKey)
		for _, pattern := range patterns {
			if strings.EqualFold(pattern, "none") {
				continue
			}
			path, err := expandAuthorizedKeyPath(pattern, user)
			if err != nil {
				return err
			}
			err = inventoryLines(ctx, path, 512<<10, func(line string) error {
				if len(line) > 8192 || line == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
					return nil
				}
				key, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
				if err != nil {
					return nil // OpenSSH also ignores malformed authorized_keys lines.
				}
				fingerprint := ssh.FingerprintSHA256(key)
				if _, found := keys[fingerprint]; !found {
					if len(keys) == 64 || len(s.SSHKeys)+len(keys) == 256 {
						return errInventoryLimit
					}
					keys[fingerprint] = protocol.SecuritySSHKey{User: user.name, Type: key.Type(), Fingerprint: fingerprint,
						Comment: inventoryText(comment, protocol.MaxInventoryTextBytes)}
				}
				return nil
			})
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		for _, key := range keys {
			s.SSHKeys = append(s.SSHKeys, key)
		}
	}
	sort.Slice(s.SSHKeys, func(i, j int) bool {
		a, b := s.SSHKeys[i], s.SSHKeys[j]
		return a.User < b.User || a.User == b.User && a.Fingerprint < b.Fingerprint
	})
	return nil
}

func collectSudoMembers(ctx context.Context, path string, users []baselineUser, s *protocol.SecuritySnapshot) error {
	seen := make(map[protocol.SecuritySudoMember]bool)
	add := func(group, user string) error {
		if user == "" {
			return nil
		}
		if len(user) > 128 {
			return errInventoryLimit
		}
		member := protocol.SecuritySudoMember{Group: group, User: user}
		if !seen[member] {
			if len(seen) == 512 {
				return errInventoryLimit
			}
			seen[member] = true
			s.SudoMembers = append(s.SudoMembers, member)
		}
		return nil
	}
	err := inventoryLines(ctx, path, 1<<20, func(line string) error {
		name, _, _ := strings.Cut(line, ":")
		if name != "sudo" && name != "wheel" && name != "admin" {
			return nil
		}
		fields := strings.Split(line, ":")
		if len(fields) != 4 {
			return errors.New("invalid sudo group entry")
		}
		gid, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil {
			return err
		}
		for member := range strings.SplitSeq(fields[3], ",") {
			if err := add(name, member); err != nil {
				return err
			}
		}
		for _, user := range users {
			if user.gid == gid {
				if err := add(name, user.name); err != nil {
					return err
				}
			}
		}
		return nil
	})
	sort.Slice(s.SudoMembers, func(i, j int) bool {
		a, b := s.SudoMembers[i], s.SudoMembers[j]
		return a.Group < b.Group || a.Group == b.Group && a.User < b.User
	})
	return err
}

func collectSudoersFiles(ctx context.Context, etc string, s *protocol.SecuritySnapshot) error {
	add := func(name string) error {
		data, err := readInventoryBytes(ctx, filepath.Join(etc, name), 256<<10)
		if err == nil {
			s.SudoersFiles = append(s.SudoersFiles, protocol.SecuritySudoersFile{Path: "/etc/" + name, SHA256: protocol.SHA256Hex(data)})
		}
		return err
	}
	if err := add("sudoers"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir, err := os.Open(filepath.Join(etc, "sudoers.d"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(512)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(entries) == 512 {
		return errInventoryLimit
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if err := add("sudoers.d/" + entry.Name()); err != nil {
			return err
		}
	}
	sort.Slice(s.SudoersFiles, func(i, j int) bool { return s.SudoersFiles[i].Path < s.SudoersFiles[j].Path })
	return nil
}

type baselinePortKey struct {
	protocol string
	port     uint16
}

func collectBaselinePorts(ctx context.Context, proc string, s *protocol.SecuritySnapshot) error {
	rangeData, err := readInventoryBytes(ctx, filepath.Join(proc, "sys/net/ipv4/ip_local_port_range"), 1024)
	if err != nil {
		return err
	}
	var low, high uint16
	if _, err := fmt.Sscan(string(rangeData), &low, &high); err != nil || low == 0 || low > high {
		return errors.New("invalid UDP ephemeral port range")
	}
	ports := make(map[baselinePortKey]map[string]bool)
	inodes := make(map[string]baselinePortKey)
	for _, table := range []string{"tcp", "tcp6", "udp", "udp6"} {
		proto := strings.TrimSuffix(table, "6")
		err := inventoryLines(ctx, filepath.Join(proc, "net", table), 4<<20, func(line string) error {
			fields := strings.Fields(line)
			if len(fields) == 0 || fields[0] == "sl" {
				return nil
			}
			if len(fields) < 10 {
				return errors.New("invalid proc socket row")
			}
			if proto == "tcp" && fields[3] != "0A" {
				return nil
			}
			ipHex, portHex, ok := strings.Cut(fields[1], ":")
			port, parseErr := strconv.ParseUint(portHex, 16, 16)
			ip, ipErr := procSocketIP(ipHex)
			if !ok || parseErr != nil || ipErr != nil {
				return errors.New("invalid proc socket address")
			}
			if port == 0 || ip.Unmap().IsLoopback() || proto == "udp" && port >= uint64(low) {
				return nil
			}
			key := baselinePortKey{proto, uint16(port)}
			if ports[key] == nil {
				if len(ports) == 256 {
					return errInventoryLimit
				}
				ports[key] = make(map[string]bool)
			}
			if fields[9] != "0" {
				if len(inodes) == 8192 {
					return errInventoryLimit
				}
				inodes[fields[9]] = key
			}
			return nil
		})
		if err != nil && !(strings.HasSuffix(table, "6") && errors.Is(err, os.ErrNotExist)) {
			return err
		}
	}
	if len(inodes) > 0 {
		if err := collectPortProcesses(ctx, proc, inodes, ports); err != nil {
			return err
		}
	}
	for key, names := range ports {
		processes := make([]string, 0, len(names))
		for name := range names {
			processes = append(processes, name)
		}
		slices.Sort(processes)
		s.Ports = append(s.Ports, protocol.SecurityPort{Protocol: key.protocol, Port: key.port, Processes: processes})
	}
	sort.Slice(s.Ports, func(i, j int) bool {
		a, b := s.Ports[i], s.Ports[j]
		return a.Protocol < b.Protocol || a.Protocol == b.Protocol && a.Port < b.Port
	})
	return nil
}

func procSocketIP(value string) (netip.Addr, error) {
	if len(value) != 8 && len(value) != 32 {
		return netip.Addr{}, errors.New("invalid proc IP length")
	}
	var decoded [16]byte
	for i := 0; i < len(value); i += 8 {
		word, err := strconv.ParseUint(value[i:i+8], 16, 32)
		if err != nil {
			return netip.Addr{}, err
		}
		binary.NativeEndian.PutUint32(decoded[i/2:], uint32(word))
	}
	if len(value) == 8 {
		return netip.AddrFrom4([4]byte(decoded[:4])), nil
	}
	return netip.AddrFrom16(decoded), nil
}

func collectPortProcesses(ctx context.Context, proc string, inodes map[string]baselinePortKey, ports map[baselinePortKey]map[string]bool) error {
	budget := 131072
	return walkInventoryDirectory(ctx, proc, 65536, func(pid string) error {
		if _, err := strconv.ParseUint(pid, 10, 32); err != nil {
			return nil
		}
		base := filepath.Join(proc, pid)
		var comm []byte
		err := walkInventoryDirectory(ctx, filepath.Join(base, "fd"), 65536, func(fd string) error {
			budget--
			if budget < 0 {
				return errInventoryLimit
			}
			target, err := os.Readlink(filepath.Join(base, "fd", fd))
			if err != nil || !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") {
				return nil
			}
			key, found := inodes[target[8:len(target)-1]]
			if !found {
				return nil
			}
			if comm == nil {
				comm, _ = readInventoryBytes(ctx, filepath.Join(base, "comm"), 1024)
			}
			name := inventoryText(strings.TrimSuffix(string(comm), "\n"), protocol.MaxInventoryTextBytes)
			if name != "" {
				ports[key][name] = true
				if len(ports[key]) > 32 {
					return errInventoryLimit
				}
			}
			return nil
		})
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
			return nil // Processes can exit or restrict fd lookups; the listening port remains known.
		}
		return err
	})
}

func walkInventoryDirectory(ctx context.Context, path string, limit int, each func(string) error) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	for {
		entries, err := dir.Readdirnames(128)
		for _, name := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			limit--
			if limit < 0 {
				return errInventoryLimit
			}
			if err := each(name); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

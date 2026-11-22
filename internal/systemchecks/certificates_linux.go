package systemchecks

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"404-probe/internal/protocol"
	"golang.org/x/sys/unix"
)

const (
	maxCertificateFiles      = 1024
	maxScanEntries           = 16000
	maxCertificateBytes      = 256 << 10
	maxTotalCertificateBytes = 32 << 20
	maxScanDepth             = 5
)

func under(path, root string) bool {
	return path == root || strings.HasPrefix(path, strings.TrimSuffix(root, "/")+"/")
}

func excludedCertPath(path string) bool {
	for _, root := range []string{"/etc/ssl/certs", "/etc/ca-certificates", "/etc/pki/ca-trust", "/proc", "/sys", "/dev"} {
		if under(path, root) {
			return true
		}
	}
	return false
}

// O_NOFOLLOW and O_NONBLOCK keep races with a replaced path from following a
// final-component symlink or blocking on a FIFO. Verify the opened inode too.
func openRegular(path string, maxSize int64) (*os.File, int64, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, 0, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxSize {
		f.Close()
		if err == nil {
			err = errors.New("not a bounded regular file")
		}
		return nil, 0, err
	}
	return f, info.Size(), nil
}

type certificateWalker struct {
	ctx            context.Context
	result         protocol.CertificateScan
	roots          []string
	denied         map[string]bool
	fingerprints   map[string]bool
	stopped        bool
	entries, files int
	fileBytes      int64
}

func scanCertificates(ctx context.Context, roots []string) protocol.CertificateScan {
	return scanCertificateSubtrees(ctx, roots, roots)
}

func scanCertificateSubtrees(ctx context.Context, roots, subtrees []string) protocol.CertificateScan {
	w := certificateWalker{ctx: ctx, denied: map[string]bool{}, fingerprints: map[string]bool{}}
	for _, root := range roots {
		if excludedCertPath(root) {
			continue
		}
		resolved, err := filepath.EvalSymlinks(root)
		if errors.Is(err, os.ErrPermission) {
			w.noAccess(root)
		}
		if err == nil && !excludedCertPath(resolved) {
			w.roots = append(w.roots, resolved)
		}
	}
	for _, target := range subtrees {
		depth := maxScanDepth + 1
		for _, root := range roots {
			if under(target, root) {
				relative := strings.TrimPrefix(strings.TrimPrefix(target, root), "/")
				if relative == "" {
					depth = 0
				} else {
					depth = min(depth, strings.Count(relative, "/")+1)
				}
			}
		}
		if depth <= maxScanDepth {
			resolved, err := filepath.EvalSymlinks(target)
			if errors.Is(err, os.ErrPermission) {
				w.noAccess(target)
			}
			if err == nil {
				w.walk(resolved, depth)
			}
		}
	}
	sort.Slice(w.result.Certificates, func(i, j int) bool { return w.result.Certificates[i].ExpiresAt < w.result.Certificates[j].ExpiresAt })
	sort.Strings(w.result.Unreadable)
	return w.result
}

func (w *certificateWalker) noAccess(dir string) {
	if w.denied[dir] {
		return
	}
	w.denied[dir] = true
	w.result.UnreadableCount++
	if len(w.result.Unreadable) < protocol.MaxUnreadableDirectories && len(dir) <= 512 {
		w.result.Unreadable = append(w.result.Unreadable, dir)
	}
}

func (w *certificateWalker) walk(dir string, depth int) {
	if excludedCertPath(dir) || depth > maxScanDepth || w.stopped {
		return
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrPermission) {
		w.noAccess(dir)
	}
	if err != nil {
		return
	}
	f := os.NewFile(uintptr(fd), dir)
	defer f.Close()
	for {
		entries, err := f.ReadDir(128)
		for _, entry := range entries {
			if w.ctx.Err() != nil || w.entries >= maxScanEntries || w.files >= maxCertificateFiles || w.fileBytes >= maxTotalCertificateBytes {
				w.result.Limited = true
				w.stopped = true
				return
			}
			w.entries++
			path := filepath.Join(dir, entry.Name())
			if entry.IsDir() {
				w.walk(path, depth+1)
				if w.stopped {
					return
				}
				continue
			}
			if len(path) > 512 || excludedCertPath(path) {
				continue
			}
			if entry.Type() != 0 && entry.Type() != os.ModeSymlink {
				continue
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".pem", ".crt", ".cer":
			default:
				continue
			}
			name := strings.ToLower(entry.Name())
			if strings.Contains(name, "privkey") || strings.HasSuffix(name, "key.pem") {
				continue
			}
			w.read(path, dir, entry.Type() == os.ModeSymlink)
		}
		if err != nil {
			return
		}
	}
}

func (w *certificateWalker) read(path, parent string, symlink bool) {
	resolved := path
	if symlink {
		var err error
		resolved, err = filepath.EvalSymlinks(path)
		if errors.Is(err, os.ErrPermission) {
			w.noAccess(parent)
		}
		if err != nil || excludedCertPath(resolved) {
			return
		}
		allowed := false
		for _, root := range w.roots {
			allowed = allowed || under(resolved, root)
		}
		// Debian's Let's Encrypt live links may resolve outside the denied subtree.
		if under(path, "/etc/letsencrypt/live") && under(resolved, "/etc/letsencrypt/archive") {
			allowed = true
		}
		if !allowed {
			return
		}
	}
	w.files++
	f, size, err := openRegular(resolved, maxCertificateBytes)
	if errors.Is(err, os.ErrPermission) {
		w.noAccess(parent)
	}
	if err != nil {
		return
	}
	defer f.Close()
	w.fileBytes += size
	readCertificates(io.LimitReader(f, maxCertificateBytes), func(cert *x509.Certificate) {
		if cert.IsCA || len(cert.DNSNames) == 0 {
			return
		}
		hash := fmt.Sprintf("%x", sha256.Sum256(cert.Raw))
		if w.fingerprints[hash] {
			return
		}
		w.fingerprints[hash] = true
		domains := make([]string, 0, len(cert.DNSNames))
		length := 0
		for _, name := range cert.DNSNames {
			if name == "" || len(name) > 253 || strings.ContainsAny(name, "\r\n\x00") {
				continue
			}
			length += len(name) + 2
			if length > 510 {
				break
			}
			domains = append(domains, name)
		}
		if len(domains) == 0 {
			return
		}
		entry := protocol.Certificate{Domains: strings.Join(domains, ", "), ExpiresAt: cert.NotAfter.UnixMilli(), Path: path, Fingerprint: hash}
		if entry.ExpiresAt <= 0 {
			return
		}
		if len(w.result.Certificates) < protocol.MaxCertificates {
			w.result.Certificates = append(w.result.Certificates, entry)
			return
		}
		// Keep the certificates that expire first when the display limit is reached.
		w.result.Limited = true
		latest := 0
		for i := range w.result.Certificates {
			if w.result.Certificates[i].ExpiresAt > w.result.Certificates[latest].ExpiresAt {
				latest = i
			}
		}
		if entry.ExpiresAt < w.result.Certificates[latest].ExpiresAt {
			w.result.Certificates[latest] = entry
		}
	})
}

func readCertificates(r io.Reader, emit func(*x509.Certificate)) {
	reader := bufio.NewReader(r)
	prefix, _ := reader.Peek(1)
	if len(prefix) == 1 && prefix[0] == 0x30 {
		if data, err := io.ReadAll(reader); err == nil {
			if cert, err := x509.ParseCertificate(data); err == nil {
				emit(cert)
			}
		}
		return
	}
	lines := bufio.NewScanner(reader)
	lines.Buffer(make([]byte, 4096), maxCertificateBytes+1)
	var block bytes.Buffer
	inCertificate := false
	for lines.Scan() {
		line := bytes.TrimSpace(lines.Bytes())
		if bytes.HasPrefix(line, []byte("-----BEGIN ")) {
			block.Reset()
			inCertificate = bytes.Equal(line, []byte("-----BEGIN CERTIFICATE-----"))
		}
		if !inCertificate {
			continue
		}
		block.Write(line)
		block.WriteByte('\n')
		if bytes.Equal(line, []byte("-----END CERTIFICATE-----")) {
			inCertificate = false
			encoded, _ := pem.Decode(block.Bytes())
			if encoded != nil {
				if cert, err := x509.ParseCertificate(encoded.Bytes); err == nil {
					emit(cert)
				}
			}
		}
	}
}

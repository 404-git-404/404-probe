package systemchecks

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"404-probe/internal/protocol"
)

const rootRequestFile = "root-collector-request.json"
const rootResultFile = "checks.json"
const maxRootExport = 256 << 10

type rootRequest struct {
	Roots       []string `json:"roots"`
	Directories []string `json:"directories"`
}

type rootResult struct {
	At         int64                    `json:"collected_at"`
	RequestKey string                   `json:"request_key"`
	Scan       protocol.CertificateScan `json:"scan"`
	Packages   protocol.PackageCheck    `json:"packages"`
	Fail2ban   protocol.Fail2banCheck   `json:"fail2ban"`
}

type cappedOutput struct{ bytes.Buffer }

func (b *cappedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, errors.New("system check command output exceeds limit")
	}
	return b.Buffer.Write(p)
}

type commandRunner func(context.Context, string, ...string) (string, error)

func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"LC_ALL=C", "PATH=/usr/sbin:/usr/bin:/sbin:/bin", "DEBIAN_FRONTEND=noninteractive"}
	result := &cappedOutput{}
	cmd.Stdout = result
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	return strings.TrimSpace(result.String()), err
}

func runShort(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return runCommand(ctx, name, args...)
}

func debianMajor() int {
	raw, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return 0
	}
	var id, version string
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, _ := strings.Cut(line, "=")
		switch key {
		case "ID":
			id = strings.Trim(value, `"'`)
		case "VERSION_ID":
			version = strings.Trim(value, `"'`)
		}
	}
	if id != "debian" {
		return 0
	}
	major, _, _ := strings.Cut(version, ".")
	n, _ := strconv.Atoi(major)
	return n
}

func (c *Collector) collect(ctx context.Context, settings protocol.SystemCheckSettings, stateDir, rootDir string) protocol.SystemChecks {
	major := debianMajor()
	result := protocol.SystemChecks{Supported: major > 0, DebianMajor: major}
	if !result.Supported {
		result.RootStatus = protocol.CheckUnsupported
		result.Fail2ban.Status = protocol.CheckUnsupported
		return result
	}
	now := time.Now()
	key := settingsKey(settings.CertificateDirectories)
	if c.scanKey != key || now.After(c.scanNext) {
		scanCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		c.scan = scanCertificates(scanCtx, append(protocol.DefaultCertificateDirectories(), settings.CertificateDirectories...))
		cancel()
		c.scanKey, c.scanNext = key, time.Now().Add(6*time.Hour)
	}
	result.CertificateScan = c.scan
	request := rootRequest{Roots: settings.CertificateDirectories, Directories: c.scan.Unreadable}
	requestErr := writeRequest(filepath.Join(stateDir, rootRequestFile), request)
	result.RootStatus = protocol.CheckDisabled
	result.Packages.Status = protocol.CheckDisabled
	result.Fail2ban.Status = protocol.CheckDisabled
	if _, err := os.Stat(rootDir); err == nil {
		if requestErr != nil {
			result.RootStatus = protocol.CheckError
		}
		var root rootResult
		err := readJSON(filepath.Join(rootDir, rootResultFile), maxRootExport, &root)
		switch {
		case err != nil || now.UnixMilli()-root.At > (15*time.Minute).Milliseconds():
			result.RootStatus = protocol.CheckError
			result.Packages.Status = protocol.CheckError
			result.Fail2ban.Status = protocol.CheckError
		case (protocol.SystemChecks{CertificateScan: root.Scan, Packages: root.Packages, Fail2ban: root.Fail2ban}).Validate() != nil:
			result.RootStatus = protocol.CheckError
			result.Packages.Status = protocol.CheckError
			result.Fail2ban.Status = protocol.CheckError
		default:
			result.RootCollectedAt = root.At
			result.Packages, result.Fail2ban = root.Packages, root.Fail2ban
			if result.RootStatus != protocol.CheckError {
				result.RootStatus = protocol.CheckOK
			}
			if root.RequestKey == settingsKey(request) {
				result.CertificateScan = mergeCertificates(c.scan, root.Scan)
			}
		}
	}
	_, systemdErr := os.Stat("/run/systemd/system")
	result.ServiceStatus, result.Services, result.FailedUnits, result.ServiceRestarts = c.restarts.collect(ctx, now, settings.WatchedServices, systemdErr == nil, runShort)
	result.NTPStatus, result.NTPSynchronized = collectNTP(ctx)
	result.RebootStatus, result.RebootRequired = rebootRequired()
	if result.RebootRequired {
		result.RebootPackages, result.RebootPackagesTruncated = readRebootPackages("/run/reboot-required.pkgs", "/var/run/reboot-required.pkgs")
	}
	if value, _ := runShort(ctx, "/usr/bin/systemd-detect-virt"); value != "" && len(value) <= 80 {
		result.Virtualization = value
	}
	return result
}

func mergeCertificates(agent, root protocol.CertificateScan) protocol.CertificateScan {
	out := root
	out.Certificates = append([]protocol.Certificate(nil), agent.Certificates...)
	seen := make(map[string]bool, len(out.Certificates))
	for _, cert := range out.Certificates {
		seen[cert.Fingerprint] = true
	}
	for _, cert := range root.Certificates {
		if !seen[cert.Fingerprint] {
			out.Certificates = append(out.Certificates, cert)
			seen[cert.Fingerprint] = true
		}
	}
	sort.SliceStable(out.Certificates, func(i, j int) bool { return out.Certificates[i].ExpiresAt < out.Certificates[j].ExpiresAt })
	if len(out.Certificates) > protocol.MaxCertificates {
		out.Certificates = out.Certificates[:protocol.MaxCertificates]
		out.Limited = true
	}
	out.UnreadableCount += agent.UnreadableCount - len(agent.Unreadable)
	out.Limited = out.Limited || agent.Limited
	return out
}

func collectNTP(ctx context.Context) (string, bool) {
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return protocol.CheckUnsupported, false
	}
	value, err := runShort(ctx, "/usr/bin/timedatectl", "show", "--property=NTPSynchronized", "--value")
	if err != nil || (value != "yes" && value != "no") {
		return protocol.CheckError, false
	}
	return protocol.CheckOK, value == "yes"
}

func rebootRequired() (string, bool) {
	for _, path := range []string{"/run/reboot-required", "/var/run/reboot-required"} {
		_, err := os.Stat(path)
		if err == nil {
			return protocol.CheckOK, true
		}
		if !errors.Is(err, os.ErrNotExist) {
			return protocol.CheckError, false
		}
	}
	return protocol.CheckOK, false
}

const (
	rebootPackageFileLimit = 16 << 10
	rebootPackageLineLimit = 256
)

func readRebootPackages(paths ...string) ([]string, bool) {
	for _, path := range paths {
		f, size, err := openRegular(path, math.MaxInt64)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, false
		}
		defer f.Close()
		limited := &io.LimitedReader{R: f, N: rebootPackageFileLimit}
		reader := bufio.NewReaderSize(limited, rebootPackageLineLimit)
		var names []string
		seen := make(map[string]bool, protocol.MaxRebootPackages)
		fileTruncated, discard := size > rebootPackageFileLimit, false
		for {
			fragment, err := reader.ReadSlice('\n')
			if err != nil && err != bufio.ErrBufferFull && err != io.EOF {
				return nil, false
			}
			// A partial final name at the file limit is not a complete package.
			if err == io.EOF && limited.N == 0 && fileTruncated {
				return names, true
			}
			if !discard {
				name := strings.TrimSpace(string(fragment))
				name = strings.ToValidUTF8(name[:min(len(name), protocol.MaxRebootPackageBytes)], "")
				if name != "" && !seen[name] {
					if len(names) == protocol.MaxRebootPackages {
						return names, true
					}
					seen[name] = true
					names = append(names, name)
				}
			}
			discard = err == bufio.ErrBufferFull
			if err == io.EOF {
				return names, fileTruncated
			}
		}
	}
	return nil, false
}

func readJSON(path string, limit int64, value any) error {
	f, _, err := openRegular(path, limit)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return errors.New("collector data exceeds size limit")
	}
	return json.Unmarshal(data, value)
}

func writeRequest(path string, request rootRequest) error {
	var previous rootRequest
	if readJSON(path, 32<<10, &previous) == nil && settingsKey(previous) == settingsKey(request) {
		return nil
	}
	return writeJSON(path, request, 0600)
}

func writeJSON(path string, value any, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".system-checks-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := json.NewEncoder(f).Encode(value); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

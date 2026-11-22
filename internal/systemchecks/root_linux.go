package systemchecks

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"404-probe/internal/protocol"
)

func RunRoot(ctx context.Context, local bool) error {
	if os.Geteuid() != 0 || debianMajor() == 0 {
		return errors.New("root collection requires root on Debian")
	}
	state, root := DefaultAgentState, DefaultRootState
	if local {
		state, root = LocalAgentState, LocalRootState
	}
	now := time.Now()
	var request rootRequest
	if err := readJSON(filepath.Join(state, rootRequestFile), 32<<10, &request); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(request.Directories) > protocol.MaxUnreadableDirectories {
		return errors.New("too many supplemental certificate directories")
	}
	for _, dir := range request.Directories {
		if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || len(dir) > 512 {
			return errors.New("invalid supplemental certificate directory")
		}
	}
	path := filepath.Join(root, rootResultFile)
	settings := protocol.SystemCheckSettings{CertificateDirectories: request.Roots}
	if err := settings.Normalize(); err != nil {
		return err
	}
	requestKey := settingsKey(request)
	result := rootResult{At: now.UnixMilli(), RequestKey: requestKey}
	var cache struct {
		Key  string
		At   int64
		Scan protocol.CertificateScan
	}
	cachePath := filepath.Join(root, "certificate-cache.json")
	_ = readJSON(cachePath, maxRootExport, &cache)
	if cache.Key != requestKey || now.UnixMilli()-cache.At >= (6*time.Hour).Milliseconds() {
		scanCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		cache.Scan = scanCertificateSubtrees(scanCtx, append(protocol.DefaultCertificateDirectories(), settings.CertificateDirectories...), request.Directories)
		cancel()
		cache.Key, cache.At = requestKey, now.UnixMilli()
		if err := writeJSON(cachePath, cache, 0600); err != nil {
			return err
		}
	}
	result.Scan = cache.Scan
	fail2banCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	result.Fail2ban = collectFail2ban(fail2banCtx, runShort)
	cancel()
	result.Packages = collectPackages(ctx, root, now)
	return writeJSON(path, result, 0640)
}

func collectFail2ban(ctx context.Context, run commandRunner) protocol.Fail2banCheck {
	const binary = "/usr/bin/fail2ban-client"
	return fail2banStatus(ctx, run, binary, func() bool {
		_, err := os.Stat(binary)
		return !errors.Is(err, os.ErrNotExist)
	})
}

func fail2banStatus(ctx context.Context, run commandRunner, binary string, installed func() bool) protocol.Fail2banCheck {
	result := protocol.Fail2banCheck{Status: protocol.CheckError}
	if !installed() {
		result.Status = protocol.CheckNotInstalled
		return result
	}
	value, err := run(ctx, binary, "status")
	if err != nil {
		// A missing socket during service shutdown is an ordinary stopped state.
		state, _ := run(ctx, "/usr/bin/systemctl", "is-active", "fail2ban.service")
		if state == "inactive" || state == "failed" || state == "unknown" {
			result.Status = protocol.CheckNotRunning
		}
		return result
	}
	reported, err := strconv.Atoi(colonValue(value, "Number of jail"))
	if err != nil || reported < 0 {
		return result
	}
	result.JailCount = reported
	names := strings.Split(colonValue(value, "Jail list"), ",")
	total := 0
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if len(result.Jails) >= protocol.MaxFail2banJails {
			break
		}
		if len(name) > 120 || strings.HasPrefix(name, "-") || strings.ContainsAny(name, "\r\n\x00") {
			return result
		}
		value, err := run(ctx, binary, "status", name)
		if err != nil {
			return result
		}
		count, err := strconv.Atoi(colonValue(value, "Currently banned"))
		if err != nil || count < 0 {
			return result
		}
		jail := protocol.Fail2banJail{Name: name, Total: count}
		for _, field := range strings.Fields(colonValue(value, "Banned IP list")) {
			if len(jail.IPs) >= protocol.MaxFail2banIPsPerJail || len(jail.IPs) >= count || total >= protocol.MaxFail2banIPs {
				break
			}
			ip, err := netip.ParseAddr(field)
			if err == nil && ip.Zone() == "" {
				jail.IPs = append(jail.IPs, ip.String())
				total++
			}
		}
		result.Jails = append(result.Jails, jail)
	}
	if len(result.Jails) != min(reported, protocol.MaxFail2banJails) {
		result.Jails = nil
		return result
	}
	result.Status = protocol.CheckOK
	return result
}

func colonValue(output, key string) string {
	for _, line := range strings.Split(output, "\n") {
		label, value, ok := strings.Cut(line, ":")
		if ok && strings.HasSuffix(strings.TrimSpace(label), key) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func collectPackages(ctx context.Context, root string, now time.Time) protocol.PackageCheck {
	if _, err := os.Stat("/usr/bin/apt"); errors.Is(err, os.ErrNotExist) {
		return protocol.PackageCheck{Status: protocol.CheckUnsupported}
	}
	stamp := filepath.Join(root, "apt-refresh.json")
	var previous int64
	_ = readJSON(stamp, 1024, &previous)
	if now.UnixMilli()-previous >= (24 * time.Hour).Milliseconds() {
		// Record the attempt first so a failing mirror is only retried daily.
		if err := writeJSON(stamp, now.UnixMilli(), 0600); err == nil {
			updateCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
			_, _ = runCommand(updateCtx, "/usr/bin/apt-get", "-o", "Acquire::Retries=0", "-o", "Acquire::http::Timeout=12", "-o", "Acquire::https::Timeout=12", "update")
			cancel()
		}
	}
	output, err := runShort(ctx, "/usr/bin/apt", "list", "--upgradable")
	if err != nil {
		return protocol.PackageCheck{Status: protocol.CheckError}
	}
	result := protocol.PackageCheck{Status: protocol.CheckOK}
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "[upgradable from:") {
			continue
		}
		_, suites, ok := strings.Cut(line, "/")
		if !ok {
			continue
		}
		fields := strings.Fields(suites)
		if len(fields) == 0 {
			continue
		}
		result.Upgradable++
		for _, suite := range strings.Split(fields[0], ",") {
			if strings.HasSuffix(suite, "-security") {
				result.Security++
				break
			}
		}
	}
	return result
}

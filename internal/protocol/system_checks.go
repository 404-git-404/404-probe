package protocol

import (
	"errors"
	"net/netip"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

const (
	MaxCertificateDirectories = 16
	MaxWatchedServices        = 32
	MaxCertificates           = 64
	MaxUnreadableDirectories  = 32
	MaxFailedUnits            = 64
	MaxServiceRestarts        = 64
	MaxFail2banJails          = 64
	MaxFail2banIPsPerJail     = 200
	MaxFail2banIPs            = 2000
)

const (
	CheckOK           = "ok"
	CheckUnsupported  = "unsupported"
	CheckError        = "error"
	CheckDisabled     = "disabled"
	CheckNotInstalled = "not_installed"
	CheckNotRunning   = "not_running"
)

type SystemCheckSettings struct {
	CertificateDirectories []string `json:"certificate_directories"`
	WatchedServices        []string `json:"watched_services"`
}

func DefaultCertificateDirectories() []string { return []string{"/etc", "/usr/local/etc"} }

var serviceName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.:@-]{0,119}$`)

func (s *SystemCheckSettings) Normalize() error {
	if len(s.CertificateDirectories) > MaxCertificateDirectories || len(s.WatchedServices) > MaxWatchedServices {
		return errors.New("too many certificate directories or watched services")
	}
	dirs := make([]string, 0, len(s.CertificateDirectories))
	for _, dir := range s.CertificateDirectories {
		dir = strings.TrimSpace(dir)
		if !strings.HasPrefix(dir, "/") || len(dir) > 384 || strings.IndexFunc(dir, unicode.IsControl) >= 0 {
			return errors.New("certificate directory must be an absolute Linux path")
		}
		dir = path.Clean(dir)
		if !slices.Contains(DefaultCertificateDirectories(), dir) && !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	services := make([]string, 0, len(s.WatchedServices))
	for _, service := range s.WatchedServices {
		service = strings.TrimSpace(service)
		if !serviceName.MatchString(service) {
			return errors.New("invalid systemd service name")
		}
		service = strings.TrimSuffix(service, ".service")
		if !slices.Contains(services, service) {
			services = append(services, service)
		}
	}
	s.CertificateDirectories, s.WatchedServices = dirs, services
	return nil
}

type Certificate struct {
	Domains     string `json:"domains"`
	ExpiresAt   int64  `json:"expires_at"`
	Path        string `json:"path"`
	Fingerprint string `json:"fingerprint"`
}

type CertificateScan struct {
	Certificates    []Certificate `json:"certificates,omitempty"`
	Unreadable      []string      `json:"unreadable,omitempty"`
	UnreadableCount int           `json:"unreadable_count,omitempty"`
	Limited         bool          `json:"limited,omitempty"`
}

type WatchedService struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	SubState string `json:"sub_state,omitempty"`
}

type ServiceRestart struct {
	Unit  string `json:"unit"`
	Count uint64 `json:"count"`
}

type Fail2banJail struct {
	Name  string   `json:"name"`
	Total int      `json:"total"`
	IPs   []string `json:"ips,omitempty"`
}

type Fail2banCheck struct {
	Status    string         `json:"status"`
	Jails     []Fail2banJail `json:"jails,omitempty"`
	JailCount int            `json:"jail_count,omitempty"`
}

type PackageCheck struct {
	Status     string `json:"status"`
	Upgradable int    `json:"upgradable"`
	Security   int    `json:"security"`
}

type SystemChecks struct {
	Supported bool `json:"supported"`
	CertificateScan
	ServiceStatus   string           `json:"service_status,omitempty"`
	Services        []WatchedService `json:"services,omitempty"`
	FailedUnits     []string         `json:"failed_units,omitempty"`
	ServiceRestarts []ServiceRestart `json:"service_restarts,omitempty"`
	NTPStatus       string           `json:"ntp_status,omitempty"`
	NTPSynchronized bool             `json:"ntp_synchronized"`
	ClockMeasured   bool             `json:"clock_measured,omitempty"`
	TimeOffsetMS    int64            `json:"time_offset_ms"`
	RebootStatus    string           `json:"reboot_status,omitempty"`
	RebootRequired  bool             `json:"reboot_required"`
	Virtualization  string           `json:"virtualization,omitempty"`
	DebianMajor     int              `json:"debian_major,omitempty"`
	RootStatus      string           `json:"root_status,omitempty"`
	RootCollectedAt int64            `json:"root_collected_at,omitempty"`
	Packages        PackageCheck     `json:"packages"`
	Fail2ban        Fail2banCheck    `json:"fail2ban"`
	LocalDNS        *LocalDNSCheck   `json:"local_dns,omitempty"`

	RebootPackages          []string `json:"reboot_packages,omitempty"`
	RebootPackagesTruncated bool     `json:"reboot_packages_truncated,omitempty"`
}

const MaxRebootPackages = 20
const MaxRebootPackageBytes = 128

func (s SystemChecks) Validate() error {
	if s.LocalDNS != nil {
		if err := s.LocalDNS.validate(); err != nil {
			return err
		}
	}
	if len(s.Certificates) > MaxCertificates || len(s.Unreadable) > MaxUnreadableDirectories ||
		s.UnreadableCount < len(s.Unreadable) || len(s.Services) > MaxWatchedServices ||
		len(s.FailedUnits) > MaxFailedUnits || len(s.ServiceRestarts) > MaxServiceRestarts || len(s.Virtualization) > 80 ||
		len(s.RebootPackages) > MaxRebootPackages ||
		len(s.Fail2ban.Jails) > MaxFail2banJails || s.Fail2ban.JailCount < len(s.Fail2ban.Jails) ||
		s.Packages.Security < 0 || s.Packages.Upgradable < s.Packages.Security {
		return errors.New("invalid system check limits")
	}
	for _, name := range s.RebootPackages {
		if name == "" || len(name) > MaxRebootPackageBytes {
			return errors.New("invalid reboot package observation")
		}
	}
	for _, cert := range s.Certificates {
		if len(cert.Fingerprint) != 64 || len(cert.Domains) > 510 || len(cert.Path) > 512 || cert.ExpiresAt <= 0 {
			return errors.New("invalid certificate observation")
		}
	}
	for _, dir := range s.Unreadable {
		if len(dir) > 512 {
			return errors.New("invalid certificate directory observation")
		}
	}
	for _, service := range s.Services {
		if !serviceName.MatchString(service.Name) || len(service.State) > 32 || len(service.SubState) > 32 {
			return errors.New("invalid service observation")
		}
	}
	for _, unit := range s.FailedUnits {
		if len(unit) > 255 {
			return errors.New("invalid failed unit observation")
		}
	}
	for _, service := range s.ServiceRestarts {
		if len(service.Unit) > 255 || !strings.HasSuffix(service.Unit, ".service") || service.Count == 0 {
			return errors.New("invalid service restart observation")
		}
	}
	total := 0
	for _, jail := range s.Fail2ban.Jails {
		total += len(jail.IPs)
		if len(jail.Name) > 120 || len(jail.IPs) > MaxFail2banIPsPerJail || total > MaxFail2banIPs || jail.Total < len(jail.IPs) {
			return errors.New("invalid fail2ban limits")
		}
		for _, ip := range jail.IPs {
			addr, err := netip.ParseAddr(ip)
			if err != nil || addr.Zone() != "" {
				return errors.New("invalid fail2ban address")
			}
		}
	}
	for _, status := range []string{s.ServiceStatus, s.NTPStatus, s.RebootStatus, s.RootStatus, s.Packages.Status, s.Fail2ban.Status} {
		switch status {
		case "", CheckOK, CheckUnsupported, CheckError, CheckDisabled, CheckNotInstalled, CheckNotRunning:
		default:
			return errors.New("invalid system check status")
		}
	}
	return nil
}

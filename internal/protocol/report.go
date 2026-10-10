package protocol

import (
	"errors"
	"math"
	"strings"
	"time"
)

const MaxReportBytes = 64 << 10

type Report struct {
	NetworkQuality      bool                         `json:"network_quality,omitempty"`
	AgentID             string                       `json:"agent_id"`
	Epoch               uint64                       `json:"epoch"`
	SessionID           string                       `json:"session_id"`
	Sequence            uint64                       `json:"sequence"`
	CollectedAt         int64                        `json:"collected_at"`
	Hostname            string                       `json:"hostname"`
	OS                  string                       `json:"os"`
	Arch                string                       `json:"arch"`
	AgentVersion        string                       `json:"agent_version,omitempty"`
	AgentUpgradeCapable bool                         `json:"agent_upgrade_capable,omitempty"`
	AgentUpgradeV2      bool                         `json:"agent_upgrade_v2,omitempty"`
	Management          *AgentManagementCapabilities `json:"management_capabilities,omitempty"`
	BootID              string                       `json:"boot_id"`
	Uptime              uint64                       `json:"uptime"`
	CPUPercent          float64                      `json:"cpu_percent"`
	CPUStealPercent     *float64                     `json:"cpu_steal_percent,omitempty"`
	CPUCores            uint32                       `json:"cpu_cores,omitempty"`
	Load1               float64                      `json:"load1"`
	Load5               float64                      `json:"load5"`
	Load15              float64                      `json:"load15"`
	RAMUsed             uint64                       `json:"ram_used"`
	RAMTotal            uint64                       `json:"ram_total"`
	RAMPercent          float64                      `json:"ram_percent"`
	SwapUsed            uint64                       `json:"swap_used"`
	SwapTotal           uint64                       `json:"swap_total"`
	SwapPercent         float64                      `json:"swap_percent"`
	DiskUsed            uint64                       `json:"disk_used"`
	DiskTotal           uint64                       `json:"disk_total"`
	DiskPercent         float64                      `json:"disk_percent"`
	DiskReadRate        *float64                     `json:"disk_read_rate,omitempty"`
	DiskWriteRate       *float64                     `json:"disk_write_rate,omitempty"`
	DiskBusyPercent     *float64                     `json:"disk_busy_percent,omitempty"`
	CountryCode         string                       `json:"country_code,omitempty"`
	RXBytes             uint64                       `json:"rx_bytes"`
	TXBytes             uint64                       `json:"tx_bytes"`
	NetworkCounters     *NetworkCounterSet           `json:"network_counters,omitempty"`
}

// NetworkCounterSet carries per-interface cumulative counters. Version zero
// is represented by a nil pointer on Report for legacy Agents.
type NetworkCounterSet struct {
	Version    uint32                    `json:"version"`
	Interfaces []NetworkInterfaceCounter `json:"interfaces"`
}

type NetworkInterfaceCounter struct {
	Name    string `json:"name"`
	RXBytes uint64 `json:"rx_bytes"`
	TXBytes uint64 `json:"tx_bytes"`
}

// AgentManagementCapabilities are opt-in fields sent only after the Server
// advertises support for management capability reports.
type AgentManagementCapabilities struct {
	RemoteRemoval     bool `json:"remote_removal"`
	CountryCodeLookup bool `json:"country_code_lookup"`
}

func (r Report) Validate() error {
	if !validID(r.AgentID, 128) || !validID(r.SessionID, 128) || !validID(r.BootID, 256) {
		return errors.New("agent_id, session_id and boot_id are required and must be reasonably sized")
	}
	if r.Epoch == 0 || r.Sequence == 0 {
		return errors.New("epoch and sequence must be greater than zero")
	}
	if r.CPUCores > 4096 {
		return errors.New("cpu_cores is outside the supported range")
	}
	if r.CollectedAt <= 0 || time.UnixMilli(r.CollectedAt).After(time.Now().Add(24*time.Hour)) {
		return errors.New("collected_at is invalid")
	}
	if !validText(r.Hostname, 255) || !validText(r.OS, 128) || !validText(r.Arch, 64) {
		return errors.New("hostname, os and arch are required and must be reasonably sized")
	}
	if r.AgentVersion != "" && (!validText(r.AgentVersion, 64) || strings.TrimSpace(r.AgentVersion) != r.AgentVersion) {
		return errors.New("agent_version is invalid")
	}
	if r.CountryCode != "" && !ValidCountryCode(r.CountryCode) {
		return errors.New("country_code must be a supported uppercase ISO alpha-2 code")
	}
	for _, v := range []float64{r.CPUPercent, r.RAMPercent, r.SwapPercent, r.DiskPercent} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 100.01 {
			return errors.New("percent values must be finite and between 0 and 100")
		}
	}
	for _, value := range []*float64{r.CPUStealPercent, r.DiskBusyPercent} {
		if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 || *value > 100.01) {
			return errors.New("optional percent values must be finite and between 0 and 100")
		}
	}
	for _, value := range []*float64{r.DiskReadRate, r.DiskWriteRate} {
		if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0) {
			return errors.New("disk rates must be finite and non-negative")
		}
	}
	for _, v := range []float64{r.Load1, r.Load5, r.Load15} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return errors.New("load values must be finite and non-negative")
		}
	}
	if r.RAMUsed > r.RAMTotal || r.SwapUsed > r.SwapTotal || r.DiskUsed > r.DiskTotal {
		return errors.New("used bytes cannot exceed total bytes")
	}
	if err := r.validateNetworkCounters(); err != nil {
		return err
	}
	return nil
}

func (r Report) validateNetworkCounters() error {
	if r.NetworkCounters == nil {
		return nil
	}
	return r.NetworkCounters.Validate(r.RXBytes, r.TXBytes)
}

// Validate checks a v1 interface snapshot and verifies its totals match the
// aggregate values carried by the surrounding report.
func (set NetworkCounterSet) Validate(rxBytes, txBytes uint64) error {
	if set.Version != 1 {
		return errors.New("network counter version is unsupported")
	}
	if set.Interfaces == nil {
		return errors.New("network interface list is required")
	}
	if len(set.Interfaces) > 256 {
		return errors.New("too many network interfaces")
	}
	const max = uint64(math.MaxInt64)
	seen := make(map[string]struct{}, len(set.Interfaces))
	var rxTotal, txTotal uint64
	for _, counter := range set.Interfaces {
		if counter.Name == "" || len(counter.Name) > 128 || strings.TrimSpace(counter.Name) != counter.Name || strings.ContainsAny(counter.Name, "\x00\r\n") {
			return errors.New("network interface name is invalid")
		}
		if _, ok := seen[counter.Name]; ok {
			return errors.New("network interface names must be unique")
		}
		seen[counter.Name] = struct{}{}
		if counter.RXBytes > max || counter.TXBytes > max {
			return errors.New("network interface counter exceeds SQLite integer range")
		}
		if counter.RXBytes > max-rxTotal || counter.TXBytes > max-txTotal {
			return errors.New("network interface counter sum exceeds SQLite integer range")
		}
		rxTotal += counter.RXBytes
		txTotal += counter.TXBytes
	}
	if rxTotal != rxBytes || txTotal != txBytes {
		return errors.New("network interface counters do not match aggregate counters")
	}
	return nil
}

func validID(s string, max int) bool {
	s = strings.TrimSpace(s)
	return s != "" && len(s) <= max && !strings.ContainsAny(s, "\x00\r\n")
}

func validText(s string, max int) bool { return validID(s, max) }

type ReportResponse struct {
	NetworkQuality *QualityConfig     `json:"network_quality,omitempty"`
	Accepted       bool               `json:"accepted"`
	Reason         string             `json:"reason,omitempty"`
	Capabilities   ReportCapabilities `json:"capabilities,omitempty"`
}

type ReportCapabilities struct {
	AgentUpgradeV2        bool `json:"agent_upgrade_v2,omitempty"`
	NetworkQuality        bool `json:"network_quality,omitempty"`
	AgentVersionReport    bool `json:"agent_version_report,omitempty"`
	LinuxMetricsReport    bool `json:"linux_metrics_report,omitempty"`
	CountryCodeReport     bool `json:"country_code_report,omitempty"`
	AgentUpgrade          bool `json:"agent_upgrade,omitempty"`
	InteractiveControl    bool `json:"interactive_control,omitempty"`
	GoogleStatus          bool `json:"google_status,omitempty"`
	Security              bool `json:"security,omitempty"`
	ManagementReport      bool `json:"management_capabilities_report,omitempty"`
	NetworkCountersReport bool `json:"network_counters_report,omitempty"`
}

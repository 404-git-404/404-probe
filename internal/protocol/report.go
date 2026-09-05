package protocol

import (
	"errors"
	"math"
	"strings"
	"time"
)

const MaxReportBytes = 64 << 10

type Report struct {
	AgentID             string  `json:"agent_id"`
	Epoch               uint64  `json:"epoch"`
	SessionID           string  `json:"session_id"`
	Sequence            uint64  `json:"sequence"`
	CollectedAt         int64   `json:"collected_at"`
	Hostname            string  `json:"hostname"`
	OS                  string  `json:"os"`
	Arch                string  `json:"arch"`
	AgentVersion        string  `json:"agent_version,omitempty"`
	AgentUpgradeCapable bool    `json:"agent_upgrade_capable,omitempty"`
	BootID              string  `json:"boot_id"`
	Uptime              uint64  `json:"uptime"`
	CPUPercent          float64 `json:"cpu_percent"`
	Load1               float64 `json:"load1"`
	Load5               float64 `json:"load5"`
	Load15              float64 `json:"load15"`
	RAMUsed             uint64  `json:"ram_used"`
	RAMTotal            uint64  `json:"ram_total"`
	RAMPercent          float64 `json:"ram_percent"`
	SwapUsed            uint64  `json:"swap_used"`
	SwapTotal           uint64  `json:"swap_total"`
	SwapPercent         float64 `json:"swap_percent"`
	DiskUsed            uint64  `json:"disk_used"`
	DiskTotal           uint64  `json:"disk_total"`
	DiskPercent         float64 `json:"disk_percent"`
	RXBytes             uint64  `json:"rx_bytes"`
	TXBytes             uint64  `json:"tx_bytes"`
}

func (r Report) Validate() error {
	if !validID(r.AgentID, 128) || !validID(r.SessionID, 128) || !validID(r.BootID, 256) {
		return errors.New("agent_id, session_id and boot_id are required and must be reasonably sized")
	}
	if r.Epoch == 0 || r.Sequence == 0 {
		return errors.New("epoch and sequence must be greater than zero")
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
	for _, v := range []float64{r.CPUPercent, r.RAMPercent, r.SwapPercent, r.DiskPercent} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 100.01 {
			return errors.New("percent values must be finite and between 0 and 100")
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
	return nil
}

func validID(s string, max int) bool {
	s = strings.TrimSpace(s)
	return s != "" && len(s) <= max && !strings.ContainsAny(s, "\x00\r\n")
}

func validText(s string, max int) bool { return validID(s, max) }

type ReportResponse struct {
	Accepted     bool               `json:"accepted"`
	Reason       string             `json:"reason,omitempty"`
	Capabilities ReportCapabilities `json:"capabilities,omitempty"`
}

type ReportCapabilities struct {
	AgentVersionReport bool `json:"agent_version_report,omitempty"`
	AgentUpgrade       bool `json:"agent_upgrade,omitempty"`
	InteractiveControl bool `json:"interactive_control,omitempty"`
	GoogleStatus       bool `json:"google_status,omitempty"`
}

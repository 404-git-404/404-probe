package protocol

import (
	"encoding/json"
	"errors"
	"math"
	"net/netip"
	"strconv"
	"time"
)

const QualityBodyLimit = 32768
const QualityBatchLimit = 64
const QualityResponseLimit = 4096
const QualityConfigRequestLimit = 512
const QualityConfigResponseLimit = 16384

type QualityConfigRequest struct {
	Epoch     string `json:"epoch"`
	SessionID string `json:"session_id"`
}

func (r QualityConfigRequest) Validate() error {
	if _, err := QualityInteger(r.Epoch); err != nil {
		return err
	}
	if !validID(r.SessionID, 128) {
		return errors.New("invalid quality session")
	}
	return nil
}

// QualityInteger is transported as a canonical decimal string, never a JS number.
func QualityInteger(s string) (int64, error) {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v <= 0 || strconv.FormatInt(v, 10) != s {
		return 0, errors.New("invalid decimal integer")
	}
	return v, nil
}

type QualityChoice struct {
	Slot     string `json:"slot"`
	Source   string `json:"source"`
	Region   string `json:"region,omitempty"`
	Protocol string `json:"protocol"`
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
}

type QualityConfigUpdate struct {
	ExpectedRevision string          `json:"expected_revision"`
	Enabled          bool            `json:"enabled"`
	IPv6             bool            `json:"ipv6"`
	Choices          []QualityChoice `json:"choices"`
}

type QualityTarget struct {
	// Host/Port on unavailable manual entries preserve saved input only;
	// they are not executable endpoints without a target ID/authorization.
	ID              string `json:"id,omitempty"`
	Slot            string `json:"slot"`
	Family          string `json:"family"`
	SlotRevision    string `json:"slot_revision"`
	Region          string `json:"region,omitempty"`
	Source          string `json:"source"`
	Protocol        string `json:"protocol"`
	Host            string `json:"host,omitempty"`
	Port            int    `json:"port,omitempty"`
	EndpointVersion string `json:"endpoint_version,omitempty"`
	Status          string `json:"status"`
}

type QualityConfig struct {
	Version       int             `json:"version"`
	Revision      string          `json:"revision"`
	Supported     bool            `json:"supported"`
	Enabled       bool            `json:"enabled"`
	IPv6          bool            `json:"ipv6"`
	IntervalMS    int             `json:"interval_ms"`
	JitterPercent int             `json:"jitter_percent"`
	TimeoutMS     int             `json:"timeout_ms"`
	Targets       []QualityTarget `json:"targets"`
}

type QualitySample struct {
	Sequence       string   `json:"seq"`
	TargetID       string   `json:"target_id"`
	ConfigRevision string   `json:"config_revision"`
	SlotRevision   string   `json:"slot_revision"`
	ScheduledAt    int64    `json:"scheduled_at"`
	StartedAt      int64    `json:"started_at"`
	FinishedAt     int64    `json:"finished_at"`
	DurationMS     float64  `json:"duration_ms"`
	Outcome        string   `json:"outcome"`
	ResolvedIP     string   `json:"resolved_ip,omitempty"`
	LatencyMS      *float64 `json:"latency_ms,omitempty"`
	Sent           int      `json:"sent"`
	Received       int      `json:"received"`
}

type QualityBatch struct {
	Version   int             `json:"version"`
	Epoch     string          `json:"epoch"`
	SessionID string          `json:"session_id"`
	Samples   []QualitySample `json:"samples"`
	Gaps      []QualityGap    `json:"gaps,omitempty"`
}

type QualityGap struct {
	Start   int64  `json:"start"`
	End     int64  `json:"end"`
	Reason  string `json:"reason"`
	Dropped *int   `json:"dropped,omitempty"`
}

// Each ACK is [request index, canonical sample seq (or empty if invalid), status].
// Every request item has a result; only committed/duplicate acknowledge storage.
type QualityAck [3]string

func (a *QualityAck) UnmarshalJSON(raw []byte) error {
	var fields []any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if len(fields) != 3 {
		return errors.New("invalid quality ACK tuple")
	}
	for i, field := range fields {
		value, ok := field.(string)
		if !ok {
			return errors.New("quality ACK fields must be strings")
		}
		a[i] = value
	}
	return nil
}

type QualityBatchResponse struct {
	Results []QualityAck `json:"results"`
	Gaps    []QualityAck `json:"gaps,omitempty"`
}

func (b QualityBatch) Validate() error {
	if b.Version != 1 || !validID(b.SessionID, 128) || (len(b.Samples) == 0 && len(b.Gaps) == 0) || len(b.Samples) > QualityBatchLimit || len(b.Gaps) > 8 {
		return errors.New("invalid quality envelope")
	}
	_, err := QualityInteger(b.Epoch)
	return err
}

func (s QualitySample) Validate(now time.Time, target QualityTarget) string {
	for _, v := range []string{s.Sequence, s.ConfigRevision, s.SlotRevision} {
		if _, err := QualityInteger(v); err != nil {
			return "invalid"
		}
	}
	if !validID(s.TargetID, 128) || len(s.ResolvedIP) > 64 || s.StartedAt <= 0 || s.ScheduledAt <= 0 || s.FinishedAt <= 0 {
		return "invalid"
	}
	if s.StartedAt < now.Add(-6*time.Hour).UnixMilli() {
		return "too_old"
	}
	if s.StartedAt > now.Add(2*time.Minute).UnixMilli() || s.FinishedAt > now.Add(2*time.Minute).UnixMilli() {
		return "clock_skew"
	}
	if s.ScheduledAt > s.StartedAt || s.FinishedAt < s.StartedAt || s.FinishedAt-s.StartedAt > 8000 {
		return "clock_skew"
	}
	if !qualityFinite(s.DurationMS) || s.DurationMS > 6000 || s.Sent < 0 || s.Sent > 1 || s.Received < 0 || s.Received > s.Sent {
		return "invalid"
	}
	if s.LatencyMS != nil && (!qualityFinite(*s.LatencyMS) || *s.LatencyMS > 6000) {
		return "invalid"
	}
	if s.ResolvedIP != "" {
		ip, err := netip.ParseAddr(s.ResolvedIP)
		if err != nil || (target.Family == "ipv4") != ip.Unmap().Is4() {
			return "invalid"
		}
	}
	switch s.Outcome {
	case "success":
		if s.LatencyMS == nil || s.ResolvedIP == "" || *s.LatencyMS > s.DurationMS+1 {
			return "invalid"
		}
	case "timeout", "dns_error", "refused", "network_error", "invalid_config", "unsupported", "permission_denied", "canceled", "not_run":
		if s.LatencyMS != nil {
			return "invalid"
		}
	default:
		return "invalid"
	}
	if target.Protocol == "tcp" {
		if s.Sent != 0 || s.Received != 0 {
			return "invalid"
		}
	} else if target.Protocol == "icmp" {
		if s.Outcome == "success" && (s.Sent != 1 || s.Received != 1) {
			return "invalid"
		}
		if s.Outcome != "success" && s.Received != 0 {
			return "invalid"
		}
		if (s.Outcome == "dns_error" || s.Outcome == "permission_denied" || s.Outcome == "unsupported" || s.Outcome == "invalid_config" || s.Outcome == "not_run") && s.Sent != 0 {
			return "invalid"
		}
		if s.Outcome == "refused" {
			return "invalid"
		}
	} else {
		return "invalid"
	}
	return ""
}

func qualityFinite(v float64) bool { return v >= 0 && !math.IsNaN(v) && !math.IsInf(v, 0) }
func (s QualitySample) Attempt() bool {
	switch s.Outcome {
	case "success", "timeout", "dns_error", "refused", "network_error":
		return true
	}
	return false
}
func (s QualitySample) PacketDenominator() bool {
	return s.Sent > 0 && (s.Outcome == "success" || s.Outcome == "timeout")
}

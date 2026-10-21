package protocol

import (
	"bytes"
	"encoding/json"
	"io"
	"time"
)

const QualityStagedSemantics = "staged_probe_v2"
const QualitySemanticsHeader = "X-404-Probe-Quality-Semantics"

// Echo order is send order, irrespective of reply order. One bounded round.
type QualityEcho struct {
	Seq    int      `json:"seq"`
	SentMS *float64 `json:"sent_ms,omitempty"`
	RTTMS  *float64 `json:"rtt_ms,omitempty"`
	Status string   `json:"status"`
}
type QualityDetails struct {
	IntervalMS       int           `json:"interval_ms,omitempty"`
	Semantics        string        `json:"semantics"`
	DNSMS            float64       `json:"dns_ms"`
	DNSOutcome       string        `json:"dns_outcome"`
	ProbeComplete    bool          `json:"probe_complete"`
	ConnectAttempted bool          `json:"connect_attempted,omitempty"`
	ConnectComplete  bool          `json:"connect_complete,omitempty"`
	ConnectMS        *float64      `json:"connect_ms,omitempty"`
	ErrorStage       string        `json:"error_stage,omitempty"`
	ErrorCode        string        `json:"error_code,omitempty"`
	Requested        int           `json:"requested,omitempty"`
	WindowMS         int           `json:"window_ms,omitempty"`
	WindowComplete   bool          `json:"window_complete,omitempty"`
	Echoes           []QualityEcho `json:"echoes,omitempty"`
}

func DecodeQualityDetails(raw json.RawMessage) (*QualityDetails, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > QualityDetailsLimit {
		return nil, io.ErrShortBuffer
	}
	var d QualityDetails
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&d); err != nil {
		return nil, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, io.ErrUnexpectedEOF
	}
	return &d, nil
}
func (d QualityDetails) Encode() json.RawMessage { b, _ := json.Marshal(d); return b }
func (s QualitySample) ValidateDetails(now time.Time, t QualityTarget) string {
	d, err := DecodeQualityDetails(s.Details)
	if err != nil || d == nil || d.Semantics != QualityStagedSemantics {
		return "invalid"
	}
	clone := s
	clone.Details = nil
	clone.Sent = 0
	clone.Received = 0
	switch clone.Outcome {
	case "partial", "no_echo_reply", "icmp_error":
		clone.Outcome = "network_error"
		clone.LatencyMS = nil
	}
	commonTarget := t
	commonTarget.Protocol = "tcp"
	if reason := clone.Validate(now, commonTarget); reason != "" {
		return reason
	}
	finite := func(v float64) bool { return qualityFinite(v) && v <= s.DurationMS+1 }
	if d.IntervalMS != 0 && (d.IntervalMS < 30000 || d.IntervalMS > 300000) {
		return "invalid"
	}
	if !finite(d.DNSMS) {
		return "invalid"
	}
	switch d.DNSOutcome {
	case "literal", "success", "dns_error", "dns_timeout", "no_address", "not_found", "canceled":
	default:
		return "invalid"
	}
	switch d.ErrorStage {
	case "", "dns", "socket", "send", "receive", "connect", "local":
	default:
		return "invalid"
	}
	switch d.ErrorCode {
	case "", "dns_error", "dns_timeout", "no_address", "not_found", "timeout", "refused", "network_error", "permission_denied", "unsupported", "canceled", "icmp_error", "clock_error", "short_write", "unsafe_address":
	default:
		return "invalid"
	}
	if t.Protocol == "tcp" {
		if s.Sent != 0 || s.Received != 0 || len(d.Echoes) != 0 || d.Requested != 0 || d.WindowMS != 0 || d.WindowComplete {
			return "invalid"
		}
		if d.ConnectAttempted != (d.ConnectMS != nil) || d.ConnectMS != nil && !finite(*d.ConnectMS) || d.ConnectComplete && !d.ConnectAttempted {
			return "invalid"
		}
		if d.ConnectMS != nil && d.DNSMS+*d.ConnectMS > s.DurationMS+1 {
			return "invalid"
		}
		if !d.ProbeComplete && s.Outcome != "canceled" && s.Outcome != "permission_denied" && s.Outcome != "unsupported" && s.Outcome != "not_run" && s.Outcome != "invalid_config" && s.Outcome != "network_error" && s.Outcome != "unsafe_address" {
			return "invalid"
		}
		if d.ConnectComplete && !d.ProbeComplete || d.ConnectAttempted && (d.DNSOutcome != "success" && d.DNSOutcome != "literal") {
			return "invalid"
		}
		if s.Outcome == "success" && (!d.ConnectComplete || s.LatencyMS == nil || *s.LatencyMS != *d.ConnectMS || d.ErrorCode != "") {
			return "invalid"
		}
		if d.ConnectComplete && s.Outcome != "success" && s.Outcome != "timeout" && s.Outcome != "refused" && s.Outcome != "network_error" {
			return "invalid"
		}
		if d.ProbeComplete && !d.ConnectComplete && d.DNSOutcome != "dns_error" && d.DNSOutcome != "dns_timeout" && d.DNSOutcome != "no_address" && d.DNSOutcome != "not_found" {
			return "invalid"
		}
		if d.ProbeComplete && !d.ConnectComplete && (s.Outcome != "dns_error" || d.ErrorStage != "dns" || d.ErrorCode != d.DNSOutcome) {
			return "invalid"
		}
		if d.ConnectComplete && s.Outcome != "success" && (d.ErrorStage != "connect" || d.ErrorCode != s.Outcome) {
			return "invalid"
		}
		if s.Outcome == "success" && d.ErrorStage != "" {
			return "invalid"
		}
		return ""
	}
	if t.Protocol != "icmp" || d.ConnectMS != nil || d.ConnectAttempted || d.ConnectComplete || d.Requested != 3 || d.WindowMS < 1 || d.WindowMS > 2000 || len(d.Echoes) != 3 {
		return "invalid"
	}
	sent, received := 0, 0
	lastSend := -1.0
	sum := 0.0
	for i, e := range d.Echoes {
		if e.Seq != i+1 {
			return "invalid"
		}
		if e.SentMS != nil {
			if !finite(*e.SentMS) || *e.SentMS < d.DNSMS-1 || i != sent || lastSend >= 0 && *e.SentMS-lastSend < 199 {
				return "invalid"
			}
			lastSend = *e.SentMS
			sent++
		}
		switch e.Status {
		case "reply":
			if e.SentMS == nil || e.RTTMS == nil || !finite(*e.RTTMS) || *e.RTTMS > float64(d.WindowMS)+1 || *e.SentMS+*e.RTTMS > s.DurationMS+1 {
				return "invalid"
			}
			received++
			sum += *e.RTTMS
		case "timeout", "icmp_error":
			if e.SentMS == nil || e.RTTMS != nil {
				return "invalid"
			}
			if e.Status == "timeout" && d.WindowComplete && s.DurationMS+1 < *e.SentMS+float64(d.WindowMS) {
				return "invalid"
			}
		case "canceled", "local_error":
			if e.RTTMS != nil || d.WindowComplete {
				return "invalid"
			}
		case "not_sent":
			if e.SentMS != nil || e.RTTMS != nil {
				return "invalid"
			}
		default:
			return "invalid"
		}
	}
	if sent != s.Sent || received != s.Received || sent > 3 || received > sent {
		return "invalid"
	}
	if d.WindowComplete {
		if d.ErrorCode != "" && d.ErrorCode != "icmp_error" && d.ErrorCode != "timeout" {
			return "invalid"
		}
		if !d.ProbeComplete || sent != 3 || d.ErrorStage != "" && d.ErrorStage != "receive" {
			return "invalid"
		}
		for _, e := range d.Echoes {
			if e.Status != "reply" && e.Status != "timeout" && e.Status != "icmp_error" {
				return "invalid"
			}
		}
	}
	if d.ProbeComplete != d.WindowComplete {
		return "invalid"
	}
	if d.WindowComplete {
		expected := "no_echo_reply"
		if received > 0 {
			expected = "partial"
		}
		if received == 3 {
			expected = "success"
		}
		if s.Outcome != expected {
			return "invalid"
		}
	} else if s.Outcome == "success" || s.Outcome == "partial" || s.Outcome == "no_echo_reply" || s.Outcome == "icmp_error" {
		return "invalid"
	}
	if s.Outcome == "success" {
		if received != 3 || !d.WindowComplete || s.LatencyMS == nil || *s.LatencyMS != sum/3 {
			return "invalid"
		}
	} else if s.LatencyMS != nil {
		return "invalid"
	}
	if s.Outcome == "partial" && (received == 0 || received == 3 || !d.WindowComplete) || s.Outcome == "no_echo_reply" && (received != 0 || !d.WindowComplete) {
		return "invalid"
	}
	return ""
}
func (s QualitySample) Staged() bool {
	d, err := DecodeQualityDetails(s.Details)
	return err == nil && d != nil && d.Semantics == QualityStagedSemantics
}

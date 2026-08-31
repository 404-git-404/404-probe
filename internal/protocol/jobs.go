package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/netip"
	"net/url"
	"strings"
	"unicode/utf8"
)

const (
	JobProtocolVersion  = 1
	MinProbeTimeoutMS   = 100
	MaxProbeTimeoutMS   = 30_000
	MaxProbeHostBytes   = 253
	MaxProbeURLBytes    = 4096
	MaxResultErrorBytes = 512
)

type ProbeType string

const (
	ProbeTypeICMPPing       ProbeType = "icmp_ping"
	ProbeTypeTCPConnect     ProbeType = "tcp_connect"
	ProbeTypeHTTP           ProbeType = "http"
	ProbeTypeSelectorSwitch ProbeType = "singbox_selector_switch"
)

func (p ProbeType) Validate() error {
	switch p {
	case ProbeTypeICMPPing, ProbeTypeTCPConnect, ProbeTypeHTTP, ProbeTypeSelectorSwitch:
		return nil
	default:
		return fmt.Errorf("unknown probe type %q", p)
	}
}

func (p ProbeType) IsNetworkProbe() bool {
	return p == ProbeTypeICMPPing || p == ProbeTypeTCPConnect || p == ProbeTypeHTTP
}

type ICMPPingConfig struct {
	Target string `json:"target"`
	Count  int    `json:"count"`
}

type TCPConnectConfig struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type HTTPConfig struct {
	URL            string `json:"url"`
	Method         string `json:"method"`
	ExpectedStatus *int   `json:"expected_status,omitempty"`
}

type SelectorSwitchConfig struct {
	Selector string `json:"selector"`
	Choice   string `json:"choice"`
}

// ProbeConfig is a typed union. Exactly one field must be non-nil and it must
// match the enclosing Job's ProbeType.
type ProbeConfig struct {
	ICMPPing       *ICMPPingConfig
	TCPConnect     *TCPConnectConfig
	HTTP           *HTTPConfig
	SelectorSwitch *SelectorSwitchConfig
}

func (c ProbeConfig) Type() (ProbeType, error) {
	var probeType ProbeType
	count := 0
	if c.ICMPPing != nil {
		probeType, count = ProbeTypeICMPPing, count+1
	}
	if c.TCPConnect != nil {
		probeType, count = ProbeTypeTCPConnect, count+1
	}
	if c.HTTP != nil {
		probeType, count = ProbeTypeHTTP, count+1
	}
	if c.SelectorSwitch != nil {
		probeType, count = ProbeTypeSelectorSwitch, count+1
	}
	if count != 1 {
		return "", errors.New("probe config must contain exactly one typed config")
	}
	return probeType, nil
}

func (c ProbeConfig) Validate(probeType ProbeType) error {
	actual, err := c.Type()
	if err != nil {
		return err
	}
	if actual != probeType {
		return fmt.Errorf("config type %q does not match probe type %q", actual, probeType)
	}
	switch probeType {
	case ProbeTypeICMPPing:
		if err := validateHost(c.ICMPPing.Target, "target"); err != nil {
			return err
		}
		if c.ICMPPing.Count < 1 || c.ICMPPing.Count > 10 {
			return errors.New("ICMP count must be between 1 and 10")
		}
	case ProbeTypeTCPConnect:
		if err := validateHost(c.TCPConnect.Host, "host"); err != nil {
			return err
		}
		if c.TCPConnect.Port < 1 || c.TCPConnect.Port > 65535 {
			return errors.New("TCP port must be between 1 and 65535")
		}
	case ProbeTypeHTTP:
		if err := validateHTTPConfig(*c.HTTP); err != nil {
			return err
		}
	case ProbeTypeSelectorSwitch:
		if err := validateOutboundName(c.SelectorSwitch.Selector); err != nil {
			return fmt.Errorf("selector: %w", err)
		}
		if err := validateOutboundName(c.SelectorSwitch.Choice); err != nil {
			return fmt.Errorf("choice: %w", err)
		}
	default:
		return fmt.Errorf("unknown probe type %q", probeType)
	}
	return nil
}

func DecodeProbeConfig(probeType ProbeType, data []byte) (ProbeConfig, error) {
	if err := probeType.Validate(); err != nil {
		return ProbeConfig{}, err
	}
	var config ProbeConfig
	switch probeType {
	case ProbeTypeICMPPing:
		var value ICMPPingConfig
		if err := decodeStrict(data, &value); err != nil {
			return ProbeConfig{}, err
		}
		config.ICMPPing = &value
	case ProbeTypeTCPConnect:
		var value TCPConnectConfig
		if err := decodeStrict(data, &value); err != nil {
			return ProbeConfig{}, err
		}
		config.TCPConnect = &value
	case ProbeTypeHTTP:
		var value HTTPConfig
		if err := decodeStrict(data, &value); err != nil {
			return ProbeConfig{}, err
		}
		config.HTTP = &value
	case ProbeTypeSelectorSwitch:
		var value SelectorSwitchConfig
		if err := decodeStrict(data, &value); err != nil {
			return ProbeConfig{}, err
		}
		config.SelectorSwitch = &value
	}
	if err := config.Validate(probeType); err != nil {
		return ProbeConfig{}, err
	}
	return config, nil
}

func MarshalProbeConfig(probeType ProbeType, config ProbeConfig) ([]byte, error) {
	if err := config.Validate(probeType); err != nil {
		return nil, err
	}
	switch probeType {
	case ProbeTypeICMPPing:
		return json.Marshal(config.ICMPPing)
	case ProbeTypeTCPConnect:
		return json.Marshal(config.TCPConnect)
	case ProbeTypeHTTP:
		return json.Marshal(config.HTTP)
	case ProbeTypeSelectorSwitch:
		return json.Marshal(config.SelectorSwitch)
	default:
		return nil, fmt.Errorf("unknown probe type %q", probeType)
	}
}

type ClaimRequest struct {
	ProtocolVersion     int         `json:"protocol_version"`
	AgentEpoch          uint64      `json:"agent_epoch"`
	SessionID           string      `json:"session_id"`
	SupportedProbeTypes []ProbeType `json:"supported_probe_types"`
}

func (r ClaimRequest) Validate() error {
	if r.ProtocolVersion != JobProtocolVersion {
		return fmt.Errorf("unsupported protocol version %d", r.ProtocolVersion)
	}
	if r.AgentEpoch == 0 || r.AgentEpoch > math.MaxInt64 {
		return errors.New("agent_epoch must be between 1 and MaxInt64")
	}
	if !validIdentifier(r.SessionID, 128) {
		return errors.New("session_id is required and must be at most 128 bytes")
	}
	if len(r.SupportedProbeTypes) == 0 || len(r.SupportedProbeTypes) > 4 {
		return errors.New("supported_probe_types must contain between 1 and 4 values")
	}
	seen := make(map[ProbeType]struct{}, len(r.SupportedProbeTypes))
	for _, probeType := range r.SupportedProbeTypes {
		if err := probeType.Validate(); err != nil {
			return err
		}
		if _, exists := seen[probeType]; exists {
			return fmt.Errorf("duplicate supported probe type %q", probeType)
		}
		seen[probeType] = struct{}{}
	}
	return nil
}

func DecodeClaimRequest(data []byte) (ClaimRequest, error) {
	var request ClaimRequest
	if err := decodeStrict(data, &request); err != nil {
		return ClaimRequest{}, err
	}
	if err := request.Validate(); err != nil {
		return ClaimRequest{}, err
	}
	return request, nil
}

type Job struct {
	ProtocolVersion int         `json:"protocol_version"`
	JobID           string      `json:"job_id"`
	ProbeType       ProbeType   `json:"probe_type"`
	Config          ProbeConfig `json:"-"`
	CreatedAt       int64       `json:"created_at"`
	NotBefore       int64       `json:"not_before"`
	ExpiresAt       int64       `json:"expires_at"`
	TimeoutMS       int         `json:"timeout_ms"`
	Attempt         int64       `json:"attempt"`
	LeaseToken      string      `json:"lease_token"`
	LeaseExpiresAt  int64       `json:"lease_expires_at"`
}

func (j Job) Validate() error {
	if j.ProtocolVersion != JobProtocolVersion {
		return fmt.Errorf("unsupported protocol version %d", j.ProtocolVersion)
	}
	if !validIdentifier(j.JobID, 128) {
		return errors.New("job_id is required and must be at most 128 bytes")
	}
	if err := j.ProbeType.Validate(); err != nil {
		return err
	}
	if err := j.Config.Validate(j.ProbeType); err != nil {
		return err
	}
	if j.CreatedAt <= 0 || j.NotBefore < j.CreatedAt || j.ExpiresAt <= j.NotBefore {
		return errors.New("job timestamps are invalid")
	}
	if j.TimeoutMS < MinProbeTimeoutMS || j.TimeoutMS > MaxProbeTimeoutMS {
		return fmt.Errorf("timeout_ms must be between %d and %d", MinProbeTimeoutMS, MaxProbeTimeoutMS)
	}
	if j.Attempt < 1 {
		return errors.New("attempt must be greater than zero")
	}
	if !validIdentifier(j.LeaseToken, 256) || j.LeaseExpiresAt <= 0 {
		return errors.New("lease token and expiry are required")
	}
	return nil
}

func (j Job) MarshalJSON() ([]byte, error) {
	if err := j.Validate(); err != nil {
		return nil, err
	}
	config, err := MarshalProbeConfig(j.ProbeType, j.Config)
	if err != nil {
		return nil, err
	}
	type wireJob struct {
		ProtocolVersion int             `json:"protocol_version"`
		JobID           string          `json:"job_id"`
		ProbeType       ProbeType       `json:"probe_type"`
		Config          json.RawMessage `json:"config"`
		CreatedAt       int64           `json:"created_at"`
		NotBefore       int64           `json:"not_before"`
		ExpiresAt       int64           `json:"expires_at"`
		TimeoutMS       int             `json:"timeout_ms"`
		Attempt         int64           `json:"attempt"`
		LeaseToken      string          `json:"lease_token"`
		LeaseExpiresAt  int64           `json:"lease_expires_at"`
	}
	return json.Marshal(wireJob{j.ProtocolVersion, j.JobID, j.ProbeType, config, j.CreatedAt, j.NotBefore, j.ExpiresAt, j.TimeoutMS, j.Attempt, j.LeaseToken, j.LeaseExpiresAt})
}

func (j *Job) UnmarshalJSON(data []byte) error {
	var wire struct {
		ProtocolVersion int             `json:"protocol_version"`
		JobID           string          `json:"job_id"`
		ProbeType       ProbeType       `json:"probe_type"`
		Config          json.RawMessage `json:"config"`
		CreatedAt       int64           `json:"created_at"`
		NotBefore       int64           `json:"not_before"`
		ExpiresAt       int64           `json:"expires_at"`
		TimeoutMS       int             `json:"timeout_ms"`
		Attempt         int64           `json:"attempt"`
		LeaseToken      string          `json:"lease_token"`
		LeaseExpiresAt  int64           `json:"lease_expires_at"`
	}
	if err := decodeStrict(data, &wire); err != nil {
		return err
	}
	config, err := DecodeProbeConfig(wire.ProbeType, wire.Config)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	*j = Job{wire.ProtocolVersion, wire.JobID, wire.ProbeType, config, wire.CreatedAt, wire.NotBefore, wire.ExpiresAt, wire.TimeoutMS, wire.Attempt, wire.LeaseToken, wire.LeaseExpiresAt}
	return j.Validate()
}

type ICMPPingResult struct {
	Sent              int     `json:"sent"`
	Received          int     `json:"received"`
	PacketLossPercent float64 `json:"packet_loss_percent"`
	LatencyMinMS      float64 `json:"latency_min_ms"`
	LatencyAvgMS      float64 `json:"latency_avg_ms"`
	LatencyMaxMS      float64 `json:"latency_max_ms"`
}

type TCPConnectResult struct {
	ConnectMS float64 `json:"connect_ms"`
}

type HTTPResult struct {
	DNSMS         float64 `json:"dns_ms"`
	ConnectMS     float64 `json:"connect_ms"`
	TLSMS         float64 `json:"tls_ms"`
	TTFBMS        float64 `json:"ttfb_ms"`
	TotalMS       float64 `json:"total_ms"`
	StatusCode    int     `json:"status_code"`
	BodyBytes     uint64  `json:"body_bytes"`
	BodyTruncated bool    `json:"body_truncated"`
}

type SelectorSwitchResult struct {
	Current string `json:"current"`
	Changed bool   `json:"changed"`
}

// ProbeResult is a typed union. Exactly one field must be non-nil.
type ProbeResult struct {
	ICMPPing       *ICMPPingResult
	TCPConnect     *TCPConnectResult
	HTTP           *HTTPResult
	SelectorSwitch *SelectorSwitchResult
}

func (r ProbeResult) Type() (ProbeType, error) {
	var probeType ProbeType
	count := 0
	if r.ICMPPing != nil {
		probeType, count = ProbeTypeICMPPing, count+1
	}
	if r.TCPConnect != nil {
		probeType, count = ProbeTypeTCPConnect, count+1
	}
	if r.HTTP != nil {
		probeType, count = ProbeTypeHTTP, count+1
	}
	if r.SelectorSwitch != nil {
		probeType, count = ProbeTypeSelectorSwitch, count+1
	}
	if count != 1 {
		return "", errors.New("probe result must contain exactly one typed payload")
	}
	return probeType, nil
}

func (r ProbeResult) Validate(probeType ProbeType) error {
	actual, err := r.Type()
	if err != nil {
		return err
	}
	if actual != probeType {
		return fmt.Errorf("result type %q does not match probe type %q", actual, probeType)
	}
	switch probeType {
	case ProbeTypeICMPPing:
		value := r.ICMPPing
		if value.Sent < 1 || value.Sent > 10 || value.Received < 0 || value.Received > value.Sent {
			return errors.New("ICMP sent/received values are invalid")
		}
		if err := validateFiniteNonNegative(value.PacketLossPercent, value.LatencyMinMS, value.LatencyAvgMS, value.LatencyMaxMS); err != nil {
			return err
		}
		if value.PacketLossPercent > 100 {
			return errors.New("packet_loss_percent must not exceed 100")
		}
		wantLoss := 100 * float64(value.Sent-value.Received) / float64(value.Sent)
		if math.Abs(value.PacketLossPercent-wantLoss) > 0.000001 {
			return errors.New("packet_loss_percent does not match sent and received")
		}
		if value.Received > 0 && (value.LatencyMinMS > value.LatencyAvgMS || value.LatencyAvgMS > value.LatencyMaxMS) {
			return errors.New("ICMP latency min/avg/max values are inconsistent")
		}
	case ProbeTypeTCPConnect:
		return validateFiniteNonNegative(r.TCPConnect.ConnectMS)
	case ProbeTypeHTTP:
		value := r.HTTP
		if err := validateFiniteNonNegative(value.DNSMS, value.ConnectMS, value.TLSMS, value.TTFBMS, value.TotalMS); err != nil {
			return err
		}
		if value.StatusCode != 0 && (value.StatusCode < 100 || value.StatusCode > 599) {
			return errors.New("HTTP status_code must be zero or between 100 and 599")
		}
		if value.BodyBytes > math.MaxInt64 {
			return errors.New("HTTP body_bytes exceeds MaxInt64")
		}
	case ProbeTypeSelectorSwitch:
		if r.SelectorSwitch.Current != "" {
			if err := validateOutboundName(r.SelectorSwitch.Current); err != nil {
				return fmt.Errorf("current: %w", err)
			}
		}
	default:
		return fmt.Errorf("unknown probe type %q", probeType)
	}
	return nil
}

func DecodeProbeResult(probeType ProbeType, data []byte) (ProbeResult, error) {
	if err := probeType.Validate(); err != nil {
		return ProbeResult{}, err
	}
	var result ProbeResult
	switch probeType {
	case ProbeTypeICMPPing:
		var value ICMPPingResult
		if err := decodeStrict(data, &value); err != nil {
			return ProbeResult{}, err
		}
		result.ICMPPing = &value
	case ProbeTypeTCPConnect:
		var value TCPConnectResult
		if err := decodeStrict(data, &value); err != nil {
			return ProbeResult{}, err
		}
		result.TCPConnect = &value
	case ProbeTypeHTTP:
		var value HTTPResult
		if err := decodeStrict(data, &value); err != nil {
			return ProbeResult{}, err
		}
		result.HTTP = &value
	case ProbeTypeSelectorSwitch:
		var value SelectorSwitchResult
		if err := decodeStrict(data, &value); err != nil {
			return ProbeResult{}, err
		}
		result.SelectorSwitch = &value
	}
	if err := result.Validate(probeType); err != nil {
		return ProbeResult{}, err
	}
	return result, nil
}

func MarshalProbeResult(probeType ProbeType, result ProbeResult) ([]byte, error) {
	if err := result.Validate(probeType); err != nil {
		return nil, err
	}
	normalized := normalizeProbeResult(result)
	switch probeType {
	case ProbeTypeICMPPing:
		return json.Marshal(normalized.ICMPPing)
	case ProbeTypeTCPConnect:
		return json.Marshal(normalized.TCPConnect)
	case ProbeTypeHTTP:
		return json.Marshal(normalized.HTTP)
	case ProbeTypeSelectorSwitch:
		return json.Marshal(normalized.SelectorSwitch)
	default:
		return nil, fmt.Errorf("unknown probe type %q", probeType)
	}
}

type JobResult struct {
	ProtocolVersion int         `json:"protocol_version"`
	LeaseToken      string      `json:"lease_token"`
	Attempt         int64       `json:"attempt"`
	AgentEpoch      uint64      `json:"agent_epoch"`
	SessionID       string      `json:"session_id"`
	StartedAt       int64       `json:"started_at"`
	FinishedAt      int64       `json:"finished_at"`
	DurationMS      float64     `json:"duration_ms"`
	Success         bool        `json:"success"`
	ResolvedIP      string      `json:"resolved_ip,omitempty"`
	ErrorCategory   string      `json:"error_category,omitempty"`
	ErrorMessage    string      `json:"error_message,omitempty"`
	Result          ProbeResult `json:"-"`
}

func (r JobResult) Validate(probeType ProbeType) error {
	if r.ProtocolVersion != JobProtocolVersion {
		return fmt.Errorf("unsupported protocol version %d", r.ProtocolVersion)
	}
	if !validIdentifier(r.LeaseToken, 256) || r.Attempt < 1 {
		return errors.New("lease_token and positive attempt are required")
	}
	if r.AgentEpoch == 0 || r.AgentEpoch > math.MaxInt64 || !validIdentifier(r.SessionID, 128) {
		return errors.New("agent epoch or session is invalid")
	}
	if r.StartedAt <= 0 || r.FinishedAt < r.StartedAt {
		return errors.New("result timestamps are invalid")
	}
	if err := validateFiniteNonNegative(r.DurationMS); err != nil {
		return err
	}
	if r.ResolvedIP != "" {
		if _, err := netip.ParseAddr(r.ResolvedIP); err != nil {
			return errors.New("resolved_ip must be a valid IP address")
		}
	}
	if !utf8.ValidString(r.ErrorMessage) || len(r.ErrorMessage) > MaxResultErrorBytes || containsControl(r.ErrorMessage) {
		return fmt.Errorf("error_message must be valid text of at most %d bytes", MaxResultErrorBytes)
	}
	if len(r.ErrorCategory) > 64 || containsControl(r.ErrorCategory) || strings.TrimSpace(r.ErrorCategory) != r.ErrorCategory {
		return errors.New("error_category is invalid")
	}
	if r.Success && (r.ErrorCategory != "" || r.ErrorMessage != "") {
		return errors.New("successful result must not contain an error")
	}
	if !r.Success && r.ErrorCategory == "" {
		return errors.New("failed result requires error_category")
	}
	return r.Result.Validate(probeType)
}

func (r JobResult) MarshalJSON() ([]byte, error) {
	probeType, err := r.Result.Type()
	if err != nil {
		return nil, err
	}
	if err := r.Validate(probeType); err != nil {
		return nil, err
	}
	payload, err := MarshalProbeResult(probeType, r.Result)
	if err != nil {
		return nil, err
	}
	return marshalJobResultWire(r, payload)
}

func DecodeJobResult(data []byte, probeType ProbeType) (JobResult, error) {
	var wire struct {
		ProtocolVersion int             `json:"protocol_version"`
		LeaseToken      string          `json:"lease_token"`
		Attempt         int64           `json:"attempt"`
		AgentEpoch      uint64          `json:"agent_epoch"`
		SessionID       string          `json:"session_id"`
		StartedAt       int64           `json:"started_at"`
		FinishedAt      int64           `json:"finished_at"`
		DurationMS      float64         `json:"duration_ms"`
		Success         bool            `json:"success"`
		ResolvedIP      string          `json:"resolved_ip"`
		ErrorCategory   string          `json:"error_category"`
		ErrorMessage    string          `json:"error_message"`
		Result          json.RawMessage `json:"result"`
	}
	if err := decodeStrict(data, &wire); err != nil {
		return JobResult{}, err
	}
	payload, err := DecodeProbeResult(probeType, wire.Result)
	if err != nil {
		return JobResult{}, fmt.Errorf("result: %w", err)
	}
	result := JobResult{
		ProtocolVersion: wire.ProtocolVersion, LeaseToken: wire.LeaseToken, Attempt: wire.Attempt,
		AgentEpoch: wire.AgentEpoch, SessionID: wire.SessionID, StartedAt: wire.StartedAt,
		FinishedAt: wire.FinishedAt, DurationMS: wire.DurationMS, Success: wire.Success,
		ResolvedIP: wire.ResolvedIP, ErrorCategory: wire.ErrorCategory, ErrorMessage: wire.ErrorMessage,
		Result: payload,
	}
	if err := result.Validate(probeType); err != nil {
		return JobResult{}, err
	}
	return result, nil
}

// CanonicalResult returns the server-normalized payload JSON and a digest of
// the complete typed measurement. Lease credentials and process identity are
// deliberately excluded from the digest because they are delivery metadata.
func CanonicalResult(probeType ProbeType, result JobResult) ([]byte, [sha256.Size]byte, error) {
	if err := result.Validate(probeType); err != nil {
		return nil, [sha256.Size]byte{}, err
	}
	normalized := result
	normalized.DurationMS = normalizeZero(normalized.DurationMS)
	if normalized.ResolvedIP != "" {
		address, _ := netip.ParseAddr(normalized.ResolvedIP)
		normalized.ResolvedIP = address.String()
	}
	normalized.Result = normalizeProbeResult(normalized.Result)
	payload, err := MarshalProbeResult(probeType, normalized.Result)
	if err != nil {
		return nil, [sha256.Size]byte{}, err
	}
	canonical := struct {
		ProbeType     ProbeType       `json:"probe_type"`
		StartedAt     int64           `json:"started_at"`
		FinishedAt    int64           `json:"finished_at"`
		DurationMS    float64         `json:"duration_ms"`
		Success       bool            `json:"success"`
		ResolvedIP    string          `json:"resolved_ip,omitempty"`
		ErrorCategory string          `json:"error_category,omitempty"`
		ErrorMessage  string          `json:"error_message,omitempty"`
		Result        json.RawMessage `json:"result"`
	}{probeType, normalized.StartedAt, normalized.FinishedAt, normalized.DurationMS, normalized.Success, normalized.ResolvedIP, normalized.ErrorCategory, normalized.ErrorMessage, payload}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return nil, [sha256.Size]byte{}, err
	}
	return payload, sha256.Sum256(encoded), nil
}

func marshalJobResultWire(result JobResult, payload []byte) ([]byte, error) {
	wire := struct {
		ProtocolVersion int             `json:"protocol_version"`
		LeaseToken      string          `json:"lease_token"`
		Attempt         int64           `json:"attempt"`
		AgentEpoch      uint64          `json:"agent_epoch"`
		SessionID       string          `json:"session_id"`
		StartedAt       int64           `json:"started_at"`
		FinishedAt      int64           `json:"finished_at"`
		DurationMS      float64         `json:"duration_ms"`
		Success         bool            `json:"success"`
		ResolvedIP      string          `json:"resolved_ip,omitempty"`
		ErrorCategory   string          `json:"error_category,omitempty"`
		ErrorMessage    string          `json:"error_message,omitempty"`
		Result          json.RawMessage `json:"result"`
	}{result.ProtocolVersion, result.LeaseToken, result.Attempt, result.AgentEpoch, result.SessionID, result.StartedAt, result.FinishedAt, result.DurationMS, result.Success, result.ResolvedIP, result.ErrorCategory, result.ErrorMessage, payload}
	return json.Marshal(wire)
}

func normalizeProbeResult(result ProbeResult) ProbeResult {
	normalized := result
	if result.ICMPPing != nil {
		value := *result.ICMPPing
		value.PacketLossPercent = normalizeZero(value.PacketLossPercent)
		value.LatencyMinMS = normalizeZero(value.LatencyMinMS)
		value.LatencyAvgMS = normalizeZero(value.LatencyAvgMS)
		value.LatencyMaxMS = normalizeZero(value.LatencyMaxMS)
		normalized.ICMPPing = &value
	}
	if result.TCPConnect != nil {
		value := *result.TCPConnect
		value.ConnectMS = normalizeZero(value.ConnectMS)
		normalized.TCPConnect = &value
	}
	if result.HTTP != nil {
		value := *result.HTTP
		value.DNSMS = normalizeZero(value.DNSMS)
		value.ConnectMS = normalizeZero(value.ConnectMS)
		value.TLSMS = normalizeZero(value.TLSMS)
		value.TTFBMS = normalizeZero(value.TTFBMS)
		value.TotalMS = normalizeZero(value.TotalMS)
		normalized.HTTP = &value
	}
	return normalized
}

func validateHTTPConfig(config HTTPConfig) error {
	if len(config.URL) == 0 || len(config.URL) > MaxProbeURLBytes || !utf8.ValidString(config.URL) || containsControl(config.URL) {
		return fmt.Errorf("URL must be valid text of at most %d bytes", MaxProbeURLBytes)
	}
	parsed, err := url.Parse(config.URL)
	if err != nil || !parsed.IsAbs() || parsed.Opaque != "" || parsed.Host == "" || parsed.Hostname() == "" {
		return errors.New("URL must be an absolute HTTP or HTTPS URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("URL scheme must be http or https")
	}
	if parsed.User != nil {
		return errors.New("URL userinfo is not allowed")
	}
	if parsed.Fragment != "" {
		return errors.New("URL fragment is not allowed")
	}
	if len(parsed.Hostname()) > MaxProbeHostBytes {
		return fmt.Errorf("URL hostname must be at most %d bytes", MaxProbeHostBytes)
	}
	if config.Method != "GET" && config.Method != "HEAD" {
		return errors.New("HTTP method must be GET or HEAD")
	}
	if config.ExpectedStatus != nil && (*config.ExpectedStatus < 100 || *config.ExpectedStatus > 599) {
		return errors.New("expected_status must be between 100 and 599")
	}
	return nil
}

func validateHost(value, field string) error {
	if len(value) == 0 || len(value) > MaxProbeHostBytes || !utf8.ValidString(value) || containsControl(value) || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must be valid text of at most %d bytes", field, MaxProbeHostBytes)
	}
	if strings.ContainsAny(value, "/?#@[]") {
		return fmt.Errorf("%s must be a hostname or IP address", field)
	}
	if strings.Contains(value, ":") {
		if _, err := netip.ParseAddr(value); err != nil {
			return fmt.Errorf("%s must be a hostname or IP address", field)
		}
	}
	return nil
}

func validIdentifier(value string, max int) bool {
	return value != "" && len(value) <= max && utf8.ValidString(value) && !containsControl(value) && strings.TrimSpace(value) == value
}

func containsControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func validateFiniteNonNegative(values ...float64) error {
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return errors.New("duration and latency values must be finite and non-negative")
		}
	}
	return nil
}

func normalizeZero(value float64) float64 {
	if value == 0 {
		return 0
	}
	return value
}

func decodeStrict(data []byte, value any) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return errors.New("JSON object is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("multiple JSON values")
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"time"
)

const (
	SecurityProtocolVersion = 1
	MaxSecurityBodyBytes    = 128 << 10
	MaxSecuritySources      = 100
)

type SecurityStatus string

const (
	SecurityStatusUnavailable SecurityStatus = "unavailable"
	SecurityStatusNoData      SecurityStatus = "no_data"
	SecurityStatusComplete    SecurityStatus = "complete"
	SecurityStatusPartial     SecurityStatus = "partial"
	SecurityStatusFailed      SecurityStatus = "failed"
)

type SecurityClassification string

const (
	SecurityObserved   SecurityClassification = "OBSERVED"
	SecurityRepeated   SecurityClassification = "REPEATED"
	SecurityPersistent SecurityClassification = "PERSISTENT"
	SecurityHighVolume SecurityClassification = "HIGH_VOLUME"
)

type SecuritySource struct {
	IP              string                   `json:"ip"`
	Count           int                      `json:"count"`
	FirstSeen       int64                    `json:"first_seen"`
	LastSeen        int64                    `json:"last_seen"`
	DurationMS      int64                    `json:"duration_ms"`
	Classifications []SecurityClassification `json:"classifications"`
}

type SecurityBatch struct {
	BatchID        string           `json:"batch_id"`
	WindowStart    int64            `json:"window_start"`
	WindowEnd      int64            `json:"window_end"`
	CollectedAt    int64            `json:"collected_at"`
	Status         SecurityStatus   `json:"status"`
	Reason         string           `json:"reason,omitempty"`
	TotalEvents    int              `json:"total_events"`
	TrackedSources int              `json:"tracked_sources"`
	DroppedSources int              `json:"dropped_sources,omitempty"`
	DroppedEvents  int              `json:"dropped_events,omitempty"`
	ScannedLines   int              `json:"scanned_lines"`
	ScannedBytes   int64            `json:"scanned_bytes"`
	Sources        []SecuritySource `json:"sources"`
}

type SecuritySubmission struct {
	ProtocolVersion int               `json:"protocol_version"`
	AgentEpoch      uint64            `json:"agent_epoch"`
	SessionID       string            `json:"session_id"`
	Status          SecurityStatus    `json:"status"`
	Reason          string            `json:"reason,omitempty"`
	Batch           *SecurityBatch    `json:"batch,omitempty"`
	Delivery        *SecurityDelivery `json:"delivery,omitempty"`
}

// SecurityDelivery describes transport coverage without changing the immutable
// collector batch. It is deliberately excluded from the batch content hash.
type SecurityDelivery struct {
	OutboxGap           bool  `json:"outbox_gap"`
	PreviousCollectedAt int64 `json:"previous_collected_at"`
}

type SecurityResponse struct {
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
}

func DecodeSecuritySubmission(data []byte) (SecuritySubmission, error) {
	var value SecuritySubmission
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return SecuritySubmission{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return SecuritySubmission{}, errors.New("invalid trailing JSON data")
	}
	if err := value.ValidateAt(time.Now()); err != nil {
		return SecuritySubmission{}, err
	}
	return value, nil
}

func (s SecuritySubmission) Validate() error {
	return s.ValidateAt(time.Now())
}

func (s SecuritySubmission) ValidateAt(now time.Time) error {
	if s.ProtocolVersion != SecurityProtocolVersion || s.AgentEpoch == 0 || !validID(s.SessionID, 128) {
		return errors.New("invalid security protocol or session")
	}
	if !validSecurityStatus(s.Status) || !validSecurityReason(s.Reason) {
		return errors.New("invalid security status")
	}
	if s.Batch == nil {
		if s.Delivery != nil {
			return errors.New("security delivery metadata requires a batch")
		}
		if s.Status != SecurityStatusUnavailable && s.Status != SecurityStatusNoData && s.Status != SecurityStatusFailed {
			return errors.New("status requires a batch")
		}
		return nil
	}
	if s.Status != s.Batch.Status {
		return errors.New("submission and batch statuses differ")
	}
	if s.Delivery != nil && (!s.Delivery.OutboxGap || s.Delivery.PreviousCollectedAt <= 0 || s.Delivery.PreviousCollectedAt >= s.Batch.CollectedAt) {
		return errors.New("invalid security delivery coverage")
	}
	return s.Batch.ValidateAt(now)
}

func (b SecurityBatch) Validate() error {
	return b.ValidateAt(time.Now())
}

func (b SecurityBatch) ValidateAt(now time.Time) error {
	if len(b.BatchID) != 32 || !lowerHex(b.BatchID) || b.WindowStart <= 0 || b.WindowEnd < b.WindowStart || b.CollectedAt < b.WindowEnd || time.UnixMilli(b.CollectedAt).After(now.Add(24*time.Hour)) {
		return errors.New("invalid security batch identity or timestamps")
	}
	if !validSecurityStatus(b.Status) || b.Status == SecurityStatusUnavailable || b.Status == SecurityStatusNoData {
		return errors.New("invalid batch status")
	}
	if !validSecurityReason(b.Reason) || b.TotalEvents < 0 || b.TotalEvents > 100000 || b.TrackedSources < 0 || b.TrackedSources > 4096 || b.DroppedSources < 0 || b.DroppedSources > 4096 || b.DroppedEvents < 0 || b.DroppedEvents > 100000 || b.ScannedLines < 0 || b.ScannedLines > 100000 || b.ScannedBytes < 0 || b.ScannedBytes > 64<<20 || b.TotalEvents > b.ScannedLines {
		return errors.New("invalid security batch counters")
	}
	if b.Status == SecurityStatusComplete && (b.Reason != "" || b.DroppedSources != 0 || b.DroppedEvents != 0) {
		return errors.New("complete security batch cannot be truncated")
	}
	if b.Status == SecurityStatusComplete && (b.TrackedSources != len(b.Sources)) {
		return errors.New("complete security batch must include every tracked source")
	}
	if (b.Status == SecurityStatusPartial || b.Status == SecurityStatusFailed) && b.Reason == "" {
		return errors.New("incomplete security batch requires a typed reason")
	}
	if len(b.Sources) > MaxSecuritySources || b.TrackedSources < len(b.Sources) {
		return errors.New("invalid security source bounds")
	}
	var previous *SecuritySource
	seenIPs := make(map[string]bool, len(b.Sources))
	sourceEvents := 0
	for index := range b.Sources {
		source := b.Sources[index]
		if err := source.Validate(b.WindowStart, b.WindowEnd); err != nil {
			return err
		}
		if seenIPs[source.IP] {
			return errors.New("duplicate security source")
		}
		seenIPs[source.IP] = true
		sourceEvents += source.Count
		if previous != nil && securitySourceLess(source, *previous) {
			return errors.New("security sources are not deterministically ordered")
		}
		previous = &b.Sources[index]
	}
	if sourceEvents > b.TotalEvents {
		return errors.New("security source counts exceed total events")
	}
	if b.Status == SecurityStatusComplete && sourceEvents != b.TotalEvents {
		return errors.New("complete security batch source counts must equal total events")
	}
	if b.Status == SecurityStatusPartial && sourceEvents+b.DroppedEvents > b.TotalEvents {
		return errors.New("partial security batch counters are inconsistent")
	}
	return nil
}

func (s SecuritySource) Validate(start, end int64) error {
	ip := net.ParseIP(s.IP)
	if ip == nil || ip.String() != s.IP || s.Count <= 0 || s.Count > 100000 || s.FirstSeen < start || s.LastSeen < s.FirstSeen || s.LastSeen > end || s.DurationMS != s.LastSeen-s.FirstSeen {
		return fmt.Errorf("invalid security source %q", s.IP)
	}
	if len(s.Classifications) == 0 || len(s.Classifications) > 2 {
		return errors.New("invalid security classifications")
	}
	seen := map[SecurityClassification]bool{}
	for _, class := range s.Classifications {
		if class != SecurityObserved && class != SecurityRepeated && class != SecurityPersistent && class != SecurityHighVolume || seen[class] {
			return errors.New("invalid security classification")
		}
		seen[class] = true
	}
	return nil
}

func SortSecuritySources(sources []SecuritySource) {
	sort.Slice(sources, func(i, j int) bool { return securitySourceLess(sources[i], sources[j]) })
}

func securitySourceLess(a, b SecuritySource) bool {
	if a.Count != b.Count {
		return a.Count > b.Count
	}
	return a.IP < b.IP
}

func validSecurityStatus(status SecurityStatus) bool {
	switch status {
	case SecurityStatusUnavailable, SecurityStatusNoData, SecurityStatusComplete, SecurityStatusPartial, SecurityStatusFailed:
		return true
	default:
		return false
	}
}

func validSecurityReason(reason string) bool {
	switch reason {
	case "", "setup_required", "platform_unsupported", "awaiting_first_collection", "export_read_failed", "invalid_export",
		"cursor_invalid_recent_24h", "journal_timeout", "journal_read_failed", "journalctl_unavailable",
		"input_limit", "line_size_limit", "malformed_or_untrusted_lines", "source_limit",
		"source_output_limit", "missing_cursor", "outbox_gap", "no_journal_entries",
		"missing_message", "invalid_reality_entry":
		return true
	default:
		return false
	}
}

func lowerHex(value string) bool {
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func SecurityStale(collectedAt int64, now time.Time) bool {
	return collectedAt <= 0 || now.Sub(time.UnixMilli(collectedAt)) > 30*time.Hour
}

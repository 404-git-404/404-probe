package agent

import (
	"404-probe/internal/protocol"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

var errQualityAuthorization = errors.New("quality session unavailable")
var errQualityV2Rejected = errors.New("quality v2 report rejected")

type qualityControl struct {
	excludedReason   string // Local platform gate, immutable for this Runner.
	mu               sync.Mutex
	wake             chan struct{}
	allowed, fetch   bool
	config           *protocol.QualityConfig
	generation       uint64
	stopSerial       uint64           // A stop fence cannot be coalesced away by a later resume.
	sequence         int64            // Survives local worker restart in this Runner/session.
	now              func() time.Time // Instance-local test clock; production keeps monotonic time.Now.
	random           func() float64
	probe            qualityProbeFunc
	uploadScheduled  func(time.Time) // Instance-local deterministic timing observation.
	scheduleObserved func(uint64, int)
	sampleQueued     func(string, string)
	// userTargets is true while the Server answered the targets header of this session: user-entered
	// targets are accepted, and catalog targets are not (guarded by mu).
	userTargets bool
	ipv6        *qualityIPv6Monitor
	// NAT64 discovery starts once, when the first user target is installed; the fields let tests
	// inject the resolver and the clock.
	nat64Started  bool
	nat64Resolver nat64Lookup
	nat64Wait     func(time.Duration) <-chan time.Time
}

func newQualityControl() *qualityControl {
	return &qualityControl{wake: make(chan struct{}, 1), ipv6: newQualityIPv6Monitor()}
}
func (q *qualityControl) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}
func (r *Runner) stopQuality(reason string) {
	if r.quality == nil {
		return
	}
	q := r.quality
	q.mu.Lock()
	q.allowed = false
	q.fetch = false
	q.config = nil
	q.generation++
	q.stopSerial++
	q.mu.Unlock()
	q.signal()
	r.logger.Info("quality stopped", "reason", reason)
}
func qualityValidateConfig(c protocol.QualityConfig) error { return qualityValidateConfigFor(c, false) }

// qualityValidateConfigFor validates a configuration. userTargets is whether this session negotiated
// user-entered targets: then active targets must be source "user" and pass the host rules shared with
// the Server; otherwise they must be catalog targets as before.
func qualityValidateConfigFor(c protocol.QualityConfig, userTargets bool) error {
	limit := 6
	if c.Version == 2 {
		limit = 12
		if c.Capabilities != nil && c.Capabilities.Validate() != nil {
			return errors.New("invalid quality capability details")
		}
		if c.Semantics != protocol.QualityV2Semantics && c.Semantics != protocol.QualityStagedSemantics {
			return errors.New("unsupported quality semantics")
		}
	}
	intervalValid := c.IntervalMS == 30000 || c.Version == 2 && c.Semantics == protocol.QualityStagedSemantics && c.IntervalMS >= 30000 && c.IntervalMS <= 300000 && c.IntervalMS%30000 == 0
	if (c.Version != 1 && c.Version != 2) || !intervalValid || c.JitterPercent != 20 || c.TimeoutMS != 5000 || len(c.Targets) > limit {
		return errors.New("invalid quality configuration")
	}
	if c.Revision != "0" {
		if _, err := protocol.QualityInteger(c.Revision); err != nil {
			return err
		}
	}
	if c.Revision == "0" && c.Enabled {
		return errors.New("active quality config lacks revision")
	}
	seen := map[string]bool{}
	for _, t := range c.Targets {
		if t.Slot != "telecom" && t.Slot != "unicom" && t.Slot != "mobile" {
			return errors.New("invalid quality slot")
		}
		if t.Family != "ipv4" && t.Family != "ipv6" {
			return errors.New("invalid quality family")
		}
		key := t.Slot + "/" + t.Family
		if !protocol.QualityProtocol(t.Protocol) {
			return errors.New("invalid quality protocol")
		}
		if c.Version == 2 {
			key += "/" + t.Protocol
			if t.Status == "active" && t.Source != "user" && (c.RegionID == "" || t.Region != c.RegionID) {
				return errors.New("quality region mismatch")
			}
		}
		if seen[key] {
			return errors.New("duplicate quality slot")
		}
		seen[key] = true
		if t.Status != "active" && t.Status != "paused" && t.Status != "unavailable" && !(c.Version == 2 && t.Status == "not_scheduled_legacy") {
			return errors.New("invalid quality status")
		}
		if t.Status != "active" {
			continue
		}
		if c.Version == 2 && c.Capabilities != nil {
			supported := false
			for _, l := range c.Capabilities.Lanes {
				if l.Family == t.Family && l.Protocol == t.Protocol {
					supported = l.Supported
				}
			}
			if !supported || c.Capabilities.ExcludedReason != "" {
				return errors.New("unsupported active quality lane")
			}
		}
		if !c.Enabled || (t.Family == "ipv6" && !c.IPv6) || t.Source != "catalog" && t.Source != "user" || (t.Source == "user") != userTargets {
			return errors.New("unauthorized quality target")
		}
		if t.Source == "user" {
			if err := qualityValidateUserTarget(c, t); err != nil {
				return err
			}
		}
		if len(t.ID) != 32 {
			return errors.New("invalid quality target ID")
		}
		if _, err := hex.DecodeString(t.ID); err != nil {
			return err
		}
		if _, err := protocol.QualityInteger(t.SlotRevision); err != nil {
			return err
		}
		slotRevision, _ := strconv.ParseInt(t.SlotRevision, 10, 64)
		configRevision, _ := strconv.ParseInt(c.Revision, 10, 64)
		if slotRevision > configRevision {
			return errors.New("quality slot revision exceeds config")
		}
		if t.Protocol == "tcp" {
			p := protocol.ProbeConfig{TCPConnect: &protocol.TCPConnectConfig{Host: t.Host, Port: t.Port}}
			if err := p.Validate(protocol.ProbeTypeTCPConnect); err != nil {
				return err
			}
		} else if t.Protocol == "icmp" {
			if t.Port != 0 {
				return errors.New("invalid ICMP port")
			}
			p := protocol.ProbeConfig{ICMPPing: &protocol.ICMPPingConfig{Target: t.Host, Count: 1}}
			if err := p.Validate(protocol.ProbeTypeICMPPing); err != nil {
				return err
			}
		} else {
			return errors.New("invalid quality protocol")
		}
	}
	return nil
}
func (q *qualityControl) install(c protocol.QualityConfig) bool {
	if qualityValidateConfigFor(c, q.userTargets) != nil {
		return false
	}
	if q.config != nil {
		old, _ := strconv.ParseInt(q.config.Revision, 10, 64)
		next, _ := strconv.ParseInt(c.Revision, 10, 64)
		if next < old {
			return false
		}
	}
	c.Targets = append([]protocol.QualityTarget(nil), c.Targets...)
	if q.config == nil || !reflect.DeepEqual(*q.config, c) {
		q.generation++
	}
	q.config = &c
	return true
}
func (r *Runner) observeQualityReport(optIn bool, response protocol.ReportResponse) {
	if r.quality == nil {
		return
	}
	if r.quality.excludedReason != "" {
		r.stopQuality(r.quality.excludedReason)
		return
	}
	if !response.Capabilities.NetworkQuality {
		r.stopQuality("capability_missing")
		return
	}
	q := r.quality
	q.mu.Lock()
	if optIn {
		if !q.allowed {
			q.generation++
		}
		q.allowed = true
		q.fetch = true
		if response.NetworkQuality != nil && q.install(*response.NetworkQuality) {
			q.fetch = false
		}
	}
	q.mu.Unlock()
	q.signal()
}
func (r *Runner) qualityHTTP(ctx context.Context, path string, body []byte, limit int, result any) error {
	return r.qualityHTTPWith(ctx, path, body, limit, result, false)
}

// qualityHTTPWith sends the targets header when the session negotiated user targets; without it the
// Server answers with the old stop format.
func (r *Runner) qualityHTTPWith(ctx context.Context, path string, body []byte, limit int, result any, userTargets bool) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(r.config.ServerURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.config.Token)
	if userTargets {
		req.Header.Set(protocol.QualityTargetsHeader, protocol.QualityTargetsUserV1)
	}
	resp, err := doAgentServerRequest(r.client, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	wire, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit+1)))
	if err != nil {
		return err
	}
	if len(wire) > limit {
		return errors.New("quality response budget exceeded")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(wire, &envelope)
		if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 404 || envelope.Error.Code == "agent_disabled" || envelope.Error.Code == "unsupported" || envelope.Error.Code == "unavailable" {
			return errQualityAuthorization
		}
		return errors.New("quality HTTP retryable response")
	}
	decoder := json.NewDecoder(bytes.NewReader(wire))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(result); err != nil {
		return err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("quality JSON trailing content")
	}
	return nil
}
func (r *Runner) fetchQualityConfig(ctx context.Context) (protocol.QualityConfig, error) {
	request := protocol.QualityConfigRequest{Epoch: strconv.FormatUint(r.epoch, 10), SessionID: r.sessionID}
	if r.qualityV2Supported.Load() {
		request.Version = 2
	}
	body, _ := json.Marshal(request)
	var c protocol.QualityConfig
	userTargets := r.qualityUserTargets.Load() && request.Version == 2
	err := r.qualityHTTPWith(ctx, "/api/v1/agent/network-quality/config", body, protocol.QualityConfigResponseLimit, &c, userTargets)
	if err == nil {
		if request.Version == 2 && c.Version != 2 {
			return c, errors.New("quality config negotiation mismatch")
		}
		err = qualityValidateConfigFor(c, userTargets)
	}
	return c, err
}

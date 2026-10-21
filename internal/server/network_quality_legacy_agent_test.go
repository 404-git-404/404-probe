package server

// The Agents of v1.0.2, v1.0.3 and v1.0.4 validate and install a quality configuration with the code
// below. It is frozen here (copied from those tags, internal/agent/network_quality_http.go) so that
// the Server's old-format response is checked against what those Agents really do, not against the
// current Agent, which stage C will change. v1.0.2 and v1.0.3 are identical and also accept the
// "manual" source; v1.0.4 accepts only "catalog" (legacyManualSource).

import (
	"404-probe/internal/protocol"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"testing"
)

type legacyQualityControl struct {
	config       *protocol.QualityConfig
	generation   uint64
	manualSource bool
}

func (q *legacyQualityControl) validate(c protocol.QualityConfig) error {
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
			if t.Status == "active" && (c.RegionID == "" || t.Region != c.RegionID) {
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
		if !c.Enabled || (t.Family == "ipv6" && !c.IPv6) || t.Source != "catalog" && !(q.manualSource && t.Source == "manual") {
			return errors.New("unauthorized quality target")
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
func (q *legacyQualityControl) install(c protocol.QualityConfig) bool {
	if q.validate(c) != nil {
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

// legacyActive is what the old Agent's scheduler would run for the installed configuration.
func (q *legacyQualityControl) active() int {
	n := 0
	if q.config != nil && q.config.Supported && q.config.Enabled {
		for _, t := range q.config.Targets {
			if t.Status == "active" && (t.Family == "ipv4" || q.config.IPv6) {
				n++
			}
		}
	}
	return n
}

func legacyInstalled(version int) protocol.QualityConfig {
	c := protocol.QualityConfig{Version: version, Revision: "1", Supported: true, Enabled: true, IPv6: true, IntervalMS: 30000, JitterPercent: 20, TimeoutMS: 5000, Semantics: protocol.QualityV2Semantics}
	if version == 1 {
		c.Semantics = ""
	}
	host := "93.184.216.34"
	target := protocol.QualityTarget{ID: "00112233445566778899aabbccddeeff", Slot: "telecom", Family: "ipv4", SlotRevision: "1", Source: "catalog", Protocol: "tcp", Host: host, Port: 443, EndpointVersion: "x", Status: "active"}
	if version == 2 {
		c.RegionID = "region-a"
		target.Region = "region-a"
		c.Capabilities = a1ServerCapabilities()
	}
	c.Targets = []protocol.QualityTarget{target}
	return c
}

// The complete old-format response (inline and pulled) is accepted by the validation and install of
// v1.0.2–v1.0.4 and leaves nothing running; the resource report is acknowledged (design 4.2, 10.3).
func TestR07OldFormatConfigIsAcceptedByOldAgents(t *testing.T) {
	for _, agent := range []struct {
		name   string
		manual bool
	}{{"v1.0.2/v1.0.3", true}, {"v1.0.4", false}} {
		for _, version := range []int{1, 2} {
			t.Run(agent.name+"/v"+strconv.Itoa(version), func(t *testing.T) {
				a, s, id, token := testApp(t)
				defer s.Close()
				defer a.Shutdown()
				var headers map[string]string
				var caps *protocol.QualityCapabilities
				if version == 2 {
					headers = map[string]string{protocol.QualityVersionHeader: "2", protocol.QualitySemanticsHeader: protocol.QualityV2Semantics}
					caps = a1ServerCapabilities()
				}
				report := func(seq uint64) protocol.Report {
					r := reportFor(id, seq)
					r.NetworkQuality = true
					r.NetworkQualityV2 = caps
					return r
				}
				w := postWithHeaders(t, a, token, "/api/v1/report", report(1), headers)
				if w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
				// Users have saved targets; the old Agent must not run them.
				grantUserTargets(t, s, id, userReport(id, 2), a.now()) // the device had been granted; the old Agent report below revokes
				configureUserTargets(t, s, id, a.now(), defaultUserHosts, 443)
				old := &legacyQualityControl{manualSource: agent.manual}
				installed := legacyInstalled(version)
				if !old.install(installed) || old.active() != 1 {
					t.Fatal("fixture: the old Agent should run its catalog target")
				}
				// 1: inline in the report response.
				w = postWithHeaders(t, a, token, "/api/v1/report", report(3), headers)
				response := decodeReportResponse(t, w)
				if w.Code != 200 || !response.Accepted || response.NetworkQuality == nil {
					t.Fatalf("report: %d %s", w.Code, w.Body.String())
				}
				checkStrictOldShape(t, mustJSON(t, response.NetworkQuality))
				if !old.install(*response.NetworkQuality) || old.active() != 0 {
					t.Fatalf("inline: install=%v active=%d: %s", false, old.active(), mustJSON(t, response.NetworkQuality))
				}
				// 2: pulled by the fallback request, from a fresh Agent state too.
				for _, fresh := range []bool{false, true} {
					state := &legacyQualityControl{manualSource: agent.manual}
					if !fresh && !state.install(installed) {
						t.Fatal("fixture")
					}
					req := protocol.QualityConfigRequest{Version: version / 2 * 2, Epoch: "1", SessionID: "session"}
					w = postWithHeaders(t, a, token, "/api/v1/agent/network-quality/config", req, headers)
					if w.Code != 200 {
						t.Fatal(w.Code, w.Body.String())
					}
					var pulled protocol.QualityConfig
					dec := json.NewDecoder(bytes.NewReader(w.Body.Bytes()))
					dec.DisallowUnknownFields()
					if err := dec.Decode(&pulled); err != nil {
						t.Fatal(err)
					}
					checkStrictOldShape(t, w.Body.Bytes())
					if !state.install(pulled) || state.active() != 0 {
						t.Fatalf("pull: active=%d: %s", state.active(), w.Body.String())
					}
				}
				// The resource data of that Agent is untouched by all of this.
				w = postWithHeaders(t, a, token, "/api/v1/report", report(4), headers)
				if w.Code != 200 || !decodeReportResponse(t, w).Accepted {
					t.Fatal("resource report", w.Code, w.Body.String())
				}
			})
		}
	}
}

// checkStrictOldShape enforces the field set an old Agent decodes and nothing a user entered.
func checkStrictOldShape(t *testing.T, wire []byte) {
	t.Helper()
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(wire, &keys); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"region_id": true, "state": true, "semantics": true, "version": true, "revision": true, "supported": true, "enabled": true, "ipv6": true, "interval_ms": true, "jitter_percent": true, "timeout_ms": true, "targets": true}
	for k := range keys {
		if !allowed[k] {
			t.Fatalf("field %q is not part of the old format: %s", k, wire)
		}
	}
	if string(keys["targets"]) != "[]" || string(keys["enabled"]) != "false" || string(keys["supported"]) != "true" {
		t.Fatalf("old format must be an empty disabled configuration: %s", wire)
	}
	if containsAny(string(wire), "example.com", "example.org", "example.net", "443", "user") {
		t.Fatalf("user target leaked: %s", wire)
	}
}

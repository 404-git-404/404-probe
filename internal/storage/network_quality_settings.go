package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"404-probe/internal/protocol"
)

// R07 network quality model: a card has three user slots, each with four sub-items (4 lanes), a
// master switch and one interval. Two revisions exist (design 2.4):
//
//	settings_revision  the Web optimistic lock; moves with any change, labels included;
//	revision           the measurement revision sent to the Agent and written into samples;
//	                   it does not move when only labels or the slot type change.

type qualityLane struct{ Slot, Family, Protocol string }

func (l qualityLane) key() string { return l.Slot + "/" + l.Family + "/" + l.Protocol }

var qualityLaneShapes = [][2]string{{"ipv4", "tcp"}, {"ipv4", "icmp"}, {"ipv6", "tcp"}, {"ipv6", "icmp"}}

func qualityLanes() []qualityLane {
	lanes := make([]qualityLane, 0, 12)
	for _, slot := range protocol.QualitySlotOrder() {
		for _, shape := range qualityLaneShapes {
			lanes = append(lanes, qualityLane{slot, shape[0], shape[1]})
		}
	}
	return lanes
}

type qualityConfigRow struct {
	exists           bool
	Revision         int64
	SettingsRevision int64
	Enabled          bool
	IntervalMS       int
	SlowThresholdMS  int
	Granted          bool
	Snapshot         *protocol.QualityCapabilities
}

// qualitySlotRow is a stored slot with the settings revision of its last change.
type qualitySlotRow struct {
	protocol.QualityV4Slot
	UpdatedRevision int64
}

func qualityDefaultSlot(slot string) protocol.QualityV4Slot {
	return protocol.QualityV4Slot{Slot: slot, Kind: "builtin", ISP: protocol.QualityISPForSlot(slot)}
}

func qualityLoadConfigTx(ctx context.Context, tx *sql.Tx, id string) (qualityConfigRow, error) {
	cfg := qualityConfigRow{IntervalMS: protocol.QualityMinIntervalMS, SlowThresholdMS: protocol.QualitySlowThresholdDefaultMS}
	var snapshot sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT revision,settings_revision,enabled,interval_ms,slow_threshold_ms,targets_granted,capability_snapshot FROM quality_config WHERE agent_id=?`, id).
		Scan(&cfg.Revision, &cfg.SettingsRevision, &cfg.Enabled, &cfg.IntervalMS, &cfg.SlowThresholdMS, &cfg.Granted, &snapshot)
	if errors.Is(err, sql.ErrNoRows) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	cfg.exists = true
	if snapshot.Valid {
		var caps protocol.QualityCapabilities
		if json.Unmarshal([]byte(snapshot.String), &caps) == nil && caps.Validate() == nil {
			cfg.Snapshot = &caps
		}
	}
	return cfg, nil
}

func qualityLoadSlotsTx(ctx context.Context, tx *sql.Tx, id string, fallbackRevision int64) ([3]qualitySlotRow, error) {
	var out [3]qualitySlotRow
	order := protocol.QualitySlotOrder()
	for i, slot := range order {
		out[i] = qualitySlotRow{QualityV4Slot: qualityDefaultSlot(slot), UpdatedRevision: fallbackRevision}
	}
	rows, err := tx.QueryContext(ctx, `SELECT slot,kind,region,isp,host,port,slot_enabled,ipv4_tcp,ipv4_icmp,ipv6_tcp,ipv6_icmp,updated_revision FROM quality_slot_settings WHERE agent_id=?`, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var r qualitySlotRow
		var port sql.NullInt64
		if err := rows.Scan(&r.Slot, &r.Kind, &r.Region, &r.ISP, &r.Host, &port, &r.SlotEnabled, &r.IPv4TCP, &r.IPv4ICMP, &r.IPv6TCP, &r.IPv6ICMP, &r.UpdatedRevision); err != nil {
			return out, err
		}
		r.Port = int(port.Int64)
		for i, slot := range order {
			if slot == r.Slot {
				out[i] = r
			}
		}
	}
	return out, rows.Err()
}

// qualityIdentity is the address identity of a target (design 2.3). Region, ISP and the slot type are
// not part of it; the port only matters for TCP.
func qualityIdentity(agent string, l qualityLane, host string, port int) string {
	p := 0
	if l.Protocol == "tcp" {
		p = port
	}
	digest := sha256.Sum256([]byte(strings.Join([]string{agent, l.Slot, l.Family, l.Protocol, host, strconv.Itoa(p)}, "\x00")))
	return hex.EncodeToString(digest[:])
}

func qualityTargetID(identity string, createdRevision int64) string {
	digest := sha256.Sum256([]byte(identity + "\x00" + strconv.FormatInt(createdRevision, 10)))
	return hex.EncodeToString(digest[:])[:32]
}

func qualityTargetPayload(l qualityLane, host string, port int) (string, error) {
	if l.Protocol == "icmp" {
		port = 0
	}
	digest := sha256.Sum256([]byte(host + ":" + strconv.Itoa(port) + "/" + l.Protocol + "/" + l.Family))
	payload, err := json.Marshal(protocol.QualityTarget{Slot: l.Slot, Family: l.Family, Protocol: l.Protocol, Source: "user", Host: host, Port: port, EndpointVersion: hex.EncodeToString(digest[:])[:16]})
	return string(payload), err
}

// qualityAuthInputs are the device-level facts that decide which lanes may run.
type qualityAuthInputs struct {
	granted  bool
	caps     *protocol.QualityCapabilities
	ipv6     string
	excluded string
}

func qualityAuthTx(ctx context.Context, tx *sql.Tx, id string, cfg qualityConfigRow) (qualityAuthInputs, error) {
	in := qualityAuthInputs{granted: cfg.Granted && cfg.Snapshot != nil, caps: cfg.Snapshot, ipv6: protocol.QualityIPv6Unknown}
	if cfg.Snapshot != nil {
		if cfg.Snapshot.IPv6Address != "" {
			in.ipv6 = cfg.Snapshot.IPv6Address
		}
		in.excluded = cfg.Snapshot.ExcludedReason
	}
	_, _, reason, err := qualityCapabilityTx(ctx, tx, id)
	if err != nil {
		return in, err
	}
	if reason != "" {
		in.excluded = reason
	}
	return in, nil
}

func qualityHostLiteral(host string) string {
	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.Is4() || addr.Is4In6() {
			return "ipv4"
		}
		return "ipv6"
	}
	return ""
}

// qualityLaneActive is the generation condition of design 2.1 without the per-request ones
// (storage pressure is applied when the configuration is read).
func qualityLaneActive(s protocol.QualityV4Slot, l qualityLane, enabled bool, in qualityAuthInputs) bool {
	if !enabled || !in.granted || in.caps == nil || in.excluded != "" || s.Host == "" || !s.SlotEnabled || !s.Sub(l.Family, l.Protocol) {
		return false
	}
	if !qualityLaneSupported(in.caps, l.Family, l.Protocol) {
		return false
	}
	if l.Family == "ipv6" && in.ipv6 == protocol.QualityIPv6None {
		return false
	}
	literal := qualityHostLiteral(s.Host)
	return !(literal == "ipv4" && l.Family == "ipv6") && !(literal == "ipv6" && l.Family == "ipv4")
}

type qualityProposal struct {
	Enabled    bool
	IntervalMS int
	// SlowThresholdMS is a setting, not a measurement: it changes the settings revision only. Zero means
	// "keep the stored value", so a proposal built without it can never reset the threshold.
	SlowThresholdMS int
	Slots           [3]protocol.QualityV4Slot
}

func qualityMeasurementSame(a, b protocol.QualityV4Slot) bool {
	return a.Host == b.Host && a.Port == b.Port && a.SlotEnabled == b.SlotEnabled && a.IPv4TCP == b.IPv4TCP && a.IPv4ICMP == b.IPv4ICMP && a.IPv6TCP == b.IPv6TCP && a.IPv6ICMP == b.IPv6ICMP
}

func qualityLabelSame(a, b protocol.QualityV4Slot) bool {
	return a.Kind == b.Kind && a.Region == b.Region && a.ISP == b.ISP
}

type qualityApplyResult struct {
	SettingsChanged, MeasurementChanged, AuthorizationChanged bool
	Revision, SettingsRevision                                int64
}

type qualityOpenInterval struct{ target, identity string }
type qualitySlotPointer struct {
	exists           bool
	revision         int64
	enabled          bool
	target, identity string
}

// qualityApplyTx is the single place that changes settings, targets, intervals and revisions
// (design 2.4). A Web save or master-switch request passes a proposal; an authorization
// recalculation passes none. Both run the same steps, so the same rules hold on every path.
func qualityApplyTx(ctx context.Context, tx *sql.Tx, id string, proposal *qualityProposal, web bool, now time.Time, pressure bool) (qualityApplyResult, error) {
	var result qualityApplyResult
	cfg, err := qualityLoadConfigTx(ctx, tx, id)
	if err != nil {
		return result, err
	}
	rows, err := qualityLoadSlotsTx(ctx, tx, id, cfg.SettingsRevision)
	if err != nil {
		return result, err
	}
	next := qualityProposal{Enabled: cfg.Enabled, IntervalMS: cfg.IntervalMS, SlowThresholdMS: cfg.SlowThresholdMS}
	for i := range rows {
		next.Slots[i] = rows[i].QualityV4Slot
	}
	var settingsChanged, measurementChanged bool
	slotChanged := [3]bool{}
	if proposal != nil {
		if !protocol.QualityIntervalValid(proposal.IntervalMS) {
			return result, ErrQualityInvalid
		}
		next = *proposal
		if next.SlowThresholdMS == 0 {
			next.SlowThresholdMS = cfg.SlowThresholdMS
		}
		if !protocol.QualitySlowThresholdValid(next.SlowThresholdMS) {
			return result, ErrQualityInvalid
		}
		if next.Enabled != cfg.Enabled || next.IntervalMS != cfg.IntervalMS {
			settingsChanged, measurementChanged = true, true
		}
		if next.SlowThresholdMS != cfg.SlowThresholdMS {
			settingsChanged = true // the threshold only changes how stored samples are classified
		}
		for i := range rows {
			if next.Slots[i].Slot != rows[i].Slot {
				return result, ErrQualityInvalid
			}
			if !qualityMeasurementSame(next.Slots[i], rows[i].QualityV4Slot) {
				measurementChanged, settingsChanged, slotChanged[i] = true, true, true
			}
			if !qualityLabelSame(next.Slots[i], rows[i].QualityV4Slot) {
				settingsChanged, slotChanged[i] = true, true
			}
		}
	}
	in, err := qualityAuthTx(ctx, tx, id, cfg)
	if err != nil {
		return result, err
	}

	// Desired and current authorization descriptors (slot, family, protocol, target identity).
	lanes := qualityLanes()
	slotIndex := map[string]int{}
	for i, slot := range protocol.QualitySlotOrder() {
		slotIndex[slot] = i
	}
	desired := map[string]string{}
	desiredIdentity := map[string]string{}
	for _, l := range lanes {
		s := next.Slots[slotIndex[l.Slot]]
		if s.Host != "" {
			desiredIdentity[l.key()] = qualityIdentity(id, l, s.Host, s.Port)
		}
		if qualityLaneActive(s, l, next.Enabled, in) {
			desired[l.key()] = desiredIdentity[l.key()]
		}
	}
	open := map[string]qualityOpenInterval{}
	intervalRows, err := tx.QueryContext(ctx, `SELECT i.slot,i.family,i.protocol,i.target_id,COALESCE(t.identity,'') FROM quality_intervals i LEFT JOIN quality_targets t ON t.id=i.target_id WHERE i.agent_id=? AND i.end_at IS NULL`, id)
	if err != nil {
		return result, err
	}
	for intervalRows.Next() {
		var l qualityLane
		var o qualityOpenInterval
		if err := intervalRows.Scan(&l.Slot, &l.Family, &l.Protocol, &o.target, &o.identity); err != nil {
			intervalRows.Close()
			return result, err
		}
		open[l.key()] = o
	}
	if err := intervalRows.Err(); err != nil {
		intervalRows.Close()
		return result, err
	}
	intervalRows.Close()
	authorizationChanged := len(desired) != len(open)
	for key, identity := range desired {
		if o, ok := open[key]; !ok || o.identity != identity {
			authorizationChanged = true
		}
	}
	current := map[string]qualitySlotPointer{}
	slotRows, err := tx.QueryContext(ctx, `SELECT s.slot,s.family,s.protocol,s.revision,s.enabled,COALESCE(s.target_id,''),COALESCE(t.identity,'') FROM quality_slots s LEFT JOIN quality_targets t ON t.id=s.target_id WHERE s.agent_id=?`, id)
	if err != nil {
		return result, err
	}
	for slotRows.Next() {
		var l qualityLane
		var p qualitySlotPointer
		p.exists = true
		if err := slotRows.Scan(&l.Slot, &l.Family, &l.Protocol, &p.revision, &p.enabled, &p.target, &p.identity); err != nil {
			slotRows.Close()
			return result, err
		}
		current[l.key()] = p
	}
	if err := slotRows.Err(); err != nil {
		slotRows.Close()
		return result, err
	}
	slotRows.Close()

	result.SettingsChanged, result.MeasurementChanged, result.AuthorizationChanged = settingsChanged, measurementChanged, authorizationChanged
	result.Revision, result.SettingsRevision = cfg.Revision, cfg.SettingsRevision
	if !settingsChanged && !measurementChanged && !authorizationChanged {
		return result, nil
	}
	newRevision := cfg.Revision
	if measurementChanged || authorizationChanged {
		if cfg.Revision == math.MaxInt64 {
			return result, ErrQualityInvalid
		}
		newRevision = cfg.Revision + 1
	}
	newSettings := cfg.SettingsRevision
	if web && settingsChanged {
		if cfg.SettingsRevision == math.MaxInt64 {
			return result, ErrQualityInvalid
		}
		newSettings = cfg.SettingsRevision + 1
	}

	for _, l := range lanes {
		s := next.Slots[slotIndex[l.Slot]]
		wantIdentity := desiredIdentity[l.key()]
		cur := current[l.key()]
		pointerChanged := wantIdentity != cur.identity || (wantIdentity == "" && cur.target != "")
		target := cur.target
		if pointerChanged {
			target = ""
			if wantIdentity != "" {
				target = qualityTargetID(wantIdentity, newRevision)
				if pressure {
					return result, ErrQualityStoragePressure
				}
				var local, global int
				if err := qualityCountTx(ctx, tx, id, "targets", &local, &global); err != nil {
					return result, err
				}
				if local >= qualityTargetDevice || global >= qualityTargetGlobal {
					return result, ErrQualityQuota
				}
				payload, err := qualityTargetPayload(l, s.Host, s.Port)
				if err != nil {
					return result, err
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO quality_targets(id,agent_id,fingerprint,payload,created_at,identity,created_revision) VALUES(?,?,?,?,?,?,?)`, target, id, target, payload, now.UnixMilli(), wantIdentity, newRevision); err != nil {
					return result, err
				}
			}
		}
		wantOpen, isDesired := desired[l.key()]
		old, isOpen := open[l.key()]
		closeOld := isOpen && (!isDesired || old.identity != wantOpen)
		openNew := isDesired && (!isOpen || closeOld)
		if closeOld {
			if _, err := tx.ExecContext(ctx, `UPDATE quality_intervals SET end_at=?,config_until=? WHERE agent_id=? AND slot=? AND family=? AND protocol=? AND end_at IS NULL`, now.UnixMilli(), cfg.Revision, id, l.Slot, l.Family, l.Protocol); err != nil {
				return result, err
			}
		}
		syntheticEnabled := s.SlotEnabled && s.Sub(l.Family, l.Protocol)
		slotRevisionChanged := pointerChanged || closeOld || openNew || (cur.exists && cur.enabled != syntheticEnabled) || (!cur.exists && syntheticEnabled)
		if slotRevisionChanged {
			var pointer any
			if target != "" {
				pointer = target
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO quality_slots(agent_id,slot,family,protocol,revision,target_id,enabled) VALUES(?,?,?,?,?,?,?) ON CONFLICT(agent_id,slot,family,protocol) DO UPDATE SET revision=excluded.revision,target_id=excluded.target_id,enabled=excluded.enabled`,
				id, l.Slot, l.Family, l.Protocol, newRevision, pointer, syntheticEnabled); err != nil {
				return result, err
			}
		}
		if openNew {
			if pressure {
				return result, ErrQualityStoragePressure
			}
			var local, global int
			if err := qualityCountTx(ctx, tx, id, "intervals", &local, &global); err != nil {
				return result, err
			}
			if local >= qualityIntervalDevice {
				return result, ErrQualityQuota
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO quality_intervals(agent_id,slot,family,protocol,revision,target_id,start_at,config_from,v1_authorized) VALUES(?,?,?,?,?,?,?,?,0)`, id, l.Slot, l.Family, l.Protocol, newRevision, target, now.UnixMilli(), newRevision); err != nil {
				return result, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE quality_targets SET first_active_at=COALESCE(first_active_at,?) WHERE id=?`, now.UnixMilli(), target); err != nil {
				return result, err
			}
		}
	}

	if proposal != nil {
		for i, slot := range next.Slots {
			updated := rows[i].UpdatedRevision
			if slotChanged[i] || !cfg.exists {
				updated = newSettings
			}
			var port any
			if slot.Host != "" {
				port = slot.Port
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO quality_slot_settings(agent_id,slot,kind,region,isp,host,port,slot_enabled,ipv4_tcp,ipv4_icmp,ipv6_tcp,ipv6_icmp,updated_revision) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
				ON CONFLICT(agent_id,slot) DO UPDATE SET kind=excluded.kind,region=excluded.region,isp=excluded.isp,host=excluded.host,port=excluded.port,slot_enabled=excluded.slot_enabled,ipv4_tcp=excluded.ipv4_tcp,ipv4_icmp=excluded.ipv4_icmp,ipv6_tcp=excluded.ipv6_tcp,ipv6_icmp=excluded.ipv6_icmp,updated_revision=excluded.updated_revision`,
				id, slot.Slot, slot.Kind, slot.Region, slot.ISP, slot.Host, port, slot.SlotEnabled, slot.IPv4TCP, slot.IPv4ICMP, slot.IPv6TCP, slot.IPv6ICMP, updated); err != nil {
				return result, err
			}
		}
	}
	defaultChoices, _ := json.Marshal(qualityDefaultChoices())
	if _, err := tx.ExecContext(ctx, `INSERT INTO quality_config(agent_id,revision,enabled,ipv6,choices,updated_at,settings_revision,interval_ms,slow_threshold_ms) VALUES(?,?,?,0,?,?,?,?,?)
		ON CONFLICT(agent_id) DO UPDATE SET revision=excluded.revision,settings_revision=excluded.settings_revision,enabled=excluded.enabled,interval_ms=excluded.interval_ms,slow_threshold_ms=excluded.slow_threshold_ms,updated_at=excluded.updated_at`,
		id, newRevision, next.Enabled, string(defaultChoices), now.UnixMilli(), newSettings, next.IntervalMS, next.SlowThresholdMS); err != nil {
		return result, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE quality_targets SET retired_at=CASE WHEN EXISTS(SELECT 1 FROM quality_slots s WHERE s.target_id=quality_targets.id) THEN NULL ELSE COALESCE(retired_at,?) END WHERE agent_id=?`, now.UnixMilli(), id); err != nil {
		return result, err
	}
	result.Revision, result.SettingsRevision = newRevision, newSettings
	return result, nil
}

package storage

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"404-probe/internal/protocol"
)

// qualityPlatformExcludedTx reports whether the Agent's platform is excluded from network quality.
func qualityPlatformExcludedTx(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	_, _, reason, err := qualityCapabilityTx(ctx, tx, id)
	return reason != "", err
}

func (s *Store) GetQualityConfigV4(ctx context.Context, id string, now time.Time) (protocol.QualityV4Config, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	budget := s.qualityBudget(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.QualityV4Config{}, err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=?)`, id).Scan(&exists); err != nil {
		return protocol.QualityV4Config{}, err
	}
	if !exists {
		return protocol.QualityV4Config{}, ErrAgentNotFound
	}
	return qualityV4ConfigTx(ctx, tx, id, now, budget)
}

// SaveQualityConfigV4 saves the settings only. It never changes the master switch.
func (s *Store) SaveQualityConfigV4(ctx context.Context, id string, request protocol.QualityV4SaveRequest, now time.Time) (protocol.QualityV4Config, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	expected, ok := protocol.ParseQualityExpectedRevision(request.ExpectedRevision)
	if !ok || !protocol.QualityIntervalValid(request.IntervalMS) || len(request.Slots) != 3 {
		return protocol.QualityV4Config{}, ErrQualityInvalid
	}
	proposal := qualityProposal{IntervalMS: request.IntervalMS}
	seen := map[string]bool{}
	for _, slot := range request.Slots {
		normalized, errs := protocol.NormalizeQualitySlot(slot)
		if len(errs) != 0 || seen[normalized.Slot] {
			return protocol.QualityV4Config{}, ErrQualityInvalid
		}
		seen[normalized.Slot] = true
		for i, name := range protocol.QualitySlotOrder() {
			if name == normalized.Slot {
				proposal.Slots[i] = normalized
			}
		}
	}
	pressure := s.qualityPressure(ctx)
	budget := s.qualityBudget(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.QualityV4Config{}, err
	}
	defer tx.Rollback()
	if err := requireActiveAgentTx(ctx, tx, id); err != nil {
		return protocol.QualityV4Config{}, err
	}
	if err := requireNoAgentRemovalTx(ctx, tx, id); err != nil {
		return protocol.QualityV4Config{}, err
	}
	cfg, err := qualityLoadConfigTx(ctx, tx, id)
	if err != nil {
		return protocol.QualityV4Config{}, err
	}
	if expected != cfg.SettingsRevision {
		return protocol.QualityV4Config{}, ErrQualityConflict
	}
	if excluded, err := qualityPlatformExcludedTx(ctx, tx, id); err != nil {
		return protocol.QualityV4Config{}, err
	} else if excluded {
		return protocol.QualityV4Config{}, ErrQualityUnsupported
	}
	proposal.Enabled = cfg.Enabled
	if _, err := qualityApplyTx(ctx, tx, id, &proposal, true, now, pressure); err != nil {
		return protocol.QualityV4Config{}, err
	}
	out, err := qualityV4ConfigTx(ctx, tx, id, now, budget)
	if err != nil {
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return protocol.QualityV4Config{}, err
	}
	return out, nil
}

// SetQualityEnabledV4 changes only the master switch. Switching on must be on a version the user
// has seen; switching off is always allowed (the safe direction); repeated requests are no-ops.
func (s *Store) SetQualityEnabledV4(ctx context.Context, id string, request protocol.QualityV4EnabledRequest, now time.Time) (protocol.QualityV4Config, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	expected, ok := protocol.ParseQualityExpectedRevision(request.ExpectedRevision)
	if !ok || request.Enabled == nil {
		return protocol.QualityV4Config{}, ErrQualityInvalid
	}
	want := *request.Enabled
	pressure := s.qualityPressure(ctx)
	budget := s.qualityBudget(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.QualityV4Config{}, err
	}
	defer tx.Rollback()
	if err := requireActiveAgentTx(ctx, tx, id); err != nil {
		return protocol.QualityV4Config{}, err
	}
	if err := requireNoAgentRemovalTx(ctx, tx, id); err != nil {
		return protocol.QualityV4Config{}, err
	}
	cfg, err := qualityLoadConfigTx(ctx, tx, id)
	if err != nil {
		return protocol.QualityV4Config{}, err
	}
	if want && expected != cfg.SettingsRevision {
		return protocol.QualityV4Config{}, ErrQualityConflict
	}
	if want != cfg.Enabled {
		if want {
			if excluded, err := qualityPlatformExcludedTx(ctx, tx, id); err != nil {
				return protocol.QualityV4Config{}, err
			} else if excluded {
				return protocol.QualityV4Config{}, ErrQualityUnsupported
			}
		}
		rows, err := qualityLoadSlotsTx(ctx, tx, id, cfg.SettingsRevision)
		if err != nil {
			return protocol.QualityV4Config{}, err
		}
		proposal := qualityProposal{Enabled: want, IntervalMS: cfg.IntervalMS}
		for i := range rows {
			proposal.Slots[i] = rows[i].QualityV4Slot
		}
		if _, err := qualityApplyTx(ctx, tx, id, &proposal, true, now, pressure); err != nil {
			return protocol.QualityV4Config{}, err
		}
	}
	out, err := qualityV4ConfigTx(ctx, tx, id, now, budget)
	if err != nil {
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return protocol.QualityV4Config{}, err
	}
	return out, nil
}

// QualityV4SlotHosts returns the stored host of each slot, used to resolve only changed names.
func (s *Store) QualityV4SlotHosts(ctx context.Context, id string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := qualityLoadSlotsTx(ctx, tx, id, 0)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, row := range rows {
		out[row.Slot] = row.Host
	}
	return out, nil
}

type qualitySupportInfo struct {
	state    string
	excluded string
}

// qualitySupportTx classifies the Agent's current session for the user interface.
func qualitySupportTx(ctx context.Context, tx *sql.Tx, id string, cfg qualityConfigRow) (qualitySupportInfo, error) {
	var supported, hasV2, userTargets bool
	var excluded string
	err := tx.QueryRowContext(ctx, `SELECT c.supported,c.v2_details IS NOT NULL,c.excluded_reason,c.user_targets FROM quality_capability c JOIN agent_state s ON c.agent_id=s.agent_id AND c.epoch=s.epoch AND c.session_id=s.session_id WHERE c.agent_id=?`, id).
		Scan(&supported, &hasV2, &excluded, &userTargets)
	if errors.Is(err, sql.ErrNoRows) {
		// A restarted Agent has no capability for the new session until its second report; a grant
		// from before the restart is still standing, so this is the negotiation, not a missing feature.
		if cfg.Granted {
			return qualitySupportInfo{state: protocol.QualitySupportNegotiating}, nil
		}
		return qualitySupportInfo{state: protocol.QualitySupportUnsupported}, nil
	}
	if err != nil {
		return qualitySupportInfo{}, err
	}
	switch {
	case excluded != "":
		return qualitySupportInfo{state: protocol.QualitySupportExcluded, excluded: excluded}, nil
	case !hasV2 && cfg.Granted:
		return qualitySupportInfo{state: protocol.QualitySupportNegotiating}, nil
	case !supported || !hasV2:
		return qualitySupportInfo{state: protocol.QualitySupportUnsupported}, nil
	case userTargets:
		return qualitySupportInfo{state: protocol.QualitySupportUserTargets}, nil
	case cfg.Granted:
		return qualitySupportInfo{state: protocol.QualitySupportNegotiating}, nil
	}
	return qualitySupportInfo{state: protocol.QualitySupportUpgradeRequired}, nil
}

// qualityEffectiveInterval never goes below what the user chose; the capacity plan only slows it down.
func qualityEffectiveInterval(user, scale int) int {
	return max(user, protocol.QualityMinIntervalMS*max(1, scale))
}

func qualityV4ConfigTx(ctx context.Context, tx *sql.Tx, id string, now time.Time, budget qualityBudget) (protocol.QualityV4Config, error) {
	out := protocol.QualityV4Config{Slots: []protocol.QualityV4Slot{}, Lanes: []protocol.QualityV4Lane{}}
	cfg, err := qualityLoadConfigTx(ctx, tx, id)
	if err != nil {
		return out, err
	}
	rows, err := qualityLoadSlotsTx(ctx, tx, id, cfg.SettingsRevision)
	if err != nil {
		return out, err
	}
	in, err := qualityAuthTx(ctx, tx, id, cfg)
	if err != nil {
		return out, err
	}
	support, err := qualitySupportTx(ctx, tx, id, cfg)
	if err != nil {
		return out, err
	}
	var revoked bool
	var disabled sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT revoked,disabled_at FROM agents WHERE id=?`, id).Scan(&revoked, &disabled); err != nil {
		return out, err
	}
	deviceInactive := revoked || disabled.Valid
	out.SettingsRevision = strconv.FormatInt(cfg.SettingsRevision, 10)
	out.ConfigRevision = strconv.FormatInt(cfg.Revision, 10)
	out.Enabled = cfg.Enabled
	out.IntervalMS = cfg.IntervalMS
	out.EffectiveIntervalMS = qualityEffectiveInterval(cfg.IntervalMS, budget.Scale)
	out.AgentSupport = support.state
	slotByName := map[string]protocol.QualityV4Slot{}
	anyHost := false
	for _, row := range rows {
		out.Slots = append(out.Slots, row.QualityV4Slot)
		slotByName[row.Slot] = row.QualityV4Slot
		anyHost = anyHost || row.Host != ""
	}

	type pointer struct {
		target   string
		revision int64
	}
	pointers := map[string]pointer{}
	pointerRows, err := tx.QueryContext(ctx, `SELECT slot,family,protocol,revision,COALESCE(target_id,'') FROM quality_slots WHERE agent_id=?`, id)
	if err != nil {
		return out, err
	}
	for pointerRows.Next() {
		var l qualityLane
		var p pointer
		if err := pointerRows.Scan(&l.Slot, &l.Family, &l.Protocol, &p.revision, &p.target); err != nil {
			pointerRows.Close()
			return out, err
		}
		pointers[l.key()] = p
	}
	if err := pointerRows.Err(); err != nil {
		pointerRows.Close()
		return out, err
	}
	pointerRows.Close()
	type openInterval struct {
		start  int64
		target string
	}
	opens := map[string]openInterval{}
	openRows, err := tx.QueryContext(ctx, `SELECT slot,family,protocol,target_id,start_at FROM quality_intervals WHERE agent_id=? AND end_at IS NULL`, id)
	if err != nil {
		return out, err
	}
	for openRows.Next() {
		var l qualityLane
		var o openInterval
		if err := openRows.Scan(&l.Slot, &l.Family, &l.Protocol, &o.target, &o.start); err != nil {
			openRows.Close()
			return out, err
		}
		opens[l.key()] = o
	}
	if err := openRows.Err(); err != nil {
		openRows.Close()
		return out, err
	}
	openRows.Close()

	// Card-level state.
	switch {
	case deviceInactive:
		out.State = protocol.QualityLaneDeviceInactive
	case support.state == protocol.QualitySupportUpgradeRequired:
		out.State = "agent_upgrade_required"
	case support.state == protocol.QualitySupportNegotiating:
		out.State = "negotiating"
	case support.state == protocol.QualitySupportUnsupported:
		out.State = "unsupported"
	case support.state == protocol.QualitySupportExcluded:
		out.State = "excluded"
	case budget.Pressure:
		out.State = "storage_pressure"
	case !cfg.Enabled:
		out.State = protocol.QualityLaneDisabled
	case !anyHost:
		out.State = protocol.QualityLaneUnset
	case budget.Degraded:
		out.State = "retention_degraded"
	case budget.Scale > 1 && out.EffectiveIntervalMS > cfg.IntervalMS:
		out.State = "capacity_throttled"
	default:
		out.State = "ready"
	}

	candidates := []qualityConditionCandidate{}
	for _, l := range qualityLanes() {
		slot := slotByName[l.Slot]
		p := pointers[l.key()]
		lane := protocol.QualityV4Lane{Slot: l.Slot, Family: l.Family, Protocol: l.Protocol, SlotRevision: strconv.FormatInt(p.revision, 10)}
		if p.target != "" {
			target := p.target
			lane.TargetID = &target
		}
		switch {
		case deviceInactive:
			lane.Status = protocol.QualityLaneDeviceInactive
		case support.state == protocol.QualitySupportExcluded:
			lane.Status = protocol.QualityLaneExcluded
		case support.state != protocol.QualitySupportUserTargets:
			lane.Status, lane.Condition = protocol.QualityLaneAgentUnavailable, support.state
		case !in.granted:
			// The session negotiated but the device-level authorization is not recorded (the
			// recalculation after the report failed): nothing runs yet, so nothing is "active".
			lane.Status, lane.Condition = protocol.QualityLaneAgentUnavailable, protocol.QualitySupportNegotiating
		case budget.Pressure:
			lane.Status = protocol.QualityLaneStoragePressure
		case !cfg.Enabled:
			lane.Status = protocol.QualityLaneDisabled
		case slot.Host == "":
			lane.Status = protocol.QualityLaneUnset
		case !slot.SlotEnabled || !slot.Sub(l.Family, l.Protocol):
			lane.Status = protocol.QualityLaneInactive
		case l.Family == "ipv6" && in.ipv6 == protocol.QualityIPv6None:
			lane.Status = protocol.QualityLaneNoIPv6
		case l.Protocol == "icmp" && !qualityLaneSupported(in.caps, l.Family, l.Protocol):
			lane.Status = protocol.QualityLaneICMPUnsupported
		default:
			lane.Status = protocol.QualityLaneActive
			if o, ok := opens[l.key()]; ok && lane.TargetID != nil && o.target == *lane.TargetID {
				candidates = append(candidates, qualityConditionCandidate{lane: l, index: len(out.Lanes), target: o.target, revision: p.revision, start: o.start, host: slot.Host})
			}
		}
		out.Lanes = append(out.Lanes, lane)
	}
	if len(candidates) > 0 {
		conditions, err := qualityAddressConditionsTx(ctx, tx, id, candidates, out.EffectiveIntervalMS, now)
		if err != nil {
			return out, err
		}
		for index, reason := range conditions {
			out.Lanes[index].Status = protocol.QualityLaneTargetAbnormal
			out.Lanes[index].Condition = reason
		}
	}
	return out, nil
}

type qualityConditionCandidate struct {
	lane     qualityLane
	index    int
	target   string
	revision int64
	start    int64
	host     string
}

// qualityAddressConditionsTx finds the lanes whose target is abnormal as seen from this Agent
// (design 7.3): the latest three samples of the current target, slot revision and session, all
// since the current interval opened, are address diagnostic events and the newest is still fresh.
// When two or more different host names qualify together, the local resolver is the likelier
// cause and none of them is marked.
func qualityAddressConditionsTx(ctx context.Context, tx *sql.Tx, id string, candidates []qualityConditionCandidate, intervalMS int, now time.Time) (map[int]string, error) {
	var epoch int64
	var session string
	if err := tx.QueryRowContext(ctx, `SELECT epoch,session_id FROM agent_state WHERE agent_id=?`, id).Scan(&epoch, &session); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	freshness := int64(intervalMS)*6/5 + 40000
	found := map[int]string{}
	for _, c := range candidates {
		rows, err := tx.QueryContext(ctx, `SELECT sample_time,details_json FROM quality_raw WHERE agent_id=? AND target_id=? AND slot_revision=? AND epoch=? AND session_id=? AND sample_time>=? ORDER BY sample_time DESC,seq DESC LIMIT 3`, id, c.target, c.revision, epoch, session, c.start)
		if err != nil {
			return nil, err
		}
		var newest int64
		reasons := []string{}
		for rows.Next() {
			var at int64
			var raw sql.NullString
			if err := rows.Scan(&at, &raw); err != nil {
				rows.Close()
				return nil, err
			}
			if len(reasons) == 0 {
				newest = at
			}
			reasons = append(reasons, qualityDiagnostic(raw))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if len(reasons) < 3 || now.UnixMilli()-newest > freshness {
			continue
		}
		ok := true
		for _, reason := range reasons {
			ok = ok && reason != ""
		}
		if ok {
			found[c.index] = reasons[0]
		}
	}
	names := map[string]bool{}
	for _, c := range candidates {
		if _, ok := found[c.index]; ok && qualityHostLiteral(c.host) == "" {
			names[c.host] = true
		}
	}
	if len(names) >= 2 {
		for _, c := range candidates {
			if qualityHostLiteral(c.host) == "" {
				delete(found, c.index)
			}
		}
	}
	return found, nil
}

// qualityDiagnostic is the address diagnostic event of a staged sample, or "" when the sample is
// anything else (success, timeout, refused, a plain DNS error, ...), which ends a run.
func qualityDiagnostic(raw sql.NullString) string {
	if !raw.Valid {
		return ""
	}
	details, err := protocol.DecodeQualityDetails([]byte(raw.String))
	if err != nil || details == nil {
		return ""
	}
	if details.ErrorCode == "unsafe_address" {
		return protocol.QualityConditionUnsafeAddress
	}
	switch details.DNSOutcome {
	case "not_found":
		return protocol.QualityConditionNotFound
	case "no_address":
		return protocol.QualityConditionNoAddress
	}
	return ""
}

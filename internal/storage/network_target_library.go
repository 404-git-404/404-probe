package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"404-probe/internal/protocol"
)

var (
	ErrNetworkTargetNotFound  = errors.New("network target not found")
	ErrNetworkTargetDuplicate = errors.New("duplicate network target")
	ErrNetworkTargetLimit     = errors.New("network target limit reached")
	ErrNetworkTargetConflict  = errors.New("network target revision conflict")
)

const networkTargetLimit = 64

type NetworkTargetFields struct {
	Region string `json:"region"`
	ISP    string `json:"isp"`
	Host   string `json:"host"`
	Port   int    `json:"port"`
}

type NetworkTargetAgent struct {
	ID   string `json:"agent_id"`
	Name string `json:"name"`
}

type NetworkTarget struct {
	ID string `json:"id"`
	NetworkTargetFields
	Revision int64                `json:"revision"`
	UsedBy   []NetworkTargetAgent `json:"used_by"`
}

type NetworkTargetUpdate struct {
	Target         NetworkTarget               `json:"target"`
	UpdatedAgents  int                         `json:"updated_agents"`
	FamilyAdjusted []NetworkTargetAgent        `json:"family_adjusted"`
	Skipped        []NetworkTargetSkippedAgent `json:"skipped"`
}

type NetworkTargetSkippedAgent struct {
	NetworkTargetAgent
	Reason string `json:"reason"`
}

type NetworkTargetDelete struct {
	Removed []NetworkTargetAgent        `json:"-"`
	Skipped []NetworkTargetSkippedAgent `json:"skipped"`
}

type NetworkTargetSkip struct {
	AgentID string `json:"agent_id"`
	Reason  string `json:"reason"`
}

type NetworkTargetApply struct {
	Added   []string            `json:"added"`
	Skipped []NetworkTargetSkip `json:"skipped"`
}

func (s *Store) ListNetworkTargets(ctx context.Context) ([]NetworkTarget, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.id,l.region,l.isp,l.host,l.port,l.revision,a.id,a.name
		FROM network_target_library l
		LEFT JOIN (SELECT DISTINCT library_id,agent_id FROM quality_slot_settings WHERE library_id IS NOT NULL) u
			ON u.library_id=l.id
		LEFT JOIN agents a ON a.id=u.agent_id
		WHERE l.retired=0
		ORDER BY l.region,l.isp,l.host,l.port,l.id,a.name,a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := []NetworkTarget{}
	for rows.Next() {
		var target NetworkTarget
		var agentID, name sql.NullString
		if err := rows.Scan(&target.ID, &target.Region, &target.ISP, &target.Host, &target.Port, &target.Revision, &agentID, &name); err != nil {
			return nil, err
		}
		if len(targets) == 0 || targets[len(targets)-1].ID != target.ID {
			target.UsedBy = []NetworkTargetAgent{}
			targets = append(targets, target)
		}
		if agentID.Valid {
			last := &targets[len(targets)-1]
			last.UsedBy = append(last.UsedBy, NetworkTargetAgent{ID: agentID.String, Name: name.String})
		}
	}
	return targets, rows.Err()
}

func readNetworkTargetTx(ctx context.Context, tx *sql.Tx, id string) (NetworkTarget, error) {
	target := NetworkTarget{ID: id, UsedBy: []NetworkTargetAgent{}}
	err := tx.QueryRowContext(ctx, `SELECT region,isp,host,port,revision FROM network_target_library WHERE id=? AND retired=0`, id).
		Scan(&target.Region, &target.ISP, &target.Host, &target.Port, &target.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNetworkTargetNotFound
	}
	return target, err
}

func networkTargetUsersTx(ctx context.Context, tx *sql.Tx, id string) ([]NetworkTargetAgent, error) {
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.name FROM agents a
		JOIN (SELECT DISTINCT agent_id FROM quality_slot_settings WHERE library_id=?) u ON u.agent_id=a.id
		ORDER BY a.name,a.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []NetworkTargetAgent{}
	for rows.Next() {
		var agent NetworkTargetAgent
		if err := rows.Scan(&agent.ID, &agent.Name); err != nil {
			return nil, err
		}
		users = append(users, agent)
	}
	return users, rows.Err()
}

func checkNetworkTargetDuplicateTx(ctx context.Context, tx *sql.Tx, id string, fields NetworkTargetFields) error {
	var exists bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM network_target_library
		WHERE retired=0 AND region=? AND isp=? AND host=? AND port=? AND id<>?)`,
		fields.Region, fields.ISP, fields.Host, fields.Port, id).Scan(&exists)
	if err == nil && exists {
		return ErrNetworkTargetDuplicate
	}
	return err
}

func (s *Store) CreateNetworkTarget(ctx context.Context, fields NetworkTargetFields) (NetworkTarget, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return NetworkTarget{}, err
	}
	defer tx.Rollback()
	if err := checkNetworkTargetDuplicateTx(ctx, tx, "", fields); err != nil {
		return NetworkTarget{}, err
	}
	var count int
	if _, err := tx.ExecContext(ctx, `DELETE FROM network_target_library WHERE retired=1 AND NOT EXISTS
		(SELECT 1 FROM quality_slot_settings WHERE library_id=network_target_library.id)`); err != nil {
		return NetworkTarget{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM network_target_library WHERE retired=0`).Scan(&count); err != nil {
		return NetworkTarget{}, err
	}
	if count >= networkTargetLimit {
		return NetworkTarget{}, ErrNetworkTargetLimit
	}
	var key [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		return NetworkTarget{}, err
	}
	target := NetworkTarget{ID: hex.EncodeToString(key[:]), NetworkTargetFields: fields, Revision: 1, UsedBy: []NetworkTargetAgent{}}
	if _, err := tx.ExecContext(ctx, `INSERT INTO network_target_library(id,region,isp,host,port,revision) VALUES(?,?,?,?,?,1)`,
		target.ID, target.Region, target.ISP, target.Host, target.Port); err != nil {
		return NetworkTarget{}, err
	}
	return target, tx.Commit()
}

func networkTargetQualityProposal(ctx context.Context, tx *sql.Tx, agentID string) (qualityProposal, error) {
	cfg, err := qualityLoadConfigTx(ctx, tx, agentID)
	if err != nil {
		return qualityProposal{}, err
	}
	rows, err := qualityLoadSlotsTx(ctx, tx, agentID, cfg)
	if err != nil {
		return qualityProposal{}, err
	}
	proposal := qualityProposal{Enabled: cfg.Enabled, IntervalMS: cfg.IntervalMS, ObjectsConfigured: cfg.ObjectsConfigured}
	for _, row := range rows {
		proposal.Slots = append(proposal.Slots, row.QualityV4Slot)
	}
	return proposal, nil
}

func networkTargetAgentSkip(ctx context.Context, tx *sql.Tx, id string) (string, error) {
	if err := requireActiveAgentTx(ctx, tx, id); err != nil {
		switch {
		case errors.Is(err, ErrAgentRevoked), errors.Is(err, ErrAgentDisabled):
			return "revoked", nil
		case errors.Is(err, ErrUnauthorized):
			return "not_found", nil
		default:
			return "", err
		}
	}
	if err := requireNoAgentRemovalTx(ctx, tx, id); err != nil {
		if errors.Is(err, ErrAgentRemovalPending) {
			return "removing", nil
		}
		return "", err
	}
	excluded, err := qualityPlatformExcludedTx(ctx, tx, id)
	if excluded && err == nil {
		return "unsupported", nil
	}
	return "", err
}

func setNetworkTargetFields(slot *protocol.QualityV4Slot, target NetworkTarget) {
	slot.Kind, slot.LibraryID = "custom", target.ID
	slot.Region, slot.ISP, slot.Host, slot.Port = target.Region, target.ISP, target.Host, target.Port
}

func adjustNetworkTargetFamily(slot *protocol.QualityV4Slot) bool {
	switch qualityHostLiteral(slot.Host) {
	case "ipv4":
		adjusted := slot.IPv6TCP || slot.IPv6ICMP
		slot.IPv6TCP, slot.IPv6ICMP = false, false
		return adjusted
	case "ipv6":
		adjusted := slot.IPv4TCP || slot.IPv4ICMP
		slot.IPv4TCP, slot.IPv4ICMP = false, false
		return adjusted
	default:
		return false
	}
}

func (s *Store) UpdateNetworkTarget(ctx context.Context, id string, expected int64, fields NetworkTargetFields, now time.Time) (NetworkTargetUpdate, error) {
	var out NetworkTargetUpdate
	pressure := s.qualityPressure(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	target, err := readNetworkTargetTx(ctx, tx, id)
	if err != nil {
		return out, err
	}
	if target.Revision != expected {
		return out, ErrNetworkTargetConflict
	}
	target.UsedBy, err = networkTargetUsersTx(ctx, tx, id)
	if err != nil {
		return out, err
	}
	out = NetworkTargetUpdate{Target: target, FamilyAdjusted: []NetworkTargetAgent{}, Skipped: []NetworkTargetSkippedAgent{}}
	if target.NetworkTargetFields == fields {
		return out, nil
	}
	if err := checkNetworkTargetDuplicateTx(ctx, tx, id, fields); err != nil {
		return out, err
	}
	target.NetworkTargetFields = fields
	target.Revision++
	if _, err := tx.ExecContext(ctx, `UPDATE network_target_library SET region=?,isp=?,host=?,port=?,revision=? WHERE id=?`,
		target.Region, target.ISP, target.Host, target.Port, target.Revision, target.ID); err != nil {
		return out, err
	}
	for _, agent := range target.UsedBy {
		reason, err := networkTargetAgentSkip(ctx, tx, agent.ID)
		if err != nil {
			return out, err
		}
		if reason != "" {
			out.Skipped = append(out.Skipped, NetworkTargetSkippedAgent{agent, reason})
			continue
		}
		proposal, err := networkTargetQualityProposal(ctx, tx, agent.ID)
		if err != nil {
			return out, err
		}
		adjusted := false
		for i := range proposal.Slots {
			slot := &proposal.Slots[i]
			if slot.LibraryID == id {
				hostChanged := slot.Host != target.Host
				setNetworkTargetFields(slot, target)
				if hostChanged && adjustNetworkTargetFamily(slot) {
					adjusted = true
				}
			}
		}
		if _, err := qualityApplyTx(ctx, tx, agent.ID, &proposal, true, now, pressure); err != nil {
			return out, err
		}
		if adjusted {
			out.FamilyAdjusted = append(out.FamilyAdjusted, agent)
		}
	}
	out.Target, out.UpdatedAgents = target, len(target.UsedBy)-len(out.Skipped)
	return out, tx.Commit()
}

func (s *Store) DeleteNetworkTarget(ctx context.Context, id string, expected int64, now time.Time) (NetworkTargetDelete, error) {
	out := NetworkTargetDelete{Skipped: []NetworkTargetSkippedAgent{}}
	pressure := s.qualityPressure(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	target, err := readNetworkTargetTx(ctx, tx, id)
	if err != nil {
		return out, err
	}
	if target.Revision != expected {
		return out, ErrNetworkTargetConflict
	}
	users, err := networkTargetUsersTx(ctx, tx, id)
	if err != nil {
		return out, err
	}
	for _, agent := range users {
		reason, err := networkTargetAgentSkip(ctx, tx, agent.ID)
		if err != nil {
			return out, err
		}
		if reason != "" {
			out.Skipped = append(out.Skipped, NetworkTargetSkippedAgent{agent, reason})
			continue
		}
		proposal, err := networkTargetQualityProposal(ctx, tx, agent.ID)
		if err != nil {
			return out, err
		}
		keep := proposal.Slots[:0]
		for _, slot := range proposal.Slots {
			if slot.LibraryID != id && (proposal.ObjectsConfigured || slot.Host != "") {
				keep = append(keep, slot)
			}
		}
		proposal.Slots = keep
		proposal.ObjectsConfigured = true
		if _, err := qualityApplyTx(ctx, tx, agent.ID, &proposal, true, now, pressure); err != nil {
			return out, err
		}
		out.Removed = append(out.Removed, agent)
	}
	query := `DELETE FROM network_target_library WHERE id=?`
	if len(out.Skipped) != 0 {
		query = `UPDATE network_target_library SET retired=1 WHERE id=?`
	}
	if _, err := tx.ExecContext(ctx, query, id); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func (s *Store) ApplyNetworkTarget(ctx context.Context, id string, agents []string, now time.Time) (NetworkTargetApply, error) {
	out := NetworkTargetApply{Added: []string{}, Skipped: []NetworkTargetSkip{}}
	pressure := s.qualityPressure(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	target, err := readNetworkTargetTx(ctx, tx, id)
	if err != nil {
		return out, err
	}
	seen := make(map[string]bool, len(agents))
	for _, agent := range agents {
		if seen[agent] {
			continue
		}
		seen[agent] = true
		reason, err := applyNetworkTargetTx(ctx, tx, target, agent, now, pressure)
		if err != nil {
			return out, err
		}
		if reason == "" {
			out.Added = append(out.Added, agent)
		} else {
			out.Skipped = append(out.Skipped, NetworkTargetSkip{AgentID: agent, Reason: reason})
		}
	}
	return out, tx.Commit()
}

func applyNetworkTargetTx(ctx context.Context, tx *sql.Tx, target NetworkTarget, agent string, now time.Time, pressure bool) (string, error) {
	if reason, err := networkTargetAgentSkip(ctx, tx, agent); reason != "" || err != nil {
		return reason, err
	}
	proposal, err := networkTargetQualityProposal(ctx, tx, agent)
	if err != nil {
		return "", err
	}
	var shown int
	keep := proposal.Slots[:0]
	for _, slot := range proposal.Slots {
		if slot.LibraryID == target.ID {
			return "already", nil
		}
		// The implicit three-slot layout can have empty placeholders.
		if !proposal.ObjectsConfigured && slot.Host == "" {
			continue
		}
		keep = append(keep, slot)
		if slot.ShowOnCard {
			shown++
		}
	}
	if len(keep) >= protocol.QualityMaxObjects {
		return "full", nil
	}
	key, err := newQualityObjectID()
	if err != nil {
		return "", err
	}
	slot := protocol.QualityV4Slot{Slot: key, SlotEnabled: true, ShowOnCard: shown < protocol.QualityMaxCardObjects,
		IPv4TCP: true, IPv4ICMP: true, IPv6TCP: true, IPv6ICMP: true}
	setNetworkTargetFields(&slot, target)
	adjustNetworkTargetFamily(&slot)
	proposal.Slots = append(keep, slot)
	proposal.ObjectsConfigured = true
	_, err = qualityApplyTx(ctx, tx, agent, &proposal, true, now, pressure)
	return "", err
}

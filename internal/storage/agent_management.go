package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"404-probe/internal/protocol"
)

// AgentManagementCapabilities is the current-session capability snapshot used
// to gate destructive and explicit management operations.
type AgentManagementCapabilities struct {
	RemoteRemoval     bool `json:"remote_removal"`
	CountryCodeLookup bool `json:"country_code_lookup"`
}

func saveAgentManagementCapabilitiesTx(ctx context.Context, tx *sql.Tx, agentID string, report protocol.Report, received time.Time) error {
	var remoteRemoval, countryCodeLookup int
	if report.Management != nil {
		remoteRemoval = boolInt(report.Management.RemoteRemoval)
		countryCodeLookup = boolInt(report.Management.CountryCodeLookup)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO agent_management_capabilities(
		agent_id,remote_removal,country_code_lookup,epoch,session_id,updated_at
	) VALUES(?,?,?,?,?,?)
	ON CONFLICT(agent_id) DO UPDATE SET
		remote_removal=excluded.remote_removal,
		country_code_lookup=excluded.country_code_lookup,
		epoch=excluded.epoch,session_id=excluded.session_id,updated_at=excluded.updated_at`,
		agentID, remoteRemoval, countryCodeLookup, int64(report.Epoch), report.SessionID, received.UnixMilli())
	return err
}

// GetAgentManagementCapabilities returns only a capability row belonging to
// the currently accepted Agent session. Missing or stale rows fail closed.
func (s *Store) GetAgentManagementCapabilities(ctx context.Context, agentID string) (AgentManagementCapabilities, error) {
	var remoteRemoval, countryCodeLookup int
	err := s.db.QueryRowContext(ctx, `SELECT c.remote_removal,c.country_code_lookup
		FROM agent_management_capabilities c
		JOIN agent_state s ON s.agent_id=c.agent_id AND s.epoch=c.epoch AND s.session_id=c.session_id
		WHERE c.agent_id=?`, agentID).Scan(&remoteRemoval, &countryCodeLookup)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentManagementCapabilities{}, nil
	}
	if err != nil {
		return AgentManagementCapabilities{}, err
	}
	return AgentManagementCapabilities{RemoteRemoval: remoteRemoval == 1, CountryCodeLookup: countryCodeLookup == 1}, nil
}

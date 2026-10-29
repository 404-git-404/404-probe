package storage

import (
	"context"
	"database/sql"
	"time"
)

// Card-only fields are read in four batches in the dashboard's existing read
// transaction. No per-device detail reads or management/security history loads.
func readDashboardCards(ctx context.Context, tx *sql.Tx, now time.Time) (map[string]DashboardRecord, error) {
	out := map[string]DashboardRecord{}
	rows, err := tx.QueryContext(ctx, `SELECT agent_id,youtube_status,youtube_region,sent_to_china,youtube_error,
 search_status,search_error,signin_status,signin_error,gemini_status,gemini_region,gemini_error,
 checked_at,unknown_streak,next_due_at,youtube_premium_notice FROM agent_google_status`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		v, _, scanErr := scanDashboardGoogle(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		r := out[v.AgentID]
		r.Google = &v
		out[v.AgentID] = r
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT a.id,COALESCE(c.supported,0),COALESCE(j.pending,0),COALESCE(j.expired,0)
 FROM agents a LEFT JOIN agent_google_status_capabilities c ON c.agent_id=a.id
 LEFT JOIN (SELECT agent_id,MAX(CASE WHEN status IN ('queued','leased') AND expires_at>? THEN 1 ELSE 0 END) pending,
 MAX(CASE WHEN status IN ('queued','leased','expired') AND expires_at<=? THEN expires_at ELSE 0 END) expired
 FROM probe_jobs WHERE probe_type='google_status' GROUP BY agent_id) j ON j.agent_id=a.id`, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var supported, pending int
		var expired int64
		if err = rows.Scan(&id, &supported, &pending, &expired); err != nil {
			rows.Close()
			return nil, err
		}
		r := out[id]
		r.GoogleSupported = supported == 1
		r.GooglePending = pending == 1
		prior := GoogleStatusSnapshot{}
		if r.Google != nil {
			prior = *r.Google
		}
		if expired > prior.CheckedAt {
			v := expiredYouTubeSnapshot(id, prior, expired)
			r.Google = &v
		}
		out[id] = r
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT agent_id,available,status,payload_json,checked_at,updated_at FROM agent_outbound_snapshots`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		v, _, scanErr := scanDashboardOutbound(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		r := out[v.AgentID]
		r.Outbound = &v
		out[v.AgentID] = r
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT s.agent_id,s.rx_total,s.tx_total,COALESCE(b.rx_total,0),COALESCE(b.tx_total,0),b.started_at
 FROM agent_state s LEFT JOIN agent_traffic_baselines b ON b.agent_id=s.agent_id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var rx, tx, brx, btx int64
		var started sql.NullInt64
		if err = rows.Scan(&id, &rx, &tx, &brx, &btx, &started); err != nil {
			rows.Close()
			return nil, err
		}
		v := TrafficTotals{RXTotal: subtractStoredTotal(rx, brx), TXTotal: subtractStoredTotal(tx, btx)}
		if started.Valid {
			v.StartedAt = &started.Int64
		}
		r := out[id]
		r.TrafficTotals = &v
		out[id] = r
	}
	err = rows.Err()
	rows.Close()
	return out, err
}

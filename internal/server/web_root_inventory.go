package server

import (
	"errors"
	"net/http"
	"time"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

const securityBaselineFreshness = 15 * time.Minute

type securityChange struct {
	Kind   string `json:"kind"`
	Change string `json:"change"`
	Item   any    `json:"item"`
}

type securityBaselineView struct {
	Status         string           `json:"status"`
	Reason         string           `json:"reason,omitempty"`
	BaselineAt     *int64           `json:"baseline_at"`
	SnapshotAt     *int64           `json:"snapshot_at"`
	SnapshotDigest string           `json:"snapshot_digest"`
	ChangeCount    int              `json:"change_count"`
	Changes        []securityChange `json:"changes"`
}

func summarizeSecurityBaseline(b storage.SecurityBaseline, now time.Time, full bool) securityBaselineView {
	out := securityBaselineView{Status: b.Status, Reason: b.Reason, SnapshotDigest: b.SnapshotDigest, Changes: []securityChange{}}
	if b.BaselineAt != 0 {
		out.BaselineAt = &b.BaselineAt
	}
	if b.SnapshotAt != 0 {
		out.SnapshotAt = &b.SnapshotAt
	}
	switch {
	case out.Status == "":
		out.Status, out.Reason = protocol.InventoryUnavailable, "not_collected"
	case out.Status == protocol.InventoryUnavailable || out.Status == "pending":
	case b.Baseline == nil || b.Current == nil:
		out.Status = "pending"
	default:
		out.Changes = securityChanges(b.Baseline, b.Current)
		out.ChangeCount = len(out.Changes)
		if now.Sub(time.UnixMilli(b.SnapshotAt)) > securityBaselineFreshness {
			out.Status = "stale"
		} else if out.ChangeCount > 0 {
			out.Status = "changed"
		}
	}
	if !full && len(out.Changes) > 20 {
		out.Changes = out.Changes[:20]
	}
	return out
}

func securityChanges(baseline, current *protocol.SecuritySnapshot) []securityChange {
	changes := []securityChange{}
	sshIdentity := func(item protocol.SecuritySSHKey) [2]string { return [2]string{item.User, item.Fingerprint} }
	type portKey struct {
		Protocol string
		Port     uint16
	}
	portIdentity := func(item protocol.SecurityPort) portKey { return portKey{item.Protocol, item.Port} }
	appendSecurityChanges(&changes, "ssh_key", baseline.SSHKeys, current.SSHKeys, sshIdentity, true, nil)
	appendSecurityChanges(&changes, "port", baseline.Ports, current.Ports, portIdentity, false, nil)
	appendSecurityChanges(&changes, "uid0", baseline.UID0, current.UID0, func(item string) string { return item }, false, nil)
	appendSecurityChanges(&changes, "sudo_member", baseline.SudoMembers, current.SudoMembers,
		func(item protocol.SecuritySudoMember) protocol.SecuritySudoMember { return item }, false, nil)
	appendSecurityChanges(&changes, "sudoers_file", baseline.SudoersFiles, current.SudoersFiles,
		func(item protocol.SecuritySudoersFile) string { return item.Path }, true,
		func(a, b protocol.SecuritySudoersFile) bool { return a.SHA256 != b.SHA256 })
	return changes
}

func appendSecurityChanges[T any, K comparable](changes *[]securityChange, kind string, baseline, current []T,
	identity func(T) K, reportRemoved bool, modified func(T, T) bool) {
	before := make(map[K]T, len(baseline))
	for _, item := range baseline {
		before[identity(item)] = item
	}
	after := make(map[K]bool, len(current))
	for _, item := range current {
		key := identity(item)
		old, exists := before[key]
		if !after[key] {
			if !exists {
				*changes = append(*changes, securityChange{kind, "added", item})
			} else if modified != nil && modified(old, item) {
				*changes = append(*changes, securityChange{kind, "modified", item})
			}
		}
		after[key] = true
	}
	if reportRemoved {
		for _, item := range baseline {
			if key := identity(item); !after[key] {
				*changes = append(*changes, securityChange{kind, "removed", item})
				after[key] = true
			}
		}
	}
}

func rootInventoryAgentID(w http.ResponseWriter, r *http.Request, route string) (string, bool) {
	id := r.PathValue("agent_id")
	if !validWebAgentID(id) || r.URL.EscapedPath() != webAgentPathPrefix+id+"/"+route || r.URL.RawQuery != "" || r.URL.ForceQuery {
		writeJobError(w, 400, "invalid_request", "invalid root inventory request")
		return "", false
	}
	return id, true
}

func (a *App) handleSecurityBaseline(w http.ResponseWriter, r *http.Request) {
	id, ok := rootInventoryAgentID(w, r, "security-baseline")
	if !ok {
		return
	}
	b, err := a.store.SecurityBaseline(r.Context(), id)
	if err != nil {
		a.rootInventoryHTTPError(w, id, err)
		return
	}
	writeJSON(w, 200, struct {
		securityBaselineView
		Baseline *protocol.SecuritySnapshot `json:"baseline"`
		Current  *protocol.SecuritySnapshot `json:"current"`
	}{summarizeSecurityBaseline(b, a.now(), true), b.Baseline, b.Current})
}

func (a *App) handleSecurityBaselineAcknowledgement(w http.ResponseWriter, r *http.Request) {
	id, ok := rootInventoryAgentID(w, r, "security-baseline-acknowledgement")
	if !ok {
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, 415, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	raw, err := readBoundedBody(w, r, 4<<10)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	var input struct {
		SnapshotDigest string `json:"snapshot_digest"`
	}
	if err := decodeStrictJSON(raw, &input); err != nil || !protocol.ValidInventoryDigest(input.SnapshotDigest) {
		writeJobError(w, 400, "invalid_request", "invalid snapshot digest")
		return
	}
	if err := a.store.AcknowledgeSecurityBaseline(r.Context(), id, input.SnapshotDigest, a.now()); err != nil {
		a.rootInventoryHTTPError(w, id, err)
		return
	}
	a.publishDashboardChange(id, "security-baseline", false)
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleSingboxStructure(w http.ResponseWriter, r *http.Request) {
	id, ok := rootInventoryAgentID(w, r, "singbox-structure")
	if !ok {
		return
	}
	view, err := a.store.SingboxStructure(r.Context(), id)
	if err != nil {
		a.rootInventoryHTTPError(w, id, err)
		return
	}
	writeJSON(w, 200, struct {
		protocol.SingboxStructureReport
		Structure *protocol.SingboxStructure `json:"structure"`
	}{view, view.Structure})
}

func (a *App) rootInventoryHTTPError(w http.ResponseWriter, id string, err error) {
	switch {
	case errors.Is(err, storage.ErrAgentNotFound):
		writeJobError(w, 404, "agent_not_found", "agent not found")
	case errors.Is(err, storage.ErrSecuritySnapshotChanged):
		writeJobError(w, 409, "snapshot_changed", "security baseline snapshot changed")
	default:
		a.logger.Error("read or update root inventory", "agent_id", id, "error", err)
		writeJobError(w, 500, "internal_error", "could not read or update root inventory")
	}
}

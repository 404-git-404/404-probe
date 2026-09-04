package protocol

import (
	"errors"
	"strings"
)

const ControlProtocolVersion = 1

// ControlClaimRequest is intentionally narrower than ClaimRequest: this lane
// can only lease the fixed sing-box selector-switch operation.
type ControlClaimRequest struct {
	ProtocolVersion int    `json:"protocol_version"`
	AgentEpoch      uint64 `json:"agent_epoch"`
	SessionID       string `json:"session_id"`
}

func (r ControlClaimRequest) Validate() error {
	if r.ProtocolVersion != ControlProtocolVersion {
		return errors.New("unsupported control protocol version")
	}
	if r.AgentEpoch == 0 || !validID(r.SessionID, 128) || strings.TrimSpace(r.SessionID) != r.SessionID {
		return errors.New("agent_epoch and session_id are required")
	}
	return nil
}

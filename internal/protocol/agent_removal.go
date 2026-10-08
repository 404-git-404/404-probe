package protocol

import (
	"errors"
	"math"
)

const (
	AgentRemovalProtocolVersion    = 1
	AgentRemovalReceiptKind        = "agent_uninstalled"
	AgentRemovalStatusDelivered    = "delivered"
	AgentRemovalStatusUninstalling = "uninstalling"
)

type AgentRemovalClaimRequest struct {
	ProtocolVersion int    `json:"protocol_version"`
	AgentEpoch      uint64 `json:"agent_epoch"`
	SessionID       string `json:"session_id"`
}

func (r AgentRemovalClaimRequest) Validate() error {
	if r.ProtocolVersion != AgentRemovalProtocolVersion || r.AgentEpoch == 0 || r.AgentEpoch > math.MaxInt64 || !validIdentifier(r.SessionID, 128) {
		return errors.New("invalid Agent removal claim request")
	}
	return nil
}

type AgentRemovalDelivery struct {
	ProtocolVersion int    `json:"protocol_version"`
	OperationID     string `json:"operation_id"`
	Status          string `json:"status"`
	ReceiptToken    string `json:"receipt_token"`
}

func (d AgentRemovalDelivery) Validate() error {
	if d.ProtocolVersion != AgentRemovalProtocolVersion || !validLowerHexIdentifier(d.OperationID, 32) ||
		(d.Status != AgentRemovalStatusDelivered && d.Status != AgentRemovalStatusUninstalling) || len(d.ReceiptToken) < 32 || len(d.ReceiptToken) > 128 {
		return errors.New("invalid Agent removal delivery")
	}
	return nil
}

type AgentRemovalStatusRequest struct {
	ProtocolVersion int    `json:"protocol_version"`
	AgentEpoch      uint64 `json:"agent_epoch"`
	SessionID       string `json:"session_id"`
	Status          string `json:"status"`
}

func (r AgentRemovalStatusRequest) Validate() error {
	if r.ProtocolVersion != AgentRemovalProtocolVersion || r.AgentEpoch == 0 || r.AgentEpoch > math.MaxInt64 || !validIdentifier(r.SessionID, 128) || r.Status != AgentRemovalStatusUninstalling {
		return errors.New("invalid Agent removal status request")
	}
	return nil
}

type AgentRemovalReceiptRequest struct {
	Receipt string `json:"receipt"`
}

func (r AgentRemovalReceiptRequest) Validate() error {
	if r.Receipt != AgentRemovalReceiptKind {
		return errors.New("invalid Agent removal receipt")
	}
	return nil
}

type AgentRemovalReceiptResponse struct {
	Completed bool `json:"completed"`
	Duplicate bool `json:"duplicate,omitempty"`
}

func validLowerHexIdentifier(value string, expectedLength int) bool {
	if len(value) != expectedLength {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

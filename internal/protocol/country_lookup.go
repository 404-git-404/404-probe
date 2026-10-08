package protocol

import (
	"errors"
	"math"
)

const CountryCodeLookupProtocolVersion = 1

const CountryCodeLookupFailure = "lookup_failed"

type AgentCountryCodeLookupClaimRequest struct {
	ProtocolVersion int    `json:"protocol_version"`
	AgentEpoch      uint64 `json:"agent_epoch"`
	SessionID       string `json:"session_id"`
}

func (r AgentCountryCodeLookupClaimRequest) Validate() error {
	if r.ProtocolVersion != CountryCodeLookupProtocolVersion || r.AgentEpoch == 0 || r.AgentEpoch > math.MaxInt64 || !validIdentifier(r.SessionID, 128) {
		return errors.New("invalid country code lookup claim request")
	}
	return nil
}

type AgentCountryCodeLookupDelivery struct {
	ProtocolVersion int    `json:"protocol_version"`
	OperationID     string `json:"operation_id"`
}

func (d AgentCountryCodeLookupDelivery) Validate() error {
	if d.ProtocolVersion != CountryCodeLookupProtocolVersion || !validLowerHexIdentifier(d.OperationID, 32) {
		return errors.New("invalid country code lookup delivery")
	}
	return nil
}

type AgentCountryCodeLookupResultRequest struct {
	ProtocolVersion int    `json:"protocol_version"`
	AgentEpoch      uint64 `json:"agent_epoch"`
	SessionID       string `json:"session_id"`
	CountryCode     string `json:"country_code,omitempty"`
	ErrorCode       string `json:"error_code,omitempty"`
}

func (r AgentCountryCodeLookupResultRequest) Validate() error {
	if r.ProtocolVersion != CountryCodeLookupProtocolVersion || r.AgentEpoch == 0 || r.AgentEpoch > math.MaxInt64 || !validIdentifier(r.SessionID, 128) {
		return errors.New("invalid country code lookup result request")
	}
	if r.CountryCode != "" && r.ErrorCode == "" && ValidCountryCode(r.CountryCode) {
		return nil
	}
	if r.CountryCode == "" && r.ErrorCode == CountryCodeLookupFailure {
		return nil
	}
	return errors.New("invalid country code lookup result")
}

type AgentCountryCodeLookupResultResponse struct {
	Accepted  bool   `json:"accepted"`
	Duplicate bool   `json:"duplicate,omitempty"`
	Status    string `json:"status"`
}

package protocol

import (
	"errors"
	"strings"
)

const (
	UpgradeProtocolVersion = 1
	MaxUpgradeBodyBytes    = 8 << 10
)

type UpgradeClaimRequest struct {
	ProtocolVersion int `json:"protocol_version"`
}

func (r UpgradeClaimRequest) Validate() error {
	if r.ProtocolVersion != UpgradeProtocolVersion {
		return errors.New("unsupported upgrade protocol version")
	}
	return nil
}

type UpgradeOperation struct {
	ProtocolVersion int    `json:"protocol_version"`
	OperationID     string `json:"operation_id"`
	FromVersion     string `json:"from_version"`
	TargetVersion   string `json:"target_version"`
	Status          string `json:"status"`
}

func (o UpgradeOperation) Validate() error {
	if o.ProtocolVersion != UpgradeProtocolVersion || !validHexID(o.OperationID, 32) ||
		!validText(o.FromVersion, 64) || !validText(o.TargetVersion, 64) || !validText(o.Status, 32) {
		return errors.New("invalid upgrade operation")
	}
	return nil
}

type UpgradeStatusRequest struct {
	ProtocolVersion int    `json:"protocol_version"`
	Status          string `json:"status"`
	FailureCode     string `json:"failure_code,omitempty"`
	FailureMessage  string `json:"failure_message,omitempty"`
}

func (r UpgradeStatusRequest) Validate() error {
	if r.ProtocolVersion != UpgradeProtocolVersion || !validText(r.Status, 32) {
		return errors.New("invalid upgrade status")
	}
	if r.FailureCode != "" && (!validText(r.FailureCode, 64) || strings.TrimSpace(r.FailureCode) != r.FailureCode) {
		return errors.New("invalid upgrade failure code")
	}
	if r.FailureMessage != "" && (!validText(r.FailureMessage, 512) || strings.TrimSpace(r.FailureMessage) != r.FailureMessage) {
		return errors.New("invalid upgrade failure message")
	}
	return nil
}

func validHexID(value string, size int) bool {
	if len(value) != size {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

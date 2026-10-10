package protocol

import (
	"404-probe/internal/buildinfo"
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
	if r.ProtocolVersion != UpgradeProtocolVersion && r.ProtocolVersion != 2 {
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
	Channel         string `json:"channel,omitempty"`
	TargetCommit    string `json:"target_commit,omitempty"`
	ServerVersion   string `json:"server_version,omitempty"`
}

func (o UpgradeOperation) Validate() error {
	if (o.ProtocolVersion != UpgradeProtocolVersion && o.ProtocolVersion != 2) || !validHexID(o.OperationID, 32) ||
		!validText(o.FromVersion, 64) || !validText(o.TargetVersion, 64) || !validText(o.Status, 32) {
		return errors.New("invalid upgrade operation")
	}
	if o.ProtocolVersion == 1 {
		if !buildinfo.IsCanonicalVersion(o.TargetVersion) || o.Channel != "" || o.TargetCommit != "" || o.ServerVersion != "" {
			return errors.New("legacy upgrade has v2 fields")
		}
	} else if !ValidUpgradeTarget(o.Channel, o.TargetVersion) || !validHexID(o.TargetCommit, 40) || !buildinfo.IsReleaseVersion(o.ServerVersion) {
		return errors.New("invalid explicit release operation")
	}
	return nil
}

func ValidUpgradeTarget(channel, version string) bool {
	return channel == "stable" && buildinfo.IsCanonicalVersion(version) || channel == "beta" && buildinfo.IsReleaseVersion(version) && !buildinfo.IsCanonicalVersion(version)
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

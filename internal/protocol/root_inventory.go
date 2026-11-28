package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

const (
	MaxSecuritySnapshotBytes = 256 << 10
	MaxSingboxStructureBytes = 256 << 10
	MaxInventoryTextBytes    = 128
	MaxSingboxItems          = 512
	InventoryUnavailable     = "unavailable"
)

type SecurityBaselineReport struct {
	Status      string            `json:"status"`
	Reason      string            `json:"reason,omitempty"`
	CollectedAt int64             `json:"collected_at"`
	Digest      string            `json:"digest"`
	Snapshot    *SecuritySnapshot `json:"snapshot,omitempty"`
}

type SecuritySnapshot struct {
	SSHKeys      []SecuritySSHKey      `json:"ssh_keys"`
	Ports        []SecurityPort        `json:"ports"`
	UID0         []string              `json:"uid0"`
	SudoMembers  []SecuritySudoMember  `json:"sudo_members"`
	SudoersFiles []SecuritySudoersFile `json:"sudoers_files"`
}

type SecuritySSHKey struct {
	User        string `json:"user"`
	Type        string `json:"type"`
	Fingerprint string `json:"fingerprint"`
	Comment     string `json:"comment"`
}

type SecurityPort struct {
	Protocol  string   `json:"protocol"`
	Port      uint16   `json:"port"`
	Processes []string `json:"processes"`
}

type SecuritySudoMember struct {
	Group string `json:"group"`
	User  string `json:"user"`
}

type SecuritySudoersFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type SingboxStructureReport struct {
	Status      string            `json:"status"`
	Reason      string            `json:"reason,omitempty"`
	CollectedAt int64             `json:"collected_at"`
	Digest      string            `json:"digest"`
	Truncated   bool              `json:"truncated"`
	Structure   *SingboxStructure `json:"structure,omitempty"`
}

type SingboxStructure struct {
	Inbounds  []SingboxInbound  `json:"inbounds"`
	Outbounds []SingboxOutbound `json:"outbounds"`
	Route     SingboxRoute      `json:"route"`
}

type SingboxInbound struct {
	Tag        string `json:"tag"`
	Type       string `json:"type"`
	ListenPort uint16 `json:"listen_port,omitempty"`
}

type SingboxOutbound struct {
	Tag       string   `json:"tag"`
	Type      string   `json:"type"`
	Detour    string   `json:"detour,omitempty"`
	Outbounds []string `json:"outbounds,omitempty"`
	Default   string   `json:"default,omitempty"`
}

type SingboxRoute struct {
	Rules   []SingboxRule    `json:"rules"`
	Final   string           `json:"final,omitempty"`
	RuleSet []SingboxRuleSet `json:"rule_set"`
}

type SingboxRule struct {
	Inbound    []string       `json:"inbound,omitempty"`
	Outbound   []string       `json:"outbound,omitempty"`
	Action     string         `json:"action,omitempty"`
	RuleSet    []string       `json:"rule_set,omitempty"`
	Conditions map[string]int `json:"conditions,omitempty"`
	Rules      []SingboxRule  `json:"rules,omitempty"`
}

type SingboxRuleSet struct {
	Tag  string `json:"tag"`
	Type string `json:"type"`
}

func InventoryDigest(value any) (string, int, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", 0, err
	}
	return SHA256Hex(data), len(data), nil
}

func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func ValidInventoryDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func validateInventory(status, reason, digest string, collectedAt int64, full bool, value any, limit int) error {
	if len(reason) > MaxInventoryTextBytes || collectedAt < 0 {
		return errors.New("invalid root inventory metadata")
	}
	switch status {
	case CheckOK:
		if collectedAt == 0 || !ValidInventoryDigest(digest) || reason != "" {
			return errors.New("invalid root inventory fingerprint")
		}
	case CheckError, InventoryUnavailable:
		if full || digest != "" {
			return errors.New("invalid failed root inventory")
		}
	default:
		return errors.New("invalid root inventory status")
	}
	if full {
		computed, size, err := InventoryDigest(value)
		if err != nil || computed != digest || size > limit {
			return errors.New("invalid root inventory document")
		}
	}
	return nil
}

func (r SecurityBaselineReport) Validate() error {
	if s := r.Snapshot; s != nil {
		if s.SSHKeys == nil || s.Ports == nil || s.UID0 == nil || s.SudoMembers == nil || s.SudoersFiles == nil ||
			len(s.SSHKeys) > 256 || len(s.Ports) > 256 || len(s.UID0) > 256 || len(s.SudoMembers) > 512 || len(s.SudoersFiles) > 512 {
			return errors.New("incomplete security baseline snapshot")
		}
	}
	return validateInventory(r.Status, r.Reason, r.Digest, r.CollectedAt, r.Snapshot != nil, r.Snapshot, MaxSecuritySnapshotBytes)
}

func (r SingboxStructureReport) Validate() error {
	return validateInventory(r.Status, r.Reason, r.Digest, r.CollectedAt, r.Structure != nil, r.Structure, MaxSingboxStructureBytes)
}

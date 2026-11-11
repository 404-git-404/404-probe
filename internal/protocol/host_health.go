package protocol

import (
	"errors"
	"math"
	"slices"
)

// UnavailableSystemName satisfies the required OS field on legacy Servers
// during negotiation when the Agent has never read its system name.
const UnavailableSystemName = "(unknown)"

type HostHealth struct {
	Disk         DiskHealth `json:"disk"`
	OSReadFailed bool       `json:"os_read_failed,omitempty"`
}

type DiskHealthStatus string
type DiskHealthReason string

const (
	DiskUnknown DiskHealthStatus = "unknown"
	DiskOK      DiskHealthStatus = "ok"
	DiskError   DiskHealthStatus = "error"

	DiskWriteEIO   DiskHealthReason = "write_eio"
	DiskReadEIO    DiskHealthReason = "read_eio"
	DiskReadOnly   DiskHealthReason = "read_only"
	DiskStalled    DiskHealthReason = "stalled"
	DiskExt4Errors DiskHealthReason = "ext4_errors"
	DiskFull       DiskHealthReason = "full"
	DiskSCSIErrors DiskHealthReason = "scsi_errors"
	DiskSlow       DiskHealthReason = "slow"
	DiskUnwritable DiskHealthReason = "unwritable"
)

func (r DiskHealthReason) Fault() bool {
	switch r {
	case DiskWriteEIO, DiskReadEIO, DiskReadOnly, DiskStalled, DiskExt4Errors:
		return true
	default:
		return false
	}
}

type DiskHealth struct {
	Status           DiskHealthStatus   `json:"status"`
	Reasons          []DiskHealthReason `json:"reasons,omitempty"`
	CheckedAt        int64              `json:"checked_at,omitempty"` // Unix milliseconds
	StartedAt        int64              `json:"started_at,omitempty"` // Unix milliseconds
	StalledSince     int64              `json:"stalled_since,omitempty"`
	ProbeMS          int64              `json:"probe_ms,omitempty"`
	ReadBackVerified *bool              `json:"read_back_verified,omitempty"`
	ReadOnlySuper    *bool              `json:"read_only_super,omitempty"`
	Container        bool               `json:"container,omitempty"`
	Filesystems      []DiskFilesystem   `json:"filesystems,omitempty"`
}

type DiskFilesystem struct {
	Scope           string  `json:"scope"` // root or data; shared mounts use root
	Ext4Errors      *uint64 `json:"ext4_errors,omitempty"`
	Ext4LastErrorAt int64   `json:"ext4_last_error_at,omitempty"` // Unix seconds from sysfs
	SCSIErrors      *uint64 `json:"scsi_errors,omitempty"`
	SCSIState       string  `json:"scsi_state,omitempty"`
}

func (h HostHealth) Validate() error {
	d := h.Disk
	const maxUnixSeconds = 253402300799
	const maxUnixMillis = maxUnixSeconds * 1000
	if d.Status != DiskUnknown && d.Status != DiskOK && d.Status != DiskError {
		return errors.New("invalid host health disk status")
	}
	if len(d.Reasons) > 9 || len(d.Filesystems) > 2 {
		return errors.New("too many disk health reasons or filesystems")
	}
	for _, timestamp := range []int64{d.CheckedAt, d.StartedAt, d.StalledSince} {
		if timestamp < 0 || timestamp > maxUnixMillis {
			return errors.New("invalid disk health timestamp")
		}
	}
	if d.ProbeMS < 0 || d.ProbeMS > maxUnixMillis || (d.Status == DiskOK && d.CheckedAt == 0) {
		return errors.New("invalid disk probe timing")
	}
	fault := false
	for i, reason := range d.Reasons {
		if slices.Contains(d.Reasons[:i], reason) {
			return errors.New("duplicate disk health reason")
		}
		if reason.Fault() {
			fault = true
			continue
		}
		if reason != DiskFull && reason != DiskSCSIErrors && reason != DiskSlow && reason != DiskUnwritable {
			return errors.New("invalid disk health reason")
		}
	}
	if fault != (d.Status == DiskError) || fault != (d.StartedAt > 0) || slices.Contains(d.Reasons, DiskStalled) != (d.StalledSince > 0) {
		return errors.New("inconsistent disk health fault")
	}
	if d.Container && (d.ReadOnlySuper != nil || len(d.Filesystems) != 0) {
		return errors.New("container cannot report host filesystem health")
	}
	for i, fs := range d.Filesystems {
		if (fs.Scope != "root" && fs.Scope != "data") || (i > 0 && fs.Scope == d.Filesystems[0].Scope) {
			return errors.New("invalid disk health filesystem scope")
		}
		if (fs.Ext4Errors != nil && *fs.Ext4Errors > math.MaxUint32) ||
			(fs.SCSIErrors != nil && *fs.SCSIErrors > math.MaxInt64) ||
			fs.Ext4LastErrorAt < 0 || fs.Ext4LastErrorAt > maxUnixSeconds ||
			(fs.SCSIState != "" && !validText(fs.SCSIState, 32)) {
			return errors.New("invalid filesystem error counters")
		}
	}
	return nil
}

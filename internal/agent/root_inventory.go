package agent

import (
	"time"

	"404-probe/internal/protocol"
)

const inventoryResendInterval = 30 * time.Minute

type inventorySent struct {
	digest string
	at     time.Time
}

func (s inventorySent) needsDocument(digest string, now time.Time) bool {
	return s.digest != digest || now.Sub(s.at) >= inventoryResendInterval
}

type inventoryReports struct {
	supported bool
	baseline  inventorySent
	structure inventorySent
}

func (s inventoryReports) prepare(checks *protocol.SystemChecks, now time.Time) {
	if !s.supported {
		checks.SecurityBaseline, checks.SingboxStructure = nil, nil
		return
	}
	if report := checks.SecurityBaseline; report != nil && !s.baseline.needsDocument(report.Digest, now) {
		copy := *report
		copy.Snapshot = nil
		checks.SecurityBaseline = &copy
	}
	if report := checks.SingboxStructure; report != nil && !s.structure.needsDocument(report.Digest, now) {
		copy := *report
		copy.Structure = nil
		checks.SingboxStructure = &copy
	}
}

func (s *inventoryReports) accepted(checks *protocol.SystemChecks, now time.Time) {
	if report := checks.SecurityBaseline; report != nil {
		switch {
		case report.Snapshot != nil:
			s.baseline = inventorySent{report.Digest, now}
		case report.Status != protocol.CheckOK:
			s.baseline = inventorySent{}
		}
	}
	if report := checks.SingboxStructure; report != nil {
		switch {
		case report.Structure != nil:
			s.structure = inventorySent{report.Digest, now}
		case report.Status != protocol.CheckOK:
			s.structure = inventorySent{}
		}
	}
}

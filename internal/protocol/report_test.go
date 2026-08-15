package protocol

import (
	"testing"
	"time"
)

func validProtocolReport() Report {
	return Report{
		AgentID: "agent", Epoch: 1, SessionID: "session", Sequence: 1, CollectedAt: time.Now().UnixMilli(),
		Hostname: "host", OS: "linux", Arch: "amd64", BootID: "boot",
		RAMTotal: 1, DiskTotal: 1,
	}
}

func TestReportRequiresEpochAndSequence(t *testing.T) {
	report := validProtocolReport()
	report.Epoch = 0
	if report.Validate() == nil {
		t.Fatal("zero epoch accepted")
	}
	report = validProtocolReport()
	report.Sequence = 0
	if report.Validate() == nil {
		t.Fatal("zero sequence accepted")
	}
	if err := validProtocolReport().Validate(); err != nil {
		t.Fatalf("valid report rejected: %v", err)
	}
}

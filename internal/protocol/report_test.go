package protocol

import (
	"encoding/json"
	"math"
	"strings"
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

func TestOptionalLinuxTelemetryIsBackwardCompatibleAndValidated(t *testing.T) {
	report := validProtocolReport()
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"cpu_steal_percent", "disk_read_rate", "disk_write_rate", "disk_busy_percent"} {
		if strings.Contains(string(encoded), field) {
			t.Fatalf("old Agent payload unexpectedly contains %s", field)
		}
	}
	invalid := math.Inf(1)
	report.DiskReadRate = &invalid
	if report.Validate() == nil {
		t.Fatal("non-finite disk rate accepted")
	}
	over := 101.0
	report = validProtocolReport()
	report.CPUStealPercent = &over
	if report.Validate() == nil {
		t.Fatal("out-of-range CPU steal accepted")
	}
}

func TestOptionalCountryCodeIsBackwardCompatibleAndValidated(t *testing.T) {
	report := validProtocolReport()
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "country_code") {
		t.Fatalf("old Agent payload unexpectedly contains country_code: %s", encoded)
	}
	for _, invalid := range []string{"us", "USA", "ÅX", "U1", "AA", "ZZ"} {
		report.CountryCode = invalid
		if report.Validate() == nil {
			t.Fatalf("invalid country code %q accepted", invalid)
		}
	}
	report.CountryCode = "US"
	if err := report.Validate(); err != nil {
		t.Fatalf("valid country code rejected: %v", err)
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

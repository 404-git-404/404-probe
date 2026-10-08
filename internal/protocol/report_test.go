package protocol

import (
	"encoding/json"
	"fmt"
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

func TestManagementCapabilitiesAreOptionalAndExplicitlyNegotiated(t *testing.T) {
	report := validProtocolReport()
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "management_capabilities") {
		t.Fatalf("unnegotiated report unexpectedly contains management capabilities: %s", encoded)
	}
	report.Management = &AgentManagementCapabilities{RemoteRemoval: true, CountryCodeLookup: true}
	encoded, err = json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Report
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Management == nil || !decoded.Management.RemoteRemoval || !decoded.Management.CountryCodeLookup {
		t.Fatalf("management capabilities did not round-trip: %+v", decoded.Management)
	}
}

func TestNetworkCountersAreOptionalAndRoundTrip(t *testing.T) {
	report := validProtocolReport()
	legacy, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacy), "network_counters") {
		t.Fatalf("legacy payload unexpectedly contains network counters: %s", legacy)
	}
	report.RXBytes, report.TXBytes = 19, 31
	report.NetworkCounters = &NetworkCounterSet{Version: 1, Interfaces: []NetworkInterfaceCounter{
		{Name: "eth0", RXBytes: 19, TXBytes: 31},
	}}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Report
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("valid v1 report rejected after round trip: %v", err)
	}
	if decoded.NetworkCounters == nil || decoded.NetworkCounters.Version != 1 || len(decoded.NetworkCounters.Interfaces) != 1 || decoded.NetworkCounters.Interfaces[0].Name != "eth0" {
		t.Fatalf("network counters did not round-trip: %+v", decoded.NetworkCounters)
	}
	report.RXBytes, report.TXBytes = 0, 0
	report.NetworkCounters = &NetworkCounterSet{Version: 1, Interfaces: []NetworkInterfaceCounter{}}
	if err := report.Validate(); err != nil {
		t.Fatalf("valid empty v1 set rejected: %v", err)
	}
}

func TestNetworkCounterV1RequiresExplicitInterfaceListInJSON(t *testing.T) {
	base, err := json.Marshal(validProtocolReport())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		value string
		valid bool
	}{
		{name: "missing", value: `{"version":1}`},
		{name: "null", value: `{"version":1,"interfaces":null}`},
		{name: "empty array", value: `{"version":1,"interfaces":[]}`, valid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(base, &payload); err != nil {
				t.Fatal(err)
			}
			payload["network_counters"] = json.RawMessage(test.value)
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			var report Report
			if err := json.Unmarshal(encoded, &report); err != nil {
				t.Fatal(err)
			}
			err = report.Validate()
			if test.valid && err != nil {
				t.Fatalf("explicit empty list rejected: %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("incomplete v1 interface list accepted")
			}
		})
	}
}

func TestNetworkCountersRejectInvalidVersionNamesAndSums(t *testing.T) {
	max := uint64(math.MaxInt64)
	invalidSets := []struct {
		name   string
		report Report
	}{
		{"version", func() Report {
			r := validProtocolReport()
			r.NetworkCounters = &NetworkCounterSet{Version: 2}
			return r
		}()},
		{"null interface list", func() Report {
			r := validProtocolReport()
			r.NetworkCounters = &NetworkCounterSet{Version: 1}
			return r
		}()},
		{"duplicate", func() Report {
			r := validProtocolReport()
			r.NetworkCounters = &NetworkCounterSet{Version: 1, Interfaces: []NetworkInterfaceCounter{{Name: "eth0"}, {Name: "eth0"}}}
			return r
		}()},
		{"empty name", func() Report {
			r := validProtocolReport()
			r.NetworkCounters = &NetworkCounterSet{Version: 1, Interfaces: []NetworkInterfaceCounter{{Name: " "}}}
			return r
		}()},
		{"trimmed name", func() Report {
			r := validProtocolReport()
			r.NetworkCounters = &NetworkCounterSet{Version: 1, Interfaces: []NetworkInterfaceCounter{{Name: " eth0"}}}
			return r
		}()},
		{"control name", func() Report {
			r := validProtocolReport()
			r.NetworkCounters = &NetworkCounterSet{Version: 1, Interfaces: []NetworkInterfaceCounter{{Name: "eth\n0"}}}
			return r
		}()},
		{"long name", func() Report {
			r := validProtocolReport()
			r.NetworkCounters = &NetworkCounterSet{Version: 1, Interfaces: []NetworkInterfaceCounter{{Name: strings.Repeat("x", 129)}}}
			return r
		}()},
		{"interface int64", func() Report {
			r := validProtocolReport()
			r.NetworkCounters = &NetworkCounterSet{Version: 1, Interfaces: []NetworkInterfaceCounter{{Name: "eth0", RXBytes: max + 1}}}
			return r
		}()},
		{"too many", func() Report {
			r := validProtocolReport()
			counters := make([]NetworkInterfaceCounter, 257)
			for i := range counters {
				counters[i].Name = fmt.Sprintf("eth%d", i)
			}
			r.NetworkCounters = &NetworkCounterSet{Version: 1, Interfaces: counters}
			return r
		}()},
		{"aggregate mismatch", func() Report {
			r := validProtocolReport()
			r.RXBytes = 2
			r.NetworkCounters = &NetworkCounterSet{Version: 1, Interfaces: []NetworkInterfaceCounter{{Name: "eth0", RXBytes: 1}}}
			return r
		}()},
		{"sum exceeds int64", func() Report {
			r := validProtocolReport()
			r.RXBytes = max
			r.NetworkCounters = &NetworkCounterSet{Version: 1, Interfaces: []NetworkInterfaceCounter{{Name: "eth0", RXBytes: max}, {Name: "eth1", RXBytes: 1}}}
			return r
		}()},
	}
	for _, test := range invalidSets {
		t.Run(test.name, func(t *testing.T) {
			if err := test.report.Validate(); err == nil {
				t.Fatal("invalid network counter set accepted")
			}
		})
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

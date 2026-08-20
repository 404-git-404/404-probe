package protocol

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func validJob() Job {
	return Job{
		ProtocolVersion: JobProtocolVersion,
		JobID:           "job-1",
		ProbeType:       ProbeTypeICMPPing,
		Config:          ProbeConfig{ICMPPing: &ICMPPingConfig{Target: "example.com", Count: 4}},
		CreatedAt:       1000,
		NotBefore:       1000,
		ExpiresAt:       60_000,
		TimeoutMS:       5000,
		Attempt:         1,
		LeaseToken:      "lease-token",
		LeaseExpiresAt:  36_000,
	}
}

func validTCPResult() JobResult {
	return JobResult{
		ProtocolVersion: JobProtocolVersion,
		LeaseToken:      "lease-token",
		Attempt:         1,
		AgentEpoch:      2,
		SessionID:       "session",
		StartedAt:       1000,
		FinishedAt:      1025,
		DurationMS:      25,
		Success:         true,
		ResolvedIP:      "192.0.2.1",
		Result:          ProbeResult{TCPConnect: &TCPConnectResult{ConnectMS: 24.5}},
	}
}

func TestProbeConfigValidation(t *testing.T) {
	status := 204
	tests := []struct {
		name      string
		probeType ProbeType
		config    ProbeConfig
		wantError bool
	}{
		{name: "icmp", probeType: ProbeTypeICMPPing, config: ProbeConfig{ICMPPing: &ICMPPingConfig{Target: "1.1.1.1", Count: 10}}},
		{name: "icmp count zero", probeType: ProbeTypeICMPPing, config: ProbeConfig{ICMPPing: &ICMPPingConfig{Target: "1.1.1.1"}}, wantError: true},
		{name: "target too long", probeType: ProbeTypeICMPPing, config: ProbeConfig{ICMPPing: &ICMPPingConfig{Target: strings.Repeat("a", 254), Count: 1}}, wantError: true},
		{name: "target URL", probeType: ProbeTypeICMPPing, config: ProbeConfig{ICMPPing: &ICMPPingConfig{Target: "https://example.com", Count: 1}}, wantError: true},
		{name: "tcp", probeType: ProbeTypeTCPConnect, config: ProbeConfig{TCPConnect: &TCPConnectConfig{Host: "example.com", Port: 443}}},
		{name: "tcp bad port", probeType: ProbeTypeTCPConnect, config: ProbeConfig{TCPConnect: &TCPConnectConfig{Host: "example.com", Port: 65536}}, wantError: true},
		{name: "http", probeType: ProbeTypeHTTP, config: ProbeConfig{HTTP: &HTTPConfig{URL: "https://example.com/health?q=1", Method: "HEAD", ExpectedStatus: &status}}},
		{name: "http URL too long", probeType: ProbeTypeHTTP, config: ProbeConfig{HTTP: &HTTPConfig{URL: "https://example.com/" + strings.Repeat("x", MaxProbeURLBytes), Method: "GET"}}, wantError: true},
		{name: "http URL control", probeType: ProbeTypeHTTP, config: ProbeConfig{HTTP: &HTTPConfig{URL: "https://example.com/\n", Method: "GET"}}, wantError: true},
		{name: "http relative", probeType: ProbeTypeHTTP, config: ProbeConfig{HTTP: &HTTPConfig{URL: "/health", Method: "GET"}}, wantError: true},
		{name: "http scheme", probeType: ProbeTypeHTTP, config: ProbeConfig{HTTP: &HTTPConfig{URL: "file:///etc/passwd", Method: "GET"}}, wantError: true},
		{name: "http userinfo", probeType: ProbeTypeHTTP, config: ProbeConfig{HTTP: &HTTPConfig{URL: "https://user@example.com/", Method: "GET"}}, wantError: true},
		{name: "http fragment", probeType: ProbeTypeHTTP, config: ProbeConfig{HTTP: &HTTPConfig{URL: "https://example.com/#fragment", Method: "GET"}}, wantError: true},
		{name: "http method", probeType: ProbeTypeHTTP, config: ProbeConfig{HTTP: &HTTPConfig{URL: "https://example.com/", Method: "POST"}}, wantError: true},
		{name: "http expected status", probeType: ProbeTypeHTTP, config: ProbeConfig{HTTP: &HTTPConfig{URL: "https://example.com/", Method: "GET", ExpectedStatus: intPointer(600)}}, wantError: true},
		{name: "mismatch", probeType: ProbeTypeHTTP, config: ProbeConfig{TCPConnect: &TCPConnectConfig{Host: "example.com", Port: 80}}, wantError: true},
		{name: "multiple", probeType: ProbeTypeHTTP, config: ProbeConfig{TCPConnect: &TCPConnectConfig{Host: "example.com", Port: 80}, HTTP: &HTTPConfig{URL: "https://example.com", Method: "GET"}}, wantError: true},
		{name: "unknown", probeType: ProbeType("command"), config: ProbeConfig{ICMPPing: &ICMPPingConfig{Target: "example.com", Count: 1}}, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.config.Validate(test.probeType)
			if (err != nil) != test.wantError {
				t.Fatalf("Validate() error=%v wantError=%t", err, test.wantError)
			}
		})
	}
}

func TestStrictConfigAndJobJSON(t *testing.T) {
	if _, err := DecodeProbeConfig(ProbeTypeTCPConnect, []byte(`{"host":"example.com","port":443,"args":[]}`)); err == nil {
		t.Fatal("unknown config field accepted")
	}
	if _, err := DecodeProbeConfig(ProbeType("shell"), []byte(`{}`)); err == nil {
		t.Fatal("unknown probe type accepted")
	}
	if _, err := DecodeProbeResult(ProbeTypeTCPConnect, []byte(`null`)); err == nil {
		t.Fatal("null typed result accepted")
	}

	job := validJob()
	encoded, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"config":{"target":"example.com","count":4}`)) {
		t.Fatalf("config was not encoded as direct typed payload: %s", encoded)
	}
	var decoded Job
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Config.ICMPPing == nil || decoded.Config.ICMPPing.Count != 4 {
		t.Fatalf("decoded job=%+v", decoded)
	}
	malformed := strings.Replace(string(encoded), `"config":{`, `"unknown":true,"config":{`, 1)
	if err := json.Unmarshal([]byte(malformed), &decoded); err == nil {
		t.Fatal("unknown job field accepted")
	}
	unknownType := strings.Replace(string(encoded), `"icmp_ping"`, `"script"`, 1)
	if err := json.Unmarshal([]byte(unknownType), &decoded); err == nil {
		t.Fatal("unknown job probe type accepted")
	}
}

func TestClaimRequestValidation(t *testing.T) {
	valid := ClaimRequest{ProtocolVersion: JobProtocolVersion, AgentEpoch: 1, SessionID: "session", SupportedProbeTypes: []ProbeType{ProbeTypeICMPPing, ProbeTypeTCPConnect}}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	tests := []ClaimRequest{
		{ProtocolVersion: 2, AgentEpoch: 1, SessionID: "session", SupportedProbeTypes: []ProbeType{ProbeTypeHTTP}},
		{ProtocolVersion: 1, AgentEpoch: 0, SessionID: "session", SupportedProbeTypes: []ProbeType{ProbeTypeHTTP}},
		{ProtocolVersion: 1, AgentEpoch: 1, SessionID: "", SupportedProbeTypes: []ProbeType{ProbeTypeHTTP}},
		{ProtocolVersion: 1, AgentEpoch: 1, SessionID: "session"},
		{ProtocolVersion: 1, AgentEpoch: 1, SessionID: "session", SupportedProbeTypes: []ProbeType{ProbeTypeHTTP, ProbeTypeHTTP}},
		{ProtocolVersion: 1, AgentEpoch: 1, SessionID: "session", SupportedProbeTypes: []ProbeType{"command"}},
	}
	for i, request := range tests {
		if err := request.Validate(); err == nil {
			t.Errorf("invalid request %d accepted: %+v", i, request)
		}
	}
	if _, err := DecodeClaimRequest([]byte(`{"protocol_version":1,"agent_epoch":1,"session_id":"session","supported_probe_types":["http"],"command":"id"}`)); err == nil {
		t.Fatal("unknown claim field accepted")
	}
}

func TestJobValidationBounds(t *testing.T) {
	job := validJob()
	if err := job.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, timeout := range []int{99, 30_001} {
		invalid := job
		invalid.TimeoutMS = timeout
		if err := invalid.Validate(); err == nil {
			t.Errorf("timeout %d accepted", timeout)
		}
	}
	invalid := job
	invalid.ExpiresAt = invalid.NotBefore
	if err := invalid.Validate(); err == nil {
		t.Fatal("non-positive job lifetime accepted")
	}
}

func TestProbeResultValidation(t *testing.T) {
	validPing := ProbeResult{ICMPPing: &ICMPPingResult{Sent: 4, Received: 3, PacketLossPercent: 25, LatencyMinMS: 1, LatencyAvgMS: 2, LatencyMaxMS: 3}}
	validHTTP := ProbeResult{HTTP: &HTTPResult{DNSMS: 1, ConnectMS: 2, TLSMS: 3, TTFBMS: 4, TotalMS: 5, StatusCode: 200, BodyBytes: 10}}
	tests := []struct {
		name      string
		probeType ProbeType
		result    ProbeResult
		wantError bool
	}{
		{name: "ping", probeType: ProbeTypeICMPPing, result: validPing},
		{name: "ping loss mismatch", probeType: ProbeTypeICMPPing, result: ProbeResult{ICMPPing: &ICMPPingResult{Sent: 4, Received: 3, PacketLossPercent: 50}}, wantError: true},
		{name: "ping order", probeType: ProbeTypeICMPPing, result: ProbeResult{ICMPPing: &ICMPPingResult{Sent: 1, Received: 1, LatencyMinMS: 3, LatencyAvgMS: 2, LatencyMaxMS: 1}}, wantError: true},
		{name: "tcp nan", probeType: ProbeTypeTCPConnect, result: ProbeResult{TCPConnect: &TCPConnectResult{ConnectMS: math.NaN()}}, wantError: true},
		{name: "tcp negative", probeType: ProbeTypeTCPConnect, result: ProbeResult{TCPConnect: &TCPConnectResult{ConnectMS: -1}}, wantError: true},
		{name: "http", probeType: ProbeTypeHTTP, result: validHTTP},
		{name: "http status", probeType: ProbeTypeHTTP, result: ProbeResult{HTTP: &HTTPResult{StatusCode: 99}}, wantError: true},
		{name: "http body overflow", probeType: ProbeTypeHTTP, result: ProbeResult{HTTP: &HTTPResult{BodyBytes: math.MaxInt64 + 1}}, wantError: true},
		{name: "mismatch", probeType: ProbeTypeTCPConnect, result: validHTTP, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.result.Validate(test.probeType)
			if (err != nil) != test.wantError {
				t.Fatalf("Validate() error=%v wantError=%t", err, test.wantError)
			}
		})
	}
}

func intPointer(value int) *int { return &value }

func TestJobResultValidationAndStrictDecode(t *testing.T) {
	result := validTCPResult()
	if err := result.Validate(ProbeTypeTCPConnect); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*JobResult)
	}{
		{name: "nan duration", mutate: func(r *JobResult) { r.DurationMS = math.NaN() }},
		{name: "bad IP", mutate: func(r *JobResult) { r.ResolvedIP = "not-an-ip" }},
		{name: "long error", mutate: func(r *JobResult) {
			r.Success = false
			r.ErrorCategory = "internal"
			r.ErrorMessage = strings.Repeat("x", 513)
		}},
		{name: "success error", mutate: func(r *JobResult) { r.ErrorCategory = "timeout" }},
		{name: "failure category missing", mutate: func(r *JobResult) { r.Success = false }},
		{name: "timestamp order", mutate: func(r *JobResult) { r.FinishedAt = r.StartedAt - 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := result
			test.mutate(&invalid)
			if err := invalid.Validate(ProbeTypeTCPConnect); err == nil {
				t.Fatal("invalid result accepted")
			}
		})
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeJobResult(encoded, ProbeTypeTCPConnect)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Result.TCPConnect == nil || decoded.Result.TCPConnect.ConnectMS != 24.5 {
		t.Fatalf("decoded=%+v", decoded)
	}
	unknown := strings.Replace(string(encoded), `"result":{`, `"command":"id","result":{`, 1)
	if _, err := DecodeJobResult([]byte(unknown), ProbeTypeTCPConnect); err == nil {
		t.Fatal("unknown result envelope field accepted")
	}
	payloadUnknown := strings.Replace(string(encoded), `"connect_ms":24.5`, `"connect_ms":24.5,"args":[]`, 1)
	if _, err := DecodeJobResult([]byte(payloadUnknown), ProbeTypeTCPConnect); err == nil {
		t.Fatal("unknown typed result field accepted")
	}
}

func TestCanonicalResultHashUsesNormalizedTypedResult(t *testing.T) {
	firstJSON := []byte(`{
		"protocol_version":1,"lease_token":"lease-a","attempt":1,"agent_epoch":2,"session_id":"session-a",
		"started_at":1000,"finished_at":1025,"duration_ms":25,"success":true,
		"resolved_ip":"2001:0db8:0:0:0:0:0:1","result":{"connect_ms":24.5}}`)
	secondJSON := []byte(`{"result":{"connect_ms":24.500},"resolved_ip":"2001:db8::1","success":true,
		"duration_ms":25.0,"finished_at":1025,"started_at":1000,"session_id":"session-b",
		"agent_epoch":3,"attempt":9,"lease_token":"different-lease","protocol_version":1}`)
	first, err := DecodeJobResult(firstJSON, ProbeTypeTCPConnect)
	if err != nil {
		t.Fatal(err)
	}
	second, err := DecodeJobResult(secondJSON, ProbeTypeTCPConnect)
	if err != nil {
		t.Fatal(err)
	}
	_, firstHash, err := CanonicalResult(ProbeTypeTCPConnect, first)
	if err != nil {
		t.Fatal(err)
	}
	_, secondHash, err := CanonicalResult(ProbeTypeTCPConnect, second)
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != secondHash {
		t.Fatalf("equivalent typed results hashed differently: %x != %x", firstHash, secondHash)
	}

	different := second
	different.Result.TCPConnect.ConnectMS = 24.6
	_, differentHash, err := CanonicalResult(ProbeTypeTCPConnect, different)
	if err != nil {
		t.Fatal(err)
	}
	if firstHash == differentHash {
		t.Fatal("different typed results produced the same hash")
	}

	positiveZero := first
	positiveZero.DurationMS = 0
	positiveZero.Result.TCPConnect.ConnectMS = 0
	negativeZero := positiveZero
	negativeZero.DurationMS = math.Copysign(0, -1)
	negativeZero.Result.TCPConnect.ConnectMS = math.Copysign(0, -1)
	_, positiveZeroHash, err := CanonicalResult(ProbeTypeTCPConnect, positiveZero)
	if err != nil {
		t.Fatal(err)
	}
	_, negativeZeroHash, err := CanonicalResult(ProbeTypeTCPConnect, negativeZero)
	if err != nil {
		t.Fatal(err)
	}
	if positiveZeroHash != negativeZeroHash {
		t.Fatalf("+0 and -0 hashed differently: %x != %x", positiveZeroHash, negativeZeroHash)
	}

	for _, infinity := range []float64{math.Inf(1), math.Inf(-1)} {
		invalid := first
		invalid.Result.TCPConnect.ConnectMS = infinity
		if err := invalid.Validate(ProbeTypeTCPConnect); err == nil {
			t.Fatalf("infinite result %v was accepted", infinity)
		}
	}
}
